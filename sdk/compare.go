package sdk

import (
	"context"
	"encoding/json"

	"github.com/ai-evals/evalkit/internal/engine"
)

// Comparer compares two published artifacts. Like Inspector it only reads.
type Comparer struct{}

func NewComparer() Comparer { return Comparer{} }

// ComparisonRequest names its two directories so they cannot be swapped
// silently.
type ComparisonRequest struct {
	BaselineDir  string
	CandidateDir string
}

// Compare reads both artifacts and compares them cell by cell, one cell per
// case and scorer. It executes nothing and writes nothing. It returns a
// *ReadError with Scope ScopeComparison, or the bare context error.
func (Comparer) Compare(ctx context.Context, req ComparisonRequest) (Comparison, error) {
	record, err := engine.CompareArtifacts(ctx, req.BaselineDir, req.CandidateDir)
	if err != nil {
		return Comparison{}, publicReadError(err)
	}
	return Comparison{record: record}, nil
}

// Comparison is a read-only result. JSON() is the bytes `evalkit compare`
// prints, without the trailing newline.
type Comparison struct{ record engine.Comparison }

// Recommendation follows the candidate's quality alone. Optional
// regressions are in Changes but never change it.
func (c Comparison) Recommendation() Recommendation {
	return Recommendation(c.record.Recommendation.String())
}

func (c Comparison) Baseline() ComparedRun  { return comparedRun(c.record.Baseline) }
func (c Comparison) Candidate() ComparedRun { return comparedRun(c.record.Candidate) }

// Count reports how many cells, over all cells and not only Changes, took
// transition t. It is 0 for a value that is not a Transition constant.
func (c Comparison) Count(t Transition) int {
	for i, n := range c.record.Counts {
		if engine.Transition(i).String() == string(t) {
			return n
		}
	}
	return 0
}

// Changes returns, in dataset order, each cell whose state or score changed.
// A score-only change has Transition Unchanged.
func (c Comparison) Changes() []Change {
	out := make([]Change, 0, len(c.record.Changes))
	for _, ch := range c.record.Changes {
		out = append(out, Change{
			CaseID: ch.CaseID, ScorerID: ch.ScorerID, Required: ch.Required,
			Transition: Transition(ch.Transition.String()), From: endpoint(ch.From), To: endpoint(ch.To),
		})
	}
	return out
}

func (c Comparison) JSON() []byte { out, _ := json.Marshal(c.record); return out }

type ComparedRun struct {
	RunID   string
	Quality QualityVerdict
}

type Change struct {
	CaseID     string
	ScorerID   string
	Required   bool
	Transition Transition
	From, To   Endpoint
}

// OutputChanged reports whether the target wrote different stdout for this
// case in the two runs. It compares whole streams by size and SHA-256, so it
// is exact even when an output is truncated. known is false unless both
// runs retained the case's output.
func (c Change) OutputChanged() (changed, known bool) {
	if c.From.Output == nil || c.To.Output == nil {
		return false, false
	}
	return c.From.Output.Size != c.To.Output.Size || c.From.Output.SHA256 != c.To.Output.SHA256, true
}

// Endpoint is one side of a Change. Score is nil unless Status is
// "SUCCEEDED". Output is nil and Diagnostics is empty when that run retained
// none, as for every run published before V7.
type Endpoint struct {
	Cell      CellState
	CaseState string
	Status    string
	Score     *float64
	Output    *Output
	// Diagnostics are the case's CASE diagnostics and this cell's scorer's
	// SCORER diagnostics, in the run's order.
	Diagnostics []Diagnostic
}

// Output is a bounded view of a case's retained target stdout. run.json
// keeps the bytes, base64, at cases[i].output.bytes.
type Output struct {
	Form      OutputForm
	Size      int64  // every byte the target wrote
	SHA256    string // hex, over all Size bytes
	Truncated bool   // the artifact keeps fewer than Size bytes
	Complete  bool   // Preview is every byte the target wrote, unchanged
	Preview   string // at most the first 1 KiB, as valid UTF-8
}

// Diagnostic is one static diagnostic. It never holds target stderr or
// scorer error text.
type Diagnostic struct {
	Code    string
	Scope   string // "CASE" or "SCORER"
	Message string
}

// OutputForm equals its JSON spelling.
type OutputForm string

const (
	OutputJSON      = OutputForm(engine.OutputJSON)
	OutputMalformed = OutputForm(engine.OutputMalformed)
	OutputOversized = OutputForm(engine.OutputOversized)
	OutputEmpty     = OutputForm(engine.OutputEmpty)
)

// Each value equals its JSON spelling. TestV6SDKVocabularyIsTheJSONVocabulary
// fails if these drift from the engine.
type (
	Recommendation string
	Transition     string
	CellState      string
)

const (
	Ship        Recommendation = "SHIP"
	Iterate     Recommendation = "ITERATE"
	Investigate Recommendation = "INVESTIGATE"
)

const (
	Unchanged      Transition = "UNCHANGED"
	Improved       Transition = "IMPROVED"
	Regressed      Transition = "REGRESSED"
	LostEvidence   Transition = "LOST_EVIDENCE"
	GainedEvidence Transition = "GAINED_EVIDENCE"
)

const (
	CellPassing CellState = "PASSING"
	CellFailing CellState = "FAILING"
	CellUnknown CellState = "UNKNOWN"
)

func comparedRun(r engine.ComparedRun) ComparedRun {
	return ComparedRun{RunID: r.RunID, Quality: QualityVerdict(r.Quality.String())}
}

// endpoint copies Score, Output and Diagnostics, so callers cannot alias the
// record JSON() encodes.
func endpoint(e engine.Endpoint) Endpoint {
	out := Endpoint{Cell: CellState(e.Cell.String()), CaseState: e.CaseState, Status: e.Status}
	if e.Score != nil {
		s := *e.Score
		out.Score = &s
	}
	if v := e.Output; v != nil {
		out.Output = &Output{Form: OutputForm(v.Form), Size: v.Size, SHA256: v.SHA256, Truncated: v.Truncated, Complete: v.Complete, Preview: v.Preview}
	}
	for _, d := range e.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, Diagnostic{Code: d.Code, Scope: d.Scope, Message: d.Message})
	}
	return out
}
