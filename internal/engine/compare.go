package engine

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CellState is the state of one (case, scorer) cell of a verified run.
type CellState uint8

const (
	CellPassing CellState = iota
	CellFailing
	CellUnknown
	cellStateCount
)

var cellStateNames = [cellStateCount]string{
	CellPassing: "PASSING",
	CellFailing: "FAILING",
	CellUnknown: "UNKNOWN",
}

func (s CellState) String() string               { return enumName(s, cellStateNames[:]) }
func (s CellState) MarshalText() ([]byte, error) { return enumText(s, cellStateNames[:]) }

var cellOfQuality = [qualityCount]CellState{
	QualityPass:          CellPassing,
	QualityFail:          CellFailing,
	QualityIndeterminate: CellUnknown,
}

// cellStateOf is the verdict one cell would get alone under a required
// scorer with the stored threshold. Deriving it from qualityOfCases, instead
// of restating the rule, means compare cannot disagree with the verdict it
// reports.
func cellStateOf(caseState string, obs ScorerObservation, threshold float64) CellState {
	verdict := qualityOfCases(
		[]V2CaseResult{{State: caseState, Scorers: []ScorerObservation{obs}}},
		[]V2Scorer{{Required: true, Threshold: threshold}},
	)
	q, ok := parseQuality(verdict)
	if !ok {
		return CellUnknown
	}
	return cellOfQuality[q]
}

// Transition classifies one cell from baseline to candidate.
type Transition uint8

const (
	TransitionUnchanged Transition = iota
	TransitionImproved
	TransitionRegressed
	TransitionLostEvidence
	TransitionGainedEvidence
	transitionCount
)

var transitionNames = [transitionCount]string{
	TransitionUnchanged:      "UNCHANGED",
	TransitionImproved:       "IMPROVED",
	TransitionRegressed:      "REGRESSED",
	TransitionLostEvidence:   "LOST_EVIDENCE",
	TransitionGainedEvidence: "GAINED_EVIDENCE",
}

func (t Transition) String() string               { return enumName(t, transitionNames[:]) }
func (t Transition) MarshalText() ([]byte, error) { return enumText(t, transitionNames[:]) }

// transitionTable[from][to] is the only place a transition is decided.
var transitionTable = [cellStateCount][cellStateCount]Transition{
	CellPassing: {CellPassing: TransitionUnchanged, CellFailing: TransitionRegressed, CellUnknown: TransitionLostEvidence},
	CellFailing: {CellPassing: TransitionImproved, CellFailing: TransitionUnchanged, CellUnknown: TransitionLostEvidence},
	CellUnknown: {CellPassing: TransitionGainedEvidence, CellFailing: TransitionGainedEvidence, CellUnknown: TransitionUnchanged},
}

// Comparison is also the wire schema of `evalkit compare` and
// sdk.Comparison.JSON(). Both marshal this one value, so they cannot drift.
type Comparison struct {
	SchemaVersion  int              `json:"schema_version"`
	Recommendation Recommendation   `json:"recommendation"`
	Baseline       ComparedRun      `json:"baseline"`
	Candidate      ComparedRun      `json:"candidate"`
	Counts         TransitionCounts `json:"counts"`
	// Changes lists, case-major in dataset order, each cell whose state or
	// score changed. Never nil.
	Changes []CellChange `json:"changes"`
}

type ComparedRun struct {
	RunID   string  `json:"run_id"`
	Quality Quality `json:"quality"`
}

type CellChange struct {
	CaseID     string     `json:"case_id"`
	ScorerID   string     `json:"scorer_id"`
	Required   bool       `json:"required"`
	Transition Transition `json:"transition"`
	From       Endpoint   `json:"from"`
	To         Endpoint   `json:"to"`
}

// Endpoint is one side of a CellChange. CaseState explains an UNKNOWN cell
// whose own scorer SUCCEEDED: a required scorer failed on the same case.
// Output and Diagnostics say why the cell moved. They are omitted when that
// side retained none, which is every artifact published before V7, so two
// such artifacts compare to their V6 bytes.
type Endpoint struct {
	Cell      CellState   `json:"cell"`
	CaseState string      `json:"case_state"`
	Status    string      `json:"status"`
	Score     *float64    `json:"score"`
	Output    *OutputView `json:"output,omitempty"`
	// Diagnostics are the case's CASE diagnostics and this cell's scorer's
	// SCORER diagnostics on this side, in run order.
	Diagnostics []CellDiagnostic `json:"diagnostics,omitempty"`
}

// outputPreviewBytes bounds the text compare prints per endpoint. Fully
// HTML-escaped it is still under 6.2 KB.
const outputPreviewBytes = 1024

// OutputView is compare's bounded view of a retained TargetOutput; run.json
// keeps the bytes. Equal Size and SHA256 mean the target wrote the same
// stream, even when truncated. Complete is true exactly when Preview is
// every byte the target wrote, unchanged.
type OutputView struct {
	Form      OutputForm `json:"form"`
	Size      int64      `json:"size"`
	SHA256    string     `json:"sha256"`
	Truncated bool       `json:"truncated"`
	Complete  bool       `json:"complete"`
	Preview   string     `json:"preview"`
}

// CellDiagnostic is a diagnostic as one cell shows it. The change already
// names the case and the scorer.
type CellDiagnostic struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

// TransitionCounts is indexed by Transition and counts every cell. It
// marshals with every transition name as a key, in enum order, zeros
// included, so `jq .counts.REGRESSED` is never null.
type TransitionCounts [transitionCount]int

