package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestV5AC01InspectVerifiedRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	o := RunJSON(context.Background(), configFor(t, "match"), dir)
	if o.State != "COMPLETED" {
		t.Fatalf("run: %+v", o)
	}
	view, err := InspectArtifact(context.Background(), dir)
	if err != nil || view.RunID != o.RunID || view.Integrity != "SELF_CONSISTENT" || view.MeasurementAccounting != "REPORTED_DEFINITION_CONSISTENT" || view.AuthenticatedAuthor {
		t.Fatalf("inspection=%+v err=%v", view, err)
	}
}
func TestV5AC02TamperedReportRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte(`{"quality":"FAIL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArtifact(context.Background(), dir); err == nil {
		t.Fatal("tampered report accepted")
	}
}
func TestV5AC03MissingCommitRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	if err := os.Remove(filepath.Join(dir, "commit.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectArtifact(context.Background(), dir); err == nil {
		t.Fatal("missing commit accepted")
	}
}
func TestV5AC04SymlinkReportRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(other, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(dir, "report.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := InspectArtifact(context.Background(), dir); err == nil {
		t.Fatal("symlink evidence accepted")
	}
}

// A self-consistent checksum is not sufficient when the stored quality
// disagrees with the retained measurement contract.
func TestV5AC05RehashedContradictoryVerdictRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	runRaw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	run["quality"] = "FAIL"
	updateRun, _ := json.Marshal(run)
	reportRaw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	report["quality"] = "FAIL"
	updateReport, _ := json.Marshal(report)
	hRun, hReport := sha256.Sum256(updateRun), sha256.Sum256(updateReport)
	marker, _ := json.Marshal(map[string]any{"version": 1, "run_sha256": hex.EncodeToString(hRun[:]), "report_sha256": hex.EncodeToString(hReport[:])})
	for name, bytes := range map[string][]byte{"run.json": updateRun, "report.json": updateReport, "commit.json": marker} {
		if err := os.WriteFile(filepath.Join(dir, name), bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InspectArtifact(context.Background(), dir); err == nil {
		t.Fatal("rehashed contradictory measurement accepted")
	}
}
func TestV5AC06MissingHistoricalMeasurementQualified(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	runRaw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	delete(run, "measurement")
	updated, _ := json.Marshal(run)
	report, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	hRun, hReport := sha256.Sum256(updated), sha256.Sum256(report)
	marker, _ := json.Marshal(map[string]any{"version": 1, "run_sha256": hex.EncodeToString(hRun[:]), "report_sha256": hex.EncodeToString(hReport[:])})
	if err := os.WriteFile(filepath.Join(dir, "run.json"), updated, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commit.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	view, err := InspectArtifact(context.Background(), dir)
	if err != nil || view.MeasurementAccounting != "NOT_RETAINED" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}
func TestV5AC07InspectionDoesNotAlterArtifact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "artifact")
	RunJSON(context.Background(), configFor(t, "match"), dir)
	before, _ := os.ReadFile(filepath.Join(dir, "run.json"))
	view, err := InspectArtifact(context.Background(), dir)
	after, _ := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil || view.RunID == "" || string(before) != string(after) {
		t.Fatalf("inspection mutated run evidence: %v", err)
	}
}
func TestV5AC08CancelledInspection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectArtifact(ctx, "/unused"); err == nil {
		t.Fatal("cancelled inspection succeeded")
	}
}

// rewriteMeasurementScorers edits the retained scorer contract of a committed
// artifact and re-hashes it, so only the contract itself can be rejected.
func rewriteMeasurementScorers(t *testing.T, dir string, edit func([]any)) {
	t.Helper()
	runRaw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var run map[string]any
	if err := json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	edit(run["measurement"].(map[string]any)["scorers"].([]any))
	updated, _ := json.Marshal(run)
	report, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	hRun, hReport := sha256.Sum256(updated), sha256.Sum256(report)
	marker, _ := json.Marshal(map[string]any{"version": 1, "run_sha256": hex.EncodeToString(hRun[:]), "report_sha256": hex.EncodeToString(hReport[:])})
	if err := os.WriteFile(filepath.Join(dir, "run.json"), updated, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "commit.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
}

// The inspector must only certify scorer contracts that config validation
// would accept: at least one required scorer, every threshold in [0,1].
func TestV5AC09InspectorRejectsContractsEvalkitWouldNotRun(t *testing.T) {
	for name, edit := range map[string]func([]any){
		"no required scorer": func(s []any) {
			for _, sc := range s {
				sc.(map[string]any)["required"] = false
			}
		},
		"threshold below 0": func(s []any) { s[0].(map[string]any)["threshold"] = -0.5 },
		"threshold above 1": func(s []any) { s[1].(map[string]any)["threshold"] = 1.5 },
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "artifact")
			o := RunJSON(context.Background(), configV2(t, []map[string]any{aCase("a", "yes"), aCase("b", "yes")}, exactScorers()), dir)
			if o.State != "COMPLETED" || o.Quality == nil || *o.Quality != "PASS" {
				t.Fatalf("run: %+v", o)
			}
			rewriteMeasurementScorers(t, dir, edit)
			if view, err := InspectArtifact(context.Background(), dir); err == nil {
				t.Fatalf("contract evalkit would not run was certified: %+v", view)
			}
		})
	}
}
