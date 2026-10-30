package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV3TargetHelper(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "mode=v3") {
		return
	}
	var input struct {
		CaseID string `json:"case_id"`
	}
	if json.NewDecoder(os.Stdin).Decode(&input) != nil {
		os.Exit(9)
	}
	switch input.CaseID {
	case "a":
		time.Sleep(200 * time.Millisecond)
	case "b":
		time.Sleep(20 * time.Millisecond)
	case "c":
		time.Sleep(100 * time.Millisecond)
	case "slow":
		time.Sleep(3 * time.Second)
	default:
		os.Exit(10)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"answer": "yes"})
	os.Exit(0)
}

func v3Config(t *testing.T, ids ...string) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cases := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		cases = append(cases, aCase(id, "yes"))
	}
	cfg := map[string]any{
		"version": 3, "cases": cases,
		"target":    map[string]any{"command": exe, "args": []string{"-test.run=^TestV3TargetHelper$", "--", "mode=v3"}},
		"scorers":   exactScorers(),
		"execution": map[string]any{"concurrency": 3, "scorer_max_attempts": 2},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestV3AC01ConcurrentCasesStableOrdering(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), v3Config(t, "a", "b", "c"), artifact)
	if o.State != "COMPLETED" || o.Quality == nil || *o.Quality != "PASS" {
		t.Fatalf("outcome: %+v", o)
	}
	for i, id := range []string{"a", "b", "c"} {
		if o.Cases[i].ID != id || o.Cases[i].Scorers[0].Attempts != 1 {
			t.Fatalf("ordering / attempts: %+v", o.Cases)
		}
	}
	if _, err := VerifyArtifact(artifact); err != nil {
		t.Fatal(err)
	}
}
func TestV3AC02BoundedRetrySuccess(t *testing.T) {
	calls := 0
	score, n, err := retryScorer(context.Background(), 3, func(context.Context) (float64, error) {
		calls++
		if calls < 3 {
			return 0, &RetryableScorerError{Err: errors.New("temporary")}
		}
		return 1, nil
	})
	if err != nil || score != 1 || n != 3 || calls != 3 {
		t.Fatalf("score=%v attempts=%d calls=%d err=%v", score, n, calls, err)
	}
}
func TestV3AC03NoRetryForPermanentError(t *testing.T) {
	calls := 0
	_, n, err := retryScorer(context.Background(), 5, func(context.Context) (float64, error) { calls++; return 0, errors.New("permanent") })
	if err == nil || calls != 1 || n != 1 {
		t.Fatalf("attempts=%d calls=%d err=%v", n, calls, err)
	}
}
func TestV3AC04RetryBudgetExhausted(t *testing.T) {
	calls := 0
	_, n, err := retryScorer(context.Background(), 2, func(context.Context) (float64, error) {
		calls++
		return 0, &RetryableScorerError{Err: errors.New("temporary")}
	})
	if err == nil || calls != 2 || n != 2 {
		t.Fatalf("attempts=%d calls=%d err=%v", n, calls, err)
	}
}
func TestV3AC05DeadlineCancelsRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(ctx, v3Config(t, "slow"), dir)
	if o.State != "CANCELLED" || o.Quality != nil {
		t.Fatalf("outcome: %+v", o)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cancelled artifact published: %v", err)
	}
}
func TestV3AC06NoConcurrencyEscalation(t *testing.T) {
	raw := v3Config(t, "a")
	var cfg V3Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Execution.Concurrency = 10000
	invalid, _ := json.Marshal(cfg)
	o := RunJSON(context.Background(), invalid, filepath.Join(t.TempDir(), "artifact"))
	if o.State != "FAILED" {
		t.Fatalf("outcome: %+v", o)
	}
}
