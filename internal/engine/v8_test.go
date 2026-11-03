package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const v8EchoTarget = `IFS= read -r line; printf '%s\n' "$line"`

// v8Script writes a /bin/sh scorer and returns its absolute path.
func v8Script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scorer.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func v8Command(id, path string, required bool, threshold float64, args ...string) map[string]any {
	sc := map[string]any{"id": id, "kind": "command", "command": path, "threshold": threshold, "required": required}
	if len(args) > 0 {
		sc["args"] = args
	}
	return sc
}

func v8Config(t *testing.T, version int, ids []string, scorers []map[string]any, concurrency, attempts int) []byte {
	t.Helper()
	cases := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		cases = append(cases, map[string]any{"id": id, "input": map[string]any{"q": id}, "expected": map[string]any{"a": id}})
	}
	cfg := map[string]any{
		"version": version, "cases": cases, "scorers": scorers,
		"target": map[string]any{"command": "/bin/sh", "args": []string{"-c", v8EchoTarget}},
	}
	if version > 2 {
		cfg["execution"] = map[string]any{"concurrency": concurrency, "scorer_max_attempts": attempts}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func v8Run(t *testing.T, raw []byte) (Outcome, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), raw, dir)
	if o.State != "COMPLETED" {
		t.Fatalf("run did not complete: %+v", o)
	}
	return o, dir
}

func v8Codes(o Outcome) []string {
	var out []string
	for _, d := range o.Diagnostics {
		out = append(out, d.Code)
	}
	return out
}

func TestV8AC02UnevaluableCommandScorer(t *testing.T) {
	uneval := v8Script(t, `cat >/dev/null; echo '{"unevaluable":"EVIDENCE_INSUFFICIENT"}'`)
	one := v8Script(t, `cat >/dev/null; echo '{"score":1}'`)

	o, _ := v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", uneval, true, 0.5)}, 1, 1))
	got := o.Cases[0]
	if *o.Quality != "INDETERMINATE" || got.State != "ERRORED" || got.Scorers[0].Status != "UNEVALUABLE" || got.Scorers[0].Reason != "EVIDENCE_INSUFFICIENT" || len(o.Diagnostics) != 0 {
		t.Fatalf("required unevaluable: quality %s case %+v diagnostics %v", *o.Quality, got, o.Diagnostics)
	}

	o, _ = v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", one, true, 0.5), v8Command("advisory", uneval, false, 0.5)}, 1, 1))
	if *o.Quality != "PASS" || o.Cases[0].State != "SCORED" || o.Cases[0].Scorers[1].Status != "UNEVALUABLE" {
		t.Fatalf("optional unevaluable: quality %s case %+v", *o.Quality, o.Cases[0])
	}
}

func TestV8AC03InvalidScorerStdout(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":         `echo 'not json'`,
		"both keys":         `echo '{"score":1,"unevaluable":"EVIDENCE_MISSING"}'`,
		"neither key":       `echo '{}'`,
		"unknown key":       `echo '{"score":1,"why":"close"}'`,
		"score above one":   `echo '{"score":1.5}'`,
		"negative score":    `echo '{"score":-0.1}'`,
		"null score":        `echo '{"score":null}'`,
		"string score":      `echo '{"score":"1"}'`,
		"unknown reason":    `echo '{"unevaluable":"TOO_HARD"}'`,
		"two values":        `echo '{"score":1}{"score":1}'`,
		"array":             `echo '[1]'`,
		"empty":             `true`,
		"oversized":         `head -c 1048577 /dev/zero | tr '\0' ' '; echo '{"score":1}'`,
		"oversized garbage": `head -c 2000000 /dev/zero`,
	} {
		t.Run(name, func(t *testing.T) {
			path := v8Script(t, "cat >/dev/null; "+body)
			o, dir := v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", path, true, 0.5)}, 1, 3))
			obs := o.Cases[0].Scorers[0]
			if *o.Quality != "INDETERMINATE" || obs.Status != "ERRORED" || obs.Attempts != 1 || obs.Score != nil {
				t.Fatalf("quality %s observation %+v", *o.Quality, obs)
			}
			if len(o.Diagnostics) != 1 || o.Diagnostics[0].Code != "SCORER_RESULT_INVALID" || o.Diagnostics[0].Message != "Scorer returned an invalid assessment" {
				t.Fatalf("diagnostics %+v", o.Diagnostics)
			}
			if _, err := VerifyArtifact(dir); err != nil {
				t.Fatalf("artifact: %v", err)
			}
		})
	}
}

