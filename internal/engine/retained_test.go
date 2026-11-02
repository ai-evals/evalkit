package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const v7StderrSecret = "evalkit-v7-stderr-secret"

// v7Printed is exactly what TestV7TargetHelper writes to stdout for each
// behavior, and v7Exit its exit status. A case's behavior is its ID up to an
// optional "#suffix", unless the target has an "as=<id>:<behavior>" argument.
// Any other behavior prints {"answer":"yes"}.
var v7Printed = map[string][]byte{
	"json":        []byte("{\"answer\":\"yes\"}\n"),
	"spaced":      []byte("{\"answer\": \"yes\"}\n"),
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
	behavior, _, _ := strings.Cut(req.CaseID, "#")
	for _, a := range os.Args {
		if id, b, ok := strings.Cut(strings.TrimPrefix(a, "as="), ":"); ok && id == req.CaseID {
			behavior = b
		}
	}
	out, ok := v7Printed[behavior]
	if !ok {
		out = v7Printed["json"]
	}
	os.Stdout.Write(out)
	if req.CaseID == "sleep" {
		time.Sleep(10 * time.Second)
	}
	os.Exit(v7Exit[behavior])
}

func v7Target(t *testing.T, overrides ...string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestV7TargetHelper$", "--", "mode=v7"}
	for _, o := range overrides {
		args = append(args, "as="+o)
	}
	return map[string]any{"command": exe, "args": args}
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

// v7Synthetic is a COMPLETED v2-shaped Outcome that VerifyArtifact accepts:
// n passing cases that each printed printed, and a CASE diagnostic on case 0
// whose message pads run.json by pad bytes.
func v7Synthetic(n int, printed []byte, pad int) Outcome {
	quality := "PASS"
	o := Outcome{SchemaVersion: 1, RunID: "synthetic", State: "COMPLETED", Quality: &quality, ConfigDigest: "d", ArtifactDir: "a"}
	o.Measurement = &MeasurementDefinition{SchemaVersion: 1, Scorers: []V2Scorer{{ID: "exact", Kind: "exact_match", Threshold: 1, Required: true}}}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		o.Measurement.CaseIDs = append(o.Measurement.CaseIDs, id)
		o.Cases = append(o.Cases, V2CaseResult{ID: id, State: "SCORED", TargetStatus: "SUCCEEDED",
			Scorers: []ScorerObservation{{ID: "exact", Status: "SUCCEEDED", Score: v6Ptr(1)}}, Output: v7Whole(OutputJSON, printed)})
	}
	o.Diagnostics = []Diagnostic{caseDiagnostic("PAD", strings.Repeat("x", pad), 0, "c0")}
	return o
}

func v7Size(t *testing.T, o Outcome, retention OutputRetention) int {
	t.Helper()
	o.RetainedOutput = retention
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return len(b) + 1
}

