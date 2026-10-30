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
	if json.NewDecoder(os.Stdin).Decode(&request) != nil || request.CaseID != "case-1" {
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