func TestV8AC04ExitTempFailIsRetried(t *testing.T) {
	counting := v8Script(t, `cat >/dev/null
n=$(cat "$1" 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > "$1"
[ "$n" -ge "$2" ] && { echo '{"score":1}'; exit 0; }
exit 75`)
	dir := t.TempDir()

	recovers := filepath.Join(dir, "recovers")
	o, _ := v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", counting, true, 1, recovers, "2")}, 1, 3))
	obs := o.Cases[0].Scorers[0]
	if *o.Quality != "PASS" || obs.Status != "SUCCEEDED" || obs.Attempts != 2 || len(o.Diagnostics) != 0 {
		t.Fatalf("75 then 0: quality %s observation %+v diagnostics %v", *o.Quality, obs, o.Diagnostics)
	}
	if n, _ := os.ReadFile(recovers); strings.TrimSpace(string(n)) != "2" {
		t.Fatalf("scorer executed %q times, want 2", n)
	}

	forever := filepath.Join(dir, "forever")
	o, _ = v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", counting, true, 1, forever, "99")}, 1, 3))
	obs = o.Cases[0].Scorers[0]
	if *o.Quality != "INDETERMINATE" || obs.Status != "ERRORED" || obs.Attempts != 3 || fmt.Sprint(v8Codes(o)) != "[SCORER_OPERATION_ERROR]" {
		t.Fatalf("75 forever: quality %s observation %+v diagnostics %v", *o.Quality, obs, o.Diagnostics)
	}
	if n, _ := os.ReadFile(forever); strings.TrimSpace(string(n)) != "3" {
		t.Fatalf("scorer executed %q times, want scorer_max_attempts 3", n)
	}
}

func TestV8AC05OtherFailuresAreNotRetried(t *testing.T) {
	for name, path := range map[string]string{
		"exit 1":          v8Script(t, `cat >/dev/null; echo '{"score":1}'; exit 1`),
		"killed":          v8Script(t, `cat >/dev/null; kill -9 $$`),
		"missing command": filepath.Join(t.TempDir(), "no-such-scorer"),
	} {
		t.Run(name, func(t *testing.T) {
			o, _ := v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", path, true, 0.5)}, 1, 3))
			obs := o.Cases[0].Scorers[0]
			if *o.Quality != "INDETERMINATE" || obs.Status != "ERRORED" || obs.Attempts != 1 || fmt.Sprint(v8Codes(o)) != "[SCORER_OPERATION_ERROR]" || o.Diagnostics[0].Message != "Scorer operation failed" {
				t.Fatalf("quality %s observation %+v diagnostics %+v", *o.Quality, obs, o.Diagnostics)
			}
		})
	}
}

