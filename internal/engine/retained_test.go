package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const v7StderrSecret = "evalkit-v7-stderr-secret"

// v7Printed is exactly what TestV7TargetHelper writes to stdout for each
// case ID, and v7Exit its exit status. Any other ID prints {"answer":"yes"}.
var v7Printed = map[string][]byte{
	"json":        []byte("{\"answer\":\"yes\"}\n"),
	"no":          []byte("{\"answer\":\"no\"}\n"),
	"malformed":   []byte("answer: maybe\n"),
	"two-values":  []byte(`{"a":1} {"b":2}`),
	"empty":       nil,
	"crash-json":  []byte(`{"answer":"yes"}`),
	"partial":     []byte("partial"),
	"bad-utf8":    []byte("\"\xff\xfe\"\n"),
	"stderr":      []byte("{\"answer\":\"yes\"}\n"),
	"exact-limit": append(append([]byte{'"'}, bytes.Repeat([]byte{'a'}, maxTargetBytes-2)...), '"'),
	"oversized":   bytes.Repeat([]byte{'<'}, 2*maxTargetBytes+1),
	"sleep":       []byte("printed-before-the-kill"),
}

var v7Exit = map[string]int{"crash-json": 3, "partial": 1, "stderr-crash": 1}

func TestV7TargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=v7") {
		return
	}
	var req struct {
		CaseID string `json:"case_id"`
	}
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(9)
	}
	if strings.HasPrefix(req.CaseID, "stderr") {
		os.Stderr.WriteString(v7StderrSecret + "\n")
	}
	out, ok := v7Printed[req.CaseID]
	if !ok {
		out = v7Printed["json"]
	}
	os.Stdout.Write(out)
	if req.CaseID == "sleep" {
		time.Sleep(10 * time.Second)
	}
	os.Exit(v7Exit[req.CaseID])
}

func v7Target(t *testing.T) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"command": exe, "args": []string{"-test.run=^TestV7TargetHelper$", "--", "mode=v7"}}
}

func v7Cases(ids ...string) []map[string]any {
	cases := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		cases = append(cases, map[string]any{"id": id, "input": map[string]any{"q": id}, "expected": map[string]any{"answer": "yes"}})
	}
	return cases
}

// v7Config is a v3 configuration over the helper target with one required
// exact_match scorer.
func v7Config(t *testing.T, concurrency int, ids ...string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version": 3, "cases": v7Cases(ids...), "target": v7Target(t),
		"scorers":   []any{map[string]any{"id": "exact", "kind": "exact_match", "threshold": 1, "required": true}},
		"execution": map[string]any{"concurrency": concurrency, "scorer_max_attempts": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func v7Run(t *testing.T, raw []byte) (Outcome, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), raw, dir)
	if o.State != "COMPLETED" {
		t.Fatalf("run: %+v", o)
	}
	return o, dir
}

func v7Sum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// v7Whole is the retained form of an output kept in full.
func v7Whole(form OutputForm, printed []byte) *TargetOutput {
	out := &TargetOutput{Form: form, Size: int64(len(printed)), SHA256: v7Sum(printed)}
	if len(printed) > 0 {
		out.Bytes = printed
	}
	return out
}

