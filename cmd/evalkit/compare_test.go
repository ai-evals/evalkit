package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-evals/evalkit/sdk"
)

// cliPublish runs a two-case v2 config through the CLI. Mode "crash" is not
// one TestCLITargetHelper answers, so every case is ERRORED.
func cliPublish(t *testing.T, mode string, threshold float64) (artifact string, runExit int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"version": 2,
		"cases": []any{
			map[string]any{"id": "case-1", "input": map[string]string{"q": "one"}, "expected": map[string]string{"answer": "yes"}},
			map[string]any{"id": "case-2", "input": map[string]string{"q": "two"}, "expected": map[string]string{"answer": "yes"}},
		},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestCLITargetHelper$", "--", "mode=" + mode}},
		"scorers": []any{map[string]any{"id": "required", "kind": "exact_match", "threshold": threshold, "required": true}},
	})
	folder := t.TempDir()
	config := filepath.Join(folder, "config.json")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	artifact = filepath.Join(folder, "artifact")
	var stdout bytes.Buffer
	return artifact, run([]string{"run", "--config", config, "--artifact-dir", artifact}, &stdout)
}

// CLI stdout and exit agree with the SDK for every recommendation and every
// refusal. Expected exits are the README contract, written as data.
func TestCLICompareParity(t *testing.T) {
	pass, _ := cliPublish(t, "pass", 1)
	tampered, _ := cliPublish(t, "pass", 1)
	if err := os.WriteFile(filepath.Join(tampered, "report.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode string
		threshold  float64
		tamper     bool
		wantExit   int
		wantRec    sdk.Recommendation
		wantError  sdk.ReadError
	}{
		{name: "ship", mode: "pass", threshold: 1, wantExit: 0, wantRec: sdk.Ship},
		{name: "iterate", mode: "fail", threshold: 1, wantExit: 1, wantRec: sdk.Iterate},
		{name: "investigate", mode: "crash", threshold: 1, wantExit: 3, wantRec: sdk.Investigate},
		{name: "not comparable", mode: "pass", threshold: 0.5, wantExit: 2,
			wantError: sdk.ReadError{Code: "COMPARISON_NOT_COMPARABLE", Scope: "COMPARISON", Reason: "SCORERS_DIFFER"}},
		{name: "tampered", tamper: true, wantExit: 2,
			wantError: sdk.ReadError{Code: "ARTIFACT_VERIFICATION_FAILED", Scope: "COMPARISON", Input: "candidate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, runExit := tampered, -1
			if !tc.tamper {
				candidate, runExit = cliPublish(t, tc.mode, tc.threshold)
			}
			var stdout bytes.Buffer
			exit := run([]string{"compare", "--baseline", pass, "--candidate", candidate}, &stdout)
			cmp, err := sdk.NewComparer().Compare(context.Background(), sdk.ComparisonRequest{BaselineDir: pass, CandidateDir: candidate})
			if exit != tc.wantExit {
				t.Fatalf("exit=%d want %d: %s", exit, tc.wantExit, stdout.String())
			}
			if tc.wantError.Code == "" {
				if err != nil || cmp.Recommendation() != tc.wantRec || !bytes.Equal(stdout.Bytes(), append(cmp.JSON(), '\n')) {
					t.Fatalf("cli %s\nsdk %s err=%v", stdout.Bytes(), cmp.JSON(), err)
				}
				var self bytes.Buffer
				if got := run([]string{"compare", "--baseline", candidate, "--candidate", candidate}, &self); got != runExit {
					t.Fatalf("self-compare exit %d, run exit %d", got, runExit)
				}
				return
			}
			var envelope struct {
				SchemaVersion int           `json:"schema_version"`
				Error         sdk.ReadError `json:"error"`
			}
			var refused *sdk.ReadError
			if json.Unmarshal(stdout.Bytes(), &envelope) != nil || envelope.SchemaVersion != 1 || envelope.Error != tc.wantError ||
				!errors.As(err, &refused) || *refused != tc.wantError {
				t.Fatalf("cli %s sdk %v", stdout.Bytes(), err)
			}
		})
	}
	var stdout bytes.Buffer
	if code := run([]string{"compare", "--baseline", pass}, &stdout); code != 2 ||
		stdout.String() != `{"error":{"code":"INVOCATION_INVALID","scope":"INVOCATION"},"schema_version":1}`+"\n" {
		t.Fatalf("missing --candidate: code=%d %s", code, stdout.String())
	}
}
