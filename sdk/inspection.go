package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ai-evals/evalkit/internal/engine"
)

// Inspector has no access to Run execution or scorer registration.
type Inspector struct{}

func NewInspector() Inspector { return Inspector{} }

type Inspection struct{ record engine.Inspection }

func (i Inspection) RunID() string                 { return i.record.RunID }
func (i Inspection) Quality() QualityVerdict       { return QualityVerdict(i.record.Quality) }
func (i Inspection) Integrity() string             { return i.record.Integrity }
func (i Inspection) MeasurementAccounting() string { return i.record.MeasurementAccounting }
func (i Inspection) JSON() []byte                  { out, _ := json.Marshal(i.record); return out }
func (_ Inspector) Inspect(ctx context.Context, artifactDir string) (Inspection, error) {
	record, err := engine.InspectArtifact(ctx, artifactDir)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Inspection{}, err
		}
		return Inspection{}, &engine.InspectionError{Code: "ARTIFACT_VERIFICATION_FAILED"}
	}
	return Inspection{record: record}, nil
}
