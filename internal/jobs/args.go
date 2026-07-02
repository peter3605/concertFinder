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
