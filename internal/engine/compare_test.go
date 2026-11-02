package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func v6Ptr(x float64) *float64 { return &x }

// v6Script maps caseID -> scorerID -> assessment. Missing entries score 1.
type v6Script map[string]map[string]AssessmentCandidate

func v6Scored(x float64) AssessmentCandidate {
	return AssessmentCandidate{Kind: "SCORED", Score: v6Ptr(x)}
}
func v6Uneval() AssessmentCandidate {
	return AssessmentCandidate{Kind: "UNEVALUABLE", Reason: "EVIDENCE_MISSING"}
}
func v6Passing(score float64) Endpoint {
	return Endpoint{CellPassing, "SCORED", "SUCCEEDED", v6Ptr(score)}
}
func v6Failing(score float64) Endpoint {
	return Endpoint{CellFailing, "SCORED", "SUCCEEDED", v6Ptr(score)}
}
func v6Unknown(caseState, status string) Endpoint {
	return Endpoint{CellUnknown, caseState, status, nil}
}

var v6IDs = []string{"a", "b"}

// v6Contract is a required "r" and an optional "o", both custom.
func v6Contract() []V2Scorer {
	return []V2Scorer{
		{ID: "r", Kind: "custom", Implementation: "v6/r@1", Threshold: 0.5, Required: true},
		{ID: "o", Kind: "custom", Implementation: "v6/o@1", Threshold: 0.5, Required: false},
	}
}

// v6Publish runs a v4 configuration whose target echoes its request, so
// runs differ only by what the scripted custom scorers return.
func v6Publish(t *testing.T, contract []V2Scorer, ids []string, script v6Script) string {
	t.Helper()
	cases := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		cases = append(cases, map[string]any{"id": id, "input": map[string]any{"n": 1}, "expected": map[string]any{"n": 1}})
	}
	custom := map[string]RegisteredScorer{}
	for _, sc := range contract {
		id := sc.ID
		custom[id] = RegisteredScorer{Implementation: sc.Implementation, Evaluate: func(_ context.Context, in ScorerInput) (AssessmentCandidate, error) {
			if a, ok := script[in.CaseID][id]; ok {
				return a, nil
			}
			return v6Scored(1), nil
		}}
	}
	raw, err := json.Marshal(map[string]any{
		"version": 4, "cases": cases, "scorers": contract,
		"target":    map[string]any{"command": "/bin/sh", "args": []string{"-c", `IFS= read -r line; printf '%s\n' "$line"`}},
		"execution": map[string]any{"concurrency": 2, "scorer_max_attempts": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "artifact")
	if o := RunJSONWithScorers(context.Background(), raw, dir, custom); o.State != "COMPLETED" {
		t.Fatalf("fixture run: %+v", o)
	}
	return dir
}

func v6Compare(t *testing.T, baseline, candidate string) Comparison {
	t.Helper()
	cmp, err := CompareArtifacts(context.Background(), baseline, candidate)
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	return cmp
}

func v6Refused(t *testing.T, baseline, candidate string, want ReadError) {
	t.Helper()
	_, err := CompareArtifacts(context.Background(), baseline, candidate)
	var refused *ReadError
	if !errors.As(err, &refused) || *refused != want {
		t.Fatalf("err=%v, want %+v", err, want)
	}
}

func v6Change(caseID, scorerID string, required bool, tr Transition, from, to Endpoint) CellChange {
	return CellChange{CaseID: caseID, ScorerID: scorerID, Required: required, Transition: tr, From: from, To: to}
}

func v6Expect(t *testing.T, cmp Comparison, rec Recommendation, changes ...CellChange) {
	t.Helper()
	if changes == nil {
		changes = []CellChange{}
	}
	total := 0
	for _, n := range cmp.Counts {
		total += n
	}
	if cmp.Recommendation != rec || !reflect.DeepEqual(cmp.Changes, changes) || total != len(v6IDs)*len(v6Contract()) {
		got, _ := json.Marshal(cmp)
		t.Fatalf("got %s\nwant recommendation %v, changes %+v", got, rec, changes)
	}
}

func TestV6AC01IdenticalRunsShip(t *testing.T) {
	base, cand := v6Publish(t, v6Contract(), v6IDs, nil), v6Publish(t, v6Contract(), v6IDs, nil)
	cmp := v6Compare(t, base, cand)
	v6Expect(t, cmp, RecommendShip)
	if cmp.Counts != (TransitionCounts{TransitionUnchanged: 4}) || cmp.Baseline.RunID == cmp.Candidate.RunID {
		t.Fatalf("%+v", cmp)
	}
	v6Expect(t, v6Compare(t, base, base), RecommendShip)
}

func TestV6AC02RequiredRegressionIterates(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	cand := v6Publish(t, v6Contract(), v6IDs, v6Script{"b": {"r": v6Scored(0)}})
	v6Expect(t, v6Compare(t, base, cand), RecommendIterate,
		v6Change("b", "r", true, TransitionRegressed, v6Passing(1), v6Failing(0)))
}

func TestV6AC03ImprovementShips(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, v6Script{"a": {"r": v6Scored(0)}})
	cand := v6Publish(t, v6Contract(), v6IDs, nil)
	v6Expect(t, v6Compare(t, base, cand), RecommendShip,
		v6Change("a", "r", true, TransitionImproved, v6Failing(0), v6Passing(1)))
}

