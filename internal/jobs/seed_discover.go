package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// SeedDiscoverMaxAttempts bounds river's retries. The seed is a backdrop
// nobody is waiting on and it runs again tomorrow, so a day that cannot be
// fetched is not worth two dozen attempts against a shared allowance.
const SeedDiscoverMaxAttempts = 2

// SeedDiscoverBudget bounds one run. Each city is at most
// ticketmaster.MaxEventPages sequential requests, and the whole job exists to
// be cheap: a run that has not finished in this long is not going to be
// improved by carrying on.
const SeedDiscoverBudget = 3 * time.Minute

// SeedDiscoverWorker fills concert_cache with city-wide Ticketmaster listings
// so the signed-out discover view has something to show.
//
// The problem it solves is a supply problem, not a code one. /api/discover is
// served entirely from concert_cache and can never call an upstream itself --
// that restriction is what makes an unauthenticated endpoint safe. But every
// other row in that table is written by a signed-in user's scan, filtered to
// that user's artists in that user's city, so the landing page shows nothing
// until real accounts have scanned real places. That failure is silent: an
// empty section renders as no section, which is also what a genuinely quiet
// week looks like.
//
// Two properties are load-bearing.
//
// The rows it writes are ordinary concert_cache rows under the tm: prefix, so
// FromCachedTicketmaster reads them with no special case and the janitor's
// 7-day prune expires them like any other. Nothing downstream knows a seeded
// row from a scanned one, which is the point -- webhttp.DiscoverCacheMaxAge is
// also 7 days, so a daily run keeps every city inside the window with six
// days to spare.
//
// And it charges the account ledger. The spend is real against Ticketmaster's
// 5000/day, it belongs to no user, and rate_ledger's user_id is a foreign key
// into users -- so rate.Ledger.ReserveAccount is where it can honestly land.
// Skipping it would leave RATE_CAP_TM_ACCOUNT_DAILY counting less than the
// account spends, and the overspend would reach signed-in users as upstream
// 403s that look exactly like artists with no shows.
type SeedDiscoverWorker struct {
	river.WorkerDefaults[SeedDiscoverArgs]
	Pool   *pgxpool.Pool
	TM     *ticketmaster.Client
	Ledger *rate.Ledger
	Cities []config.SeedCity

	// save writes one city's payload. Defaults to concert_cache; tests
	// replace it to run the whole job without a database, the same seam
	// webhttp.DiscoverHandler uses to read one without.
	save func(ctx context.Context, key string, blob []byte) error
}

func (w *SeedDiscoverWorker) writer() func(context.Context, string, []byte) error {
	if w.save != nil {
		return w.save
	}
	return func(ctx context.Context, key string, blob []byte) error {
		return db.SaveCachedConcerts(ctx, w.Pool, key, blob)
	}
}

// Timeout gives the job its own deadline. River's default is 60s, which a
// handful of cities paging through a busy market can exceed.
func (w *SeedDiscoverWorker) Timeout(*river.Job[SeedDiscoverArgs]) time.Duration {
	return SeedDiscoverBudget
}

func (w *SeedDiscoverWorker) Work(ctx context.Context, _ *river.Job[SeedDiscoverArgs]) error {
	if len(w.Cities) == 0 || w.TM == nil {
		// Configured off, or no Ticketmaster client in this deployment.
		// Neither is an error; both are worth one line, because the symptom
		// they produce is an empty landing page and no other signal.
		slog.Info("discover seed: nothing to do", "cities", len(w.Cities), "tm_configured", w.TM != nil)
		return nil
	}

	// One block for the whole run, sized at the worst case: every city
	// paging to the API's deep-paging limit. Reserving up front rather than
	// per city is what makes a partly-funded run stop cleanly instead of
	// getting halfway through each city.
	want := len(w.Cities) * ticketmaster.MaxEventPages
	res, err := w.Ledger.ReserveAccount(ctx, rate.SourceTicketmaster, want)
	if err != nil {
		// ReserveAccount grants nothing on a failed charge, on purpose: see
		// its comment. Log and stop rather than spending against a counter
		// nobody is keeping.
		slog.Warn("discover seed: account quota unavailable, skipping today", "err", err)
		return nil
	}
	defer func() { _ = res.Release(context.WithoutCancel(ctx)) }()

	var seeded, events int
	for _, city := range w.Cities {
		if ctx.Err() != nil {
			break
		}
		// Page 0 is charged here; every page after it is charged by the
		// permit below, one permit per request, the same accounting
		// loadOrFetchTM does for a scan.
		if !res.Take() {
			slog.Warn("discover seed: account quota exhausted mid-run",
				"seeded", seeded, "remaining", len(w.Cities)-seeded)
			break
		}
		n, err := w.seedCity(ctx, city, res)
		if err != nil {
			// One city's failure is not the others'. A market that 5xxs
			// today keeps whatever it already had until the janitor's
			// 7-day horizon, and tomorrow's run tries again.
			slog.Warn("discover seed: city failed", "city", city.Name, "err", err)
			continue
		}
		seeded++
		events += n
	}
	slog.Info("discover seed complete", "cities", seeded, "of", len(w.Cities), "events", events)
	return nil
}

// seedCity fetches and caches one city, returning how many events were
// written. A truncated fetch is not written at all.
func (w *SeedDiscoverWorker) seedCity(ctx context.Context, city config.SeedCity, res *rate.Reservation) (int, error) {
	loc := concerts.DiscoverSeedLocation(city.Latitude, city.Longitude)
	// The reservation is passed in rather than held on the worker: the worker
	// is one long-lived value shared by every run, and a block stored on it
	// would be yesterday's.
	morePages := func() bool { return res.Take() }

	evs, complete, err := w.TM.SearchEventsNear(ctx, loc.Latitude, loc.Longitude, loc.RadiusMiles, morePages)
	if err != nil {
		return 0, err
	}
	if !complete {
		// The same rule loadOrFetchTM follows, and for the same reason: a
		// truncated listing written here is served for the life of the row,
		// and no later run can correct it while the row is still being
		// answered from. Skipping the write self-heals tomorrow.
		slog.Info("discover seed: partial fetch not cached", "city", city.Name, "events", len(evs))
		return 0, nil
	}
	blob, err := json.Marshal(evs)
	if err != nil {
		return 0, err
	}
	if err := w.writer()(ctx, concerts.DiscoverSeedCacheKey(loc), blob); err != nil {
		return 0, err
	}
	return len(evs), nil
}