func TestV7AC05BudgetLadder(t *testing.T) {
	for n, want := range map[int]int{1: maxTargetBytes, 8: maxTargetBytes, 9: 932067, 100: 83886, 1000: 8388, 40000: 209} {
		if got := retentionAllowance(n); got != want {
			t.Errorf("retentionAllowance(%d) = %d, want %d", n, got, want)
		}
	}
	limit := int(maxEvidenceFileBytes)
	small := []byte(`"` + strings.Repeat("s", 98) + `"`)
	huge := append(append([]byte{'"'}, bytes.Repeat([]byte{'h'}, maxTargetBytes-2)...), '"')
	skeleton := func(o Outcome) Outcome { return mapOutputs(o, keepPrefix(0)) }
	dropped := func(o Outcome) Outcome { return mapOutputs(o, dropOutput) }
	base := v7Synthetic(4, small, 0)
	padSkeleton := limit - v7Size(t, skeleton(base), OutputsTrimmed) - 100
	gap := v7Size(t, skeleton(base), OutputsTrimmed) - v7Size(t, dropped(base), OutputsNone)
	padDropped := limit - v7Size(t, dropped(base), OutputsNone) - gap/2

	for name, tc := range map[string]struct {
		o         Outcome
		retention OutputRetention
		kept      func(t *testing.T, kept []int)
	}{
		"as is": {v7Synthetic(4, small, 0), OutputsRetained, func(t *testing.T, kept []int) {
			if !slices.Equal(kept, []int{100, 100, 100, 100}) {
				t.Errorf("kept %v", kept)
			}
		}},
		"common prefix": {v7Synthetic(20, huge, 0), OutputsTrimmed, func(t *testing.T, kept []int) {
			if kept[0] < 600_000 || kept[0] >= maxTargetBytes || slices.Max(kept) != slices.Min(kept) {
				t.Errorf("kept %v", kept)
			}
		}},
		"metadata only": {v7Synthetic(4, small, padSkeleton), OutputsTrimmed, func(t *testing.T, kept []int) {
			if !slices.Equal(kept, []int{0, 0, 0, 0}) {
				t.Errorf("kept %v", kept)
			}
		}},
		"no outputs": {v7Synthetic(4, small, padDropped), OutputsNone, func(t *testing.T, kept []int) {
			if kept != nil {
				t.Errorf("kept %v", kept)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "artifact")
			sealed, err := publish(context.Background(), dir, tc.o)
			if err != nil {
				t.Fatalf("publish: %v", err)
			}
			var kept []int
			for _, c := range sealed.Cases {
				if c.Output != nil {
					kept = append(kept, len(c.Output.Bytes))
					if want := tc.o.Cases[0].Output; c.Output.Size != want.Size || c.Output.SHA256 != want.SHA256 || c.Output.Form != want.Form ||
						!bytes.HasPrefix(want.Bytes, c.Output.Bytes) || c.Output.Truncated != (len(c.Output.Bytes) < len(want.Bytes)) {
						t.Errorf("trimmed output %s is not a prefix of %s", v7Describe(c.Output), v7Describe(want))
					}
				}
			}
			tc.kept(t, kept)
			untrimmed := dropped(sealed)
			untrimmed.RetainedOutput = ""
			if sealed.RetainedOutput != tc.retention || !reflect.DeepEqual(untrimmed, dropped(tc.o)) {
				t.Errorf("sealed retention %s, or trimming changed more than outputs", sealed.RetainedOutput)
			}
			for _, name := range []string{"run.json", "report.json", "commit.json"} {
				if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Size() > maxEvidenceFileBytes {
					t.Errorf("%s: %v", name, err)
				}
			}
			verified, err := VerifyArtifact(dir)
			if err != nil || !reflect.DeepEqual(verified, sealed) {
				t.Fatalf("sealed record is not what run.json holds: %v", err)
			}
			if view, err := InspectArtifact(context.Background(), dir); err != nil || view.RetainedOutput != tc.retention {
				t.Errorf("inspect %+v %v", view, err)
			}
			again, _, _ := encodeRunEvidence(tc.o)
			first, _ := os.ReadFile(filepath.Join(dir, "run.json"))
			if !bytes.Equal(first, again) {
				t.Error("encoding is not deterministic")
			}
		})
	}

	t.Run("too large", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "artifact")
		over := v7Synthetic(4, small, limit-v7Size(t, dropped(base), OutputsNone)+1)
		if _, err := publish(context.Background(), dir, over); !errors.Is(err, errRunEvidenceTooLarge) {
			t.Fatalf("publish: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("published: %v", err)
		}
	})

	t.Run("too large end to end", func(t *testing.T) {
		scorers := make([]any, 20)
		for j := range scorers {
			scorers[j] = map[string]any{"id": fmt.Sprintf("s%02d", j), "kind": "exact_match", "threshold": 1, "required": true}
		}
		ids := make([]string, 10000)
		for i := range ids {
			ids[i] = "dup"
		}
		raw, _ := json.Marshal(map[string]any{"version": 2, "cases": v7Cases(ids...), "target": v7Target(t), "scorers": scorers})
		dir := filepath.Join(t.TempDir(), "artifact")
		o := RunJSON(context.Background(), raw, dir)
		if last := o.Diagnostics[len(o.Diagnostics)-1]; o.State != "FAILED" || last.Code != "ARTIFACT_COMMIT_FAILED" || len(o.Cases) != len(ids) {
			t.Fatalf("state %s, last diagnostic %+v, %d cases", o.State, last, len(o.Cases))
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("an artifact inspect cannot read was published: %v", err)
		}
	})
}

