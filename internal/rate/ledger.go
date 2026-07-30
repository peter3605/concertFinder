// Package rate enforces per-user, per-source daily quotas on outbound API
// calls that consume a shared upstream budget. Design §8.3.
//
// Sources of quota pain we care about (in order):
//   - Ticketmaster: 5000 req/day account-wide. One user with 200 unresolved
//     artists on a daily cron can burn through this in under a week of two
//     users.
//   - Bandsintown: personal-use quota is soft but they will start denying
//     traffic if any single client is loud.
//   - Songkick: 5000 req/day per developer key.
//
// The ledger is a simple upsert-and-check: on each fresh (cache-miss)
// outbound request we atomically increment (user, source, today) and compare
// the new count to the configured cap. When over, the search layer treats
// the source as unavailable for that user for the rest of the day.
package rate

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Source is the enum of quota-tracked upstream services.
type Source string

const (
	SourceTicketmaster Source = "ticketmaster"
	SourceBandsintown  Source = "bandsintown"
	SourceSongkick     Source = "songkick"
)

// Caps holds the per-user, per-day upper bound for each source. Zero
// disables enforcement for that source.
type Caps struct {
	Ticketmaster int
	Bandsintown  int
	Songkick     int
}

// Cap returns the configured cap for a source, or 0 if unset.
func (c Caps) Cap(s Source) int {
	switch s {
	case SourceTicketmaster:
		return c.Ticketmaster
	case SourceBandsintown:
		return c.Bandsintown
	case SourceSongkick:
		return c.Songkick
	}
	return 0
}

// Ledger is a threadsafe DB-backed rate counter. Concurrent calls to
// CheckAndIncrement for the same (user, source) serialize via Postgres row
// locking rather than in-process — safe across replicas.
type Ledger struct {
	Pool *pgxpool.Pool
	Caps Caps
}

// CheckAndIncrement atomically adds 1 to today's counter for (user, source)
// and returns true if the resulting count is within the cap. If the cap is 0
// (disabled) it always returns true and skips the DB write entirely.
//
// Errors are treated as "allow" so a transient DB blip doesn't take down
// concert search. Callers should still log via the returned error.
func (l *Ledger) CheckAndIncrement(ctx context.Context, userID uuid.UUID, source Source) (bool, error) {
	c := l.Caps.Cap(source)
	if c <= 0 {
		return true, nil
	}
	// UPSERT and RETURNING gets us an atomic increment-and-read in one round trip.
	const q = `
INSERT INTO rate_ledger (user_id, source, day, count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (user_id, source, day) DO UPDATE SET count = rate_ledger.count + 1
RETURNING count
`
	day := time.Now().UTC().Format("2006-01-02")
	var newCount int
	if err := l.Pool.QueryRow(ctx, q, userID, string(source), day).Scan(&newCount); err != nil {
		return true, err // fail open
	}
	return newCount <= c, nil
}

// AllowFromContext is the callsite-friendly form: reads the user ID from ctx
// (put there by NewContext) and checks the ledger. If no user is in ctx
// (unit tests, one-off scripts) the request is allowed unconditionally.
func (l *Ledger) AllowFromContext(ctx context.Context, source Source) bool {
	userID, ok := UserFromContext(ctx)
	if !ok {
		return true
	}
	allowed, _ := l.CheckAndIncrement(ctx, userID, source)
	return allowed
}

type userIDKey struct{}

// NewContext returns a new context that carries the user ID. Called by the
// scan worker (and any other code that runs concerts.Search on behalf of a
// user) before invoking search functions that consult the ledger.
func NewContext(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// UserFromContext extracts the user ID previously stashed by NewContext.
func UserFromContext(ctx context.Context) (uuid.UUID, bool) {
	v := ctx.Value(userIDKey{})
	if v == nil {
		return uuid.Nil, false
	}
	id, ok := v.(uuid.UUID)
	return id, ok
}
