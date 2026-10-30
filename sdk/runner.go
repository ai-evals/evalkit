// Package sdk provides a narrow, versioned v0.4 Go SDK over evalkit's
// authoritative evaluation engine. In-process scorers are trusted code, not
// sandboxed plugins.
package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ai-evals/evalkit/internal/engine"
)

type RunState string
type QualityVerdict string

type Request struct {
	ConfigJSON  []byte
	ArtifactDir string
}

type ScorerInput struct {
	caseID   string
	scorerID string
	actual   []byte
	expected []byte
}

func (i ScorerInput) CaseID() string       { return i.caseID }
func (i ScorerInput) ScorerID() string     { return i.scorerID }
func (i ScorerInput) ActualJSON() []byte   { return append([]byte(nil), i.actual...) }
func (i ScorerInput) ExpectedJSON() []byte { return append([]byte(nil), i.expected...) }

type Assessment struct {
	kind   string
	score  *float64
	reason string
}

func Scored(v float64) Assessment          { return Assessment{kind: "SCORED", score: &v} }
func Unevaluable(reason string) Assessment { return Assessment{kind: "UNEVALUABLE", reason: reason} }

const (
	ReasonEvidenceMissing      = "EVIDENCE_MISSING"
	ReasonEvidenceInsufficient = "EVIDENCE_INSUFFICIENT"
	ReasonNotApplicable        = "EVIDENCE_NOT_APPLICABLE"
)

// RetryableError is an explicitly typed scorer attempt failure. Its public
// message is static; arbitrary provider causes are not copied into artifacts.
type RetryableError struct{}

func (RetryableError) Error() string { return "transient scorer operation failed" }

func Retryable() error { return RetryableError{} }

type Scorer interface {
	Evaluate(context.Context, ScorerInput) (Assessment, error)
}

type Registration struct {
	ID             string
	Implementation string
	Scorer         Scorer
}

type Runner struct {
	extensions map[string]engine.RegisteredScorer
}

func NewRunner(regs ...Registration) (*Runner, error) {
	r := &Runner{extensions: make(map[string]engine.RegisteredScorer, len(regs))}
	for _, reg := range regs {
		if reg.ID == "" || reg.Implementation == "" || reg.Scorer == nil {
			return nil, errors.New("invalid scorer registration")
		}
		if _, duplicate := r.extensions[reg.ID]; duplicate {
			return nil, errors.New("duplicate scorer registration")
		}
		// A mutex serializes calls on a shared scorer instance by default.
		// The SDK does not invent thread safety for user Go implementations.
		var mu sync.Mutex
		scorer := reg.Scorer
		r.extensions[reg.ID] = engine.RegisteredScorer{
			Implementation: reg.Implementation,
			Evaluate: func(ctx context.Context, input engine.ScorerInput) (engine.AssessmentCandidate, error) {
				mu.Lock()
				defer mu.Unlock()
				a, err := scorer.Evaluate(ctx, ScorerInput{
					caseID: input.CaseID, scorerID: input.ScorerID,
					actual: append([]byte(nil), input.Actual...), expected: append([]byte(nil), input.Expected...),
				})
				if err != nil {
					var transient RetryableError
					if errors.As(err, &transient) {
						return engine.AssessmentCandidate{}, &engine.RetryableScorerError{}
					}
					return engine.AssessmentCandidate{}, errors.New("scorer operation failed")
				}
				var score *float64
				if a.score != nil {
					v := *a.score
					score = &v
				}
				return engine.AssessmentCandidate{Kind: a.kind, Score: score, Reason: a.reason}, nil
			},
		}
	}
	return r, nil
}

type Result struct{ outcome engine.Outcome }

func (r Result) RunID() string   { return r.outcome.RunID }
func (r Result) State() RunState { return RunState(r.outcome.State) }
func (r Result) Quality() (QualityVerdict, bool) {
	if r.outcome.Quality == nil {
		return "", false
	}
	return QualityVerdict(*r.outcome.Quality), true
}
func (r Result) JSON() []byte { b, _ := json.Marshal(r.outcome); return b }

type RunError struct {
	Code  string
	state string
}

func (e *RunError) Error() string { return fmt.Sprintf("evalkit run %s: %s", e.state, e.Code) }
func (e *RunError) Is(target error) bool {
	return (e.Code == "RUN_DEADLINE_EXCEEDED" && target == context.DeadlineExceeded) || (e.Code == "RUN_CANCELLED" && target == context.Canceled)
}

func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	if r == nil {
		return Result{}, errors.New("nil runner")
	}
	// Freeze request bytes at invocation entry. Extensions cannot alter the
	// executing config by mutating the original Go []byte.
	raw := append([]byte(nil), req.ConfigJSON...)
	o := engine.RunJSONWithScorers(ctx, raw, req.ArtifactDir, r.extensions)
	result := Result{outcome: o}
	if o.State == "COMPLETED" {
		return result, nil
	}
	code := "RUN_FAILED"
	if len(o.Diagnostics) > 0 {
		code = o.Diagnostics[len(o.Diagnostics)-1].Code
	}
	return result, &RunError{Code: code, state: o.State}
}
