package sdk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
)

func sdkTampered(t *testing.T) (artifact, tampered string) {
	t.Helper()
	artifact = filepath.Join(t.TempDir(), "artifact")
	if o := engine.RunJSON(context.Background(), sdkConfig(t, "exact_match", true, "", 2), artifact); o.State != "COMPLETED" {
		t.Fatalf("run: %+v", o)
	}
	tampered = filepath.Join(t.TempDir(), "tampered")
	if err := os.CopyFS(tampered, os.DirFS(artifact)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tampered, "report.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	return artifact, tampered
}

func TestV6SDKInspectRefusalIsPublic(t *testing.T) {
	_, tampered := sdkTampered(t)
	_, err := NewInspector().Inspect(context.Background(), tampered)
	var refused *ReadError
	if !errors.As(err, &refused) || *refused != (ReadError{Code: CodeArtifactVerificationFailed, Scope: ScopeInspection}) ||
		err.Error() != "evalkit inspection: ARTIFACT_VERIFICATION_FAILED" {
		t.Fatalf("inspect err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewInspector().Inspect(ctx, tampered); !errors.Is(err, context.Canceled) || errors.As(err, &refused) {
		t.Fatalf("cancelled inspect err=%v", err)
	}
}

func TestV6SDKCompareRefusalsArePublic(t *testing.T) {
	artifact, tampered := sdkTampered(t)
	_, err := NewComparer().Compare(context.Background(), ComparisonRequest{BaselineDir: artifact, CandidateDir: tampered})
	var refused *ReadError
	if !errors.As(err, &refused) || *refused != (ReadError{Code: CodeArtifactVerificationFailed, Scope: ScopeComparison, Input: InputCandidate}) ||
		err.Error() != "evalkit comparison: ARTIFACT_VERIFICATION_FAILED candidate" {
		t.Fatalf("compare err=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewComparer().Compare(ctx, ComparisonRequest{BaselineDir: artifact, CandidateDir: artifact}); !errors.Is(err, context.Canceled) || errors.As(err, &refused) {
		t.Fatalf("cancelled compare err=%v", err)
	}
	cmp, err := NewComparer().Compare(context.Background(), ComparisonRequest{BaselineDir: artifact, CandidateDir: artifact})
	if err != nil || cmp.Recommendation() != Ship || cmp.Count(Unchanged) != 2 || cmp.Count("NOT_A_TRANSITION") != 0 || len(cmp.Changes()) != 0 ||
		cmp.Baseline() != cmp.Candidate() || cmp.Candidate().Quality != "PASS" {
		t.Fatalf("self compare: %s %v", cmp.JSON(), err)
	}
}
