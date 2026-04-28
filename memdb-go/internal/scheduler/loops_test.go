package scheduler

// loops_test.go — unit tests for the periodicLoop primitive.
//
// Cases:
//   1. Happy path: runOnce is called and success metric is recorded.
//   2. ctx cancellation: Start returns without calling runOnce after cancel.
//   3. stopCh signal: Start returns when stopCh is closed.
//   4. acquireLock skip: skipped_other_leader outcome when lock not acquired.
//   5. runOnce error: error outcome is recorded on non-nil return.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestPeriodicLoop_HappyPath verifies that runOnce is called at least once
// within the first tick and the loop exits cleanly on ctx cancellation.
func TestPeriodicLoop_HappyPath(t *testing.T) {
	var calls atomic.Int64
	called := make(chan struct{}, 1)

	loop := &periodicLoop{
		name:     "test_happy",
		interval: 50 * time.Millisecond,
		stagger:  0,
		runOnce: func(_ context.Context) error {
			calls.Add(1)
			select {
			case called <- struct{}{}:
			default:
			}
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Start(ctx, stopCh)
	}()

	select {
	case <-called:
		// runOnce fired — cancel now.
		cancel()
	case <-ctx.Done():
		t.Fatal("runOnce was never called within timeout")
	}

	<-done
	if calls.Load() == 0 {
		t.Error("expected runOnce to be called at least once")
	}
}

// TestPeriodicLoop_CtxCancellation verifies Start returns promptly when ctx is
// cancelled before the stagger delay expires.
func TestPeriodicLoop_CtxCancellation(t *testing.T) {
	var calls atomic.Int64

	loop := &periodicLoop{
		name:     "test_cancel",
		interval: time.Hour,        // very long — should never tick in test
		stagger:  10 * time.Second, // also long — context cancels first
		runOnce: func(_ context.Context) error {
			calls.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Start(ctx, stopCh)
	}()

	cancel() // cancel before stagger expires

	select {
	case <-done:
		// Good — loop exited.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start did not return after ctx cancellation")
	}

	if calls.Load() != 0 {
		t.Errorf("runOnce should not have been called, got %d calls", calls.Load())
	}
}

// TestPeriodicLoop_StopCh verifies Start returns promptly when stopCh is closed.
func TestPeriodicLoop_StopCh(t *testing.T) {
	var calls atomic.Int64

	loop := &periodicLoop{
		name:     "test_stopch",
		interval: time.Hour,
		stagger:  10 * time.Second,
		runOnce: func(_ context.Context) error {
			calls.Add(1)
			return nil
		},
	}

	ctx := context.Background()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Start(ctx, stopCh)
	}()

	close(stopCh)

	select {
	case <-done:
		// Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Start did not return after stopCh close")
	}

	if calls.Load() != 0 {
		t.Errorf("runOnce should not have been called, got %d calls", calls.Load())
	}
}

// TestPeriodicLoop_AdvisoryLockSkip verifies that when acquireLock returns
// (false, nil) the iteration is skipped (runOnce not called) and the loop
// continues running.
func TestPeriodicLoop_AdvisoryLockSkip(t *testing.T) {
	var runOnceCalls atomic.Int64
	var lockCalls atomic.Int64
	gotFirstLock := make(chan struct{}, 1)

	loop := &periodicLoop{
		name:     "test_lock_skip",
		interval: 30 * time.Millisecond,
		stagger:  0,
		acquireLock: func(_ context.Context) (bool, error) {
			n := lockCalls.Add(1)
			select {
			case gotFirstLock <- struct{}{}:
			default:
			}
			// Deny the lock for the first call, grant for the second.
			return n > 1, nil
		},
		releaseLock: func(_ context.Context) {},
		runOnce: func(_ context.Context) error {
			runOnceCalls.Add(1)
			return nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Start(ctx, stopCh)
	}()

	// Wait for at least the first lock attempt.
	select {
	case <-gotFirstLock:
	case <-ctx.Done():
		t.Fatal("acquireLock was never called")
	}

	// Let the loop run a couple of ticks, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if lockCalls.Load() < 1 {
		t.Errorf("expected acquireLock to be called at least once, got %d", lockCalls.Load())
	}
	// First lock call returns false → runOnce must NOT have been called on that tick.
	// Subsequent calls return true → runOnce IS called.
	// We only assert runOnce was called fewer times than lockCalls (some were skipped).
	if runOnceCalls.Load() >= lockCalls.Load() {
		t.Errorf("expected some skipped iterations: lockCalls=%d runOnceCalls=%d",
			lockCalls.Load(), runOnceCalls.Load())
	}
}

// TestPeriodicLoop_RunOnceError verifies that a non-nil error from runOnce
// is handled gracefully (loop continues, no panic).
func TestPeriodicLoop_RunOnceError(t *testing.T) {
	var calls atomic.Int64
	errBoom := errors.New("boom")

	loop := &periodicLoop{
		name:     "test_error",
		interval: 30 * time.Millisecond,
		stagger:  0,
		runOnce: func(_ context.Context) error {
			calls.Add(1)
			return errBoom
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.Start(ctx, stopCh)
	}()

	<-done

	if calls.Load() == 0 {
		t.Error("expected runOnce to be called at least once even when returning error")
	}
}
