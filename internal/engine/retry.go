package engine

import (
	"context"
	"errors"
)

// RetryableScorerError classifies a typed transient scorer operation error.
// It is not authorization to bypass the sealed attempt and deadline budgets.
type RetryableScorerError struct{ Err error }

func (e *RetryableScorerError) Error() string { return "transient scorer operation failed" }
func (e *RetryableScorerError) Unwrap() error { return e.Err }

// retryOperation is shared across built-in and custom scorer variants. The
// attempt limit counts executions, not additional retries.
func retryOperation[T any](ctx context.Context, limit int, evaluate func(context.Context) (T, error)) (T, int, error) {
	var zero T
	if limit < 1 {
		return zero, 0, errors.New("invalid attempt budget")
	}
	for n := 1; n <= limit; n++ {
		if err := ctx.Err(); err != nil {
			return zero, n - 1, err
		}
		result, err := evaluate(ctx)
		if err == nil {
			return result, n, nil
		}
		var transient *RetryableScorerError
		if !errors.As(err, &transient) || n == limit {
			return zero, n, err
		}
	}
	return zero, limit, errors.New("attempt budget exhausted")
}
func retryScorer(ctx context.Context, limit int, evaluate func(context.Context) (float64, error)) (float64, int, error) {
	return retryOperation(ctx, limit, evaluate)
}
func retryAssessment(ctx context.Context, limit int, evaluate func(context.Context) (AssessmentCandidate, error)) (AssessmentCandidate, int, error) {
	return retryOperation(ctx, limit, evaluate)
}
