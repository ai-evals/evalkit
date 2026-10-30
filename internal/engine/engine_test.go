package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=") {
		return
	}
	// The controlled child process receives a JSON request on stdin.
	var request struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil || request.CaseID != "case-1" {
		os.Exit(9)
	}
	mode := ""
	for _, a := range os.Args {
		if strings.HasPrefix(a, "mode=") {
			mode = strings.TrimPrefix(a, "mode=")
		}
	}
	switch mode {
	case "match":
		os.Stdout.Write([]byte(`{"answer":"yes"}`))
	case "mismatch":
		os.Stdout.Write([]byte(`{"answer":"no"}`))
	case "invalid":
		os.Stdout.Write([]byte(`{"answer":"yes"} {"extra":true}`))
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func configFor(t *testing.T, mode string) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	conf := map[string]any{
		"version": 1,
		"case":    map[string]any{"id": "case-1", "input": map[string]any{"question": "ready?"}, "expected": map[string]any{"answer": "yes"}},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestTargetHelper$", "--", "mode=" + mode}},
		"scorer":  map[string]any{"kind": "exact_match", "threshold": 1.0},
	}
	raw, err := json.Marshal(conf)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestV1AC01Pass(t *testing.T) {
	raw := configFor(t, "match")
	out := RunJSON(context.Background(), raw, filepath.Join(t.TempDir(), "artifact"))
	if out.State != "COMPLETED" || out.Quality == nil || *out.Quality != "PASS" {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Case == nil || out.Case.Score == nil || *out.Case.Score != 1 {
		t.Fatalf("case: %+v", out.Case)
	}
	for _, name := range []string{"run.json", "report.json", "commit.json"} {
		if _, err := os.Stat(filepath.Join(out.ArtifactDir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV1AC02Fail(t *testing.T) {
	raw := configFor(t, "mismatch")
	out := RunJSON(context.Background(), raw, filepath.Join(t.TempDir(), "artifact"))
	if out.State != "COMPLETED" || out.Quality == nil || *out.Quality != "FAIL" {
		t.Fatalf("outcome: %+v", out)
	}
	if out.Case == nil || out.Case.Score == nil || *out.Case.Score != 0 {
		t.Fatalf("case: %+v", out.Case)
	}
}

func TestInvalidTargetIsIndeterminate(t *testing.T) {
	out := RunJSON(context.Background(), configFor(t, "invalid"), filepath.Join(t.TempDir(), "artifact"))
	if out.State != "COMPLETED" || out.Quality == nil || *out.Quality != "INDETERMINATE" {
		t.Fatalf("outcome: %+v", out)
	}
}

func TestConfigValidationFailureDoesNotCommit(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact")
	out := RunJSON(context.Background(), []byte(`{"version":1,"unexpected":true}`), artifact)
	if out.State != "FAILED" || out.Quality != nil {
		t.Fatalf("outcome: %+v", out)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact should not exist: %v", err)
	}
}

func TestPublicationFailurePreventsCompletion(t *testing.T) {
	out := RunJSON(context.Background(), configFor(t, "match"), filepath.Join(t.TempDir(), "missing-parent", "artifact"))
	if out.State != "FAILED" || out.Quality != nil {
		t.Fatalf("outcome: %+v", out)
	}
	if len(out.Diagnostics) == 0 || out.Diagnostics[len(out.Diagnostics)-1].Code != "ARTIFACT_COMMIT_FAILED" {
		t.Fatalf("diagnostics: %+v", out.Diagnostics)
	}
}

func TestTamperedArtifactIsNotVerified(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact")
	out := RunJSON(context.Background(), configFor(t, "match"), artifact)
	if out.State != "COMPLETED" {
		t.Fatalf("run failed: %+v", out)
	}
	if err := os.WriteFile(filepath.Join(artifact, "report.json"), []byte(`{"run_id":"fake","quality":"PASS"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArtifact(artifact); err == nil {
		t.Fatal("expected artifact integrity failure")
	}
}

func TestCancelledBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	artifact := filepath.Join(t.TempDir(), "artifact")
	out := RunJSON(ctx, configFor(t, "match"), artifact)
	if out.State != "CANCELLED" || out.Quality != nil {
		t.Fatalf("outcome: %+v", out)
	}
	if _, err := os.Stat(artifact); !os.IsNotExist(err) {
		t.Fatalf("artifact should not exist: %v", err)
	}
}
