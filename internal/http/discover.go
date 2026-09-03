package http

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
)

// Discover* bound the signed-out view. Every one of them is a ceiling on
// work done for a request nobody has authenticated.
const (
	// DiscoverMaxEvents caps the response. It is a teaser under a sign-in
	// button, not a listings site.
	DiscoverMaxEvents = 50

	// DiscoverDefaultRadius is used when ?radius= is absent, matching the
	// radius a new account starts with.
	DiscoverDefaultRadius = 50

	// DiscoverCacheMaxAge is how old a concert_cache row may be and still
	// feed this view. Deliberately far above CONCERT_CACHE_TTL_HOURS (12h):
	// that TTL decides when a *scan* refetches, and applying it here would
	// leave the login page empty every afternoon. Seven days matches the
	// janitor's prune horizon, so this reads exactly the rows that still
	// exist, and every event in them is still filtered against today.
	DiscoverCacheMaxAge = 7 * 24 * time.Hour

	// DiscoverMaxRows caps how many cached payloads one refresh decodes.
	DiscoverMaxRows = 2000

	// DiscoverLoadTimeout bounds one refresh of that set.
	DiscoverLoadTimeout = 5 * time.Second

	// DiscoverFailureBackoff is how long a failed load is left alone.
	DiscoverFailureBackoff = 30 * time.Second

	// DiscoverRefreshInterval is how long a decoded candidate set is reused.
	// The rows behind it are rewritten by scans running hours apart, so a
	// few minutes of staleness is invisible, while decoding thousands of
	// payloads per request on an unauthenticated endpoint is not.
	DiscoverRefreshInterval = 5 * time.Minute
)

// DiscoverHandler serves GET /api/discover — unauthenticated "popular shows
// near you", built entirely from concert_cache.
//
// The load-bearing property is what it cannot do. It never calls an upstream
// API and never touches the rate ledger: an unauthenticated endpoint that
// spends the account's Ticketmaster allowance is a quota drain with a URL,
// and the allowance is the thing that decides whether signed-in users get a
// complete feed. Everything here is a read of data some other user's scan
// already paid for.
//
// It is also not personalised and must never look like it is. The acts on
// these cards are Ticketmaster's lineups near a coordinate, so both clients
// label the section "popular shows near you" rather than anything implying
// we know who is asking.
type DiscoverHandler struct {
	Pool *pgxpool.Pool

	// blobs loads raw cached payloads. Defaults to concert_cache on first
	// use; tests replace it to run without a database.
	blobs func(context.Context) ([][]byte, error)
	// now is time.Now, overridden in tests.
	now func() time.Time

	mu       sync.Mutex
	loadedAt time.Time
	// retryNotBefore holds off another attempt after a failed load. Without
	// it a database outage turns every request into its own DiscoverLoadTimeout
	// wait, serialised behind the mutex — the section is optional, and it
	// must not become the slowest thing on the page while it is unavailable.
	retryNotBefore time.Time
	candidates     []concerts.Concert
}

type discoverResponse struct {
	Location concerts.Location `json:"location"`
	// Count and Events mirror /me/concerts so both clients reuse their
	// existing Event model and card. There is no facets block, no
	// computed_at and no refreshing flag: nothing here refreshes on demand.
	Count  int              `json:"count"`
	Events []concerts.Event `json:"events"`
}

func (h *DiscoverHandler) Get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil || !validCoords(lat, lng) {
		http.Error(w, "lat and lng are required and must name a real point", http.StatusBadRequest)
		return
	}
	radius := DiscoverDefaultRadius
	if v := q.Get("radius"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "radius must be a whole number of miles", http.StatusBadRequest)
			return
		}
		// Clamped rather than refused: this is a browser on a marketing page
		// with no way to show the user a validation message.
		radius = min(max(n, 1), 500)
	}
	loc := concerts.Location{Latitude: lat, Longitude: lng, RadiusMiles: radius}

	candidates, err := h.load(r.Context())
	if err != nil {
		slog.Warn("discover: cache read failed", "err", err)
		// An empty list, not a 500. The caller is a login page that renders
		// nothing when there is nothing; a 500 would turn a quiet cache into
		// an error state on the first screen a stranger sees.
		writeJSON(w, discoverResponse{Location: loc, Count: 0, Events: []concerts.Event{}})
		return
	}

	events := concerts.GroupEvents(concerts.Near(candidates, loc))
	if len(events) > DiscoverMaxEvents {
		events = events[:DiscoverMaxEvents]
	}
	writeJSON(w, discoverResponse{Location: loc, Count: len(events), Events: events})
}

// load returns the decoded candidate set, refreshing it at most once per
// DiscoverRefreshInterval.
//
// The refresh happens under the mutex on purpose. It serialises concurrent
// misses into one database read and one decode, which is what a cold cache
// plus a burst of traffic on the login page would otherwise multiply.
func (h *DiscoverHandler) load(ctx context.Context) ([]concerts.Concert, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.clock()
	if h.candidates != nil && now.Sub(h.loadedAt) < DiscoverRefreshInterval {
		return h.candidates, nil
	}
	if now.Before(h.retryNotBefore) {
		// A recent attempt failed. Serve whatever we still hold — possibly
		// nothing, which the caller renders as an empty section — rather
		// than retrying on every request.
		return h.candidates, nil
	}
	// Detached from the caller's context, with a deadline of its own: the
	// result is shared by every visitor, so one browser navigating away
	// mid-refresh must not cancel the read the next three are waiting on.
	// The deadline is what keeps that detachment from outliving the request
	// that started it.
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DiscoverLoadTimeout)
	defer cancel()
	blobs, err := h.source()(loadCtx)
	if err != nil {
		h.retryNotBefore = now.Add(DiscoverFailureBackoff)
		// Keep serving the previous set if there is one: a database blip
		// should cost freshness, not the whole section.
		if h.candidates != nil {
			return h.candidates, nil
		}
		return nil, err
	}
	h.retryNotBefore = time.Time{}
	// The past-show floor is applied here, at decode time, and again per
	// request would be redundant: the candidate set is at most
	// DiscoverRefreshInterval old, which cannot cross a day boundary
	// unnoticed for more than that.
	h.candidates = concerts.FromCachedTicketmaster(blobs, startOfUTCDay(now))
	h.loadedAt = now
	return h.candidates, nil
}

func (h *DiscoverHandler) source() func(context.Context) ([][]byte, error) {
	if h.blobs != nil {
		return h.blobs
	}
	return func(ctx context.Context) ([][]byte, error) {
		return db.ScanCachedConcerts(ctx, h.Pool, concerts.CachePrefixTicketmaster, DiscoverCacheMaxAge, DiscoverMaxRows)
	}
}

func (h *DiscoverHandler) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}
