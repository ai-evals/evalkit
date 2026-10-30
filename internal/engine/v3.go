package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// V3Config introduces bounded case-worker concurrency and scorer retry budget.
// No retry is granted for target commands, which might have side effects.
type V3Config struct {
	V2Config
	Execution struct {
		Concurrency       int `json:"concurrency"`
		ScorerMaxAttempts int `json:"scorer_max_attempts"`
	} `json:"execution"`
}

func runV3(ctx context.Context, raw []byte, artifactDir string, o Outcome) Outcome {
	var cfg V3Config
	if len(raw) > maxConfigBytes {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration exceeds size limit")
	}
	if err := decodeOne(raw, &cfg); err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Invalid configuration JSON")
	}
	if cfg.Version != 3 {
		return fail(o, "CONFIG_INVALID", "RUN", "Expected version 3")
	}
	if cfg.Execution.Concurrency < 1 || cfg.Execution.Concurrency > 16 {
		return fail(o, "CONFIG_INVALID", "RUN", "Concurrency must be in [1,16]")
	}
	if cfg.Execution.ScorerMaxAttempts < 1 || cfg.Execution.ScorerMaxAttempts > 10 {
		return fail(o, "CONFIG_INVALID", "RUN", "Scorer attempt budget must be in [1,10]")
	}
	// Reuse all V2 dataset and scorer contract validation. V3 adds only
	// execution policy, without duplicating semantic validation.
	base := cfg.V2Config
	base.Version = 2
	b, err := json.Marshal(base)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Cannot encode configuration")
	}
	validated, _, err := validateV2(b)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	original, err := json.Marshal(cfg)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Cannot encode configuration")
	}
	sum := sha256.Sum256(append([]byte("evalkit:v3:config:\x00"), original...))
	digest := hex.EncodeToString(sum[:])
	if digest == "" {
		return fail(o, "CONFIG_INVALID", "RUN", errors.New("invalid fingerprint").Error())
	}
	return runCases(ctx, validated, digest, artifactDir, o, cfg.Execution.Concurrency, cfg.Execution.ScorerMaxAttempts)
}
