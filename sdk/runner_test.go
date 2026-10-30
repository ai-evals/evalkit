package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSDKTargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=sdk") {
		return
	}
	var req struct {
		CaseID string `json:"case_id"`
	}
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(9)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"answer": "yes"})
	os.Exit(0)
}

type functionScorer func(context.Context, ScorerInput) (Assessment, error)

func (f functionScorer) Evaluate(ctx context.Context, in ScorerInput) (Assessment, error) {
	return f(ctx, in)
}

func sdkConfig(t *testing.T, kind string, required bool, implementation string, cases int) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cs := make([]any, 0, cases)
	for i := 0; i < cases; i++ {
		cs = append(cs, map[string]any{"id": string(rune('a' + i)), "input": map[string]string{"q": "ready?"}, "expected": map[string]string{"answer": "yes"}})
	}
	config := map[string]any{
		"version": 4, "cases": cs,
		"target":    map[string]any{"command": exe, "args": []string{"-test.run=^TestSDKTargetHelper$", "--", "mode=sdk"}},
		"scorers":   []any{map[string]any{"id": "custom", "kind": kind, "implementation": implementation, "threshold": 1.0, "required": required}},
		"execution": map[string]any{"concurrency": 3, "scorer_max_attempts": 3},
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runnerWith(t *testing.T, scorer Scorer) *Runner {
	t.Helper()
	runner, err := NewRunner(Registration{ID: "custom", Implementation: "example/custom@1", Scorer: scorer})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func sdkRun(t *testing.T, runner *Runner, raw []byte) (Result, error) {
	t.Helper()
	return runner.Run(context.Background(), Request{ConfigJSON: raw, ArtifactDir: filepath.Join(t.TempDir(), "artifact")})
}

func TestV4AC01CustomScorerPass(t *testing.T) {
	r := runnerWith(t, functionScorer(func(_ context.Context, in ScorerInput) (Assessment, error) {
		if in.CaseID() == "" || in.ScorerID() != "custom" {
			t.Fatalf("incorrect input projection")
		}
		var got, want any
		_ = json.Unmarshal(in.ActualJSON(), &got)
		_ = json.Unmarshal(in.ExpectedJSON(), &want)
		if got != nil && want != nil {
			return Scored(1), nil
		}
		return Scored(0), nil
	}))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 2))
	q, ok := out.Quality()
	if err != nil || out.State() != "COMPLETED" || !ok || q != "PASS" {
		t.Fatalf("state=%s quality=%q err=%v", out.State(), q, err)
	}
}
func TestV4AC02ZeroScoreIsFail(t *testing.T) {
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Scored(0), nil }))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 1))
	q, _ := out.Quality()
	if err != nil || q != "FAIL" {
		t.Fatalf("quality=%q err=%v", q, err)
	}
}
func TestV4AC03UnevaluableIsIndeterminate(t *testing.T) {
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Unevaluable(ReasonEvidenceMissing), nil }))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 1))
	q, _ := out.Quality()
	if err != nil || q != "INDETERMINATE" {
		t.Fatalf("quality=%q err=%v", q, err)
	}
}
func TestV4AC04InvalidScoreRejected(t *testing.T) {
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Scored(1.7), nil }))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 1))
	q, _ := out.Quality()
	if err != nil || q != "INDETERMINATE" || !strings.Contains(string(out.JSON()), "SCORER_RESULT_INVALID") {
		t.Fatalf("result=%s err=%v", out.JSON(), err)
	}
}
func TestV4AC05BoundedTransientRetry(t *testing.T) {
	var calls atomic.Int64
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) {
		if calls.Add(1) < 3 {
			return Assessment{}, Retryable()
		}
		return Scored(1), nil
	}))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 1))
	q, _ := out.Quality()
	if err != nil || q != "PASS" || calls.Load() != 3 {
		t.Fatalf("quality=%q calls=%d err=%v", q, calls.Load(), err)
	}
	if !strings.Contains(string(out.JSON()), `"attempts":3`) {
		t.Fatal("attempt accounting missing")
	}
}
func TestV4AC06IdentityMismatchRejected(t *testing.T) {
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Scored(1), nil }))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/other@1", 1))
	if err == nil || out.State() != "FAILED" {
		t.Fatalf("state=%q err=%v", out.State(), err)
	}
}
func TestV4AC07ImmutableProjection(t *testing.T) {
	var calls atomic.Int64
	r := runnerWith(t, functionScorer(func(_ context.Context, in ScorerInput) (Assessment, error) {
		first := in.ActualJSON()
		copyFirst := in.ActualJSON()
		first[0] = '!'
		if string(copyFirst) != string(in.ActualJSON()) {
			t.Error("extension mutated scorer input")
		}
		calls.Add(1)
		return Scored(1), nil
	}))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 3))
	q, _ := out.Quality()
	if err != nil || q != "PASS" || calls.Load() != 3 {
		t.Fatalf("quality=%q calls=%d err=%v", q, calls.Load(), err)
	}
}
func TestV4AC08DefaultSerialScorerInstance(t *testing.T) {
	var active, peak atomic.Int64
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) {
		n := active.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		return Scored(1), nil
	}))
	out, err := sdkRun(t, r, sdkConfig(t, "custom", true, "example/custom@1", 3))
	q, _ := out.Quality()
	if err != nil || q != "PASS" || peak.Load() != 1 {
		t.Fatalf("quality=%q peak=%d err=%v", q, peak.Load(), err)
	}
}
func TestV4AC09CancellationIsTyped(t *testing.T) {
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Scored(1), nil }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := r.Run(ctx, Request{ConfigJSON: sdkConfig(t, "custom", true, "example/custom@1", 1), ArtifactDir: filepath.Join(t.TempDir(), "artifact")})
	if out.State() != "CANCELLED" || !errors.Is(err, context.Canceled) {
		t.Fatalf("state=%q err=%v", out.State(), err)
	}
}