// A required UNEVALUABLE errs the whole case, so its optional cell loses
// evidence too even though that scorer SUCCEEDED.
func TestV6AC04LostEvidenceInvestigates(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	cand := v6Publish(t, v6Contract(), v6IDs, v6Script{"b": {"r": v6Uneval()}})
	v6Expect(t, v6Compare(t, base, cand), RecommendInvestigate,
		v6Change("b", "r", true, TransitionLostEvidence, v6Passing(1), v6Unknown("ERRORED", "UNEVALUABLE")),
		v6Change("b", "o", false, TransitionLostEvidence, v6Passing(1), Endpoint{CellUnknown, "ERRORED", "SUCCEEDED", v6Ptr(1)}))
	v6Expect(t, v6Compare(t, cand, base), RecommendShip,
		v6Change("b", "r", true, TransitionGainedEvidence, v6Unknown("ERRORED", "UNEVALUABLE"), v6Passing(1)),
		v6Change("b", "o", false, TransitionGainedEvidence, Endpoint{CellUnknown, "ERRORED", "SUCCEEDED", v6Ptr(1)}, v6Passing(1)))
}

func TestV6AC05OptionalRegressionDoesNotBlock(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	cand := v6Publish(t, v6Contract(), v6IDs, v6Script{"b": {"o": v6Scored(0)}})
	cmp := v6Compare(t, base, cand)
	v6Expect(t, cmp, RecommendShip,
		v6Change("b", "o", false, TransitionRegressed, v6Passing(1), v6Failing(0)))
	if cmp.Counts != (TransitionCounts{TransitionUnchanged: 3, TransitionRegressed: 1}) {
		t.Fatalf("counts %+v", cmp.Counts)
	}
}

func TestV6AC06MeasurementMismatchRefused(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	edit := func(f func([]V2Scorer)) []V2Scorer { c := v6Contract(); f(c); return c }
	for name, tc := range map[string]struct {
		candidate func(*testing.T) string
		reason    string
	}{
		"case order": {func(t *testing.T) string { return v6Publish(t, v6Contract(), []string{"b", "a"}, nil) }, ReasonCasesDiffer},
		"case set":   {func(t *testing.T) string { return v6Publish(t, v6Contract(), []string{"a"}, nil) }, ReasonCasesDiffer},
		"threshold": {func(t *testing.T) string {
			return v6Publish(t, edit(func(c []V2Scorer) { c[0].Threshold = 0.6 }), v6IDs, nil)
		}, ReasonScorersDiffer},
		"required flag": {func(t *testing.T) string {
			return v6Publish(t, edit(func(c []V2Scorer) { c[1].Required = true }), v6IDs, nil)
		}, ReasonScorersDiffer},
		"scorer id": {func(t *testing.T) string {
			return v6Publish(t, edit(func(c []V2Scorer) { c[1].ID = "o2" }), v6IDs, nil)
		}, ReasonScorersDiffer},
		"implementation": {func(t *testing.T) string {
			return v6Publish(t, edit(func(c []V2Scorer) { c[0].Implementation = "v6/r@2" }), v6IDs, nil)
		}, ReasonScorersDiffer},
	} {
		t.Run(name, func(t *testing.T) {
			v6Refused(t, base, tc.candidate(t), ReadError{Code: CodeComparisonNotComparable, Scope: ScopeComparison, Reason: tc.reason})
		})
	}
}

func TestV6AC07HistoricalNotRetainedRefused(t *testing.T) {
	historical := v6Publish(t, v6Contract(), v6IDs, nil)
	v6Rewrite(t, historical, func(run, _ map[string]any) { delete(run, "measurement") })
	current := v6Publish(t, v6Contract(), v6IDs, nil)
	notRetained := ReadError{Code: CodeComparisonNotComparable, Scope: ScopeComparison, Reason: ReasonMeasurementNotRetained}
	notRetained.Input = InputBaseline
	v6Refused(t, historical, current, notRetained)
	notRetained.Input = InputCandidate
	v6Refused(t, current, historical, notRetained)
	if view, err := InspectArtifact(context.Background(), historical); err != nil || view.MeasurementAccounting != "NOT_RETAINED" {
		t.Fatalf("inspect still reads historical artifacts: %+v %v", view, err)
	}
}