func TestV7AC01EveryExecutePathRetains(t *testing.T) {
	type want struct {
		output       *TargetOutput
		targetStatus string
		message      string
	}
	oversized := v7Printed["oversized"]
	for name, tc := range map[string]struct {
		ids  []string
		want []want
	}{
		"small outputs": {
			ids: []string{"json", "malformed", "two-values", "empty", "crash-json", "partial", "bad-utf8"},
			want: []want{
				{v7Whole(OutputJSON, v7Printed["json"]), "SUCCEEDED", ""},
				{v7Whole(OutputMalformed, v7Printed["malformed"]), "ERRORED", "target output must be exactly one JSON value"},
				{v7Whole(OutputMalformed, v7Printed["two-values"]), "ERRORED", "target output must be exactly one JSON value"},
				{v7Whole(OutputEmpty, nil), "ERRORED", "target output must be exactly one JSON value"},
				{v7Whole(OutputJSON, v7Printed["crash-json"]), "ERRORED", "target process failed"},
				{v7Whole(OutputMalformed, v7Printed["partial"]), "ERRORED", "target process failed"},
				{v7Whole(OutputJSON, v7Printed["bad-utf8"]), "SUCCEEDED", ""},
			},
		},
		"limits": {
			ids: []string{"exact-limit", "oversized"},
			want: []want{
				{v7Whole(OutputJSON, v7Printed["exact-limit"]), "SUCCEEDED", ""},
				{&TargetOutput{Form: OutputOversized, Size: int64(len(oversized)), SHA256: v7Sum(oversized), Truncated: true, Bytes: oversized[:maxTargetBytes]},
					"ERRORED", "target output exceeds V1 size limit"},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, _ := v7Run(t, v7Config(t, 4, tc.ids...))
			var messages []string
			for _, d := range o.Diagnostics {
				messages = append(messages, d.Message)
			}
			var wantMessages []string
			for i, w := range tc.want {
				got := o.Cases[i]
				if !reflect.DeepEqual(got.Output, w.output) || got.TargetStatus != w.targetStatus {
					t.Errorf("%s: status %s output %s, want %s %s", tc.ids[i], got.TargetStatus, v7Describe(got.Output), w.targetStatus, v7Describe(w.output))
				}
				if w.message != "" {
					wantMessages = append(wantMessages, w.message)
				}
			}
			if !reflect.DeepEqual(messages, wantMessages) {
				t.Errorf("diagnostic messages %q, want %q", messages, wantMessages)
			}
		})
	}
	t.Run("v1", func(t *testing.T) {
		o, _ := v7Run(t, configFor(t, "match"))
		if want := v7Whole(OutputJSON, []byte(`{"answer":"yes"}`)); !reflect.DeepEqual(o.Case.Output, want) {
			t.Errorf("v1 match output %s, want %s", v7Describe(o.Case.Output), v7Describe(want))
		}
		o, _ = v7Run(t, configFor(t, "crash"))
		if want := v7Whole(OutputEmpty, nil); !reflect.DeepEqual(o.Case.Output, want) || o.Case.TargetStatus != "ERRORED" {
			t.Errorf("v1 crash %s output %s, want %s", o.Case.TargetStatus, v7Describe(o.Case.Output), v7Describe(want))
		}
	})
	t.Run("never started", func(t *testing.T) {
		missing := Config{}
		missing.Case.ID, missing.Case.Input = "x", json.RawMessage(`1`)
		missing.Target.Command = filepath.Join(t.TempDir(), "no-such-target")
		s, err := execute(context.Background(), missing)
		if want := v7Whole(OutputEmpty, nil); err == nil || err.Error() != "target process failed" || !reflect.DeepEqual(retainOutput(s, maxTargetBytes), want) {
			t.Errorf("missing command: err=%v output %s", err, v7Describe(retainOutput(s, maxTargetBytes)))
		}
		missing.Target.Command = "/bin/sh"
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "no-such-dir"))
		s, err = execute(context.Background(), missing)
		if want := v7Whole(OutputEmpty, nil); err == nil || err.Error() != "cannot prepare target workspace" || !reflect.DeepEqual(retainOutput(s, maxTargetBytes), want) {
			t.Errorf("workspace failure: err=%v output %s", err, v7Describe(retainOutput(s, maxTargetBytes)))
		}
	})
}

func v7Describe(o *TargetOutput) string {
	if o == nil {
		return "<none>"
	}
	return fmt.Sprintf("%s/size=%d/kept=%d/truncated=%v/sha=%.12s/%q", o.Form, o.Size, len(o.Bytes), o.Truncated, o.SHA256, o.Bytes[:min(len(o.Bytes), 40)])
}

func TestV7AC02InvalidCasesRetainNothing(t *testing.T) {
	o, _ := v7Run(t, v7Config(t, 2, "dup", "dup", "", "json"))
	for i, c := range o.Cases {
		if invalid := i < 3; (c.Output == nil) != invalid || (c.TargetStatus == "SKIPPED") != invalid {
			t.Errorf("case %d (%q): %s output %s", i, c.ID, c.TargetStatus, v7Describe(c.Output))
		}
	}
}

// Every retained output is base64, so the secret is searched for in the
// decoded bytes as well as in the raw files and the run's stdout record.
func TestV7AC03StderrNeverRetained(t *testing.T) {
	o, dir := v7Run(t, v7Config(t, 2, "stderr", "stderr-crash"))
	stdout, _ := json.Marshal(o)
	haystacks := map[string][]byte{"stdout": stdout}
	for _, name := range []string{"run.json", "report.json", "commit.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		haystacks[name] = raw
	}
	for _, c := range o.Cases {
		if c.Output == nil {
			t.Fatalf("case %s retained nothing, so the check below proves nothing", c.ID)
		}
		haystacks["output of "+c.ID] = c.Output.Bytes
	}
	for name, h := range haystacks {
		if bytes.Contains(h, []byte(v7StderrSecret)) {
			t.Errorf("stderr reached %s", name)
		}
	}
	if !bytes.Contains(haystacks["output of stderr"], []byte(`"answer"`)) {
		t.Fatal("stdout of the same execution was not retained")
	}
}

