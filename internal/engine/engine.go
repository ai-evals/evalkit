package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
)

const maxConfigBytes = 1 << 20
const maxTargetBytes = 1 << 20

// Config is the deliberately narrow V1 schema: one case, one command target,
// and one required exact-match scorer. Other fields are rejected.
type Config struct {
	Version int `json:"version"`
	Case    struct {
		ID       string          `json:"id"`
		Input    json.RawMessage `json:"input"`
		Expected json.RawMessage `json:"expected"`
	} `json:"case"`
	Target struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"target"`
	Scorer struct {
		Kind      string  `json:"kind"`
		Threshold float64 `json:"threshold"`
	} `json:"scorer"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Message string `json:"message"`
}

type CaseResult struct {
	ID           string   `json:"id"`
	TargetStatus string   `json:"target_status"`
	ScorerStatus string   `json:"scorer_status"`
	Score        *float64 `json:"score,omitempty"`
}

type Outcome struct {
	SchemaVersion int          `json:"schema_version"`
	RunID         string       `json:"run_id"`
	State         string       `json:"state"`
	Quality       *string      `json:"quality"`
	ConfigDigest  string       `json:"config_digest,omitempty"`
	Case          *CaseResult  `json:"case,omitempty"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
	ArtifactDir   string       `json:"artifact_dir,omitempty"`
}

func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

func decodeOne(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func decodeJSONValue(raw []byte) (any, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one JSON value")
	}
	return v, nil
}

func validate(raw []byte) (Config, string, error) {
	var c Config
	if len(raw) > maxConfigBytes {
		return c, "", errors.New("configuration exceeds V1 size limit")
	}
	if err := decodeOne(raw, &c); err != nil {
		return c, "", fmt.Errorf("invalid configuration JSON: %w", err)
	}
	if c.Version != 1 {
		return c, "", errors.New("only configuration version 1 is supported")
	}
	if c.Case.ID == "" || strings.TrimSpace(c.Target.Command) == "" {
		return c, "", errors.New("case.id and target.command are required")
	}
	if c.Scorer.Kind != "exact_match" || c.Scorer.Threshold < 0 || c.Scorer.Threshold > 1 {
		return c, "", errors.New("V1 requires exact_match scorer with threshold in [0,1]")
	}
	if _, err := decodeJSONValue(c.Case.Input); err != nil {
		return c, "", errors.New("case.input must be one valid JSON value")
	}
	if _, err := decodeJSONValue(c.Case.Expected); err != nil {
		return c, "", errors.New("case.expected must be one valid JSON value")
	}
	// Canonical struct encoding is stable for the fixed V1 schema. Future
	// schemas require explicit versioned fingerprint interpretation.
	canonical, err := json.Marshal(c)
	if err != nil {
		return c, "", err
	}
	sum := sha256.Sum256(append([]byte("evalkit:v1:config:\x00"), canonical...))
	return c, hex.EncodeToString(sum[:]), nil
}

type boundedBuffer struct {
	buf      bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	capacity := maxTargetBytes - b.buf.Len()
	if capacity < n {
		b.overflow = true
		if capacity > 0 {
			_, _ = b.buf.Write(p[:capacity])
		}
		return n, nil
	}
	_, _ = b.buf.Write(p)
	return n, nil
}

func execute(ctx context.Context, c Config) ([]byte, error) {
	workdir, err := os.MkdirTemp("", "evalkit-target-")
	if err != nil {
		return nil, errors.New("cannot prepare target workspace")
	}
	defer os.RemoveAll(workdir)
	payload, _ := json.Marshal(struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}{c.Case.ID, c.Case.Input})
	cmd := exec.CommandContext(ctx, c.Target.Command, c.Target.Args...)
	cmd.Dir = workdir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(append(payload, '\n'))
	var stdout boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard // Never echo arbitrary target diagnostics as trusted output.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("target process failed")
	}
	if stdout.overflow {
		return nil, errors.New("target output exceeds V1 size limit")
	}
	if _, err := decodeJSONValue(stdout.buf.Bytes()); err != nil {
		return nil, errors.New("target output must be exactly one JSON value")
	}
	return stdout.buf.Bytes(), nil
}

func fail(o Outcome, code, scope, message string) Outcome {
	o.State = "FAILED"
	o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: code, Scope: scope, Message: message})
	return o
}

// RunJSON creates a Run before validation and returns its final structured
// outcome. A completed quality FAIL is a successful framework operation.
func RunJSON(ctx context.Context, raw []byte, artifactDir string) Outcome {
	o := Outcome{SchemaVersion: 1, RunID: newRunID(), State: "VALIDATING"}
	if o.RunID == "" {
		return fail(o, "RUN_ID_UNAVAILABLE", "RUN", "Cannot allocate run identity")
	}
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	c, digest, err := validate(raw)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	o.ConfigDigest = digest
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	o.State = "RUNNING"
	result := &CaseResult{ID: c.Case.ID}
	o.Case = result
	output, err := execute(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		result.TargetStatus = "ERRORED"
		result.ScorerStatus = "ERRORED"
		o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: "TARGET_EXECUTION_ERROR", Scope: "CASE", Message: err.Error()})
	} else {
		result.TargetStatus = "SUCCEEDED"
		got, _ := decodeJSONValue(output)
		want, _ := decodeJSONValue(c.Case.Expected)
		score := 0.0
		if reflect.DeepEqual(got, want) {
			score = 1.0
		}
		result.Score = &score
		result.ScorerStatus = "SUCCEEDED"
	}
	quality := "INDETERMINATE"
	if result.ScorerStatus == "SUCCEEDED" {
		quality = "PASS"
		if *result.Score < c.Scorer.Threshold {
			quality = "FAIL"
		}
	}
	o.State = "FINALIZING"
	// Completion and quality become externally authoritative only after
	// required evidence/report publication succeeds.
	committed := o
	committed.State = "COMPLETED"
	committed.Quality = &quality
	committed.ArtifactDir = artifactDir
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	if err := publish(ctx, artifactDir, committed); err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		return fail(o, "ARTIFACT_COMMIT_FAILED", "RUN", "Required artifact publication failed")
	}
	return committed
}

func cancelled(o Outcome, err error) Outcome {
	o.State = "CANCELLED"
	o.Quality = nil
	code := "RUN_CANCELLED"
	if errors.Is(err, context.DeadlineExceeded) {
		code = "RUN_DEADLINE_EXCEEDED"
	}
	o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: code, Scope: "RUN", Message: "Evaluation interrupted"})
	return o
}

func writeSync(name string, contents []byte) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(contents); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func publish(ctx context.Context, finalDir string, outcome Outcome) error {
	if finalDir == "" {
		return errors.New("artifact output directory required")
	}
	finalDir, err := filepath.Abs(finalDir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(finalDir)
	if _, err := os.Stat(finalDir); err == nil {
		return errors.New("artifact output already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".evalkit-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	runJSON, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	runJSON = append(runJSON, '\n')
	reportJSON, err := json.MarshalIndent(struct {
		RunID   string   `json:"run_id"`
		Quality string   `json:"quality"`
		Score   *float64 `json:"score,omitempty"`
	}{outcome.RunID, *outcome.Quality, outcome.Case.Score}, "", "  ")
	if err != nil {
		return err
	}
	reportJSON = append(reportJSON, '\n')
	if err = writeSync(filepath.Join(staging, "run.json"), runJSON); err != nil {
		return err
	}
	if err = writeSync(filepath.Join(staging, "report.json"), reportJSON); err != nil {
		return err
	}
	hRun, hReport := sha256.Sum256(runJSON), sha256.Sum256(reportJSON)
	commitJSON, err := json.MarshalIndent(struct {
		Version      int    `json:"version"`
		RunSHA256    string `json:"run_sha256"`
		ReportSHA256 string `json:"report_sha256"`
	}{1, hex.EncodeToString(hRun[:]), hex.EncodeToString(hReport[:])}, "", "  ")
	if err != nil {
		return err
	}
	if err = writeSync(filepath.Join(staging, "commit.json"), append(commitJSON, '\n')); err != nil {
		return err
	}
	stageDir, err := os.Open(staging)
	if err != nil {
		return err
	}
	if err = stageDir.Sync(); err != nil {
		stageDir.Close()
		return err
	}
	stageDir.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(staging, finalDir); err != nil {
		return err
	}
	// The directory rename is the publication point. A concurrent context
	// cancellation after this point cannot retroactively unpublish the run.
	parentDir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer parentDir.Close()
	return parentDir.Sync()
}

// VerifyArtifact checks the narrow V1 required evidence closure before
// treating a published run as authoritative. This does not authenticate the
// artifact's author or verify external provider/model identities.
func VerifyArtifact(dir string) (Outcome, error) {
	var o Outcome
	commitRaw, err := os.ReadFile(filepath.Join(dir, "commit.json"))
	if err != nil {
		return o, errors.New("commit marker unavailable")
	}
	var marker struct {
		Version      int    `json:"version"`
		RunSHA256    string `json:"run_sha256"`
		ReportSHA256 string `json:"report_sha256"`
	}
	if err := decodeOne(commitRaw, &marker); err != nil || marker.Version != 1 {
		return o, errors.New("unsupported commit marker")
	}
	runRaw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return o, errors.New("run evidence unavailable")
	}
	reportRaw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return o, errors.New("required report unavailable")
	}
	hRun, hReport := sha256.Sum256(runRaw), sha256.Sum256(reportRaw)
	if hex.EncodeToString(hRun[:]) != marker.RunSHA256 || hex.EncodeToString(hReport[:]) != marker.ReportSHA256 {
		return o, errors.New("artifact integrity mismatch")
	}
	if err := decodeOne(runRaw, &o); err != nil {
		return Outcome{}, errors.New("invalid run evidence")
	}
	var report struct {
		RunID   string   `json:"run_id"`
		Quality string   `json:"quality"`
		Score   *float64 `json:"score"`
	}
	if err := decodeOne(reportRaw, &report); err != nil {
		return Outcome{}, errors.New("invalid report")
	}
	if o.State != "COMPLETED" || o.Quality == nil || o.RunID != report.RunID || *o.Quality != report.Quality {
		return Outcome{}, errors.New("artifact semantic mismatch")
	}
	if o.Case == nil || ((o.Case.Score == nil) != (report.Score == nil)) || (o.Case.Score != nil && *o.Case.Score != *report.Score) {
		return Outcome{}, errors.New("artifact score mismatch")
	}
	return o, nil
}