func TestV6AC08TamperedInputRefused(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	cand := v6Publish(t, v6Contract(), v6IDs, nil)
	if err := os.WriteFile(filepath.Join(cand, "report.json"), []byte(`{"quality":"PASS"}`), 0600); err != nil {
		t.Fatal(err)
	}
	failed := ReadError{Code: CodeArtifactVerificationFailed, Scope: ScopeComparison, Input: InputCandidate}
	v6Refused(t, base, cand, failed)
	v6Refused(t, filepath.Join(t.TempDir(), "missing"), base, ReadError{Code: CodeArtifactVerificationFailed, Scope: ScopeComparison, Input: InputBaseline})
	historical := v6Publish(t, v6Contract(), v6IDs, nil)
	v6Rewrite(t, historical, func(run, _ map[string]any) { delete(run, "measurement") })
	v6Refused(t, historical, cand, failed)
}

func TestV6AC09V1ArtifactsCompare(t *testing.T) {
	v1 := func(mode string) string {
		dir := filepath.Join(t.TempDir(), "artifact")
		if o := RunJSON(context.Background(), configFor(t, mode), dir); o.State != "COMPLETED" {
			t.Fatalf("v1 run: %+v", o)
		}
		return dir
	}
	cmp := v6Compare(t, v1("match"), v1("mismatch"))
	want := []CellChange{v6Change("case-1", "exact_match", true, TransitionRegressed, v6Passing(1), v6Failing(0))}
	if cmp.Recommendation != RecommendIterate || !reflect.DeepEqual(cmp.Changes, want) {
		t.Fatalf("v1 compare: %+v", cmp)
	}
	cmp = v6Compare(t, v1("match"), v1("crash"))
	want = []CellChange{v6Change("case-1", "exact_match", true, TransitionLostEvidence, v6Passing(1), v6Unknown("ERRORED", "ERRORED"))}
	if cmp.Recommendation != RecommendInvestigate || !reflect.DeepEqual(cmp.Changes, want) {
		t.Fatalf("v1 crash compare: %+v", cmp)
	}
	// A v2 run whose retained contract equals v1's synthetic one compares
	// with it directly.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"version": 2,
		"cases":   []any{map[string]any{"id": "case-1", "input": map[string]any{"question": "ready?"}, "expected": map[string]any{"answer": "yes"}}},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestTargetHelper$", "--", "mode=match"}},
		"scorers": []any{map[string]any{"id": "exact_match", "kind": "exact_match", "threshold": 1.0, "required": true}},
	})
	v2 := filepath.Join(t.TempDir(), "artifact")
	if o := RunJSON(context.Background(), raw, v2); o.State != "COMPLETED" {
		t.Fatalf("v2 run: %+v", o)
	}
	if cmp := v6Compare(t, v1("match"), v2); cmp.Recommendation != RecommendShip || len(cmp.Changes) != 0 {
		t.Fatalf("v1 vs v2: %+v", cmp)
	}
}

func TestV6AC10ReadOnlyAndCancellable(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, nil)
	cand := v6Publish(t, v6Contract(), v6IDs, v6Script{"a": {"r": v6Scored(0)}})
	snapshot := func() map[string]string {
		files := map[string]string{}
		for _, dir := range []string{base, cand} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				info, _ := e.Info()
				raw, _ := os.ReadFile(filepath.Join(dir, e.Name()))
				files[filepath.Join(dir, e.Name())] = info.Mode().String() + info.ModTime().String() + string(raw)
			}
		}
		return files
	}
	before := snapshot()
	first, _ := json.Marshal(v6Compare(t, base, cand))
	second, _ := json.Marshal(v6Compare(t, base, cand))
	if !reflect.DeepEqual(before, snapshot()) || string(first) != string(second) {
		t.Fatal("compare altered an input artifact or was not deterministic")
	}
	// Nonexistent dirs would otherwise be refused, so a bare context error
	// proves the check precedes the first read.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for want, ctx := range map[error]context.Context{context.Canceled: cancelled, context.DeadlineExceeded: expired} {
		_, err := CompareArtifacts(ctx, "/nonexistent-baseline", "/nonexistent-candidate")
		var refused *ReadError
		if !errors.Is(err, want) || errors.As(err, &refused) {
			t.Fatalf("err=%v, want bare %v", err, want)
		}
	}
}

