package fallback

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The regression: Wait used to guard lastReq with a sync.Mutex and hold it
// across the ~1.1s inter-request sleep. A scan fanning out over 200 artists
// piled every goroutine onto that lock, and because Lock() is not
// ctx-aware, a cancelled scan could not extract them — a job with a
// 5-minute budget was observed running 978s.

func TestRateLimiter_WaitReturnsWhenCtxCancelledWhileQueued(t *testing.T) {
	r := newRateLimiter(time.Hour) // long enough that nobody gets through on merit

	// Occupy the turnstile so everyone else has to queue for it.
	holder, holderCancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = r.Wait(holder) // takes the slot, then sleeps out the gap
	}()
	// Let the first caller claim the slot.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan error, 1)
	go func() { queued <- r.Wait(ctx) }()

	// The queued caller must observe cancellation promptly rather than
	// blocking until the holder's gap elapses.
	cancel()
	select {
	case err := <-queued:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued caller did not observe cancellation — Wait is blocking uninterruptibly")
	}

	holderCancel()
	<-firstDone
}

func TestRateLimiter_EnforcesGapBetweenRequests(t *testing.T) {
	const gap = 80 * time.Millisecond
	r := newRateLimiter(gap)
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := r.Wait(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	// First call is free; the next two each wait out the gap.
	if elapsed := time.Since(start); elapsed < 2*gap {
		t.Errorf("expected >= %v of spacing, got %v", 2*gap, elapsed)
	}
}

func TestRateLimiter_SerializesConcurrentCallers(t *testing.T) {
	const gap = 40 * time.Millisecond
	r := newRateLimiter(gap)
	ctx := context.Background()

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Wait(ctx)
		}()
	}
	wg.Wait()
	// 4 callers, 3 enforced gaps between them.
	if elapsed := time.Since(start); elapsed < 3*gap {
		t.Errorf("callers were not serialized: %v < %v", elapsed, 3*gap)
	}
}

func TestRateLimiter_CancelledWaitDoesNotConsumeTheSlot(t *testing.T) {
	// A caller that bails must leave the limiter usable for the next one.
	r := newRateLimiter(10 * time.Millisecond)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_ = r.Wait(cancelled)

	done := make(chan error, 1)
	go func() { done <- r.Wait(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("limiter unusable after a cancelled Wait: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("turnstile was leaked by a cancelled Wait")
	}
}
