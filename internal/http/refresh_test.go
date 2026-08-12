package http

import (
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/db"
)

func TestRefreshAllowedWithNoSnapshot(t *testing.T) {
	// Nothing to protect, and a scan is exactly what the user is asking for.
	if _, reason := refreshRefusal(nil, time.Now()); reason != "" {
		t.Errorf("expected allow, got %q", reason)
	}
}

func TestRefreshRefusedRightAfterAScan(t *testing.T) {
	now := time.Now()
	snap := &db.ConcertSnapshot{ComputedAt: now.Add(-time.Minute)}
	at, reason := refreshRefusal(snap, now)
	if reason != "too_soon" {
		t.Fatalf("expected too_soon, got %q", reason)
	}
	// The UI needs to say *when*, not just no.
	if at == nil {
		t.Fatal("refusal must carry the instant it lifts")
	}
	if want := snap.ComputedAt.Add(ManualRefreshMinInterval); !at.Equal(want) {
		t.Errorf("retry at %v, want %v", at, want)
	}
}

func TestRefreshAllowedOnceIntervalPasses(t *testing.T) {
	now := time.Now()
	snap := &db.ConcertSnapshot{ComputedAt: now.Add(-ManualRefreshMinInterval - time.Second)}
	if _, reason := refreshRefusal(snap, now); reason != "" {
		t.Errorf("expected allow, got %q", reason)
	}
}

// Quota exhaustion has to outrank recency: a scan enqueued before the ledger's
// UTC day rolls over is guaranteed to come back capped and to overwrite the
// snapshot with another complete=false.
func TestRefreshRefusedWhileQuotaExhausted(t *testing.T) {
	now := time.Now()
	reset := now.Add(3 * time.Hour)
	snap := &db.ConcertSnapshot{
		// Old enough that the interval check alone would let it through.
		ComputedAt: now.Add(-24 * time.Hour),
		RetryAfter: &reset,
	}
	at, reason := refreshRefusal(snap, now)
	if reason != "quota_exhausted" {
		t.Fatalf("expected quota_exhausted, got %q", reason)
	}
	if at == nil || !at.Equal(reset) {
		t.Errorf("should report the quota reset instant, got %v", at)
	}
}

// A retry_after that has already passed is spent, not sticky.
func TestRefreshAllowedAfterQuotaResets(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	snap := &db.ConcertSnapshot{
		ComputedAt: now.Add(-24 * time.Hour),
		RetryAfter: &past,
	}
	if _, reason := refreshRefusal(snap, now); reason != "" {
		t.Errorf("expected allow, got %q", reason)
	}
}
