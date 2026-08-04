package jobs

import (
	"slices"
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/concerts"
)

// One scan per (user, location) may be in flight. The previous
// ByPeriod-based uniqueness let a second scan start 30s into a ~60s scan;
// the first had already reserved the user's entire daily quota block, so
// the second got zero permits, reported rate_capped, and overwrote the
// first one's good snapshot with complete=false.
func TestScanConcertsArgs_UniqueWhileInFlight(t *testing.T) {
	opts := ScanConcertsArgs{}.InsertOpts()

	if opts.UniqueOpts.ByPeriod != 0 {
		t.Errorf("ByPeriod must not be used: a time window shorter than a scan "+
			"lets scans overlap and starve each other (got %v)", opts.UniqueOpts.ByPeriod)
	}
	if !opts.UniqueOpts.ByArgs {
		t.Error("ByArgs must be set so uniqueness is per user+location")
	}
	if opts.MaxAttempts != ScanMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", opts.MaxAttempts, ScanMaxAttempts)
	}

	// River requires these four; retryable is ours, so a failed-and-waiting
	// scan also blocks a duplicate.
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
		rivertype.JobStateRetryable,
	} {
		if !slices.Contains(opts.UniqueOpts.ByState, required) {
			t.Errorf("ByState is missing %q — a duplicate scan could start", required)
		}
	}

	// A finished scan must not block the next legitimate refresh.
	for _, final := range []rivertype.JobState{
		rivertype.JobStateCompleted,
		rivertype.JobStateCancelled,
		rivertype.JobStateDiscarded,
	} {
		if slices.Contains(opts.UniqueOpts.ByState, final) {
			t.Errorf("ByState includes terminal state %q — stale snapshots could never refresh", final)
		}
	}
}

func TestIsOnlyRateCapped(t *testing.T) {
	cases := []struct {
		name string
		err  *concerts.IncompleteError
		want bool
	}{
		{
			// Time fixes this and nothing else will; don't retry.
			name: "capped with full artist coverage",
			err:  &concerts.IncompleteError{RateCapped: true, SkippedArtists: 0},
			want: true,
		},
		{
			// Artists were dropped too — a retry can still add coverage.
			name: "capped and artists skipped",
			err:  &concerts.IncompleteError{RateCapped: true, SkippedArtists: 12},
			want: false,
		},
		{
			name: "budget overrun only",
			err:  &concerts.IncompleteError{RateCapped: false, SkippedArtists: 12},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isOnlyRateCapped(c.err); got != c.want {
				t.Errorf("isOnlyRateCapped = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNextQuotaReset(t *testing.T) {
	// Must line up with internal/rate's UTC day bucket.
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "midday UTC rolls to tomorrow 00:00Z",
			now:  time.Date(2026, 7, 31, 12, 34, 56, 0, time.UTC),
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "one second before reset",
			now:  time.Date(2026, 7, 31, 23, 59, 59, 0, time.UTC),
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "month boundary",
			now:  time.Date(2026, 7, 31, 23, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// A non-UTC clock must still resolve to the UTC boundary: 22:00
			// on the 31st at -04:00 is already 02:00 on Aug 1 in UTC.
			name: "local zone is normalized to UTC first",
			now:  time.Date(2026, 7, 31, 22, 0, 0, 0, time.FixedZone("EDT", -4*60*60)),
			want: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextQuotaReset(c.now)
			if !got.Equal(c.want) {
				t.Errorf("nextQuotaReset(%s) = %s, want %s",
					c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
			}
			if !got.After(c.now) {
				t.Errorf("reset %s must be in the future relative to %s",
					got.Format(time.RFC3339), c.now.Format(time.RFC3339))
			}
		})
	}
}

func TestLocationKeyIsStable(t *testing.T) {
	// The janitor's snapshot prune compares against keys built here, so the
	// format has exactly one producer and must not drift.
	got := LocationKey(concerts.Location{Latitude: 39.290385, Longitude: -76.612189, RadiusMiles: 50})
	if want := "39.2904,-76.6122,50"; got != want {
		t.Errorf("LocationKey = %q, want %q", got, want)
	}
}
