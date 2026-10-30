package engine

import (
	"context"
	"errors"
	"fmt"
)

// MeasurementDefinition records the required scorer contract needed to
// independently check quality accounting for newly committed artifacts.
// It does not authenticate an artifact signer or remote model implementation.
type MeasurementDefinition struct {
	SchemaVersion int        `json:"schema_version"`
	CaseIDs       []string   `json:"case_ids"`
	Scorers       []V2Scorer `json:"scorers"`
}

func measurementForCases(cases []V2Case, scorers []V2Scorer) *MeasurementDefinition {
	m := &MeasurementDefinition{SchemaVersion: 1, Scorers: append([]V2Scorer(nil), scorers...)}
	for _, c := range cases {
		m.CaseIDs = append(m.CaseIDs, c.ID)
	}
	return m
}

type Inspection struct {
	SchemaVersion         int    `json:"schema_version"`
	RunID                 string `json:"run_id"`
	State                 string `json:"state"`
	Quality               string `json:"quality"`
	ConfigDigest          string `json:"config_digest"`
	Integrity             string `json:"integrity"`
	MeasurementAccounting string `json:"measurement_accounting"`
	AuthenticatedAuthor   bool   `json:"authenticated_author"`
}

type InspectionError struct{ Code string }

func (e *InspectionError) Error() string { return fmt.Sprintf("evalkit inspection: %s", e.Code) }

// InspectArtifact never invokes a target, scorer, provider or credential
// resolver. It provides only evidence claims actually established by reading.
func InspectArtifact(ctx context.Context, dir string) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	out, err := VerifyArtifact(dir)
	if err != nil {
		return Inspection{}, &InspectionError{Code: "ARTIFACT_VERIFICATION_FAILED"}
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	if out.Quality == nil {
		return Inspection{}, errors.New("committed quality missing")
	}
	status := "NOT_RETAINED"
	if out.Measurement != nil {
		status = "REPORTED_DEFINITION_CONSISTENT"
	}
	return Inspection{
		SchemaVersion: 1, RunID: out.RunID, State: out.State, Quality: *out.Quality,
		ConfigDigest: out.ConfigDigest, Integrity: "SELF_CONSISTENT",
		MeasurementAccounting: status, AuthenticatedAuthor: false,
	}, nil
}

// verifyMeasurementAccounting checks the self-described scorer policy and
// stored outcomes. It cannot verify external model authenticity or recreate
// raw inputs that weren't retained.
func verifyMeasurementAccounting(o Outcome) error {
	if o.Measurement == nil {
		return nil
	} // Historical v0.1-v0.4 artifacts
	m := o.Measurement
	if m.SchemaVersion != 1 || len(m.CaseIDs) == 0 || len(m.Scorers) == 0 {
		return errors.New("unsupported measurement evidence")
	}
	if len(o.Cases) > 0 {
		if len(m.CaseIDs) != len(o.Cases) {
			return errors.New("measurement case count mismatch")
		}
		for i, c := range o.Cases {
			if c.ID != m.CaseIDs[i] || len(c.Scorers) != len(m.Scorers) {
				return errors.New("measurement case/scorer identity mismatch")
			}
			for j, sc := range c.Scorers {
				if sc.ID != m.Scorers[j].ID || !validObservedScore(sc) {
					return errors.New("invalid scorer observation")
				}
			}
		}
		if o.Quality == nil || qualityOfCases(o.Cases, m.Scorers) != *o.Quality {
			return errors.New("measurement verdict mismatch")
		}
		return nil
	}
	if o.Case == nil || len(m.CaseIDs) != 1 || m.CaseIDs[0] != o.Case.ID || len(m.Scorers) != 1 {
		return errors.New("legacy measurement mismatch")
	}
	sc := m.Scorers[0]
	if !sc.Required || sc.Threshold < 0 || sc.Threshold > 1 {
		return errors.New("invalid required scorer contract")
	}
	quality := "INDETERMINATE"
	if o.Case.ScorerStatus == "SUCCEEDED" && o.Case.Score != nil {
		if *o.Case.Score < 0 || *o.Case.Score > 1 {
			return errors.New("invalid score")
		}
		quality = "PASS"
		if *o.Case.Score < sc.Threshold {
			quality = "FAIL"
		}
	}
	if o.Quality == nil || quality != *o.Quality {
		return errors.New("legacy verdict mismatch")
	}
	return nil
}

func validObservedScore(sc ScorerObservation) bool {
	switch sc.Status {
	case "SUCCEEDED":
		return sc.Score != nil && *sc.Score >= 0 && *sc.Score <= 1
	case "ERRORED", "SKIPPED", "UNEVALUABLE":
		return sc.Score == nil
	default:
		return false
	}
}
