package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A child process target fixture for the versioned multi-case protocol.
func TestV2TargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=v2") {
		return
	}
	var req struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(9)
	}
	switch req.CaseID {
	case "a", "b", "c":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"answer": "yes"})
	default:
		os.Exit(10)
	}
	os.Exit(0)
}

func configV2(t *testing.T, cases []map[string]any, scorers []map[string]any) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := map[string]any{
		"version": 2,
		"cases":   cases,
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestV2TargetHelper$", "--", "mode=v2"}},
		"scorers": scorers,
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func aCase(id, expected string) map[string]any {
	return map[string]any{"id": id, "input": map[string]string{"question": "ready?"}, "expected": map[string]string{"answer": expected}}
}
func exactScorers() []map[string]any {
	return []map[string]any{{"id": "required-exact", "kind": "exact_match", "threshold": 1.0, "required": true}, {"id": "optional-exact", "kind": "exact_match", "threshold": 0.5, "required": false}}
}

func TestV2AC01MultipleCasesPass(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), configV2(t, []map[string]any{aCase("a", "yes"), aCase("b", "yes")}, exactScorers()), dir)
	if o.State != "COMPLETED" || o.Quality == nil || *o.Quality != "PASS" || len(o.Cases) != 2 {
		t.Fatalf("outcome: %+v", o)
	}
	for _, c := range o.Cases {
		if c.State != "SCORED" || len(c.Scorers) != 2 || c.Scorers[0].Score == nil || *c.Scorers[0].Score != 1 {
			t.Fatalf("case: %+v", c)
		}
	}
	verified, err := VerifyArtifact(dir)
	if err != nil || len(verified.Cases) != 2 {
		t.Fatalf("verify: %+v, %v", verified, err)
	}
}
func TestV2AC02OneThresholdFails(t *testing.T) {
	o := RunJSON(context.Background(), configV2(t, []map[string]any{aCase("a", "yes"), aCase("b", "no")}, exactScorers()), filepath.Join(t.TempDir(), "artifact"))
	if o.State != "COMPLETED" || o.Quality == nil || *o.Quality != "FAIL" {
		t.Fatalf("outcome: %+v", o)
	}
	if o.Cases[1].Scorers[0].Score == nil || *o.Cases[1].Scorers[0].Score != 0 {
		t.Fatalf("zero score was lost: %+v", o.Cases[1])
	}
}
func TestV2AC03DuplicateCasesPreservedAndInvalid(t *testing.T) {
	o := RunJSON(context.Background(), configV2(t, []map[string]any{aCase("a", "yes"), aCase("a", "yes"), aCase("b", "yes")}, exactScorers()), filepath.Join(t.TempDir(), "artifact"))
	if o.State != "COMPLETED" || o.Quality == nil || *o.Quality != "INDETERMINATE" {
		t.Fatalf("outcome: %+v", o)
	}
	if o.Cases[0].State != "INVALID" || o.Cases[1].State != "INVALID" || o.Cases[2].State != "SCORED" {
		t.Fatalf("invalid case accounting: %+v", o.Cases)
	}
}
func TestV2AC04EmptyDatasetFailsValidation(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), configV2(t, nil, exactScorers()), artifact)
	if o.State != "FAILED" || o.Quality != nil {
		t.Fatalf("outcome: %+v", o)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact unexpectedly exists: %v", err)
	}
}
func TestV2AC05NoRequiredScorerRejected(t *testing.T) {
	optional := []map[string]any{{"id": "optional", "kind": "exact_match", "threshold": 0.0, "required": false}}
	o := RunJSON(context.Background(), configV2(t, []map[string]any{aCase("a", "yes")}, optional), filepath.Join(t.TempDir(), "artifact"))
	if o.State != "FAILED" {
		t.Fatalf("outcome: %+v", o)
	}
}
func TestV2AC06CaseInvalidJSONAccounted(t *testing.T) {
	// A RawMessage can only be malformed after hand-editing the serialized payload.
	raw := configV2(t, []map[string]any{aCase("a", "yes"), aCase("b", "yes")}, exactScorers())
	var cfg V2Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Cases[0].Input = nil
	modified, _ := json.Marshal(cfg)
	modified = []byte(strings.Replace(string(modified), `"input":null,`, ``, 1))
	o := RunJSON(context.Background(), modified, filepath.Join(t.TempDir(), "artifact"))
	if o.State != "COMPLETED" || o.Cases[0].State != "INVALID" || *o.Quality != "INDETERMINATE" {
		t.Fatalf("outcome: %+v", o)
	}
}
