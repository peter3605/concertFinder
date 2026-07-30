package jobs

import "github.com/google/uuid"

// RefreshAffinityArgs recomputes one user's affinity profile out-of-band.
type RefreshAffinityArgs struct {
	UserID uuid.UUID `json:"user_id"`
}

func (RefreshAffinityArgs) Kind() string { return "refresh_affinity" }

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