// v7Base is a v4 run whose artifact holds every kind of retained evidence:
// a scored case, a duplicate pair, a failed target and a scorer error.
func v7Base(t *testing.T) string {
	t.Helper()
	judge := RegisteredScorer{Implementation: "v7/judge@1", Evaluate: func(_ context.Context, in ScorerInput) (AssessmentCandidate, error) {
		if in.CaseID == "b" {
			return AssessmentCandidate{}, errors.New("judge unavailable")
		}
		return v6Scored(1), nil
	}}
	raw, _ := json.Marshal(map[string]any{
		"version": 4, "cases": v7Cases("json", "dup", "dup", "partial", "b"), "target": v7Target(t),
		"scorers": []any{
			map[string]any{"id": "exact", "kind": "exact_match", "threshold": 1, "required": true},
			map[string]any{"id": "judge", "kind": "custom", "implementation": "v7/judge@1", "threshold": 0.5, "required": false},
		},
		"execution": map[string]any{"concurrency": 2, "scorer_max_attempts": 1},
	})
	dir := filepath.Join(t.TempDir(), "artifact")
	if o := RunJSONWithScorers(context.Background(), raw, dir, map[string]RegisteredScorer{"judge": judge}); o.State != "COMPLETED" {
		t.Fatalf("run: %+v", o)
	}
	return dir
}

func TestV7AC11VerifierRefusesImpossibleEvidence(t *testing.T) {
	base := v7Base(t)
	cases := func(run map[string]any) []any { return run["cases"].([]any) }
	output := func(run map[string]any, i int) map[string]any {
		return cases(run)[i].(map[string]any)["output"].(map[string]any)
	}
	subject := func(run map[string]any, i int) map[string]any {
		return run["diagnostics"].([]any)[i].(map[string]any)["subject"].(map[string]any)
	}
	b64 := func(b string) string { return base64.StdEncoding.EncodeToString([]byte(b)) }
	stripSubjects := func(run map[string]any) {
		for _, d := range run["diagnostics"].([]any) {
			delete(d.(map[string]any), "subject")
		}
	}
	stripOutputs := func(run map[string]any) {
		for _, c := range cases(run) {
			delete(c.(map[string]any), "output")
		}
	}
	for name, edit := range map[string]func(run, report map[string]any){
		"unknown form":            func(run, _ map[string]any) { output(run, 0)["form"] = "XML" },
		"missing form":            func(run, _ map[string]any) { delete(output(run, 0), "form") },
		"bytes longer than size":  func(run, _ map[string]any) { output(run, 0)["size"] = 16 },
		"size beyond whole bytes": func(run, _ map[string]any) { output(run, 0)["size"] = 18 },
		"whole output truncated":  func(run, _ map[string]any) { output(run, 0)["truncated"] = true },
		"other bytes":             func(run, _ map[string]any) { output(run, 0)["bytes"] = b64("{\"answer\":\"yeS\"}\n") },
		"sha256 of other bytes":   func(run, _ map[string]any) { output(run, 0)["sha256"] = v7Sum([]byte("x")) },
		"sha256 not lowercase": func(run, _ map[string]any) {
			output(run, 0)["sha256"] = strings.ToUpper(output(run, 0)["sha256"].(string))
		},
		"empty form with bytes":   func(run, _ map[string]any) { output(run, 3)["form"] = "EMPTY" },
		"oversized form in limit": func(run, _ map[string]any) { output(run, 3)["form"] = "OVERSIZED" },
		"malformed label on json": func(run, _ map[string]any) { output(run, 0)["form"] = "MALFORMED" },
		"succeeded target with malformed prefix": func(run, _ map[string]any) {
			o := output(run, 0)
			o["form"], o["bytes"], o["truncated"] = "MALFORMED", b64("{\"a"), true
			run["retained_output"] = "TRIMMED"
		},
		"output on an invalid case":    func(run, _ map[string]any) { cases(run)[1].(map[string]any)["output"] = output(run, 3) },
		"executed case without output": func(run, _ map[string]any) { delete(cases(run)[3].(map[string]any), "output") },
		"retention says whole":         func(run, _ map[string]any) { o := output(run, 0); o["bytes"], o["truncated"] = b64("{"), true },
		"retention says none":          func(run, _ map[string]any) { run["retained_output"] = "NONE" },
		"unknown retention":            func(run, _ map[string]any) { run["retained_output"] = "MOST" },
		"outputs without retention":    func(run, _ map[string]any) { delete(run, "retained_output"); stripSubjects(run) },
		"subjects without retention":   func(run, _ map[string]any) { delete(run, "retained_output"); stripOutputs(run) },
		"unattributed diagnostic":      func(run, _ map[string]any) { delete(run["diagnostics"].([]any)[0].(map[string]any), "subject") },
		"run diagnostic": func(run, _ map[string]any) {
			run["diagnostics"].([]any)[0].(map[string]any)["scope"] = "RUN"
		},
		"case index out of range":       func(run, _ map[string]any) { subject(run, 0)["case_index"] = 5 },
		"negative case index":           func(run, _ map[string]any) { subject(run, 0)["case_index"] = -1 },
		"another case's ID":             func(run, _ map[string]any) { subject(run, 0)["case_id"] = "json" },
		"case diagnostic with a scorer": func(run, _ map[string]any) { subject(run, 0)["scorer_id"] = "exact" },
		"unknown scorer":                func(run, _ map[string]any) { subject(run, 3)["scorer_id"] = "nope" },
		"scorer diagnostic, no scorer":  func(run, _ map[string]any) { delete(subject(run, 3), "scorer_id") },
		"output in the report": func(_, report map[string]any) {
			report["cases"].([]any)[0].(map[string]any)["output"] = map[string]any{}
		},
		"retention in the report": func(_, report map[string]any) { report["retained_output"] = "RETAINED" },
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "artifact")
			if err := os.CopyFS(dir, os.DirFS(base)); err != nil {
				t.Fatal(err)
			}
			v6Rewrite(t, dir, edit)
			if o, err := VerifyArtifact(dir); err == nil {
				got, _ := json.Marshal(o)
				t.Fatalf("accepted %s", got)
			}
		})
	}
	v6Rewrite(t, base, func(_, _ map[string]any) {})
	if _, err := VerifyArtifact(base); err != nil {
		t.Fatalf("an unmodified map round trip is refused, so every refusal above may be vacuous: %v", err)
	}
}

