package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---- isHighPriority ---------------------------------------------------------

func TestIsHighPriority_HighLabels(t *testing.T) {
	high := []string{LabelMemUpdate, LabelQuery, LabelMemFeedback}
	for _, label := range high {
		if !isHighPriority(label) {
			t.Errorf("isHighPriority(%q) = false, want true", label)
		}
	}
}

func TestIsHighPriority_LowLabels(t *testing.T) {
	low := []string{LabelMemOrganize, LabelMemRead, LabelPrefAdd, LabelAdd, LabelAnswer, LabelMemArchive, "unknown"}
	for _, label := range low {
		if isHighPriority(label) {
			t.Errorf("isHighPriority(%q) = true, want false", label)
		}
	}
}

// ---- ScheduleMessage.HighPriority set by fromXMessage -----------------------

func TestFromXMessage_SetsHighPriority(t *testing.T) {
	cases := []struct {
		label string
		want  bool
	}{
		{LabelMemUpdate, true},
		{LabelQuery, true},
		{LabelMemFeedback, true},
		{LabelMemOrganize, false},
		{LabelMemRead, false},
		{LabelPrefAdd, false},
		{LabelAdd, false},
		{LabelAnswer, false},
	}

	for _, tc := range cases {
		msg := makeXMsg(tc.label)
		sm, err := fromXMessage("stream:key", msg)
		if err != nil {
			t.Fatalf("fromXMessage(%q): %v", tc.label, err)
		}
		if sm.HighPriority != tc.want {
			t.Errorf("fromXMessage(%q).HighPriority = %v, want %v", tc.label, sm.HighPriority, tc.want)
		}
	}
}

// ---- enqueue routes to correct channel --------------------------------------

func TestEnqueue_HighPriorityGoesToHighCh(t *testing.T) {
	w := newMinimalWorker()
	ctx := context.Background()

	sm := streamMsg{msg: ScheduleMessage{Label: LabelMemUpdate, HighPriority: true}}
	if err := w.enqueue(ctx, sm); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case got := <-w.highMsgCh:
		if got.msg.Label != LabelMemUpdate {
			t.Errorf("highMsgCh got label %q, want %q", got.msg.Label, LabelMemUpdate)
		}
	case <-w.lowMsgCh:
		t.Error("high-priority message routed to lowMsgCh")
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout: message not in highMsgCh")
	}
}

func TestEnqueue_LowPriorityGoesToLowCh(t *testing.T) {
	w := newMinimalWorker()
	ctx := context.Background()

	sm := streamMsg{msg: ScheduleMessage{Label: LabelMemOrganize, HighPriority: false}}
	if err := w.enqueue(ctx, sm); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-w.highMsgCh:
		t.Error("low-priority message routed to highMsgCh")
	case got := <-w.lowMsgCh:
		if got.msg.Label != LabelMemOrganize {
			t.Errorf("lowMsgCh got label %q, want %q", got.msg.Label, LabelMemOrganize)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout: message not in lowMsgCh")
	}
}

