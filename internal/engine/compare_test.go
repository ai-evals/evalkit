package engine

import (
	"encoding/json"
	"testing"
)

func v6Ptr(x float64) *float64 { return &x }

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
