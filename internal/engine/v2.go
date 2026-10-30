package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Threshold float64 `json:"threshold"`
	Required  bool    `json:"required"`
}

type ScorerObservation struct {
	ID     string   `json:"id"`
	Status string   `json:"status"`
	Score  *float64 `json:"score,omitempty"`
}

type V2CaseResult struct {
	ID           string              `json:"id"`
	State        string              `json:"state"`
	TargetStatus string              `json:"target_status"`
	Scorers      []ScorerObservation `json:"scorers"`
}

func validateV2(raw []byte) (V2Config, string, error) {
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
		if scorer.Kind != "exact_match" || scorer.Threshold < 0 || scorer.Threshold > 1 {
			return cfg, "", errors.New("unsupported scorer or invalid threshold")
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

func runV2(ctx context.Context, raw []byte, artifactDir string, o Outcome) Outcome {
	cfg, digest, err := validateV2(raw)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	o.ConfigDigest = digest
	if err = ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	o.State = "RUNNING"

	counts := map[string]int{}
	for _, c := range cfg.Cases {
		counts[c.ID]++
	}
	incomplete, failed := false, false
	for _, c := range cfg.Cases {
		if err = ctx.Err(); err != nil {
			return cancelled(o, err)
		}
		result := V2CaseResult{ID: c.ID, State: "PENDING"}
		for _, s := range cfg.Scorers {
			result.Scorers = append(result.Scorers, ScorerObservation{ID: s.ID, Status: "SKIPPED"})
		}
		invalid := c.ID == "" || counts[c.ID] > 1
		if _, err = decodeJSONValue(c.Input); err != nil {
			invalid = true
		}
		if _, err = decodeJSONValue(c.Expected); err != nil {
			invalid = true
		}
		if invalid {
			result.State = "INVALID"
			result.TargetStatus = "SKIPPED"
			incomplete = true
			o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: "DATASET_INVALID_CASE", Scope: "CASE", Message: "Invalid case identity or JSON input"})
			o.Cases = append(o.Cases, result)
			continue
		}
		// Reuse the validated bounded V1 command protocol without duplicating
		// process execution or output validation semantics.
		var target Config
		target.Case.ID = c.ID
		target.Case.Input = append(json.RawMessage(nil), c.Input...)
		target.Target.Command = cfg.Target.Command
		target.Target.Args = append([]string(nil), cfg.Target.Args...)
		output, executeErr := execute(ctx, target)
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		if executeErr != nil {
			result.State = "ERRORED"
			result.TargetStatus = "ERRORED"
			incomplete = true
			for j := range result.Scorers {
				result.Scorers[j].Status = "ERRORED"
			}
			o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: "TARGET_EXECUTION_ERROR", Scope: "CASE", Message: executeErr.Error()})
		} else {
			result.State = "SCORED"
			result.TargetStatus = "SUCCEEDED"
			actual, _ := decodeJSONValue(output)
			expected, _ := decodeJSONValue(c.Expected)
			for i, s := range cfg.Scorers {
				score := exactMatchScore(actual, expected)
				result.Scorers[i] = ScorerObservation{ID: s.ID, Status: "SUCCEEDED", Score: &score}
				if s.Required && score < s.Threshold {
					failed = true
				}
			}
		}
		o.Cases = append(o.Cases, result)
	}
	quality := "PASS"
	if incomplete {
		quality = "INDETERMINATE"
	} else if failed {
		quality = "FAIL"
	}
	o.State = "FINALIZING"
	committed := o
	committed.State = "COMPLETED"
	committed.Quality = &quality
	committed.ArtifactDir = artifactDir
	if err = ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	if err = publish(ctx, artifactDir, committed); err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		return fail(o, "ARTIFACT_COMMIT_FAILED", "RUN", "Required artifact publication failed")
	}
	return committed
}
