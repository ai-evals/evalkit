package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
)

// The SDK's string constants cannot be derived from the engine's integer
// enums, so this pins them: same values, same order, nothing missing.
func TestV6SDKVocabularyIsTheJSONVocabulary(t *testing.T) {
	check := func(kind string, public []string, text func(uint8) ([]byte, error)) {
		t.Helper()
		var engineNames []string
		for i := 0; i < 256; i++ {
			b, err := text(uint8(i))
			if err != nil {
				break
			}
			engineNames = append(engineNames, string(b))
		}
		if !slices.Equal(engineNames, public) {
			t.Errorf("%s: engine %v, sdk %v", kind, engineNames, public)
		}
	}
	check("Recommendation", []string{string(Ship), string(Iterate), string(Investigate)},
		func(i uint8) ([]byte, error) { return engine.Recommendation(i).MarshalText() })
	check("Transition", []string{string(Unchanged), string(Improved), string(Regressed), string(LostEvidence), string(GainedEvidence)},
		func(i uint8) ([]byte, error) { return engine.Transition(i).MarshalText() })
	check("CellState", []string{string(CellPassing), string(CellFailing), string(CellUnknown)},
		func(i uint8) ([]byte, error) { return engine.CellState(i).MarshalText() })
}

// sdkV7Publish runs cases a and b through a target that prints
// {"answer":<answer>} and a required custom scorer that answers score.
func sdkV7Publish(t *testing.T, answer string, score func(caseID string) (Assessment, error)) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"version": 4,
		"cases": []any{
			map[string]any{"id": "a", "input": 1, "expected": map[string]string{"answer": "yes"}},
			map[string]any{"id": "b", "input": 2, "expected": map[string]string{"answer": "yes"}},
		},
		"target":    map[string]any{"command": "/bin/sh", "args": []string{"-c", `IFS= read -r _; printf '{"answer":"%s"}\n' "$0"`, answer}},
		"scorers":   []any{map[string]any{"id": "custom", "kind": "custom", "implementation": "example/custom@1", "threshold": 1, "required": true}},
		"execution": map[string]any{"concurrency": 2, "scorer_max_attempts": 1},
	})
	dir := filepath.Join(t.TempDir(), "artifact")
	runner := runnerWith(t, functionScorer(func(_ context.Context, in ScorerInput) (Assessment, error) { return score(in.CaseID()) }))
	if _, err := runner.Run(context.Background(), Request{ConfigJSON: raw, ArtifactDir: dir}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestV7SDKChangesCarryEvidence(t *testing.T) {
	passes := func(string) (Assessment, error) { return Scored(1), nil }
	base := sdkV7Publish(t, "yes", passes)
	cand := sdkV7Publish(t, "no", func(id string) (Assessment, error) {
		if id == "a" {
			return Assessment{}, errors.New("judge down")
		}
		return Scored(0), nil
	})
	cmp, err := NewComparer().Compare(context.Background(), ComparisonRequest{BaselineDir: base, CandidateDir: cand})
	if err != nil {
		t.Fatal(err)
	}
	yes := &Output{Form: OutputJSON, Size: 17, SHA256: "863528c9b14f3ea195d472c71227d5a606cecb578932bdad916d7f01511e4671", Complete: true, Preview: "{\"answer\":\"yes\"}\n"}
	no := &Output{Form: OutputJSON, Size: 16, SHA256: "424d38bfd14d06dea6829f4745fd6a0e9a8591644d4fd1c836ae1045408d062b", Complete: true, Preview: "{\"answer\":\"no\"}\n"}
	one, zero := 1.0, 0.0
	want := []Change{
		{CaseID: "a", ScorerID: "custom", Required: true, Transition: LostEvidence,
			From: Endpoint{Cell: CellPassing, CaseState: "SCORED", Status: "SUCCEEDED", Score: &one, Output: yes},
			To: Endpoint{Cell: CellUnknown, CaseState: "ERRORED", Status: "ERRORED", Output: no,
				Diagnostics: []Diagnostic{{Code: "SCORER_OPERATION_ERROR", Scope: "SCORER", Message: "Scorer operation failed"}}}},
		{CaseID: "b", ScorerID: "custom", Required: true, Transition: Regressed,
			From: Endpoint{Cell: CellPassing, CaseState: "SCORED", Status: "SUCCEEDED", Score: &one, Output: yes},
			To:   Endpoint{Cell: CellFailing, CaseState: "SCORED", Status: "SUCCEEDED", Score: &zero, Output: no}},
	}
	changes := cmp.Changes()
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes %+v\njson %s", changes, cmp.JSON())
	}
	for _, ch := range changes {
		if changed, known := ch.OutputChanged(); !changed || !known {
			t.Errorf("%s: OutputChanged() = %v, %v", ch.CaseID, changed, known)
		}
	}
	before := string(cmp.JSON())
	changes[0].To.Output.Preview, changes[0].To.Diagnostics[0].Code = "edited", "EDITED"
	if again := cmp.Changes(); !reflect.DeepEqual(again, want) || string(cmp.JSON()) != before {
		t.Fatal("Changes() aliases the comparison")
	}

	same := sdkV7Publish(t, "yes", func(id string) (Assessment, error) { return Scored(map[string]float64{"a": 1, "b": 0}[id]), nil })
	cmp, err = NewComparer().Compare(context.Background(), ComparisonRequest{BaselineDir: base, CandidateDir: same})
	if err != nil || len(cmp.Changes()) != 1 {
		t.Fatalf("%s %v", cmp.JSON(), err)
	}
	if changed, known := cmp.Changes()[0].OutputChanged(); changed || !known {
		t.Errorf("same output, new score: OutputChanged() = %v, %v", changed, known)
	}

	view, err := NewInspector().Inspect(context.Background(), base)
	old, oldErr := NewInspector().Inspect(context.Background(), filepath.Join("..", "cmd", "evalkit", "testdata", "v0.6.0", "artifacts", "v2-pass"))
	if err != nil || oldErr != nil || view.RetainedOutput() != "RETAINED" || old.RetainedOutput() != "" {
		t.Errorf("RetainedOutput() %q and %q for v0.6.0, err %v %v", view.RetainedOutput(), old.RetainedOutput(), err, oldErr)
	}
}

func TestV7SDKOutputChangedNeedsBothSides(t *testing.T) {
	out := &Output{Size: 1, SHA256: "x"}
	for _, ch := range []Change{{}, {From: Endpoint{Output: out}}, {To: Endpoint{Output: out}}} {
		if changed, known := ch.OutputChanged(); changed || known {
			t.Errorf("%+v: OutputChanged() = %v, %v", ch, changed, known)
		}
	}
}
