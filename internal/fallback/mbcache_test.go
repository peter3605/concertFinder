package fallback

import (
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/db"
)

// A resolved homepage doesn't go stale — the artist's own site is stable and
// re-fetching it would spend the 1 req/sec turnstile for nothing.
func TestMBCachePositiveNeverExpires(t *testing.T) {
	e := mbCacheEntry{url: "https://artist.example", resolvedAt: time.Now().Add(-5 * 365 * 24 * time.Hour)}
	if !e.live() {
		t.Error("a positive resolution should be trusted regardless of age")
	}
}

// The bug this guards: MusicBrainz is a user-edited database and URL
// relationships are added to it continuously, so "no homepage" describes
// today, not the artist. Cached forever it became a silent permanent
// exclusion from the fallback chain — the same failure the Ticketmaster
// negative cache had before NegativeResolutionTTL.
func TestMBCacheNegativeExpires(t *testing.T) {
	fresh := mbCacheEntry{url: "", resolvedAt: time.Now().Add(-time.Hour)}
	if !fresh.live() {
		t.Error("a recent negative should still be trusted")
	}
	stale := mbCacheEntry{url: "", resolvedAt: time.Now().Add(-db.NegativeMBURLTTL - time.Minute)}
	if stale.live() {
		t.Error("a negative past NegativeMBURLTTL must be re-asked, not trusted forever")
	}
}

// The hot cache is consulted before the DB, so an expiry enforced only in SQL
// would never be reached: 5000 slots against 200 artists a scan means a
// negative is never evicted for the life of the process.
func TestMBCacheBoundaryIsTheSharedTTL(t *testing.T) {
	justInside := mbCacheEntry{url: "", resolvedAt: time.Now().Add(-db.NegativeMBURLTTL + time.Minute)}
	if !justInside.live() {
		t.Error("just inside the TTL should still be a hit")
	}
}

// A zero-value entry (e.g. a type assertion landing on something unexpected)
// must not be read as a live negative that suppresses lookups forever.
func TestMBCacheZeroValueIsNotLive(t *testing.T) {
	if (mbCacheEntry{}).live() {
		t.Error("zero-value entry must not be trusted")
	}
}