// Expected values are the decided transition and cell tables written as
// data, never derived from transitionTable or cellStateOf.
func TestV6AC11TransitionTableExhaustive(t *testing.T) {
	want := map[[2]string]string{
		{"PASSING", "PASSING"}: "UNCHANGED",
		{"FAILING", "FAILING"}: "UNCHANGED",
		{"UNKNOWN", "UNKNOWN"}: "UNCHANGED",
		{"FAILING", "PASSING"}: "IMPROVED",
		{"PASSING", "FAILING"}: "REGRESSED",
		{"PASSING", "UNKNOWN"}: "LOST_EVIDENCE",
		{"FAILING", "UNKNOWN"}: "LOST_EVIDENCE",
		{"UNKNOWN", "PASSING"}: "GAINED_EVIDENCE",
		{"UNKNOWN", "FAILING"}: "GAINED_EVIDENCE",
	}
	seen := 0
	for from := range cellStateCount {
		for to := range cellStateCount {
			w, ok := want[[2]string{from.String(), to.String()}]
			if got := transitionTable[from][to].String(); !ok || got != w {
				t.Errorf("%v -> %v = %s, want %s", from, to, got, w)
			}
			seen++
		}
	}
	if seen != len(want) {
		t.Fatalf("table has %d cells, the decided table has %d", seen, len(want))
	}

	for _, c := range []struct {
		caseState, status string
		score             *float64
		threshold         float64
		want              string
	}{
		{"SCORED", "SUCCEEDED", v6Ptr(0.4), 0.5, "FAILING"},
		{"SCORED", "SUCCEEDED", v6Ptr(0.5), 0.5, "PASSING"},
		{"SCORED", "SUCCEEDED", v6Ptr(0.9), 0.5, "PASSING"},
		{"SCORED", "SUCCEEDED", v6Ptr(0), 0, "PASSING"},
		{"SCORED", "ERRORED", nil, 0.5, "UNKNOWN"},
		{"SCORED", "UNEVALUABLE", nil, 0.5, "UNKNOWN"},
		{"SCORED", "SKIPPED", nil, 0.5, "UNKNOWN"},
		{"ERRORED", "SUCCEEDED", v6Ptr(1), 0.5, "UNKNOWN"},
		{"ERRORED", "ERRORED", nil, 0.5, "UNKNOWN"},
		{"INVALID", "SKIPPED", nil, 0.5, "UNKNOWN"},
	} {
		got := cellStateOf(c.caseState, ScorerObservation{Status: c.status, Score: c.score}, c.threshold)
		if got.String() != c.want {
			t.Errorf("cell(%s,%s,%v,th=%v) = %v, want %s", c.caseState, c.status, c.score, c.threshold, got, c.want)
		}
	}
}

// qualityNames cannot be derived from the frozen producer files, so this
// fails if they diverge. It also pins the recommendation row and the counts
// wire shape as data.
func TestV6VerdictVocabulary(t *testing.T) {
	one := func(state, status string, score *float64) string {
		return qualityOfCases([]V2CaseResult{{State: state, Scorers: []ScorerObservation{{Status: status, Score: score}}}}, []V2Scorer{{Required: true, Threshold: 0.5}})
	}
	recommends := map[string]string{"PASS": "SHIP", "FAIL": "ITERATE", "INDETERMINATE": "INVESTIGATE"}
	for _, produced := range []string{one("SCORED", "SUCCEEDED", v6Ptr(1)), one("SCORED", "SUCCEEDED", v6Ptr(0)), one("ERRORED", "ERRORED", nil)} {
		q, ok := parseQuality(produced)
		if !ok || q.String() != produced || q.Recommendation().String() != recommends[produced] {
			t.Errorf("producer verdict %q: parsed=%v ok=%v recommendation=%v", produced, q, ok, q.Recommendation())
		}
	}
	if int(qualityCount) != len(recommends) {
		t.Errorf("Quality has %d values, the decided policy has %d", qualityCount, len(recommends))
	}
	counts, err := json.Marshal(TransitionCounts{1, 0, 2, 0, 3})
	if err != nil || string(counts) != `{"UNCHANGED":1,"IMPROVED":0,"REGRESSED":2,"LOST_EVIDENCE":0,"GAINED_EVIDENCE":3}` {
		t.Errorf("counts JSON %s %v", counts, err)
	}
	if _, err := json.Marshal(Transition(transitionCount)); err == nil {
		t.Error("out-of-range Transition marshalled")
	}
}

func TestV6AC12ScoreOnlyChangeListed(t *testing.T) {
	base := v6Publish(t, v6Contract(), v6IDs, v6Script{"a": {"r": v6Scored(0.6)}, "b": {"r": v6Uneval()}})
	cand := v6Publish(t, v6Contract(), v6IDs, v6Script{"a": {"r": v6Scored(0.9)}, "b": {"r": AssessmentCandidate{Kind: "UNEVALUABLE", Reason: "EVIDENCE_INSUFFICIENT"}}})
	cmp := v6Compare(t, base, cand)
	v6Expect(t, cmp, RecommendInvestigate,
		v6Change("a", "r", true, TransitionUnchanged, v6Passing(0.6), v6Passing(0.9)))
	if cmp.Counts != (TransitionCounts{TransitionUnchanged: 4}) {
		t.Fatalf("counts %+v", cmp.Counts)
	}
}
