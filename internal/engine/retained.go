package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"slices"
)

// maxRetainedOutputBytes bounds the target stdout one run keeps over all its
// cases. retentionAllowance splits it evenly by case count, so what a case
// keeps never depends on which worker finished first.
const maxRetainedOutputBytes = 8 << 20

// OutputForm is the shape of what a target wrote to stdout.
type OutputForm string

const (
	// OutputJSON is exactly one JSON value under decodeJSONValue, the test
	// execute applies, so every SUCCEEDED target has it. A target that exits
	// non-zero can print one too.
	OutputJSON OutputForm = "JSON"
	// OutputMalformed is any other nonempty output within maxTargetBytes.
	OutputMalformed OutputForm = "MALFORMED"
	// OutputOversized is more than maxTargetBytes, so it is always truncated.
	OutputOversized OutputForm = "OVERSIZED"
	// OutputEmpty is zero bytes, including a target that never started.
	OutputEmpty OutputForm = "EMPTY"
)

var outputForms = []OutputForm{OutputJSON, OutputMalformed, OutputOversized, OutputEmpty}

func (f OutputForm) valid() bool { return slices.Contains(outputForms, f) }

// UnmarshalText refuses an unknown form, so a decoded artifact never holds
// one. A missing "form" key still decodes to "", which checkOutput refuses.
func (f *OutputForm) UnmarshalText(b []byte) error {
	if form := OutputForm(b); form.valid() {
		*f = form
		return nil
	}
	return errors.New("unknown output form")
}

// TargetOutput is one case's retained target stdout, in run.json only.
// Size and SHA256 cover every byte the target wrote, so two outputs compare
// exactly even when both are truncated. Bytes is a prefix of that stream and
// marshals as base64: nothing in it is HTML-escaped or re-indented, invalid
// UTF-8 survives, and a map[string]any round trip leaves it unchanged.
type TargetOutput struct {
	Form      OutputForm `json:"form"`
	Size      int64      `json:"size"`
	SHA256    string     `json:"sha256"`
	Truncated bool       `json:"truncated,omitempty"`
	Bytes     []byte     `json:"bytes,omitempty"`
}

// DiagnosticSubject names the case, and for SCORER scope the scorer, that a
// diagnostic is about. CaseIndex is the key: case IDs may be "" or
// duplicated, and those are exactly the cases DATASET_INVALID_CASE reports.
// Scorer IDs are validated nonempty and unique, so omitempty is unambiguous.
type DiagnosticSubject struct {
	CaseIndex int    `json:"case_index"`
	CaseID    string `json:"case_id"`
	ScorerID  string `json:"scorer_id,omitempty"`
}

// caseDiagnostic and scorerDiagnostic are the only constructors of
// attributed diagnostics, so Scope and Subject always agree.
func caseDiagnostic(code, message string, index int, caseID string) Diagnostic {
	return Diagnostic{Code: code, Scope: "CASE", Message: message, Subject: &DiagnosticSubject{CaseIndex: index, CaseID: caseID}}
}

func scorerDiagnostic(code, message string, index int, caseID, scorerID string) Diagnostic {
	return Diagnostic{Code: code, Scope: "SCORER", Message: message, Subject: &DiagnosticSubject{CaseIndex: index, CaseID: caseID, ScorerID: scorerID}}
}

// targetStdout is what one execute call learned about stdout. The zero value
// means nothing was read, which is what execute returns on cancellation.
type targetStdout struct {
	captured bool
	head     []byte // the first min(size, maxTargetBytes) bytes; aliases execute's buffer
	size     int64
	sum      [sha256.Size]byte
}

// stdoutDigest counts and hashes every byte the target writes, past the
// bounded buffer's limit too. Like boundedBuffer it never short-writes, so
// the child never blocks.
type stdoutDigest struct {
	n int64
	h hash.Hash
}

func (d *stdoutDigest) Write(p []byte) (int, error) {
	if d.h == nil {
		d.h = sha256.New()
	}
	d.h.Write(p)
	d.n += int64(len(p))
	return len(p), nil
}

// capture must run only once no stdout copier can still write: before the
// process started, or after cmd.Wait returned.
func (d *stdoutDigest) capture(head []byte) targetStdout {
	s := targetStdout{captured: true, head: head, size: d.n, sum: sha256.Sum256(nil)}
	if d.h != nil {
		d.h.Sum(s.sum[:0])
	}
	return s
}

// retentionAllowance is how many bytes of one case's output a run of n
// cases keeps: all of it up to 8 cases, about 82 KiB at 100 cases.
func retentionAllowance(n int) int {
	return min(maxTargetBytes, maxRetainedOutputBytes/max(n, 1))
}

// retainOutput is nil exactly for an uncaptured execution. It copies, so the
// worker's 1 MiB capture buffer is released instead of pinned by a subslice.
func retainOutput(s targetStdout, allowance int) *TargetOutput {
	if !s.captured {
		return nil
	}
	k := min(len(s.head), allowance)
	out := &TargetOutput{Form: classifyOutput(s.size, s.head), Size: s.size, SHA256: hex.EncodeToString(s.sum[:]), Truncated: int64(k) < s.size}
	if k > 0 {
		out.Bytes = append([]byte(nil), s.head[:k]...)
	}
	return out
}

// classifyOutput is the one definition of an output's form. head must be the
// whole stream whenever 0 < size <= maxTargetBytes.
func classifyOutput(size int64, head []byte) OutputForm {
	switch {
	case size == 0:
		return OutputEmpty
	case size > maxTargetBytes:
		return OutputOversized
	}
	if _, err := decodeJSONValue(head); err != nil {
		return OutputMalformed
	}
	return OutputJSON
}
