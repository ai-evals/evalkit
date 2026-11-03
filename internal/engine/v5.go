package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
)

// exitTempFail is EX_TEMPFAIL from sysexits.h, the one exit status a command
// scorer uses to ask for another attempt.
const exitTempFail = 75

// commandAssessor runs a version 5 command scorer under the target's
// isolation. It retains nothing a scorer writes except a validated score or
// reason, so scorer output cannot reach diagnostics or artifacts.
func commandAssessor(command string, args []string) assessor {
	return func(ctx context.Context, in ScorerInput) (AssessmentCandidate, error) {
		var line bytes.Buffer
		enc := json.NewEncoder(&line)
		enc.SetEscapeHTML(false)
		err := enc.Encode(struct {
			CaseID   string          `json:"case_id"`
			ScorerID string          `json:"scorer_id"`
			Input    json.RawMessage `json:"input"`
			Expected json.RawMessage `json:"expected"`
			Output   json.RawMessage `json:"output"`
		}{in.CaseID, in.ScorerID, in.Input, in.Expected, in.Actual})
		if err != nil {
			return AssessmentCandidate{}, errors.New("cannot encode scorer request")
		}
		var stdout boundedBuffer
		if err := runIsolated(ctx, "evalkit-scorer-", command, args, line.Bytes(), &stdout); err != nil {
			if ctx.Err() != nil {
				return AssessmentCandidate{}, ctx.Err()
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == exitTempFail {
				return AssessmentCandidate{}, &RetryableScorerError{}
			}
			return AssessmentCandidate{}, errors.New("scorer process failed")
		}
		if stdout.overflow {
			return AssessmentCandidate{}, nil
		}
		return parseScorerResult(stdout.buf.Bytes()), nil
	}
}

// parseScorerResult maps a scorer's stdout to a candidate. Anything but one
// object with exactly one of score or unevaluable yields the zero candidate,
// which validCandidate rejects.
func parseScorerResult(stdout []byte) AssessmentCandidate {
	var fields map[string]json.RawMessage
	if decodeOne(stdout, &fields) != nil || len(fields) != 1 {
		return AssessmentCandidate{}
	}
	if raw, ok := fields["score"]; ok {
		var score *float64
		if decodeOne(raw, &score) != nil || score == nil {
			return AssessmentCandidate{}
		}
		return AssessmentCandidate{Kind: "SCORED", Score: score}
	}
	if raw, ok := fields["unevaluable"]; ok {
		var reason *string
		if decodeOne(raw, &reason) != nil || reason == nil {
			return AssessmentCandidate{}
		}
		return AssessmentCandidate{Kind: "UNEVALUABLE", Reason: *reason}
	}
	return AssessmentCandidate{}
}