func TestV8ScorerEnvHelper(t *testing.T) {
	out := os.Getenv("V8_ENV_OUT")
	for _, a := range os.Args {
		if p, ok := strings.CutPrefix(a, "v8env="); ok {
			out = p
		}
	}
	if out == "" {
		return
	}
	_ = os.WriteFile(out, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	os.Stdout.WriteString(`{"score":1}`)
	os.Exit(0)
}

func TestV8AC06ScorerSeesProtocolOnly(t *testing.T) {
	t.Setenv("EVALKIT_V8_SECRET", "parent-secret")
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	recorder := v8Script(t, `cat > "$1"; echo 'stderr-secret-9f2c' >&2; echo '{"score":1}'`)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	envOut := filepath.Join(dir, "env")
	raw, _ := json.Marshal(map[string]any{
		"version": 5,
		"cases":   []any{map[string]any{"id": "c1", "input": map[string]any{"q": "<&>"}, "expected": map[string]any{"a": 1}}},
		"target":  map[string]any{"command": "/bin/sh", "args": []string{"-c", v8EchoTarget}},
		"scorers": []any{
			v8Command("recorder", recorder, true, 1, stdin),
			v8Command("env", exe, false, 1, "-test.run=^TestV8ScorerEnvHelper$", "--", "v8env="+envOut),
		},
		"execution": map[string]any{"concurrency": 1, "scorer_max_attempts": 1},
	})
	o, artifact := v8Run(t, raw)
	if *o.Quality != "PASS" {
		t.Fatalf("quality %s: %+v", *o.Quality, o)
	}

	got, _ := os.ReadFile(stdin)
	const escaped = "\\u003c\\u0026\\u003e" // evalkit encodes the config and target request with HTML escaping
	want := `{"case_id":"c1","scorer_id":"recorder","input":{"q":"` + escaped + `"},"expected":{"a":1},"output":{"case_id":"c1","input":{"q":"` + escaped + `"}}}` + "\n"
	if string(got) != want {
		t.Fatalf("scorer stdin\n got %q\nwant %q", got, want)
	}

	env, _ := os.ReadFile(envOut)
	if lines := strings.Split(string(env), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "PATH=") {
		t.Fatalf("scorer environment %q, want only PATH", env)
	}

	stdout, _ := json.Marshal(o)
	for _, name := range []string{"run.json", "report.json", "commit.json"} {
		b, _ := os.ReadFile(filepath.Join(artifact, name))
		stdout = append(stdout, b...)
	}
	for _, secret := range []string{"stderr-secret-9f2c", "parent-secret"} {
		if strings.Contains(string(stdout), secret) {
			t.Fatalf("%q reached the outcome or artifacts", secret)
		}
	}
}

func TestV8AC07DeadlineKillsSleepingScorer(t *testing.T) {
	sleeper := v8Script(t, `cat >/dev/null; sleep 30; echo '{"score":1}'`)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "artifact")
	start := time.Now()
	o := RunJSON(ctx, v8Config(t, 5, []string{"c1", "c2"}, []map[string]any{v8Command("judge", sleeper, true, 0.5)}, 2, 3), dir)
	if o.State != "CANCELLED" || o.Quality != nil || fmt.Sprint(v8Codes(o)) != "[RUN_DEADLINE_EXCEEDED]" {
		t.Fatalf("outcome %+v", o)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("run took %s, the scorer was not killed at the deadline", elapsed)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled run published: %v", err)
	}
}

func TestV8AC08CommandScorerValidation(t *testing.T) {
	path := v8Script(t, `echo '{"score":1}'`)
	exact := map[string]any{"id": "exact", "kind": "exact_match", "threshold": 1, "required": true}
	for _, tc := range []struct {
		name    string
		version int
		scorer  map[string]any
		message string
	}{
		{"version 2", 2, v8Command("judge", path, true, 1), "command scorers require configuration version 5"},
		{"version 3", 3, v8Command("judge", path, true, 1), "command scorers require configuration version 5"},
		{"version 4", 4, v8Command("judge", path, true, 1), "command scorers require configuration version 5"},
		{"version 4 args on exact_match", 4, map[string]any{"id": "e", "kind": "exact_match", "threshold": 1, "required": true, "args": []string{"x"}}, "command scorers require configuration version 5"},
		{"no command", 5, map[string]any{"id": "judge", "kind": "command", "threshold": 1, "required": true}, "command scorer requires a command"},
		{"blank command", 5, v8Command("judge", "  ", true, 1), "command scorer requires a command"},
		{"command on exact_match", 5, map[string]any{"id": "e", "kind": "exact_match", "threshold": 1, "required": true, "command": path}, "only command scorers take command and args"},
		{"args on exact_match", 5, map[string]any{"id": "e", "kind": "exact_match", "threshold": 1, "required": true, "args": []string{"x"}}, "only command scorers take command and args"},
		{"implementation on command", 5, map[string]any{"id": "judge", "kind": "command", "command": path, "implementation": "x@1", "threshold": 1, "required": true}, "command scorer takes no implementation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "artifact")
			o := RunJSON(context.Background(), v8Config(t, tc.version, []string{"c1"}, []map[string]any{exact, tc.scorer}, 1, 1), dir)
			if o.State != "FAILED" || len(o.Diagnostics) != 1 || o.Diagnostics[0].Code != "CONFIG_INVALID" || !strings.HasSuffix(o.Diagnostics[0].Message, tc.message) {
				t.Fatalf("outcome %+v, want CONFIG_INVALID %q", o, tc.message)
			}
		})
	}
	o := RunJSON(context.Background(), []byte(`{"version":6}`), filepath.Join(t.TempDir(), "artifact"))
	if len(o.Diagnostics) != 1 || !strings.HasSuffix(o.Diagnostics[0].Message, "supported versions are 1, 2, 3, 4 and 5") {
		t.Fatalf("version 6: %+v", o.Diagnostics)
	}
}

