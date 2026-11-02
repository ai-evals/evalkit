package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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

// OutputRetention is what a published run kept of its targets' stdout.
// encodeRunEvidence records it in run.json, verifyRetainedEvidence recomputes
// it, and inspect repeats it. Artifacts published before V7 have none.
type OutputRetention string

const (
	// OutputsRetained: every case whose target ran keeps its whole output.
	OutputsRetained OutputRetention = "RETAINED"
	// OutputsTrimmed: every such case keeps an output, and at least one keeps
	// only a prefix. Size and SHA-256 still cover the whole stream.
	OutputsTrimmed OutputRetention = "TRIMMED"
	// OutputsNone: no case keeps an output, because even their form, size
	// and SHA-256 would push run.json past maxEvidenceFileBytes.
	OutputsNone OutputRetention = "NONE"
)

func (r *OutputRetention) UnmarshalText(b []byte) error {
	switch v := OutputRetention(b); v {
	case OutputsRetained, OutputsTrimmed, OutputsNone:
		*r = v
		return nil
	}
	return errors.New("unknown output retention")
}

func retentionOf(cases []V2CaseResult) OutputRetention {
	kept, executed := false, false
	for _, c := range cases {
		executed = executed || c.TargetStatus != "SKIPPED"
		if c.Output != nil {
			if c.Output.Truncated {
				return OutputsTrimmed
			}
			kept = true
		}
	}
	if executed && !kept {
		return OutputsNone
	}
	return OutputsRetained
}

// retainedFieldOverhead over-estimates the bytes one output's "bytes" line
// adds to run.json beyond its base64 text, at any depth MarshalIndent uses.
// Only the first trim guess depends on it, never the final size check.
const retainedFieldOverhead = 64

// errRunEvidenceTooLarge: even without any retained output, run.json would
// exceed what VerifyArtifact reads. publish fails instead of publishing a
// COMPLETED artifact that inspect and compare must refuse.
var errRunEvidenceTooLarge = errors.New("run evidence exceeds the evidence read limit")

// mapOutputs returns a copy of o whose retained outputs are replaced by f's
// result, nil dropping one. It copies Case and Cases, so it never aliases
// the caller's results, and it changes nothing but outputs.
func mapOutputs(o Outcome, f func(TargetOutput) *TargetOutput) Outcome {
	if o.Case != nil {
		c := *o.Case
		if c.Output != nil {
			c.Output = f(*c.Output)
		}
		o.Case = &c
	}
	if o.Cases != nil {
		o.Cases = slices.Clone(o.Cases)
		for i := range o.Cases {
			if o.Cases[i].Output != nil {
				o.Cases[i].Output = f(*o.Cases[i].Output)
			}
		}
	}
	return o
}

func dropOutput(TargetOutput) *TargetOutput { return nil }

func keepPrefix(n int) func(TargetOutput) *TargetOutput {
	return func(t TargetOutput) *TargetOutput {
		k := min(len(t.Bytes), n)
		t.Bytes, t.Truncated = t.Bytes[:k:k], int64(k) < t.Size
		if k == 0 {
			t.Bytes = nil
		}
		return &t
	}
}

// withoutOutputs is report.json's projection of the run's cases. publish
// writes it and VerifyArtifact compares against it, so the report can never
// carry outputs. A nil list stays nil, so a v1 report still has no "cases".
func withoutOutputs(cases []V2CaseResult) []V2CaseResult {
	return mapOutputs(Outcome{Cases: cases}, dropOutput).Cases
}

// encodeRunEvidence is the one run.json encoder. It returns the bytes and the
// Outcome they encode, which differs from o only in retained output and its
// retention, and only when o would not fit maxEvidenceFileBytes. Each rung
// is tried only when the one before does not fit:
//
//  1. o as it is.
//  2. Every output cut to one common prefix length, the longest that fits,
//     down to none, which keeps form, size and SHA-256.
//  3. No output at all.
//  4. errRunEvidenceTooLarge.
func encodeRunEvidence(o Outcome) ([]byte, Outcome, error) {
	seal := func(t Outcome) ([]byte, Outcome, bool, error) {
		t.RetainedOutput = retentionOf(observedCases(t))
		b, err := json.MarshalIndent(t, "", "  ")
		b = append(b, '\n')
		return b, t, int64(len(b)) <= maxEvidenceFileBytes, err
	}
	if b, t, fits, err := seal(o); err != nil || fits {
		return b, t, err
	}
	skeleton, t, fits, err := seal(mapOutputs(o, keepPrefix(0)))
	if err != nil {
		return nil, Outcome{}, err
	}
	if fits {
		if n := longestFittingPrefix(o, maxEvidenceFileBytes-int64(len(skeleton))); n > 0 {
			if b, cut, fits, err := seal(mapOutputs(o, keepPrefix(n))); err != nil || fits {
				return b, cut, err
			}
		}
		return skeleton, t, nil
	}
	if b, t, fits, err := seal(mapOutputs(o, dropOutput)); err != nil || fits {
		return b, t, err
	}
	return nil, Outcome{}, errRunEvidenceTooLarge
}

