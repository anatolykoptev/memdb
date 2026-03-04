package metrics

import (
	"context"
	"log/slog"
	"time"
)

const slowThreshold = 5 * time.Second

// TrackOperation logs a warning if fn takes longer than 5 seconds.
func TrackOperation(ctx context.Context, name string, fn func(context.Context) error) error {
	start := time.Now()
	err := fn(ctx)
	elapsed := time.Since(start)
	if elapsed > slowThreshold {
		slog.WarnContext(ctx, "slow operation",
			slog.String("op", name),
			slog.Duration("elapsed", elapsed),
		)
	}
	return err
}