func TestV8AC09CompareKeysOnCommandAndArgs(t *testing.T) {
	path := v8Script(t, `cat >/dev/null; echo '{"score":0.75}'`)
	publish := func(args ...string) string {
		_, dir := v8Run(t, v8Config(t, 5, []string{"c1", "c2"}, []map[string]any{v8Command("judge", path, true, 0.5, args...)}, 2, 1))
		return dir
	}
	baseline := publish("--strict")
	if cmp, err := CompareArtifacts(context.Background(), baseline, publish("--strict")); err != nil || cmp.Recommendation != RecommendShip {
		t.Fatalf("identical v5 runs: %+v err=%v", cmp, err)
	}
	_, err := CompareArtifacts(context.Background(), baseline, publish("--lenient"))
	var refused *ReadError
	if !errors.As(err, &refused) || refused.Code != CodeComparisonNotComparable || refused.Reason != ReasonScorersDiffer {
		t.Fatalf("args change: err=%v, want SCORERS_DIFFER", err)
	}
}

func TestV8AC10InspectVerifiesV5Artifact(t *testing.T) {
	path := v8Script(t, `cat >/dev/null; echo '{"score":0.75}'`)
	_, dir := v8Run(t, v8Config(t, 5, []string{"c1"}, []map[string]any{v8Command("judge", path, true, 0.5, "a b")}, 1, 1))
	in, err := InspectArtifact(context.Background(), dir)
	if err != nil || in.Quality != "PASS" || in.MeasurementAccounting != "REPORTED_DEFINITION_CONSISTENT" {
		t.Fatalf("inspect: %+v err=%v", in, err)
	}
	v, err := VerifyArtifact(dir)
	if err != nil || v.Measurement.Scorers[0].Command != path || fmt.Sprint(v.Measurement.Scorers[0].Args) != "[a b]" {
		t.Fatalf("measurement %+v err=%v", v.Measurement, err)
	}
}

func TestV8AC12ConcurrentCommandScorersKeepOrder(t *testing.T) {
	path := v8Script(t, `IFS= read -r l; case "$l" in *'"case_id":"odd'*) echo '{"score":0.25}';; *) echo '{"score":0.75}';; esac`)
	var ids []string
	for i := 0; i < 40; i++ {
		ids = append(ids, fmt.Sprintf("%s-%d", map[bool]string{true: "odd", false: "even"}[i%2 == 1], i))
	}
	o, _ := v8Run(t, v8Config(t, 5, ids, []map[string]any{v8Command("judge", path, true, 0.5)}, 16, 1))
	for i, c := range o.Cases {
		want := 0.75
		if i%2 == 1 {
			want = 0.25
		}
		if c.ID != ids[i] || c.Scorers[0].Score == nil || *c.Scorers[0].Score != want {
			t.Fatalf("slot %d holds %s score %v, want %s score %v", i, c.ID, c.Scorers[0].Score, ids[i], want)
		}
	}
	if *o.Quality != "FAIL" {
		t.Fatalf("quality %s", *o.Quality)
	}
}
