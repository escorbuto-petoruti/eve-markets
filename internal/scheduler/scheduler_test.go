package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-markets/internal/esi"
)

type fakeWait struct {
	waits []time.Duration
	// onWait runs after recording; it may cancel the context.
	onWait func(n int)
}

func (f *fakeWait) wait(ctx context.Context, d time.Duration) error {
	f.waits = append(f.waits, d)
	if f.onWait != nil {
		f.onWait(len(f.waits))
	}
	return ctx.Err()
}

func TestLoopRunsImmediatelyThenEveryInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runs := 0
	fw := &fakeWait{onWait: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	loop(ctx, 15*time.Minute, func(context.Context) error { runs++; return nil }, fw.wait)
	if runs != 2 {
		t.Errorf("runs = %d, want 2", runs)
	}
	if len(fw.waits) != 2 || fw.waits[0] != 15*time.Minute {
		t.Errorf("waits = %v", fw.waits)
	}
}

func TestLoopWaitsRetryAfterOnRateLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	fw := &fakeWait{onWait: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	loop(ctx, time.Minute, func(context.Context) error {
		calls++
		if calls == 1 {
			return &esi.RateLimitError{Status: 429, RetryAfter: 10 * time.Minute}
		}
		return errors.New("other failure does not stop the loop")
	}, fw.wait)
	if len(fw.waits) < 2 || fw.waits[0] != 10*time.Minute || fw.waits[1] != time.Minute {
		t.Errorf("waits = %v, want [10m 1m]", fw.waits)
	}
}

func TestLoopStopsWhenContextCancelledDuringRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runs := 0
	fw := &fakeWait{}
	loop(ctx, time.Minute, func(context.Context) error { runs++; cancel(); return ctx.Err() }, fw.wait)
	if runs != 1 || len(fw.waits) != 0 {
		t.Errorf("runs=%d waits=%v", runs, fw.waits)
	}
}

func TestLoopNeverOverlapsAndStopsOnCancelReal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var inflight, overlap, runs atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		Loop(ctx, time.Millisecond, func(context.Context) error {
			if inflight.Add(1) > 1 {
				overlap.Add(1)
			}
			time.Sleep(3 * time.Millisecond)
			inflight.Add(-1)
			runs.Add(1)
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Loop did not stop after cancel")
	}
	if overlap.Load() != 0 || runs.Load() < 2 {
		t.Errorf("overlap=%d runs=%d", overlap.Load(), runs.Load())
	}
}