func (c TransitionCounts) MarshalJSON() ([]byte, error) {
	b := []byte{'{'}
	for t := range transitionCount {
		if t > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendQuote(b, t.String())
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(c[t]), 10)
	}
	return append(b, '}'), nil
}

// CompareArtifacts reads two published artifacts and compares them cell by
// cell. It executes nothing and writes nothing. It returns the bare context
// error or a *ReadError and nothing else. Verification of both inputs
// outranks comparability.
func CompareArtifacts(ctx context.Context, baselineDir, candidateDir string) (Comparison, error) {
	baseline, err := loadVerified(ctx, baselineDir, ReadError{Scope: ScopeComparison, Input: InputBaseline})
	if err != nil {
		return Comparison{}, err
	}
	candidate, err := loadVerified(ctx, candidateDir, ReadError{Scope: ScopeComparison, Input: InputCandidate})
	if err != nil {
		return Comparison{}, err
	}
	return compareRuns(baseline, candidate)
}

// compareRuns is the whole comparison policy, a pure function over two
// verified runs. Its only error is a COMPARISON_NOT_COMPARABLE *ReadError.
func compareRuns(baseline, candidate verifiedRun) (Comparison, error) {
	if err := refuseIncomparable(baseline, candidate); err != nil {
		return Comparison{}, err
	}
	contract := baseline.measured.contract
	out := Comparison{
		SchemaVersion:  1,
		Recommendation: candidate.quality.Recommendation(),
		Baseline:       ComparedRun{RunID: baseline.runID, Quality: baseline.quality},
		Candidate:      ComparedRun{RunID: candidate.runID, Quality: candidate.quality},
		Changes:        []CellChange{},
	}
	for i, caseID := range contract.CaseIDs {
		b, c := baseline.measured.cases[i], candidate.measured.cases[i]
		for j, sc := range contract.Scorers {
			from := endpointOf(b.State, b.Scorers[j], sc.Threshold)
			to := endpointOf(c.State, c.Scorers[j], sc.Threshold)
			t := transitionTable[from.Cell][to.Cell]
			out.Counts[t]++
			if from.Cell != to.Cell || !sameScore(from.Score, to.Score) {
				out.Changes = append(out.Changes, CellChange{CaseID: caseID, ScorerID: sc.ID, Required: sc.Required, Transition: t,
					From: baseline.measured.explain(i, sc.ID, from), To: candidate.measured.explain(i, sc.ID, to)})
			}
		}
	}
	return out, nil
}

// refuseIncomparable compares the whole retained MeasurementDefinition, so
// custom implementation identity is part of the key and a field added to
// V2Scorer later joins it without an edit here. config_digest is not part of
// the key: it hashes the target, which is what a comparison varies.
func refuseIncomparable(baseline, candidate verifiedRun) error {
	refusal := &ReadError{Code: CodeComparisonNotComparable, Scope: ScopeComparison}
	switch {
	case baseline.measured == nil:
		refusal.Input, refusal.Reason = InputBaseline, ReasonMeasurementNotRetained
	case candidate.measured == nil:
		refusal.Input, refusal.Reason = InputCandidate, ReasonMeasurementNotRetained
	case !slices.Equal(baseline.measured.contract.CaseIDs, candidate.measured.contract.CaseIDs):
		refusal.Reason = ReasonCasesDiffer
	case !reflect.DeepEqual(baseline.measured.contract, candidate.measured.contract):
		refusal.Reason = ReasonScorersDiffer
	default:
		return nil
	}
	return refusal
}

// endpointOf copies the score, so a Comparison never aliases the read model.
func endpointOf(caseState string, obs ScorerObservation, threshold float64) Endpoint {
	e := Endpoint{Cell: cellStateOf(caseState, obs, threshold), CaseState: caseState, Status: obs.Status}
	if obs.Score != nil {
		s := *obs.Score
		e.Score = &s
	}
	return e
}

// explain attaches case i's retained evidence for scorer's cell. It runs
// after the listing decision, so evidence never decides what is listed.
func (m *measuredRun) explain(i int, scorer string, e Endpoint) Endpoint {
	if out := m.cases[i].Output; out != nil {
		preview, complete := previewOf(out.Bytes)
		e.Output = &OutputView{Form: out.Form, Size: out.Size, SHA256: out.SHA256, Truncated: out.Truncated,
			Complete: complete && !out.Truncated, Preview: preview}
	}
	for _, d := range m.diagnostics[i] {
		if d.Scope == "CASE" || d.Subject.ScorerID == scorer {
			e.Diagnostics = append(e.Diagnostics, CellDiagnostic{Code: d.Code, Scope: d.Scope, Message: d.Message})
		}
	}
	return e
}

// previewOf is the first outputPreviewBytes of b as valid UTF-8, without a
// rune the cut split. whole reports whether that is all of b, unchanged.
func previewOf(b []byte) (preview string, whole bool) {
	p := b[:min(len(b), outputPreviewBytes)]
	if len(p) < len(b) {
		for k := 1; k < utf8.UTFMax && k <= len(p); k++ {
			if utf8.RuneStart(p[len(p)-k]) {
				if !utf8.FullRune(p[len(p)-k:]) {
					p = p[:len(p)-k]
				}
				break
			}
		}
	}
	preview = strings.ToValidUTF8(string(p), "\uFFFD")
	return preview, len(p) == len(b) && preview == string(b)
}

func sameScore(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