func TestV7AC12ReportKeepsV060Shape(t *testing.T) {
	o, dir := v7Run(t, v7Config(t, 2, "json", "oversized", "dup", "dup"))
	report, _ := os.ReadFile(filepath.Join(dir, "report.json"))
	var shape struct {
		Keys  map[string]json.RawMessage
		Cases []map[string]json.RawMessage `json:"cases"`
	}
	if json.Unmarshal(report, &shape.Keys) != nil || json.Unmarshal(report, &shape) != nil {
		t.Fatal("report.json does not decode")
	}
	if got := slices.Sorted(maps.Keys(shape.Keys)); !slices.Equal(got, []string{"cases", "quality", "run_id"}) {
		t.Errorf("report keys %v", got)
	}
	for _, c := range shape.Cases {
		if got := slices.Sorted(maps.Keys(c)); !slices.Equal(got, []string{"id", "scorers", "state", "target_status"}) {
			t.Errorf("report case keys %v", got)
		}
	}
	if o.RetainedOutput != OutputsTrimmed || o.Cases[1].Output == nil || !o.Cases[1].Output.Truncated {
		t.Errorf("retention %s", o.RetainedOutput)
	}
	view, err := InspectArtifact(context.Background(), dir)
	encoded, _ := json.Marshal(view)
	if err != nil || !bytes.Contains(encoded, []byte(`"measurement_accounting":"REPORTED_DEFINITION_CONSISTENT","retained_output":"TRIMMED","authenticated_author":false}`)) {
		t.Errorf("inspect %s %v", encoded, err)
	}
	_, whole := v7Run(t, v7Config(t, 1, "json"))
	if view, err := InspectArtifact(context.Background(), whole); err != nil || view.RetainedOutput != OutputsRetained {
		t.Errorf("inspect %+v %v", view, err)
	}
}

func TestV7AC08ConcurrencyDoesNotChangeArtifacts(t *testing.T) {
	var ids []string
	for k := range 4 {
		for _, b := range []string{"json", "no", "malformed", "partial", "oversized"} {
			ids = append(ids, fmt.Sprintf("%s#%d", b, k))
		}
	}
	normalized := func(concurrency int) map[string][]byte {
		o, dir := v7Run(t, v7Config(t, concurrency, ids...))
		files := map[string][]byte{}
		for _, name := range []string{"run.json", "report.json"} {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, varying := range []string{o.RunID, dir, o.ConfigDigest} {
				raw = bytes.ReplaceAll(raw, []byte(varying), []byte("VARIES"))
			}
			files[name] = raw
		}
		if o.RetainedOutput != OutputsTrimmed {
			t.Fatalf("retention %s: the oversized cases should have been cut to the allowance", o.RetainedOutput)
		}
		return files
	}
	serial, parallel := normalized(1), normalized(16)
	for name := range serial {
		if !bytes.Equal(serial[name], parallel[name]) {
			t.Errorf("%s differs between concurrency 1 and 16", name)
		}
	}
}
