package jobs

import (
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// RefreshAffinityArgs recomputes one user's affinity profile out-of-band.
type RefreshAffinityArgs struct {
	UserID uuid.UUID `json:"user_id"`
}

func (RefreshAffinityArgs) Kind() string { return "refresh_affinity" }

// InsertOpts bounds retries and collapses duplicate enqueues, neither of
// which river's defaults do.
//
// MaxAttempts: the default is 25. A profile that cannot be computed —
// a revoked Spotify grant is the common case — is not going to compute on the
// twenty-fifth try either, and each attempt is a full six-endpoint fan-out
// against the user's Spotify quota.
//
// UniqueOpts: the nightly fanout and the scan path can both want a refresh for
// the same user, and hydration is expensive enough that running it twice
// concurrently is pure waste. Same shape as ScanConcertsArgs — terminal states
// stay out of the list so a finished refresh doesn't block the next one.
func (RefreshAffinityArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: AffinityMaxAttempts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true, // per user
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// FanoutAffinityRefreshArgs is enqueued by the daily periodic schedule. Its
// worker finds active users and enqueues one RefreshAffinityArgs per user.
// Splitting it in two keeps the periodic tick tiny and makes retries per-user.
type FanoutAffinityRefreshArgs struct{}

func (FanoutAffinityRefreshArgs) Kind() string { return "fanout_affinity_refresh" }

// ScanConcertsArgs runs the full TM + BIT + fallback fan-out for one user at
// one location, writes the merged result to user_concert_snapshots. Enqueued
// on login (pre-warm), by the SWR read path when the snapshot is stale, and
// by the daily fanout.
//
// Location is baked into args (rather than re-read from DB inside the worker)
// so the snapshot's location_key matches the caller's expectation even if
// the user changes location mid-flight.
type ScanConcertsArgs struct {
	UserID      uuid.UUID `json:"user_id"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	RadiusMiles int       `json:"radius_miles"`
}

func (ScanConcertsArgs) Kind() string { return "scan_concerts" }

// InsertOpts governs retries and, critically, concurrency for this job type.
//
// MaxAttempts: river's default is 25, which for an account that can never
// scan successfully (a revoked Spotify grant, say) means two dozen full
// 200-artist fan-outs.
//
// UniqueOpts: at most ONE scan per (user, location) may exist in any
// non-final state. A time-window uniqueness (the previous ByPeriod: 30s)
// does not do this — a scan takes ~60s, so the window lapsed while the job
// was still running and a second scan started underneath it. The two then
// starved each other: the first reserves the user's whole daily quota
// block up front, so the second got zero permits, reported rate_capped,
// and wrote complete=false over the first one's good snapshot. Observed
// live as a concert list oscillating between 20 and 21 results with a
// permanent "refreshing" spinner.
//
// Completed/cancelled/discarded are deliberately NOT in the state list:
// once a scan finishes, a genuinely stale snapshot must be refreshable.
func (ScanConcertsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: ScanMaxAttempts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true, // per user + location
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// FanoutScanConcertsArgs is the periodic-tick counterpart. Its worker finds
// active users with saved locations and enqueues one ScanConcertsArgs per user.
type FanoutScanConcertsArgs struct{}

func (FanoutScanConcertsArgs) Kind() string { return "fanout_scan_concerts" }

// SendDigestArgs sends the daily digest email to one opted-in user.
type SendDigestArgs struct {
	UserID uuid.UUID `json:"user_id"`
}

func (SendDigestArgs) Kind() string { return "send_digest" }

// FanoutSendDigestArgs is the periodic-tick counterpart: finds opted-in
// users with a stored email and enqueues one SendDigestArgs each.
type FanoutSendDigestArgs struct{}

func (FanoutSendDigestArgs) Kind() string { return "fanout_send_digest" }

// JanitorArgs prunes stale rows: rate_ledger > 90 days, concert_cache > 7
// days, oauth_handshakes past expiry, snapshots whose location hasn't been
// used in 30 days. Runs daily.
type JanitorArgs struct{}

func (JanitorArgs) Kind() string { return "janitor" }

// SendInstantNotifyArgs delivers an immediate email for a specific set of
// newly-discovered concerts (dedup_keys) belonging to artists the user has
// subscribed to. Enqueued by the ScanConcerts worker after each successful
// scan; the concert bodies are looked up out of the just-written snapshot.
type SendInstantNotifyArgs struct {
	UserID    uuid.UUID `json:"user_id"`
	DedupKeys []string  `json:"dedup_keys"`
}

func (SendInstantNotifyArgs) Kind() string { return "send_instant_notify" }
