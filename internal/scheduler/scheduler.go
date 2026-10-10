// Package scheduler runs a job periodically in the background.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
)

// Loop runs fn once immediately and then again interval after each run
// finishes, so runs never overlap. It returns when ctx is cancelled. A failed
// run is logged and never stops the loop; after an ESI rate limit the next run
// waits the Retry-After when that is longer than interval.
func Loop(ctx context.Context, interval time.Duration, fn func(context.Context) error) {
	loop(ctx, interval, fn, sleep)
}

func loop(ctx context.Context, interval time.Duration, fn func(context.Context) error, wait func(context.Context, time.Duration) error) {
	for ctx.Err() == nil {
		err := fn(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := interval
		if err != nil {
			var rl *esi.RateLimitError
			if errors.As(err, &rl) {
				delay = max(interval, rl.RetryAfter)
				slog.Warn("collector backing off after rate limit", "wait", delay)
			} else {
				slog.Error("scheduled run failed", "err", err)
			}
		}
		if wait(ctx, delay) != nil {
			return
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
