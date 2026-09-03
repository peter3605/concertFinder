package config

import (
	"testing"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// loadDefaults runs Load() with only the required vars set, so the assertions
// below describe the shipped defaults rather than whatever is in the
// developer's environment.
func loadDefaults(t *testing.T) *Config {
	t.Helper()
	for _, k := range []string{
		"RATE_CAP_TM_PER_USER_DAILY",
		"RATE_CAP_SONGKICK_PER_USER_DAILY",
		"CONCERT_CACHE_TTL_HOURS",
		"PHASE2_FALLBACK_BUDGET_SECONDS",
		"PHASE2_FALLBACK_CONCURRENCY",
		"DAILY_AFFINITY_HOUR_UTC",
		"DAILY_SCAN_HOUR_UTC",
		"DAILY_DIGEST_HOUR_UTC",
		"DAILY_JANITOR_HOUR_UTC",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x/y")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// A per-user daily cap below the cost of a COLD scan means a new user can
// never cover their own profile: the scan spends the whole allowance partway
// through and reports itself incomplete. Neither failure this has had raises
// an error — both present as a concert list quietly holding a fraction of the
// shows it should.
//
// Measured against artists x CallsPerArtistColdScan, not against the artist
// count. That distinction is the entire point of this test now: the earlier
// version compared against MaxScoredArtists alone, which is the WARM cost, so
// it passed a 250 cap that covered 125 of 200 artists. Ticketmaster
// resolution is two-stage and a first-ever scan pays for both stages.
func TestDailyCapsCoverAColdScan(t *testing.T) {
	cfg := loadDefaults(t)
	coldScanCost := spotify.MaxScoredArtists * ticketmaster.CallsPerArtistColdScan
	for _, c := range []struct {
		name string
		cap  int
	}{
		{"ticketmaster", cfg.RateCapTMPerUserDaily},
	} {
		if c.cap > 0 && c.cap < coldScanCost {
			t.Errorf("%s daily cap %d is below the cold-scan cost (%d artists x %d calls = %d): "+
				"a new user could never cover their profile on their first day",
				c.name, c.cap, spotify.MaxScoredArtists,
				ticketmaster.CallsPerArtistColdScan, coldScanCost)
		}
	}
}

// The cache is what keeps a user's daily cost well under their cap. If it
// expired several times a day, each expiry would cost a fresh call per
// artist and the caps above would stop being sufficient.
func TestCacheTTLOutlivesTheSWRRefreshWindow(t *testing.T) {
	cfg := loadDefaults(t)
	ttlHours := cfg.ConcertCacheTTLHours
	if ttlHours == 0 {
		ttlHours = int(concerts.DefaultCacheTTL.Hours())
	}
	if ttlHours < cfg.SnapshotStaleAfterHours {
		t.Errorf("cache TTL (%dh) is shorter than the snapshot staleness window (%dh): "+
			"every SWR refresh would spend fresh upstream quota",
			ttlHours, cfg.SnapshotStaleAfterHours)
	}
}

// The janitor deletes concert_cache rows older than 7 days. If the TTL ever
// exceeded that, the janitor would be evicting rows the search layer still
// considers live.
func TestCacheTTLIsShorterThanJanitorRetention(t *testing.T) {
	const janitorRetentionHours = 7 * 24
	cfg := loadDefaults(t)
	ttlHours := cfg.ConcertCacheTTLHours
	if ttlHours == 0 {
		ttlHours = int(concerts.DefaultCacheTTL.Hours())
	}
	if ttlHours >= janitorRetentionHours {
		t.Errorf("cache TTL (%dh) meets or exceeds janitor retention (%dh): "+
			"the janitor would prune entries the search layer still trusts",
			ttlHours, janitorRetentionHours)
	}
}

func TestFallbackConcurrencyDefaultsToOne(t *testing.T) {
	// The fallback resolvers share one 1 req/sec turnstile each, so admitting
	// more than one scan divides throughput without shrinking any scan's
	// budget.
	if cfg := loadDefaults(t); cfg.Phase2FallbackConcurrency != 1 {
		t.Errorf("Phase2FallbackConcurrency = %d, want 1", cfg.Phase2FallbackConcurrency)
	}
}

// The digest reads the snapshot the nightly scan writes. Scheduled too close
// together, it reads yesterday's — silently, since a stale snapshot is still
// a valid one. The gap has to clear the scan fanout's 60-minute spread plus
// the per-job budget and retries.
func TestDigestRunsFarEnoughAfterTheScan(t *testing.T) {
	cfg := loadDefaults(t)
	if gap := cfg.ScanDigestGapHours(); gap < MinScanDigestGapHours {
		t.Errorf("digest runs %dh after the scan (scan %02d:00, digest %02d:00), want >= %dh: "+
			"every digest would describe the previous day's scan",
			gap, cfg.DailyScanHourUTC, cfg.DailyDigestHourUTC, MinScanDigestGapHours)
	}
}

// The gap is modular, so a scan late in the day with a digest after midnight
// is two hours apart rather than negative twenty-two.
func TestScanDigestGapWrapsAroundMidnight(t *testing.T) {
	cfg := Config{DailyScanHourUTC: 23, DailyDigestHourUTC: 1}
	if got := cfg.ScanDigestGapHours(); got != 2 {
		t.Errorf("23:00 -> 01:00 gap = %d, want 2", got)
	}
}

// An hour outside 0-23 must fall back to the default. time.Date normalizes
// hour 25 into the next day, so passing it through would quietly move a job
// to a time nobody chose.
func TestOutOfRangeHourFallsBackToDefault(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x/y")
	t.Setenv("DAILY_SCAN_HOUR_UTC", "25")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DailyScanHourUTC != 7 {
		t.Errorf("DailyScanHourUTC = %d, want the 7 default", cfg.DailyScanHourUTC)
	}
}
