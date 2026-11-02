package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ai-evals/evalkit/internal/engine"
)

// Inspector has no access to Run execution or scorer registration.
type Inspector struct{}

func NewInspector() Inspector { return Inspector{} }

type Inspection struct{ record engine.Inspection }

func (i Inspection) RunID() string                 { return i.record.RunID }
func (i Inspection) Quality() QualityVerdict       { return QualityVerdict(i.record.Quality) }
func (i Inspection) Integrity() string             { return i.record.Integrity }
func (i Inspection) MeasurementAccounting() string { return i.record.MeasurementAccounting }
func (i Inspection) JSON() []byte                  { out, _ := json.Marshal(i.record); return out }

// Inspect returns a *ReadError or the bare context error.
func (_ Inspector) Inspect(ctx context.Context, artifactDir string) (Inspection, error) {
	record, err := engine.InspectArtifact(ctx, artifactDir)
	if err != nil {
		return Inspection{}, publicReadError(err)
	}
	return Inspection{record: record}, nil
}

// ReadError is the refusal Inspector.Inspect and Comparer.Compare return
// when their inputs cannot support an answer. Cancellation is never a
// ReadError: those methods return context.Canceled or
// context.DeadlineExceeded itself.
type ReadError struct {
	Code   string // a Code* constant
	Scope  string // ScopeInspection or ScopeComparison
	Input  string // for ScopeComparison, the Input* the refusal concerns, if one
	Reason string // for CodeComparisonNotComparable, a Reason* constant
}

func (e *ReadError) Error() string { return (*engine.ReadError)(e).Error() }

const (
	CodeArtifactVerificationFailed = engine.CodeArtifactVerificationFailed
	CodeComparisonNotComparable    = engine.CodeComparisonNotComparable

	ScopeInspection = engine.ScopeInspection
	ScopeComparison = engine.ScopeComparison

	InputBaseline  = engine.InputBaseline
	InputCandidate = engine.InputCandidate

	ReasonMeasurementNotRetained = engine.ReasonMeasurementNotRetained
	ReasonCasesDiffer            = engine.ReasonCasesDiffer
	ReasonScorersDiffer          = engine.ReasonScorersDiffer
)

// publicReadError is the only place the SDK names an engine error type. The
// conversion compiles only while both ReadError structs have the same fields.
func publicReadError(err error) error {
	var refused *engine.ReadError
	if errors.As(err, &refused) {
		public := ReadError(*refused)
		return &public
	}
	return err
}
