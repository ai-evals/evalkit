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
	// Subject is nil for RUN scope and in artifacts published before V7.
	Subject *DiagnosticSubject `json:"subject,omitempty"`
}

type CaseResult struct {
	ID           string        `json:"id"`
	TargetStatus string        `json:"target_status"`
	ScorerStatus string        `json:"scorer_status"`
	Score        *float64      `json:"score,omitempty"`
	Output       *TargetOutput `json:"output,omitempty"`
}

type Outcome struct {
	SchemaVersion int                    `json:"schema_version"`
	RunID         string                 `json:"run_id"`
	State         string                 `json:"state"`
	Quality       *string                `json:"quality"`
	ConfigDigest  string                 `json:"config_digest,omitempty"`
	Case          *CaseResult            `json:"case,omitempty"`
	Cases         []V2CaseResult         `json:"cases,omitempty"`
	Measurement   *MeasurementDefinition `json:"measurement,omitempty"`
	// RetainedOutput is set by publish, so only a sealed record carries it.
	RetainedOutput OutputRetention `json:"retained_output,omitempty"`
	Diagnostics    []Diagnostic    `json:"diagnostics,omitempty"`
	ArtifactDir    string          `json:"artifact_dir,omitempty"`
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

// errWorkspace is a failure before the process started, so it has no stdout.
var errWorkspace = errors.New("cannot prepare workspace")

// runIsolated starts command without a shell, in a fresh working directory
// named by workdirPrefix, with only PATH in its environment and its own
// process group. stderr is discarded unread, so no process diagnostic can
// reach evalkit output. It returns errWorkspace, ctx's error once ctx is
// done, or runProcessGroup's error. After a ctx error the stdout copier may
// still be writing, so the caller must not read stdout.
func runIsolated(ctx context.Context, workdirPrefix, command string, args []string, stdin []byte, stdout io.Writer) error {
	workdir, err := os.MkdirTemp("", workdirPrefix)
	if err != nil {
		return errWorkspace
	}
	defer os.RemoveAll(workdir)
	cmd := exec.Command(command, args...)
	cmd.Dir = workdir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := runProcessGroup(ctx, cmd); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// execute returns what it captured from stdout on every path except
// cancellation, so a malformed, oversized or failed execution keeps its
// bytes. On success head is the buffer execute returned before V7.
func execute(ctx context.Context, c Config) (targetStdout, error) {
	var stream stdoutDigest
	payload, _ := json.Marshal(struct {
		CaseID string          `json:"case_id"`
		Input  json.RawMessage `json:"input"`
	}{c.Case.ID, c.Case.Input})
	var stdout boundedBuffer
	if err := runIsolated(ctx, "evalkit-target-", c.Target.Command, c.Target.Args, append(payload, '\n'), io.MultiWriter(&stdout, &stream)); err != nil {
		switch {
		case errors.Is(err, errWorkspace):
			return stream.capture(nil), errors.New("cannot prepare target workspace")
		case ctx.Err() != nil:
			return targetStdout{}, ctx.Err()
		}
		// Any other error is a failed Start, where no copier ran, or a
		// finished cmd.Wait.
		return stream.capture(stdout.buf.Bytes()), errors.New("target process failed")
	}
	captured := stream.capture(stdout.buf.Bytes())
	if stdout.overflow {
		return captured, errors.New("target output exceeds V1 size limit")
	}
	if _, err := decodeJSONValue(captured.head); err != nil {
		return captured, errors.New("target output must be exactly one JSON value")
	}
	return captured, nil
}

func fail(o Outcome, code, scope, message string) Outcome {
	o.State = "FAILED"
	o.Diagnostics = append(o.Diagnostics, Diagnostic{Code: code, Scope: scope, Message: message})
	return o
}

// RunJSON creates a Run before validation and returns its final structured
// outcome. A completed quality FAIL is a successful framework operation.
func RunJSON(ctx context.Context, raw []byte, artifactDir string) Outcome {
	return runJSON(ctx, raw, artifactDir, nil)
}

// RunJSONWithScorers is the internal bridge used by the narrow public Go SDK.
func RunJSONWithScorers(ctx context.Context, raw []byte, artifactDir string, extensions map[string]RegisteredScorer) Outcome {
	return runJSON(ctx, raw, artifactDir, extensions)
}

func runJSON(ctx context.Context, raw []byte, artifactDir string, extensions map[string]RegisteredScorer) Outcome {
	o := Outcome{SchemaVersion: 1, RunID: newRunID(), State: "VALIDATING"}
	if o.RunID == "" {
		return fail(o, "RUN_ID_UNAVAILABLE", "RUN", "Cannot allocate run identity")
	}
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	var header struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &header); err == nil {
		if header.Version == nil {
			return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: configuration version is required")
		}
		switch *header.Version {
		case 1:
		case 2:
			return runV2(ctx, raw, artifactDir, o)
		case 3:
			return runV3(ctx, raw, artifactDir, o)
		case 4, 5:
			return runV4(ctx, *header.Version, raw, artifactDir, o, extensions)
		default:
			return fail(o, "CONFIG_INVALID", "RUN", fmt.Sprintf("Configuration validation failed: unsupported configuration version %d; supported versions are 1, 2, 3, 4 and 5", *header.Version))
		}
	}
	c, digest, err := validate(raw)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	o.ConfigDigest = digest
	o.Measurement = measurementForCases([]V2Case{{ID: c.Case.ID}}, []V2Scorer{{ID: "exact_match", Kind: "exact_match", Threshold: c.Scorer.Threshold, Required: true}})
	if err := ctx.Err(); err != nil {
		return cancelled(o, err)
	}
	o.State = "RUNNING"
	result := &CaseResult{ID: c.Case.ID}
	o.Case = result
	output, err := execute(ctx, c)
	result.Output = retainOutput(output, retentionAllowance(1))
	if err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		result.TargetStatus = "ERRORED"
		result.ScorerStatus = "ERRORED"
		o.Diagnostics = append(o.Diagnostics, caseDiagnostic("TARGET_EXECUTION_ERROR", err.Error(), 0, c.Case.ID))
	} else {
		result.TargetStatus = "SUCCEEDED"
		got, _ := decodeJSONValue(output.head)
		want, _ := decodeJSONValue(c.Case.Expected)
		score := exactMatchScore(got, want)
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
	sealed, err := publish(ctx, artifactDir, committed)
	if err != nil {
		if ctx.Err() != nil {
			return cancelled(o, ctx.Err())
		}
		return fail(o, "ARTIFACT_COMMIT_FAILED", "RUN", "Required artifact publication failed")
	}
	return sealed
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

// publish seals outcome into finalDir and returns the Outcome run.json holds,
// so the caller prints the record it sealed. It differs from outcome only in
// retained output, trimmed when it would not fit what VerifyArtifact reads.
func publish(ctx context.Context, finalDir string, outcome Outcome) (Outcome, error) {
	if finalDir == "" {
		return Outcome{}, errors.New("artifact output directory required")
	}
	finalDir, err := filepath.Abs(finalDir)
	if err != nil {
		return Outcome{}, err
	}
	parent := filepath.Dir(finalDir)
	if _, err := os.Stat(finalDir); err == nil {
		return Outcome{}, errors.New("artifact output already exists")
	} else if !os.IsNotExist(err) {
		return Outcome{}, err
	}
	staging, err := os.MkdirTemp(parent, ".evalkit-stage-")
	if err != nil {
		return Outcome{}, err
	}
	defer os.RemoveAll(staging)
	runJSON, outcome, err := encodeRunEvidence(outcome)
	if err != nil {
		return Outcome{}, err
	}
	var legacyScore *float64
	if outcome.Case != nil {
		legacyScore = outcome.Case.Score
	}
	reportJSON, err := json.MarshalIndent(struct {
		RunID   string         `json:"run_id"`
		Quality string         `json:"quality"`
		Score   *float64       `json:"score,omitempty"`
		Cases   []V2CaseResult `json:"cases,omitempty"`
	}{outcome.RunID, *outcome.Quality, legacyScore, withoutOutputs(outcome.Cases)}, "", "  ")
	if err != nil {
		return Outcome{}, err
	}
	reportJSON = append(reportJSON, '\n')
	if int64(len(reportJSON)) > maxEvidenceFileBytes {
		return Outcome{}, errRunEvidenceTooLarge
	}
	if err = writeSync(filepath.Join(staging, "run.json"), runJSON); err != nil {
		return Outcome{}, err
	}
	if err = writeSync(filepath.Join(staging, "report.json"), reportJSON); err != nil {
		return Outcome{}, err
	}
	hRun, hReport := sha256.Sum256(runJSON), sha256.Sum256(reportJSON)
	commitJSON, err := json.MarshalIndent(struct {
		Version      int    `json:"version"`
		RunSHA256    string `json:"run_sha256"`
		ReportSHA256 string `json:"report_sha256"`
	}{1, hex.EncodeToString(hRun[:]), hex.EncodeToString(hReport[:])}, "", "  ")
	if err != nil {
		return Outcome{}, err
	}
	if err = writeSync(filepath.Join(staging, "commit.json"), append(commitJSON, '\n')); err != nil {
		return Outcome{}, err
	}
	stageDir, err := os.Open(staging)
	if err != nil {
		return Outcome{}, err
	}
	if err = stageDir.Sync(); err != nil {
		stageDir.Close()
		return Outcome{}, err
	}
	stageDir.Close()
	if err = ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err = os.Rename(staging, finalDir); err != nil {
		return Outcome{}, err
	}
	// The directory rename is the publication point. A concurrent context
	// cancellation after this point cannot retroactively unpublish the run.
	parentDir, err := os.Open(parent)
	if err != nil {
		return Outcome{}, err
	}
	defer parentDir.Close()
	if err = parentDir.Sync(); err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}

// VerifyArtifact checks the narrow V1 required evidence closure before
// treating a published run as authoritative. This does not authenticate the
// artifact's author or verify external provider/model identities.
func VerifyArtifact(dir string) (Outcome, error) {
	var o Outcome
	commitRaw, err := readEvidenceFile(filepath.Join(dir, "commit.json"))
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
	runRaw, err := readEvidenceFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return o, errors.New("run evidence unavailable")
	}
	reportRaw, err := readEvidenceFile(filepath.Join(dir, "report.json"))
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
		RunID   string         `json:"run_id"`
		Quality string         `json:"quality"`
		Score   *float64       `json:"score"`
		Cases   []V2CaseResult `json:"cases"`
	}
	if err := decodeOne(reportRaw, &report); err != nil {
		return Outcome{}, errors.New("invalid report")
	}
	if o.SchemaVersion != 1 || o.RunID == "" || o.State != "COMPLETED" || o.Quality == nil || o.RunID != report.RunID || *o.Quality != report.Quality {
		return Outcome{}, errors.New("artifact semantic mismatch")
	}
	if len(o.Cases) > 0 {
		if !reflect.DeepEqual(withoutOutputs(o.Cases), report.Cases) || report.Score != nil || o.Case != nil {
			return Outcome{}, errors.New("artifact case evidence mismatch")
		}
	} else if o.Case == nil || ((o.Case.Score == nil) != (report.Score == nil)) || (o.Case.Score != nil && *o.Case.Score != *report.Score) {
		return Outcome{}, errors.New("artifact score mismatch")
	}
	if err := verifyMeasurementAccounting(o); err != nil {
		return Outcome{}, err
	}
	if err := verifyRetainedEvidence(o); err != nil {
		return Outcome{}, err
	}
	return o, nil
}

// exactMatchScore is the one canonical exact-match implementation across
// configuration versions and supported invocation surfaces.
func exactMatchScore(actual, expected any) float64 {
	if reflect.DeepEqual(actual, expected) {
		return 1
	}
	return 0
}
