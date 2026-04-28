package scheduler

// loops.go — generic periodic-loop primitive for the scheduler package.
//
// periodicLoop encapsulates the stagger → ticker → for-loop pattern shared
// by every background goroutine in the scheduler (pagerank, periodic_reorg,
// and future loops such as F3 re-summarizer and F11 bi-temporal invalidator).
//
// Usage:
//
//	(&periodicLoop{
//	    name:     "my_loop",
//	    interval: 6 * time.Hour,
//	    stagger:  30 * time.Second,
//	    runOnce:  func(ctx context.Context) error { ... },
//	}).Start(ctx)
//
// Metrics emitted per iteration:
//   - memdb.scheduler.loop_iterations_total{name, outcome=success|error|skipped_other_leader}
//   - memdb.scheduler.loop_duration_ms{name} histogram

import (
	"context"
	"time"
)

// periodicLoop is a reusable stagger+ticker loop that runs runOnce on every
// interval tick and emits per-iteration metrics.
//
// acquireLock and releaseLock are optional: when non-nil, Start acquires the
// lock before calling runOnce and releases it afterwards.  If acquireLock
// returns (false, nil) the iteration is skipped with outcome=skipped_other_leader.
type periodicLoop struct {
	name     string        // metric label (e.g. "pagerank", "periodic_reorg")
	interval time.Duration // tick period
	stagger  time.Duration // one-shot delay before the first tick

	// acquireLock, if non-nil, is called before each runOnce invocation.
	// Returns (true, nil) when the lock is held, (false, nil) when another
	// replica holds it, or (false, err) on a database/transport error.
	acquireLock func(ctx context.Context) (bool, error)

	// releaseLock, if non-nil, is called in a defer after each runOnce
	// invocation (only when acquireLock returned true).
	releaseLock func(ctx context.Context)

	// runOnce executes the per-iteration work.  Returning a non-nil error
	// causes outcome=error to be recorded; nil causes outcome=success.
	runOnce func(ctx context.Context) error
}

// Start blocks until ctx is cancelled.  It applies the stagger delay, then
// calls runOnce on every interval tick.  Emits loop_iterations_total and
// loop_duration_ms metrics on every iteration.
func (p *periodicLoop) Start(ctx context.Context, stopCh <-chan struct{}) {
	lmx := loopMx()

	// Staggered start: wait before first tick so multiple loops don't collide.
	if p.stagger > 0 {
		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-time.After(p.stagger):
		}
	}

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.runIteration(ctx, lmx)

		select {
		case <-ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
		}
	}
}

// runIteration executes one loop tick: optional lock, runOnce, metrics.
func (p *periodicLoop) runIteration(ctx context.Context, lmx *loopMetricsStruct) {
	start := time.Now()

	// Advisory lock gate (HA: elect a single leader across replicas).
	if p.acquireLock != nil {
		locked, err := p.acquireLock(ctx)
		if err != nil {
			lmx.Iterations.Add(ctx, 1, labelLoopOutcome(p.name, "error"))
			elapsed := float64(time.Since(start).Milliseconds())
			lmx.DurationMs.Record(ctx, elapsed, labelLoopName(p.name))
			return
		}
		if !locked {
			lmx.Iterations.Add(ctx, 1, labelLoopOutcome(p.name, "skipped_other_leader"))
			return
		}
		if p.releaseLock != nil {
			defer p.releaseLock(ctx)
		}
	}

	err := p.runOnce(ctx)
	elapsed := float64(time.Since(start).Milliseconds())

	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	lmx.Iterations.Add(ctx, 1, labelLoopOutcome(p.name, outcome))
	lmx.DurationMs.Record(ctx, elapsed, labelLoopName(p.name))
}