// longestFittingPrefix is the longest common prefix length whose "bytes"
// lines, over-estimated, add at most room bytes to the rung-2 skeleton.
func longestFittingPrefix(o Outcome, room int64) int {
	var held []int
	for _, c := range observedCases(o) {
		if c.Output != nil && len(c.Output.Bytes) > 0 {
			held = append(held, len(c.Output.Bytes))
		}
	}
	cost := func(n int) int64 {
		var total int64
		for _, h := range held {
			total += int64(base64.StdEncoding.EncodedLen(min(h, n))) + retainedFieldOverhead
		}
		return total
	}
	lo, hi := 0, slices.Max(append(held, 0))
	for lo < hi {
		if mid := lo + (hi-lo+1)/2; cost(mid) <= room {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// verifyRetainedEvidence refuses retained output and attribution evalkit could
// not have produced. An artifact published before V7 has no retention, so
// it must carry neither, and then this check accepts it unchanged. It needs
// no measurement, because historical fixtures are V7 artifacts with theirs
// deleted.
func verifyRetainedEvidence(o Outcome) error {
	cases := observedCases(o)
	refused := errors.New("retained evidence evalkit could not have produced")
	if o.RetainedOutput == "" {
		if slices.ContainsFunc(cases, func(c V2CaseResult) bool { return c.Output != nil }) ||
			slices.ContainsFunc(o.Diagnostics, func(d Diagnostic) bool { return d.Subject != nil }) {
			return refused
		}
		return nil
	}
	if retentionOf(cases) != o.RetainedOutput {
		return refused
	}
	for _, c := range cases {
		executed := c.TargetStatus != "SKIPPED"
		if c.Output == nil {
			if executed && o.RetainedOutput != OutputsNone {
				return refused
			}
		} else if !executed || !possibleOutput(*c.Output, c.TargetStatus) {
			return refused
		}
	}
	for _, d := range o.Diagnostics {
		if !attributedTo(d, cases) {
			return refused
		}
	}
	return nil
}

// possibleOutput holds TargetOutput's invariants. A truncated JSON or
// MALFORMED form cannot be recomputed from its prefix, so it is trusted.
func possibleOutput(t TargetOutput, targetStatus string) bool {
	kept := int64(len(t.Bytes))
	sum, err := hex.DecodeString(t.SHA256)
	switch {
	case !t.Form.valid(), err != nil, len(sum) != sha256.Size, hex.EncodeToString(sum) != t.SHA256,
		kept > t.Size, kept > maxTargetBytes, t.Truncated != (kept < t.Size),
		(t.Form == OutputEmpty) != (t.Size == 0), (t.Form == OutputOversized) != (t.Size > maxTargetBytes),
		targetStatus == "SUCCEEDED" && t.Form != OutputJSON:
		return false
	}
	return t.Truncated || sha256.Sum256(t.Bytes) == [sha256.Size]byte(sum) && classifyOutput(t.Size, t.Bytes) == t.Form
}

// attributedTo reports whether d names a real case of cases, and for SCORER
// scope a scorer that observed it.
func attributedTo(d Diagnostic, cases []V2CaseResult) bool {
	s := d.Subject
	if s == nil || s.CaseIndex < 0 || s.CaseIndex >= len(cases) || cases[s.CaseIndex].ID != s.CaseID {
		return false
	}
	switch d.Scope {
	case "CASE":
		return s.ScorerID == ""
	case "SCORER":
		return s.ScorerID != "" && slices.ContainsFunc(cases[s.CaseIndex].Scorers, func(o ScorerObservation) bool { return o.ID == s.ScorerID })
	}
	return false
}
