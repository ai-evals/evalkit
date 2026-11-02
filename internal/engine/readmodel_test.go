package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// v6Rewrite edits run.json and report.json as JSON maps and re-hashes the
// commit marker, so only the edited semantics can be rejected.
func v6Rewrite(t *testing.T, dir string, edit func(run, report map[string]any)) {
	t.Helper()
	load := func(name string) map[string]any {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	run, report := load("run.json"), load("report.json")
	edit(run, report)
	runRaw, _ := json.Marshal(run)
	reportRaw, _ := json.Marshal(report)
	hRun, hReport := sha256.Sum256(runRaw), sha256.Sum256(reportRaw)
	marker, _ := json.Marshal(map[string]any{"version": 1, "run_sha256": hex.EncodeToString(hRun[:]), "report_sha256": hex.EncodeToString(hReport[:])})
	for name, b := range map[string][]byte{"run.json": runRaw, "report.json": reportRaw, "commit.json": marker} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// The one reader is stricter than V5's inspect: re-hashed artifacts holding
// a verdict or an observation evalkit cannot produce are refused.
func TestV6ReadModelRefusesEvidenceEvalkitCannotProduce(t *testing.T) {
	for name, edit := range map[string]func(run, report map[string]any){
		"historical unknown verdict": func(run, report map[string]any) {
			delete(run, "measurement")
			run["quality"], report["quality"] = "MAYBE", "MAYBE"
		},
		"v1 skipped scorer with a score": func(run, report map[string]any) {
			run["case"].(map[string]any)["scorer_status"] = "SKIPPED"
			run["quality"], report["quality"] = "INDETERMINATE", "INDETERMINATE"
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "artifact")
			if o := RunJSON(context.Background(), configFor(t, "match"), dir); o.State != "COMPLETED" {
				t.Fatalf("run: %+v", o)
			}
			v6Rewrite(t, dir, edit)
			if _, err := VerifyArtifact(dir); err != nil {
				t.Fatalf("fixture must pass V5 verification: %v", err)
			}
			_, err := InspectArtifact(context.Background(), dir)
			var refused *ReadError
			if !errors.As(err, &refused) || *refused != (ReadError{Code: CodeArtifactVerificationFailed, Scope: ScopeInspection}) ||
				err.Error() != "evalkit inspection: ARTIFACT_VERIFICATION_FAILED" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
