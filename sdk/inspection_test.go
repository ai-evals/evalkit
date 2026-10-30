package sdk

import (
	"context"
	"github.com/ai-evals/evalkit/internal/engine"
	"path/filepath"
	"testing"
)

func TestV5SDKInspectorReadOnly(t *testing.T) {
	// Use the SDK's existing controlled helper target and v4 exact scorer.
	raw := sdkConfig(t, "exact_match", true, "", 1)
	artifact := filepath.Join(t.TempDir(), "artifact")
	outcome := engine.RunJSON(context.Background(), raw, artifact)
	if outcome.State != "COMPLETED" {
		t.Fatalf("run: %+v", outcome)
	}
	inspect := NewInspector()
	view, err := inspect.Inspect(context.Background(), artifact)
	if err != nil || view.Integrity() != "SELF_CONSISTENT" || view.Quality() != "PASS" {
		t.Fatalf("view=%s err=%v", view.JSON(), err)
	}
}
