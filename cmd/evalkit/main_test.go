package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
)

func TestCLITargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=") {
		return
	}
	var request struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}
	if json.NewDecoder(os.Stdin).Decode(&request) != nil || (request.CaseID != "case-1" && request.CaseID != "case-2") {
		os.Exit(9)
	}
	for _, a := range os.Args {
		switch a {
		case "mode=pass":
			os.Stdout.Write([]byte(`{"answer":"yes"}`))
			os.Exit(0)
		case "mode=fail":
			os.Stdout.Write([]byte(`{"answer":"no"}`))
			os.Exit(0)
		}
	}
	os.Exit(8)
}

func TestCLIOutcomeParity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode, wantQuality string
		wantExit          int
	}{{"pass", "PASS", 0}, {"fail", "FAIL", 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			config := map[string]any{
				"version": 1,
				"case":    map[string]any{"id": "case-1", "input": map[string]any{"question": "ready?"}, "expected": map[string]any{"answer": "yes"}},
				"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestCLITargetHelper$", "--", "mode=" + tc.mode}},
				"scorer":  map[string]any{"kind": "exact_match", "threshold": 1.0},
			}
			raw, _ := json.Marshal(config)
			folder := t.TempDir()
			configPath := filepath.Join(folder, "evalkit.json")
			if err := os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			artifactDir := filepath.Join(folder, "artifact")
			var stdout bytes.Buffer
			exit := run([]string{"run", "--config", configPath, "--artifact-dir", artifactDir}, &stdout)
			if exit != tc.wantExit {
				t.Fatalf("exit=%d, want=%d; %s", exit, tc.wantExit, stdout.String())
			}
			var outcome engine.Outcome
			if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
				t.Fatal(err)
			}
			if outcome.State != "COMPLETED" || outcome.Quality == nil || *outcome.Quality != tc.wantQuality {
				t.Fatalf("outcome=%+v", outcome)
			}
			if _, err := engine.VerifyArtifact(artifactDir); err != nil {
				t.Fatalf("commit not verifiable: %v", err)
			}
		})
	}
}

func TestVersionCommand(t *testing.T) {
	var stdout bytes.Buffer
	if code := run([]string{"version"}, &stdout); code != 0 || !strings.Contains(stdout.String(), "evalkit ") {
		t.Fatalf("version: code=%d output=%q", code, stdout.String())
	}
}

func TestCLIV2MultipleCaseParity(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	cfg := map[string]any{
		"version": 2,
		"cases": []any{
			map[string]any{"id": "case-1", "input": map[string]string{"q": "one"}, "expected": map[string]string{"answer": "yes"}},
			map[string]any{"id": "case-2", "input": map[string]string{"q": "two"}, "expected": map[string]string{"answer": "yes"}},
		},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestCLITargetHelper$", "--", "mode=pass"}},
		"scorers": []any{map[string]any{"id": "required", "kind": "exact_match", "threshold": 1.0, "required": true}},
	}
	raw, _ := json.Marshal(cfg)
	configPath := filepath.Join(folder, "v2.json")
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(folder, "artifact")
	var stdout bytes.Buffer
	if code := run([]string{"run", "--config", configPath, "--artifact-dir", artifact}, &stdout); code != 0 {
		t.Fatalf("exit=%d: %s", code, stdout.String())
	}
	var outcome engine.Outcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.Quality == nil || *outcome.Quality != "PASS" || len(outcome.Cases) != 2 {
		t.Fatalf("outcome: %+v", outcome)
	}
	if _, err := engine.VerifyArtifact(artifact); err != nil {
		t.Fatal(err)
	}
}

func TestCLICustomScorerRequiresSDKRegistration(t *testing.T) {
	folder := t.TempDir()
	config := map[string]any{
		"version":   4,
		"cases":     []any{map[string]any{"id": "case-1", "input": map[string]any{"q": "ready?"}, "expected": map[string]any{"answer": "yes"}}},
		"target":    map[string]any{"command": "unused-executable", "args": []string{}},
		"scorers":   []any{map[string]any{"id": "custom", "kind": "custom", "implementation": "example/custom@1", "required": true, "threshold": 1.0}},
		"execution": map[string]any{"concurrency": 1, "scorer_max_attempts": 1},
	}
	raw, _ := json.Marshal(config)
	path := filepath.Join(folder, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(folder, "artifact")
	var stdout bytes.Buffer
	if code := run([]string{"run", "--config", path, "--artifact-dir", artifact}, &stdout); code != 2 {
		t.Fatalf("exit=%d: %s", code, stdout.String())
	}
	var outcome engine.Outcome
	if err := json.Unmarshal(stdout.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.State != "FAILED" {
		t.Fatalf("outcome: %+v", outcome)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("unauthorized artifact: %v", err)
	}
}

func TestCLIInspectVerifiedArtifact(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"version": 1,
		"case":    map[string]any{"id": "case-1", "input": map[string]string{"q": "ready?"}, "expected": map[string]string{"answer": "yes"}},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestCLITargetHelper$", "--", "mode=pass"}},
		"scorer":  map[string]any{"kind": "exact_match", "threshold": 1.0},
	}
	raw, _ := json.Marshal(cfg)
	folder := t.TempDir()
	config := filepath.Join(folder, "run.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(folder, "artifact")
	var runResult, inspection bytes.Buffer
	if code := run([]string{"run", "--config", config, "--artifact-dir", artifact}, &runResult); code != 0 {
		t.Fatalf("run failed: %s", runResult.String())
	}
	if code := run([]string{"inspect", "--artifact-dir", artifact}, &inspection); code != 0 {
		t.Fatalf("inspect failed: %s", inspection.String())
	}
	var view engine.Inspection
	if err := json.Unmarshal(inspection.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Quality != "PASS" || view.Integrity != "SELF_CONSISTENT" {
		t.Fatalf("view: %+v", view)
	}
}
func TestCLIInspectInvalidArtifact(t *testing.T) {
	var stdout bytes.Buffer
	if code := run([]string{"inspect", "--artifact-dir", t.TempDir()}, &stdout); code != 2 {
		t.Fatalf("code=%d output=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "ARTIFACT_VERIFICATION_FAILED") {
		t.Fatalf("missing typed error: %s", stdout.String())
	}
}
