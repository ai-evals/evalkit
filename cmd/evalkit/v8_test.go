package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
)

// v8Scorer writes a command scorer that gives case "good" 0.8 and every
// other case 0.3. Its path is part of the measurement, so runs that compare
// share one.
func v8Scorer(t *testing.T) string {
	t.Helper()
	scorer := filepath.Join(t.TempDir(), "graded.sh")
	script := "#!/bin/sh\nIFS= read -r line\ncase \"$line\" in *'\"case_id\":\"good\"'*) echo '{\"score\":0.8}';; *) echo '{\"score\":0.3}';; esac\n"
	if err := os.WriteFile(scorer, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return scorer
}

// v8Publish runs a v5 config through the CLI and returns the artifact, the
// outcome it printed and its exit code.
func v8Publish(t *testing.T, scorer string, ids []string, args ...string) (string, engine.Outcome, int) {
	t.Helper()
	folder := t.TempDir()
	var cases []any
	for _, id := range ids {
		cases = append(cases, map[string]any{"id": id, "input": map[string]string{"q": id}, "expected": map[string]string{"answer": "yes"}})
	}
	judge := map[string]any{"id": "graded", "kind": "command", "command": scorer, "threshold": 0.5, "required": true}
	if len(args) > 0 {
		judge["args"] = args
	}
	raw, _ := json.Marshal(map[string]any{
		"version": 5, "cases": cases,
		"target":    map[string]any{"command": "/bin/sh", "args": []string{"-c", `IFS= read -r line; printf '%s\n' "$line"`}},
		"scorers":   []any{judge},
		"execution": map[string]any{"concurrency": 2, "scorer_max_attempts": 1},
	})
	config := filepath.Join(folder, "config.json")
	if err := os.WriteFile(config, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(folder, "artifact")
	var stdout bytes.Buffer
	exit := run([]string{"run", "--config", config, "--artifact-dir", artifact}, &stdout)
	var o engine.Outcome
	if err := json.Unmarshal(stdout.Bytes(), &o); err != nil {
		t.Fatalf("run stdout: %v: %s", err, stdout.String())
	}
	return artifact, o, exit
}

func TestV8AC01CLIGradedCommandScorer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ids      []string
		quality  string
		exit     int
		lastWant float64
	}{
		{"pass", []string{"good"}, "PASS", 0, 0.8},
		{"fail", []string{"good", "weak"}, "FAIL", 1, 0.3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, o, exit := v8Publish(t, v8Scorer(t), tc.ids)
			last := o.Cases[len(o.Cases)-1].Scorers[0]
			if exit != tc.exit || o.Quality == nil || *o.Quality != tc.quality || last.Score == nil || *last.Score != tc.lastWant {
				t.Fatalf("exit=%d outcome %+v", exit, o)
			}
		})
	}
}

func TestV8AC09CLIInspectAndCompareV5(t *testing.T) {
	scorer := v8Scorer(t)
	baseline, _, _ := v8Publish(t, scorer, []string{"good"}, "--model", "a")
	var inspection bytes.Buffer
	if exit := run([]string{"inspect", "--artifact-dir", baseline}, &inspection); exit != 0 {
		t.Fatalf("inspect exit=%d: %s", exit, inspection.String())
	}
	var view engine.Inspection
	if err := json.Unmarshal(inspection.Bytes(), &view); err != nil || view.Quality != "PASS" || view.MeasurementAccounting != "REPORTED_DEFINITION_CONSISTENT" {
		t.Fatalf("inspection %s err=%v", inspection.String(), err)
	}

	same, _, _ := v8Publish(t, scorer, []string{"good"}, "--model", "a")
	var stdout bytes.Buffer
	if exit := run([]string{"compare", "--baseline", baseline, "--candidate", same}, &stdout); exit != 0 {
		t.Fatalf("identical compare exit=%d: %s", exit, stdout.String())
	}

	changed, _, _ := v8Publish(t, scorer, []string{"good"}, "--model", "b")
	stdout.Reset()
	exit := run([]string{"compare", "--baseline", baseline, "--candidate", changed}, &stdout)
	var refusal struct{ Error struct{ Code, Reason string } }
	if err := json.Unmarshal(stdout.Bytes(), &refusal); err != nil || exit != 2 || refusal.Error.Code != "COMPARISON_NOT_COMPARABLE" || refusal.Error.Reason != "SCORERS_DIFFER" {
		t.Fatalf("args change: exit=%d %s", exit, stdout.String())
	}
}
