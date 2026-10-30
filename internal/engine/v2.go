package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// V2Config is the versioned multi-case, multi-scorer definition.
// Scorers can be required or optional; required coverage determines suite quality.
type V2Config struct {
	Version int      `json:"version"`
	Cases   []V2Case `json:"cases"`
	Target  struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"target"`
	Scorers []V2Scorer `json:"scorers"`
}

type V2Case struct {
	ID       string          `json:"id"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

type V2Scorer struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Threshold      float64 `json:"threshold"`
	Required       bool    `json:"required"`
	Implementation string  `json:"implementation,omitempty"`
}

type ScorerObservation struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Score    *float64 `json:"score,omitempty"`
	Attempts int      `json:"attempts,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type V2CaseResult struct {
	ID           string              `json:"id"`
	State        string              `json:"state"`
	TargetStatus string              `json:"target_status"`
	Scorers      []ScorerObservation `json:"scorers"`
}

func validateV2(raw []byte) (V2Config, string, error) {
	return validateV2WithCustom(raw, nil)
}

func validateV2WithCustom(raw []byte, registered map[string]RegisteredScorer) (V2Config, string, error) {
	var cfg V2Config
	if len(raw) > maxConfigBytes {
		return cfg, "", errors.New("configuration exceeds size limit")
	}
	if err := decodeOne(raw, &cfg); err != nil {
		return cfg, "", fmt.Errorf("invalid configuration JSON: %w", err)
	}
	if cfg.Version != 2 {
		return cfg, "", errors.New("expected configuration version 2")
	}
	if len(cfg.Cases) == 0 {
		return cfg, "", errors.New("cases must be nonempty")
	}
	if strings.TrimSpace(cfg.Target.Command) == "" {
		return cfg, "", errors.New("target.command is required")
	}
	if len(cfg.Scorers) == 0 {
		return cfg, "", errors.New("scorers must be nonempty")
	}
	required := 0
	scorerIDs := map[string]bool{}
	for _, scorer := range cfg.Scorers {
		if scorer.ID == "" || scorerIDs[scorer.ID] {
			return cfg, "", errors.New("scorer IDs must be nonempty and unique")
		}
		scorerIDs[scorer.ID] = true
		if scorer.Threshold < 0 || scorer.Threshold > 1 {
			return cfg, "", errors.New("invalid scorer threshold")
		}
		switch scorer.Kind {
		case "exact_match":
		case "custom":
			registeredScorer, ok := registered[scorer.ID]
			if !ok || scorer.Implementation == "" || registeredScorer.Implementation != scorer.Implementation || registeredScorer.Evaluate == nil {
				return cfg, "", errors.New("custom scorer identity or registration unavailable")
			}
		default:
			return cfg, "", errors.New("unsupported scorer kind")
		}
		if scorer.Required {
			required++
		}
	}
	if required == 0 {
		return cfg, "", errors.New("at least one required scorer is needed")
	}
	canonical, err := json.Marshal(cfg)
	if err != nil {
		return cfg, "", err
	}
	sum := sha256.Sum256(append([]byte("evalkit:v2:config:\x00"), canonical...))
	return cfg, hex.EncodeToString(sum[:]), nil
}

// runV2 retains the serialized behavior of the V2 contract. V3 shares the
// same validated case/scorer evaluation logic with a bounded worker pool.
func runV2(ctx context.Context, raw []byte, artifactDir string, o Outcome) Outcome {
	cfg, digest, err := validateV2(raw)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	return runCases(ctx, cfg, digest, artifactDir, o, 1, 1, nil)
}

// evaluateCase never mutates shared configuration or authoritative state. Results
// are returned to the coordinator and committed in dataset ordinal order.
func evaluateCase(ctx context.Context, cfg V2Config, c V2Case, occurrences int, maxAttempts int, custom map[string]RegisteredScorer) (V2CaseResult, []Diagnostic) {
	result := V2CaseResult{ID: c.ID, State: "PENDING"}
	for _, sc := range cfg.Scorers {
		result.Scorers = append(result.Scorers, ScorerObservation{ID: sc.ID, Status: "SKIPPED"})
	}
	invalid := c.ID == "" || occurrences > 1
	if _, err := decodeJSONValue(c.Input); err != nil {
		invalid = true
	}
	if _, err := decodeJSONValue(c.Expected); err != nil {
		invalid = true
	}
	if invalid {
		result.State = "INVALID"
		result.TargetStatus = "SKIPPED"
		return result, []Diagnostic{{Code: "DATASET_INVALID_CASE", Scope: "CASE", Message: "Invalid case identity or JSON input"}}
	}
	var target Config
	target.Case.ID = c.ID
	target.Case.Input = append(json.RawMessage(nil), c.Input...)
	target.Target.Command = cfg.Target.Command
	target.Target.Args = append([]string(nil), cfg.Target.Args...)
	output, err := execute(ctx, target)
	if err != nil {
		result.State = "ERRORED"
		result.TargetStatus = "ERRORED"
		for i := range result.Scorers {
			result.Scorers[i].Status = "ERRORED"
		}
		return result, []Diagnostic{{Code: "TARGET_EXECUTION_ERROR", Scope: "CASE", Message: err.Error()}}
	}
	result.State = "SCORED"
	result.TargetStatus = "SUCCEEDED"
	actual, _ := decodeJSONValue(output)
	expected, _ := decodeJSONValue(c.Expected)
	var diagnostics []Diagnostic
	requiredError := false
	for i, sc := range cfg.Scorers {
		var candidate AssessmentCandidate
		var attempts int
		var scoreErr error
		if sc.Kind == "exact_match" {
			var score float64
			score, attempts, scoreErr = retryScorer(ctx, maxAttempts, func(context.Context) (float64, error) { return exactMatchScore(actual, expected), nil })
			if scoreErr == nil {
				candidate = AssessmentCandidate{Kind: "SCORED", Score: &score}
			}
		} else {
			ext := custom[sc.ID]
			input := ScorerInput{CaseID: c.ID, ScorerID: sc.ID, Actual: append(json.RawMessage(nil), output...), Expected: append(json.RawMessage(nil), c.Expected...)}
			candidate, attempts, scoreErr = retryAssessment(ctx, maxAttempts, func(ctx context.Context) (AssessmentCandidate, error) { return ext.Evaluate(ctx, input.Clone()) })
		}
		observation := ScorerObservation{ID: sc.ID, Attempts: attempts}
		if scoreErr != nil || !validCandidate(candidate) {
			observation.Status = "ERRORED"
			if sc.Required {
				requiredError = true
			}
			code := "SCORER_RESULT_INVALID"
			message := "Scorer returned an invalid assessment"
			if scoreErr != nil {
				code = "SCORER_OPERATION_ERROR"
				message = "Scorer operation failed"
			}
			diagnostics = append(diagnostics, Diagnostic{Code: code, Scope: "SCORER", Message: message})
		} else if candidate.Kind == "UNEVALUABLE" {
			observation.Status = "UNEVALUABLE"
			observation.Reason = candidate.Reason
			if sc.Required {
				requiredError = true
			}
		} else {
			observation.Status = "SUCCEEDED"
			score := *candidate.Score
			observation.Score = &score
		}
		result.Scorers[i] = observation
	}
	if requiredError {
		result.State = "ERRORED"
	}
	return result, diagnostics
}

func qualityOfCases(results []V2CaseResult, scorers []V2Scorer) string {
	incomplete, failed := false, false
	for _, c := range results {
		if c.State != "SCORED" {
			incomplete = true
		}
		for i, sc := range scorers {
			if !sc.Required {
				continue
			}
			if i >= len(c.Scorers) || c.Scorers[i].Status != "SUCCEEDED" || c.Scorers[i].Score == nil {
				incomplete = true
			} else if *c.Scorers[i].Score < sc.Threshold {
				failed = true
			}
		}
	}
	if incomplete {
		return "INDETERMINATE"
	}
	if failed {
		return "FAIL"
	}
	return "PASS"
}

func runCases(ctx context.Context, cfg V2Config, digest, artifactDir string, o Outcome, concurrency, maxAttempts int, custom map[string]RegisteredScorer) Outcome {
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	o.ConfigDigest = digest
	o.State = "RUNNING"
	occurrences := make(map[string]int, len(cfg.Cases))
	for _, c := range cfg.Cases {
		occurrences[c.ID]++
	}
	results := make([]V2CaseResult, len(cfg.Cases))
	diagnostics := make([][]Diagnostic, len(cfg.Cases))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				results[i], diagnostics[i] = evaluateCase(ctx, cfg, cfg.Cases[i], occurrences[cfg.Cases[i].ID], maxAttempts, custom)
			}
		}()
	}
dispatch:
	for i := range cfg.Cases {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	o.Cases = results
	for _, items := range diagnostics {
		o.Diagnostics = append(o.Diagnostics, items...)
	}
	quality := qualityOfCases(results, cfg.Scorers)
	o.State = "FINALIZING"
	committed := o
	committed.State = "COMPLETED"
	committed.Quality = &quality
	committed.ArtifactDir = artifactDir
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	if err := publish(ctx, artifactDir, committed); err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		return fail(o, "ARTIFACT_COMMIT_FAILED", "RUN", "Required artifact publication failed")
	}
	return committed
}
