package sdk

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV8AC11SDKRunsCustomAndCommandScorers(t *testing.T) {
	scorer := filepath.Join(t.TempDir(), "half.sh")
	if err := os.WriteFile(scorer, []byte("#!/bin/sh\ncat >/dev/null\necho '{\"score\":0.5}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(sdkConfig(t, "custom", true, "example/custom@1", 2), &config); err != nil {
		t.Fatal(err)
	}
	config["version"] = 5
	config["scorers"] = append(config["scorers"].([]any), map[string]any{"id": "half", "kind": "command", "command": scorer, "threshold": 0.5, "required": true})
	raw, _ := json.Marshal(config)
	r := runnerWith(t, functionScorer(func(context.Context, ScorerInput) (Assessment, error) { return Scored(1), nil }))
	out, err := sdkRun(t, r, raw)
	q, ok := out.Quality()
	if err != nil || !ok || q != "PASS" {
		t.Fatalf("quality=%q err=%v: %s", q, err, out.JSON())
	}
	var o struct {
		Cases []struct {
			Scorers []struct {
				ID    string
				Score *float64
			}
		}
	}
	if err := json.Unmarshal(out.JSON(), &o); err != nil || len(o.Cases) != 2 {
		t.Fatalf("result %s err=%v", out.JSON(), err)
	}
	for _, c := range o.Cases {
		if c.Scorers[0].Score == nil || *c.Scorers[0].Score != 1 || c.Scorers[1].Score == nil || *c.Scorers[1].Score != 0.5 {
			t.Fatalf("scores %s", out.JSON())
		}
	}
}
