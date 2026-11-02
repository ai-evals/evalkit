package engine

import (
	"context"
	"strings"
)

// ReadError is the only non-context error InspectArtifact and
// CompareArtifacts return. It never carries file-system, hash or parse
// detail. The CLI prints it as the "error" object of its refusal envelope.
// Input and Reason are omitted when empty, so inspect's V5 bytes hold.
type ReadError struct {
	Code   string `json:"code"`
	Scope  string `json:"scope"`
	Input  string `json:"input,omitempty"`
	Reason string `json:"reason,omitempty"`
}

const (
	// CodeArtifactVerificationFailed: the directory is missing, tampered,
	// not COMPLETED, or holds evidence evalkit could not have produced.
	CodeArtifactVerificationFailed = "ARTIFACT_VERIFICATION_FAILED"
	// CodeComparisonNotComparable: both inputs verify, but one retains no
	// measurement or the two measurements are not identical.
	CodeComparisonNotComparable = "COMPARISON_NOT_COMPARABLE"

	ScopeInspection = "INSPECTION"
	ScopeComparison = "COMPARISON"

	InputBaseline  = "baseline"
	InputCandidate = "candidate"

	ReasonMeasurementNotRetained = "MEASUREMENT_NOT_RETAINED"
	ReasonCasesDiffer            = "CASES_DIFFER"
	ReasonScorersDiffer          = "SCORERS_DIFFER"
)

// Error keeps V5's inspect message, "evalkit inspection: CODE".
func (e *ReadError) Error() string {
	msg := "evalkit " + strings.ToLower(e.Scope) + ": " + e.Code
	for _, detail := range []string{e.Input, e.Reason} {
		if detail != "" {
			msg += " " + detail
		}
	}
	return msg
}

// verifiedRun is the one read model over a published artifact. Only
// loadVerified constructs it, so every field is trusted: the artifact passed
// VerifyArtifact, and its verdict is a parsed Quality.
type verifiedRun struct {
	runID        string
	quality      Quality
	configDigest string
	retention    OutputRetention
	// measured is nil exactly when the artifact predates retained
	// measurement (v0.1-v0.4).
	measured *measuredRun
}

// measuredRun is a retained measurement contract plus its observations in
// one shape for every configuration version. loadVerified establishes:
//
//   - cases[i].ID == contract.CaseIDs[i], for every i;
//   - cases[i].Scorers[j].ID == contract.Scorers[j].ID, for every j;
//   - every observation satisfies validObservedScore;
//   - qualityOfCases(cases, contract.Scorers) is the run's quality.
//
// So cell (i, j) is cases[i].Scorers[j] under contract.Scorers[j].
type measuredRun struct {
	contract MeasurementDefinition
	cases    []V2CaseResult
}

// loadVerified is the one reader of a published artifact. A cancelled
// caller reads nothing and gets the bare context error. Every refusal is the
// given refusal with Code set to CodeArtifactVerificationFailed.
func loadVerified(ctx context.Context, dir string, refusal ReadError) (verifiedRun, error) {
	if err := ctx.Err(); err != nil {
		return verifiedRun{}, err
	}
	refusal.Code = CodeArtifactVerificationFailed
	o, err := VerifyArtifact(dir)
	if err != nil {
		return verifiedRun{}, &refusal
	}
	if err := ctx.Err(); err != nil {
		return verifiedRun{}, err
	}
	q, ok := o.CompletedQuality()
	if !ok {
		return verifiedRun{}, &refusal
	}
	run := verifiedRun{runID: o.RunID, quality: q, configDigest: o.ConfigDigest, retention: o.RetainedOutput}
	if o.Measurement == nil {
		return run, nil
	}
	cases := observedCases(o)
	for _, c := range cases {
		for _, obs := range c.Scorers {
			if !validObservedScore(obs) {
				return verifiedRun{}, &refusal
			}
		}
	}
	// For v2+ this repeats verifyMeasurementAccounting. For v1 it proves the
	// lift in observedCases reproduces the legacy verdict.
	if qualityOfCases(cases, o.Measurement.Scorers) != q.String() {
		return verifiedRun{}, &refusal
	}
	run.measured = &measuredRun{contract: *o.Measurement, cases: cases}
	return run, nil
}

// observedCases is the only place a reader branches on artifact shape. The
// v1 legacy case becomes a one-case list observed by the contract's sole
// scorer, the synthetic exact_match the v1 producer records. Its case state
// is SCORED when the scorer succeeded and ERRORED otherwise, which is
// evaluateCase's rule for a failed target. verifyMeasurementAccounting has
// already checked that the v1 contract has exactly one case and one scorer.
func observedCases(o Outcome) []V2CaseResult {
	if len(o.Cases) > 0 {
		return o.Cases
	}
	state := "ERRORED"
	if o.Case.ScorerStatus == "SUCCEEDED" {
		state = "SCORED"
	}
	// verifyRetainedEvidence lifts v1 artifacts whose measurement was
	// deleted, where the scorer has no retained ID.
	scorerID := ""
	if o.Measurement != nil {
		scorerID = o.Measurement.Scorers[0].ID
	}
	return []V2CaseResult{{
		ID:           o.Case.ID,
		State:        state,
		TargetStatus: o.Case.TargetStatus,
		Scorers: []ScorerObservation{{
			ID:     scorerID,
			Status: o.Case.ScorerStatus,
			Score:  o.Case.Score,
		}},
		Output: o.Case.Output,
	}}
}
