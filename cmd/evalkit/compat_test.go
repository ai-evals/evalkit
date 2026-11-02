package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
	"github.com/ai-evals/evalkit/sdk"
)

// The corpus and golden.json were written by the v0.6.0 binary
// (testdata/v0.6.0/generate.sh), so they are what v0.6.0 printed, not what
// this tree prints.
func TestV7AC06V060ArtifactsReadByteIdentically(t *testing.T) {
	root := filepath.Join("testdata", "v0.6.0")
	raw, err := os.ReadFile(filepath.Join(root, "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		Args   []string `json:"args"`
		Stdout string   `json:"stdout"`
		Exit   int      `json:"exit"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil || len(golden) == 0 {
		t.Fatalf("golden.json: %v", err)
	}
	for _, g := range golden {
		args := append([]string(nil), g.Args...)
		for i, a := range args {
			if strings.HasPrefix(a, "artifacts/") {
				args[i] = filepath.Join(root, a)
			}
		}
		var stdout bytes.Buffer
		if exit := run(args, &stdout); exit != g.Exit || stdout.String() != g.Stdout {
			t.Errorf("%v\n got exit %d: %s\nwant exit %d: %s", g.Args, exit, stdout.String(), g.Exit, g.Stdout)
		}
	}
}

// The CLI prints one record and the SDK exposes it through getters, so every
// evidence field the CLI prints must be what the getter returns.
func TestV7AC10CLIAndSDKAgreeOnEvidence(t *testing.T) {
	pass, _ := cliPublish(t, "pass", 1)
	for _, mode := range []string{"fail", "crash"} {
		candidate, _ := cliPublish(t, mode, 1)
		var stdout bytes.Buffer
		run([]string{"compare", "--baseline", pass, "--candidate", candidate}, &stdout)
		var printed struct {
			Changes []struct {
				From, To struct {
					Output      *sdk.Output      `json:"output"`
					Diagnostics []sdk.Diagnostic `json:"diagnostics"`
				}
			}
		}
		if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
			t.Fatal(err)
		}
		cmp, err := sdk.NewComparer().Compare(context.Background(), sdk.ComparisonRequest{BaselineDir: pass, CandidateDir: candidate})
		if err != nil || len(cmp.Changes()) != 2 || len(printed.Changes) != 2 {
			t.Fatalf("%s: %s %v", mode, stdout.String(), err)
		}
		for i, ch := range cmp.Changes() {
			p := printed.Changes[i]
			if ch.From.Output == nil || ch.To.Output == nil || !reflect.DeepEqual(p.From.Output, ch.From.Output) || !reflect.DeepEqual(p.To.Output, ch.To.Output) ||
				!reflect.DeepEqual(p.From.Diagnostics, ch.From.Diagnostics) || !reflect.DeepEqual(p.To.Diagnostics, ch.To.Diagnostics) {
				t.Errorf("%s change %d: cli %+v, sdk %+v", mode, i, p, ch)
			}
		}
	}
	var inspected bytes.Buffer
	if run([]string{"inspect", "--artifact-dir", pass}, &inspected) != 0 {
		t.Fatal(inspected.String())
	}
	view, err := sdk.NewInspector().Inspect(context.Background(), pass)
	if err != nil || inspected.String() != string(view.JSON())+"\n" || view.RetainedOutput() != "RETAINED" {
		t.Errorf("inspect cli %s sdk %s %q %v", inspected.String(), view.JSON(), view.RetainedOutput(), err)
	}
}

func TestV7RunStdoutIsTheSealedRecord(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"version": 2,
		"cases":   []any{map[string]any{"id": "case-1", "input": 1, "expected": map[string]string{"answer": "yes"}}, map[string]any{"id": "case-2", "input": 2, "expected": 0}},
		"target":  map[string]any{"command": exe, "args": []string{"-test.run=^TestCLITargetHelper$", "--", "mode=pass"}},
		"scorers": []any{map[string]any{"id": "required", "kind": "exact_match", "threshold": 1, "required": true}},
	})
	folder := t.TempDir()
	config, artifact := filepath.Join(folder, "config.json"), filepath.Join(folder, "artifact")
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if exit := run([]string{"run", "--config", config, "--artifact-dir", artifact}, &stdout); exit != 1 {
		t.Fatalf("exit %d: %s", exit, stdout.String())
	}
	sealed, err := os.ReadFile(filepath.Join(artifact, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var printed, committed engine.Outcome
	if json.Unmarshal(stdout.Bytes(), &printed) != nil || json.Unmarshal(sealed, &committed) != nil ||
		!reflect.DeepEqual(printed, committed) || printed.RetainedOutput != engine.OutputsRetained || printed.Cases[0].Output == nil {
		t.Fatalf("stdout %s\nrun.json %s", stdout.String(), sealed)
	}
}
