package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// newTestListener builds a Listener with hand-fed channels. CollectSignals
// never touches the pgx connection, so no database is needed to exercise its
// window semantics.
func newTestListener(buffer int) *Listener {
	return &Listener{
		channel: "pgcov_test",
		signals: make(chan types.CoverageSignal, buffer),
		errors:  make(chan error, 10),
	}
}

func signal(id string) types.CoverageSignal {
	return types.CoverageSignal{SignalID: id, Timestamp: time.Now()}
}

// TestCollectSignals_IdleWindowResetsPerSignal is the regression test for the
// truncation defect: the deadline used to be created once and never reset, so
// it capped the entire collection phase rather than the gap between signals.
//
// Twenty signals arriving 25ms apart span ~500ms - five times the 100ms window.
// Under the old total-window behaviour collection stopped after ~4 of them and
// the rest were silently discarded by the deferred Close. With an idle window,
// every signal restarts the clock and all twenty arrive.
func TestCollectSignals_IdleWindowResetsPerSignal(t *testing.T) {
	const (
		count   = 20
		gap     = 25 * time.Millisecond
		window  = 100 * time.Millisecond
		timeout = 10 * time.Second
	)

	l := newTestListener(1000)

	go func() {
		for i := 0; i < count; i++ {
			time.Sleep(gap)
			l.signals <- signal("a.sql:0:1")
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	got, err := l.CollectSignals(ctx, window)
	if err != nil {
		t.Fatalf("CollectSignals: %v", err)
	}
	if len(got) != count {
		t.Errorf("collected %d signals, want %d - the grace period must be an idle "+
			"window, not a cap on the whole collection phase", len(got), count)
	}
}

// TestCollectSignals_StopsAfterIdleWindow is the other half of the contract:
// once the stream really does go quiet, collection must end promptly rather
// than waiting for ctx.
func TestCollectSignals_StopsAfterIdleWindow(t *testing.T) {
	l := newTestListener(10)
	l.signals <- signal("a.sql:0:1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	got, err := l.CollectSignals(ctx, 100*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("CollectSignals: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("collected %d signals, want 1", len(got))
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v; must return shortly after the idle window elapses", elapsed)
	}
}

// TestCollectSignals_DrainsBufferOnDeadline covers the second half of the loss
// path: signals already delivered into the buffer must not be thrown away just
// because the idle deadline fired.
func TestCollectSignals_DrainsBufferOnDeadline(t *testing.T) {
	const buffered = 50

	l := newTestListener(1000)
	for i := 0; i < buffered; i++ {
		l.signals <- signal("a.sql:0:1")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A window this short expires almost immediately; everything already
	// buffered must still come back.
	got, err := l.CollectSignals(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("CollectSignals: %v", err)
	}
	if len(got) != buffered {
		t.Errorf("collected %d signals, want %d - buffered signals must be drained "+
			"before returning on the idle deadline", len(got), buffered)
	}
}

// TestCollectSignals_DrainsBufferOnContextCancel asserts the same drain happens
// when the caller's context ends, and that ctx.Err() is still reported.
func TestCollectSignals_DrainsBufferOnContextCancel(t *testing.T) {
	const buffered = 10

	l := newTestListener(1000)
	for i := 0; i < buffered; i++ {
		l.signals <- signal("a.sql:0:1")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := l.CollectSignals(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(got) != buffered {
		t.Errorf("collected %d signals, want %d - cancellation must still drain the buffer",
			len(got), buffered)
	}
}

// TestCollectSignals_ClosedChannelsDoNotSpin guards the select against a closed
// errors channel, which is always ready and would otherwise busy-loop until the
// window expired. receiveLoop closes both channels when it exits.
func TestCollectSignals_ClosedChannelsDoNotSpin(t *testing.T) {
	l := newTestListener(10)
	l.signals <- signal("a.sql:0:1")
	close(l.errors)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var got []types.CoverageSignal
	go func() {
		defer close(done)
		got, _ = l.CollectSignals(ctx, 100*time.Millisecond)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CollectSignals did not return; a closed errors channel is spinning the select")
	}

	if len(got) != 1 {
		t.Errorf("collected %d signals, want 1", len(got))
	}
}

// TestCollectSignals_ClosedSignalsChannelReturns covers listener shutdown mid
// collection: a closed signals channel ends collection without an error.
func TestCollectSignals_ClosedSignalsChannelReturns(t *testing.T) {
	l := newTestListener(10)
	l.signals <- signal("a.sql:0:1")
	close(l.signals)

	got, err := l.CollectSignals(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("CollectSignals: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("collected %d signals, want 1", len(got))
	}
}