func TestV7AC04DiagnosticsAttributed(t *testing.T) {
	judge := RegisteredScorer{Implementation: "v7/judge@1", Evaluate: func(_ context.Context, in ScorerInput) (AssessmentCandidate, error) {
		if in.CaseID == "b" {
			return AssessmentCandidate{}, errors.New("judge unavailable")
		}
		return v6Scored(1), nil
	}}
	raw, _ := json.Marshal(map[string]any{
		"version": 4, "cases": v7Cases("a", "dup", "dup", "", "partial", "b"), "target": v7Target(t),
		"scorers": []any{
			map[string]any{"id": "exact", "kind": "exact_match", "threshold": 1, "required": true},
			map[string]any{"id": "judge", "kind": "custom", "implementation": "v7/judge@1", "threshold": 0.5, "required": false},
		},
		"execution": map[string]any{"concurrency": 3, "scorer_max_attempts": 1},
	})
	o := RunJSONWithScorers(context.Background(), raw, filepath.Join(t.TempDir(), "artifact"), map[string]RegisteredScorer{"judge": judge})
	if o.State != "COMPLETED" {
		t.Fatalf("run: %+v", o)
	}
	invalid := "Invalid case identity or JSON input"
	want := []Diagnostic{
		{Code: "DATASET_INVALID_CASE", Scope: "CASE", Message: invalid, Subject: &DiagnosticSubject{CaseIndex: 1, CaseID: "dup"}},
		{Code: "DATASET_INVALID_CASE", Scope: "CASE", Message: invalid, Subject: &DiagnosticSubject{CaseIndex: 2, CaseID: "dup"}},
		{Code: "DATASET_INVALID_CASE", Scope: "CASE", Message: invalid, Subject: &DiagnosticSubject{CaseIndex: 3, CaseID: ""}},
		{Code: "TARGET_EXECUTION_ERROR", Scope: "CASE", Message: "target process failed", Subject: &DiagnosticSubject{CaseIndex: 4, CaseID: "partial"}},
		{Code: "SCORER_OPERATION_ERROR", Scope: "SCORER", Message: "Scorer operation failed", Subject: &DiagnosticSubject{CaseIndex: 5, CaseID: "b", ScorerID: "judge"}},
	}
	if !reflect.DeepEqual(o.Diagnostics, want) {
		got, _ := json.Marshal(o.Diagnostics)
		t.Fatalf("diagnostics %s", got)
	}
	encoded, _ := json.Marshal(o.Diagnostics[2])
	if !bytes.Contains(encoded, []byte(`"subject":{"case_index":3,"case_id":""}`)) {
		t.Errorf("an empty case ID must still be written: %s", encoded)
	}

	v1, _ := v7Run(t, configFor(t, "crash"))
	if !reflect.DeepEqual(v1.Diagnostics, []Diagnostic{{Code: "TARGET_EXECUTION_ERROR", Scope: "CASE", Message: "target process failed", Subject: &DiagnosticSubject{CaseIndex: 0, CaseID: "case-1"}}}) {
		t.Errorf("v1 diagnostics %+v", v1.Diagnostics)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, o := range map[string]Outcome{
		"cancelled": RunJSON(cancelled, v7Config(t, 1, "json"), filepath.Join(t.TempDir(), "artifact")),
		"invalid":   RunJSON(context.Background(), []byte(`{"version":3}`), filepath.Join(t.TempDir(), "artifact")),
		"commit":    RunJSON(context.Background(), v7Config(t, 1, "partial"), filepath.Join(t.TempDir(), "missing", "artifact")),
	} {
		last := o.Diagnostics[len(o.Diagnostics)-1]
		if last.Scope != "RUN" || last.Subject != nil {
			t.Errorf("%s: RUN diagnostic attributed: %+v", name, last)
		}
	}
}

func TestV7AC09CancellationRetainsNothing(t *testing.T) {
	sleeper := Config{}
	sleeper.Case.ID, sleeper.Case.Input = "sleep", json.RawMessage(`1`)
	sleeper.Target.Command, sleeper.Target.Args = v7Target(t)["command"].(string), v7Target(t)["args"].([]string)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	s, err := execute(ctx, sleeper)
	if !errors.Is(err, context.DeadlineExceeded) || retainOutput(s, maxTargetBytes) != nil {
		t.Fatalf("cancelled execute: err=%v output %s", err, v7Describe(retainOutput(s, maxTargetBytes)))
	}

	ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(ctx, v7Config(t, 2, "json", "sleep"), dir)
	stdout, _ := json.Marshal(o)
	if o.State != "CANCELLED" || o.Cases != nil || bytes.Contains(stdout, []byte(`"output"`)) {
		t.Fatalf("cancelled run: %s", stdout)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cancelled run published: %v", err)
	}
}
