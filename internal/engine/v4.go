package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// V4 uses the same bounded concurrency and attempt policy as V3, but admits
// exactly identified registered custom Go scorers through the SDK.
func runV4(ctx context.Context, raw []byte, artifactDir string, o Outcome, custom map[string]RegisteredScorer) Outcome {
	var cfg V3Config
	if len(raw) > maxConfigBytes {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration exceeds size limit")
	}
	if err := decodeOne(raw, &cfg); err != nil || cfg.Version != 4 {
		return fail(o, "CONFIG_INVALID", "RUN", "Invalid version 4 configuration")
	}
	if cfg.Execution.Concurrency < 1 || cfg.Execution.Concurrency > 16 || cfg.Execution.ScorerMaxAttempts < 1 || cfg.Execution.ScorerMaxAttempts > 10 {
		return fail(o, "CONFIG_INVALID", "RUN", "Invalid execution policy")
	}
	base := cfg.V2Config
	base.Version = 2
	rawBase, err := json.Marshal(base)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Invalid configuration encoding")
	}
	validated, _, err := validateV2WithCustom(rawBase, custom)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	canon, err := json.Marshal(cfg)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Cannot encode configuration")
	}
	sum := sha256.Sum256(append([]byte("evalkit:v4:config:\x00"), canon...))
	return runCases(ctx, validated, hex.EncodeToString(sum[:]), artifactDir, o, cfg.Execution.Concurrency, cfg.Execution.ScorerMaxAttempts, custom)
}
