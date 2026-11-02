package sdk

import (
	"slices"
	"testing"

	"github.com/ai-evals/evalkit/internal/engine"
)

// The SDK's string constants cannot be derived from the engine's integer
// enums, so this pins them: same values, same order, nothing missing.
func TestV6SDKVocabularyIsTheJSONVocabulary(t *testing.T) {
	check := func(kind string, public []string, text func(uint8) ([]byte, error)) {
		t.Helper()
		var engineNames []string
		for i := 0; i < 256; i++ {
			b, err := text(uint8(i))
			if err != nil {
				break
			}
			engineNames = append(engineNames, string(b))
		}
		if !slices.Equal(engineNames, public) {
			t.Errorf("%s: engine %v, sdk %v", kind, engineNames, public)
		}
	}
	check("Recommendation", []string{string(Ship), string(Iterate), string(Investigate)},
		func(i uint8) ([]byte, error) { return engine.Recommendation(i).MarshalText() })
	check("Transition", []string{string(Unchanged), string(Improved), string(Regressed), string(LostEvidence), string(GainedEvidence)},
		func(i uint8) ([]byte, error) { return engine.Transition(i).MarshalText() })
	check("CellState", []string{string(CellPassing), string(CellFailing), string(CellUnknown)},
		func(i uint8) ([]byte, error) { return engine.CellState(i).MarshalText() })
}
