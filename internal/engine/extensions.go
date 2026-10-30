package engine

import (
	"context"
	"encoding/json"
	"math"
)

// ScorerInput is a logical immutable projection; its Clone method copies
// nested JSON before crossing each extension boundary.
type ScorerInput struct {
	CaseID   string
	ScorerID string
	Actual   json.RawMessage
	Expected json.RawMessage
}

func (s ScorerInput) Clone() ScorerInput {
	s.Actual = append(json.RawMessage(nil), s.Actual...)
	s.Expected = append(json.RawMessage(nil), s.Expected...)
	return s
}

type AssessmentCandidate struct {
	Kind   string
	Score  *float64
	Reason string
}

type RegisteredScorer struct {
	Implementation string
	Evaluate       func(context.Context, ScorerInput) (AssessmentCandidate, error)
}

func validCandidate(c AssessmentCandidate) bool {
	switch c.Kind {
	case "SCORED":
		return c.Score != nil && !math.IsNaN(*c.Score) && !math.IsInf(*c.Score, 0) && *c.Score >= 0 && *c.Score <= 1 && c.Reason == ""
	case "UNEVALUABLE":
		return c.Score == nil && (c.Reason == "EVIDENCE_MISSING" || c.Reason == "EVIDENCE_INSUFFICIENT" || c.Reason == "EVIDENCE_NOT_APPLICABLE")
	default:
		return false
	}
}