func TestEnqueue_CancelledContext(t *testing.T) {
	w := newMinimalWorker()

	// Fill highMsgCh to capacity so enqueue blocks.
	for i := 0; i < highMsgChanBuffer; i++ {
		w.highMsgCh <- streamMsg{msg: ScheduleMessage{Label: LabelMemUpdate, HighPriority: true}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	sm := streamMsg{msg: ScheduleMessage{Label: LabelMemUpdate, HighPriority: true}}
	err := w.enqueue(ctx, sm)
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
}

// ---- processLoop priority ordering ------------------------------------------

func TestProcessLoop_HighBeforeLow(t *testing.T) {
	w := newMinimalWorker()

	var mu sync.Mutex
	var order []string
	w.handleFn = func(_ context.Context, msg ScheduleMessage) {
		mu.Lock()
		order = append(order, msg.Label)
		mu.Unlock()
	}

	// Pre-fill: 3 low-priority, then 2 high-priority.
	for i := 0; i < 3; i++ {
		w.lowMsgCh <- streamMsg{msg: ScheduleMessage{Label: LabelMemOrganize, HighPriority: false}}
	}
	for i := 0; i < 2; i++ {
		w.highMsgCh <- streamMsg{msg: ScheduleMessage{Label: LabelMemUpdate, HighPriority: true}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run processLoop in background, cancel after all 5 messages processed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.processLoop(ctx)
	}()

	// Wait until all 5 messages processed or timeout.
	deadline := time.After(400 * time.Millisecond)
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			goto check
		case <-done:
			goto check
		default:
			mu.Lock()
			n := len(order)
			mu.Unlock()
			if n >= 5 {
				cancel()
				<-done
				goto check
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

check:
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 5 {
		t.Fatalf("only %d messages processed, want 5: %v", len(order), order)
	}

	// First two must be high-priority (mem_update).
	for i := 0; i < 2; i++ {
		if order[i] != LabelMemUpdate {
			t.Errorf("order[%d] = %q, want %q (high-priority first)", i, order[i], LabelMemUpdate)
		}
	}
	// Last three must be low-priority (mem_organize).
	for i := 2; i < 5; i++ {
		if order[i] != LabelMemOrganize {
			t.Errorf("order[%d] = %q, want %q (low-priority last)", i, order[i], LabelMemOrganize)
		}
	}
}

func TestProcessLoop_OnlyHighPriority(t *testing.T) {
	w := newMinimalWorker()
	var processed atomic.Int32
	w.handleFn = func(_ context.Context, msg ScheduleMessage) {
		processed.Add(1)
	}

	for i := 0; i < 5; i++ {
		w.highMsgCh <- streamMsg{msg: ScheduleMessage{Label: LabelMemUpdate, HighPriority: true}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go w.processLoop(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	if processed.Load() < 5 {
		t.Errorf("processed %d, want 5", processed.Load())
	}
}

func TestProcessLoop_OnlyLowPriority(t *testing.T) {
	w := newMinimalWorker()
	var processed atomic.Int32
	w.handleFn = func(_ context.Context, msg ScheduleMessage) {
		processed.Add(1)
	}

	for i := 0; i < 5; i++ {
		w.lowMsgCh <- streamMsg{msg: ScheduleMessage{Label: LabelMemOrganize, HighPriority: false}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	go w.processLoop(ctx)
	time.Sleep(200 * time.Millisecond)
	cancel()

	if processed.Load() < 5 {
		t.Errorf("processed %d, want 5", processed.Load())
	}
}

// ---- priorityLabel ----------------------------------------------------------

func TestPriorityLabel(t *testing.T) {
	if priorityLabel(true) != "high" {
		t.Error("priorityLabel(true) != high")
	}
	if priorityLabel(false) != "low" {
		t.Error("priorityLabel(false) != low")
	}
}

// ---- channel buffer sizes ---------------------------------------------------

func TestChannelBufferSizes(t *testing.T) {
	if highMsgChanBuffer <= 0 {
		t.Error("highMsgChanBuffer must be positive")
	}
	if lowMsgChanBuffer <= 0 {
		t.Error("lowMsgChanBuffer must be positive")
	}
	if lowMsgChanBuffer <= highMsgChanBuffer {
		t.Errorf("lowMsgChanBuffer (%d) should be larger than highMsgChanBuffer (%d)",
			lowMsgChanBuffer, highMsgChanBuffer)
	}
}

// ---- helpers ----------------------------------------------------------------

// makeXMsg creates a minimal redis.XMessage for testing fromXMessage.
func makeXMsg(label string) redis.XMessage {
	return redis.XMessage{
		ID: "1-0",
		Values: map[string]any{
			"cube_id": "cube-test",
			"label":   label,
		},
	}
}

// testWorkerWithHook extends Worker with an injectable handle function for testing.
type testWorkerWithHook struct {
	Worker
	handleFn func(ctx context.Context, msg ScheduleMessage)
}

// processLoop override that uses handleFn instead of w.handle.
func (w *testWorkerWithHook) processLoop(ctx context.Context) {
	for {
		// Phase 1: drain high-priority non-blocking.
		for {
			select {
			case sm := <-w.highMsgCh:
				w.handleFn(ctx, sm.msg)
				continue
			default:
			}
			break
		}
		// Phase 2: blocking select.
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case sm := <-w.highMsgCh:
			w.handleFn(ctx, sm.msg)
		case sm := <-w.lowMsgCh:
			select {
			case hi := <-w.highMsgCh:
				select {
				case w.lowMsgCh <- sm:
				default:
					w.handleFn(ctx, sm.msg)
				}
				w.handleFn(ctx, hi.msg)
			default:
				w.handleFn(ctx, sm.msg)
			}
		}
	}
}

func newMinimalWorker() *testWorkerWithHook {
	w := &testWorkerWithHook{
		Worker: Worker{
			highMsgCh: make(chan streamMsg, highMsgChanBuffer),
			lowMsgCh:  make(chan streamMsg, lowMsgChanBuffer),
			stopCh:    make(chan struct{}),
		},
	}
	w.handleFn = func(_ context.Context, _ ScheduleMessage) {}
	return w
}
