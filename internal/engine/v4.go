package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// runV4 runs versions 4 and 5. Both use the bounded concurrency and attempt
// policy of V3 and admit exactly identified registered custom Go scorers
// through the SDK. Version 5 also admits command scorers, and nothing else
// differs, so one path keeps their validation and execution identical.
func runV4(ctx context.Context, version int, raw []byte, artifactDir string, o Outcome, custom map[string]RegisteredScorer) Outcome {
	var cfg V3Config
	if len(raw) > maxConfigBytes {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration exceeds size limit")
	}
	if err := decodeOne(raw, &cfg); err != nil || cfg.Version != version {
		return fail(o, "CONFIG_INVALID", "RUN", fmt.Sprintf("Invalid version %d configuration", version))
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
	validated, _, err := validateV2WithCustom(rawBase, custom, version == 5)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Configuration validation failed: "+err.Error())
	}
	canon, err := json.Marshal(cfg)
	if err != nil {
		return fail(o, "CONFIG_INVALID", "RUN", "Cannot encode configuration")
	}
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("evalkit:v%d:config:\x00", version)), canon...))
	return runCases(ctx, validated, hex.EncodeToString(sum[:]), artifactDir, o, cfg.Execution.Concurrency, cfg.Execution.ScorerMaxAttempts, custom)
}
