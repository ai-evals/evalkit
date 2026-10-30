package engine

import (
	"context"
	"errors"
)

// RetryableScorerError marks a typed transient scorer operation error. A
// retryable error is not itself a retry authorization; Run deadline and
// configured attempt budgets remain authoritative.
type RetryableScorerError struct{ Err error }

func (e *RetryableScorerError) Error() string { return "transient scorer operation failed" }
func (e *RetryableScorerError) Unwrap() error { return e.Err }

func retryScorer(ctx context.Context, limit int, evaluate func(context.Context) (float64, error)) (float64, int, error) {
	if limit < 1 {
		return 0, 0, errors.New("invalid attempt budget")
	}
	for n := 1; n <= limit; n++ {
		if err := ctx.Err(); err != nil {
			return 0, n - 1, err
		}
		value, err := evaluate(ctx)
		if err == nil {
			return value, n, nil
		}
		var transient *RetryableScorerError
		if !errors.As(err, &transient) || n == limit {
			return 0, n, err
		}
	}
	return 0, limit, errors.New("attempt budget exhausted")
}
