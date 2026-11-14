package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// RefreshAffinityWorker recomputes one user's profile.
type RefreshAffinityWorker struct {
	river.WorkerDefaults[RefreshAffinityArgs]
	Pool     *pgxpool.Pool
	Affinity *affinity.Service
}

// AffinityMaxAttempts bounds river's retries for a profile refresh. See
// RefreshAffinityArgs.InsertOpts.
const AffinityMaxAttempts = 3

// AffinityBudget is the max wall-clock for one refresh job. River's default
// job timeout is 60s, which is the *same* number as affinity.ComputeTimeout —
// so the two raced, and river could cancel the job at the exact moment the
// compute was about to finish. This leaves room for the surrounding user load
// and profile write on either side of the compute.
const AffinityBudget = 90 * time.Second

func (w *RefreshAffinityWorker) Timeout(*river.Job[RefreshAffinityArgs]) time.Duration {
	return AffinityBudget
}

func (w *RefreshAffinityWorker) Work(ctx context.Context, job *river.Job[RefreshAffinityArgs]) error {
	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	// No Spotify identity, or one that was disconnected: there is no profile
	// to refresh and never will be until they sign in with Spotify again.
	// The fanout already skips both, so reaching this means a job enqueued
	// before a disconnect; erroring would only buy river's retries of a
	// refresh that cannot succeed.
	if !user.HasSpotifyCredential() {
		slog.Info("affinity refresh skipped: no spotify connection", "user_id", user.ID)
		return nil
	}
	if _, err := w.Affinity.Compute(ctx, affinity.User{ID: user.ID, SpotifyUserID: user.SpotifyUserID}); err != nil {
		if errors.Is(err, auth.ErrSpotifyDisconnected) {
			slog.Info("affinity refresh skipped: spotify disconnected", "user_id", user.ID)
			return nil
		}
		return err
	}
	slog.Info("refreshed affinity", "user_id", user.ID)
	return nil
}

// ScanConcertsWorker runs the full concert scan for one user + location and
// writes the merged result to user_concert_snapshots. Bounded by a generous
// per-job timeout since it includes rate-limited MB + Nominatim work.
type ScanConcertsWorker struct {
	river.WorkerDefaults[ScanConcertsArgs]
	Pool     *pgxpool.Pool
	Affinity *affinity.Service
	// Deps is a factory that returns a fresh concerts.SearchDeps for the job.
	// Kept as a closure so the worker doesn't have to know how to construct
	// TM/BIT clients or the fallback chain.
	Deps func() concerts.SearchDeps
	// MinFallbackScore mirrors the HTTP handler's threshold.
	MinFallbackScore float64
	// River lets this worker enqueue follow-up jobs (currently the
	// instant-notify email fires here). Nil = feature disabled.
	River *river.Client[pgx.Tx]
	// Ledger issues the per-user daily quota reservations this scan spends
	// (design §8.3). Nil = no enforcement.
	Ledger *rate.Ledger
}

// ScanMaxAttempts bounds river's retries for a scan. River's default is 25,
// which for a permanently-failing scan (revoked Spotify grant, say) means
// two dozen full fan-out attempts. Three is enough to ride out a transient
// upstream problem without turning a broken account into sustained load.
const ScanMaxAttempts = 3

func (w *ScanConcertsWorker) NextRetry(job *river.Job[ScanConcertsArgs]) time.Time {
	// Linear-ish backoff: 1min, 5min. Scans are expensive; retrying one in
	// a few seconds mostly re-runs work that hasn't had time to change.
	delay := time.Duration(job.Attempt) * 4 * time.Minute
	if delay < time.Minute {
		delay = time.Minute
	}
	return time.Now().Add(delay)
}

// ScanBudget is the max time one scan job may run. Includes serialized MB
// (~2s per unresolved artist) and Nominatim (~1s per unique city) work, so
// generous. River's default per-job timeout is 60s which is far too tight;
// Timeout() below overrides it and Work() applies the same bound internally.
const ScanBudget = 5 * time.Minute

// Timeout tells river how long to wait before considering this job stuck.
// Overrides river's 60s default.
func (w *ScanConcertsWorker) Timeout(*river.Job[ScanConcertsArgs]) time.Duration {
	return ScanBudget
}

func (w *ScanConcertsWorker) Work(ctx context.Context, job *river.Job[ScanConcertsArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, ScanBudget)
	defer cancel()

	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	genrePicks, err := db.ListGenrePicks(ctx, w.Pool, user.ID)
	if err != nil {
		return fmt.Errorf("load genre picks: %w", err)
	}
	genres := genreTargets(genrePicks)
	targets, err := w.targetsFor(ctx, user, len(genres) > 0)
	if err != nil {
		return err
	}
	if len(targets) == 0 && len(genres) == 0 {
		// No Spotify identity and no picks of either kind: there is nothing
		// to search for, and no snapshot is written, because an empty
		// complete snapshot would read as "your area is quiet" for the full
		// staleness window. The SWR
		// handler answers needs_picks for this user and does not enqueue, so
		// this is only reachable from a job queued before the picks went.
		slog.Info("scan_concerts: nothing to scan", "user", user.ID)
		return nil
	}

	// Take out this scan's quota up front (design §8.3): one upsert per
	// source instead of one per call, sized to the workload. Whatever we
	// don't spend goes back at the end. Release uses a detached context so
	// an expired scan budget can't leave the block charged.
	reservations := w.Ledger.ReserveAll(ctx, user.ID, scanQuotaFor(targets, len(genres)))
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer releaseCancel()
		reservations.Release(releaseCtx)
	}()
	ctx = rate.NewContext(ctx, reservations)

	loc := concerts.Location{
		Latitude:    job.Args.Latitude,
		Longitude:   job.Args.Longitude,
		RadiusMiles: job.Args.RadiusMiles,
	}
	deps := w.Deps()
	deps.MinFallbackScore = w.MinFallbackScore

	// Genres first: at most ticketmaster.MaxGenrePages calls each, usually
	// none (a listing is shared by everyone in the grid cell), so they are
	// never the thing a 200-artist profile starves of quota.
	genreFound, genreHorizon, genreErr := concerts.SearchGenres(ctx, deps, genres, loc)

	// A non-nil error here still carries usable partial results; see
	// concerts.IncompleteError.
	found, searchErr := concerts.Search(ctx, deps, targets, loc)
	found, searchErr = mergeGenreResults(found, searchErr, genreFound, genreErr)
	var incomplete *concerts.IncompleteError
	switch {
	case searchErr == nil:
	case errors.As(searchErr, &incomplete):
		// Per-source quota accounting goes in this line, not just the
		// rate_capped boolean. An incomplete scan writes complete = false,
		// which the SWR handler treats as permanently stale, so this is the
		// log someone reads when a feed will not fill — and on its own
		// "rate_capped: true" names no source, no numbers, and no reason.
		// Diagnosing it otherwise means reading the ledger table afterwards
		// and reconstructing arithmetic across concurrent scans and refunds,
		// against a reservation that has already been released.
		//
		// Read before Release (which runs in the deferred func above and
		// zeroes the counters), so these are the values as spent.
		attrs := []any{
			"user", user.ID,
			"skipped_artists", incomplete.SkippedArtists,
			"total_artists", incomplete.TotalArtists,
			"rate_capped", incomplete.RateCapped,
			"found", len(found),
		}
		for _, s := range reservations.Stats() {
			// granted < wanted means the day's cap was already partly spent
			// when this scan started. denied > 0 means a call was actually
			// turned away. They are different failures with different fixes —
			// raise the cap versus stop scanning so often — and the old line
			// could not tell them apart.
			attrs = append(attrs, string(s.Source), fmt.Sprintf(
				"granted=%d/%d used=%d denied=%d", s.Granted, s.Wanted, s.Used, s.Denied,
			))
		}
		slog.Warn("scan_concerts incomplete", attrs...)
	default:
		return searchErr
	}

	// A quota-capped scan can't do better until the ledger's UTC day turns
	// over, so record when it's worth trying again. Any other flavor of
	// incompleteness (budget overrun) leaves this nil and retries freely.
	var retryAfter *time.Time
	if incomplete != nil && incomplete.RateCapped {
		t := nextQuotaReset(time.Now())
		retryAfter = &t
	}
	// Persist partial results too — a half-filled page beats an empty one —
	// but record that they're partial so the SWR handler keeps treating the
	// snapshot as stale instead of sitting on it for the full window.
	if err := persistScanResults(ctx, w.Pool, user.ID, loc, found, incomplete == nil, retryAfter, genreHorizon); err != nil {
		return err
	}
	slog.Info("scan_concerts done",
		"user", user.ID,
		"location", LocationKey(loc),
		"concerts", len(found),
		"complete", incomplete == nil,
		"retry_after", retryAfter,
	)

	// Notification hooks. Both fire only when the user opted into that
	// channel AND is subscribed to at least one artist that had a net-new
	// concert. Errors are logged but don't fail the scan.
	//
	// The two are independent on purpose: opting into push does not imply
	// email, the ledger channels are separate (migration 0016), and a user
	// opted into both must receive both.
	if w.River != nil {
		// Detached for the same reason the writes above are: this runs after
		// up to a full ScanBudget of searching, and an enqueue refused because
		// the scan's own context expired loses the notification silently —
		// nothing else ever revisits this snapshot's net-new set.
		notifyCtx, cancelNotify := detached(ctx, persistBudget)
		w.enqueueNotifications(notifyCtx, user, found)
		cancelNotify()
	}

	// Surface incompleteness to river so the job retries (bounded by
	// ScanMaxAttempts) rather than reporting success on a truncated scan.
	// The snapshot above is already durable, so a retry only adds coverage.
	//
	// Exception: a purely quota-capped scan. Every retry before the UTC day
	// rolls over is guaranteed to be capped identically, so retrying just
	// burns worker slots to reproduce the same result. The snapshot carries
	// complete=false and a retry_after, which is the durable record that
	// work remains — river doesn't need to hold the job open too.
	if incomplete != nil && !isOnlyRateCapped(incomplete) {
		return searchErr
	}
	return nil
}

// PersistScanResults writes one scan's results: each concert into the shared
// `concerts` table, and the user's snapshot of dedup_keys pointing at them.
//
// Normalized snapshots (migration 0012) mean storage is shared across users —
// two users following the same Taylor Swift show write one row, not two.
// Which is exactly why the row may hold nothing about *this* user's artist:
// it is last-writer-wins across everyone sharing the key. The artist's ID and
// genres go on this user's snapshot instead (CF-28; see
// concerts.ApplyArtistRefs), and EncodeSharedRow is what keeps them out of
// the row.
//
// Exported so a test can drive the real write path for two users colliding
// on one dedup_key without standing up the Spotify and Ticketmaster halves
// of a scan.
func PersistScanResults(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, loc concerts.Location, found []concerts.Concert, complete bool, retryAfter *time.Time) error {
	return persistScanResults(ctx, pool, userID, loc, found, complete, retryAfter, "")
}

func persistScanResults(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, loc concerts.Location, found []concerts.Concert, complete bool, retryAfter *time.Time, genreHorizon string) error {
	dedupKeys := make([]string, len(found))
	rows := make([]db.ConcertRow, len(found))
	for i, c := range found {
		body, err := concerts.EncodeSharedRow(c)
		if err != nil {
			return err
		}
		rows[i] = db.ConcertRow{DedupKey: c.DedupKey, Data: body, EventDate: c.Date}
		dedupKeys[i] = c.DedupKey
	}
	artistRefs, err := json.Marshal(concerts.RefsOf(found))
	if err != nil {
		return err
	}
	return persistScan(ctx, poolStore{pool: pool}, rows, db.ConcertSnapshot{
		UserID:       userID,
		LocationKey:  LocationKey(loc),
		DedupKeys:    dedupKeys,
		ComputedAt:   time.Now(),
		Complete:     complete,
		RetryAfter:   retryAfter,
		ArtistRefs:   artistRefs,
		GenreHorizon: genreHorizon,
	})
}

// mergeGenreResults folds a scan's genre listings into its artist results.
//
// Artist results go in first, so where a show is both -- a picked or profile
// artist playing a night that is also in a picked genre -- the identity the
// snapshot records is the artist's own (a Spotify ID and the profile's
// genres, or the pick's), and the genre listing only contributes links. The
// two incompleteness reports are combined rather than either winning, since
// either alone is reason to keep the snapshot stale.
func mergeGenreResults(found []concerts.Concert, searchErr error, genreFound []concerts.Concert, genreErr error) ([]concerts.Concert, error) {
	var gi *concerts.IncompleteError
	if genreErr != nil && !errors.As(genreErr, &gi) {
		gi = &concerts.IncompleteError{Cause: genreErr}
	}
	if len(genreFound) > 0 {
		m := concerts.NewMerger()
		for _, c := range found {
			m.Add(c)
		}
		for _, c := range genreFound {
			m.Add(c)
		}
		found = m.All()
	}
	if gi == nil {
		return found, searchErr
	}
	var si *concerts.IncompleteError
	switch {
	case searchErr == nil:
		return found, gi
	case errors.As(searchErr, &si):
		merged := *si
		merged.SkippedArtists += gi.SkippedArtists
		merged.TotalArtists += gi.TotalArtists
		merged.RateCapped = merged.RateCapped || gi.RateCapped
		if merged.Cause == nil {
			merged.Cause = gi.Cause
		}
		return found, &merged
	default:
		return found, searchErr
	}
}

// genreTargets turns genre picks into the genre listings a scan fetches.
func genreTargets(picks []db.GenrePick) []concerts.GenreTarget {
	out := make([]concerts.GenreTarget, 0, len(picks))
	for _, p := range picks {
		out = append(out, concerts.GenreTarget{ID: p.GenreID, Name: p.Name})
	}
	return out
}

// persistBudget is how long the writes that follow a scan get. They run on a
// context detached from the scan's, so this is also the bound that stops a
// hung write holding a river worker slot open indefinitely.
const persistBudget = 15 * time.Second

// detached returns a context for work that must survive the scan budget
// running out. Same shape as the reservation Release above, and for the same
// reason: by the time these run, ctx may already be done.
func detached(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), d)
}

// concertStore is the slice of the database the persist step touches. Narrow
// on purpose: it is the seam a test uses to check that an expired scan context
// still writes, which is the whole point of the step existing separately.
type concertStore interface {
	UpsertConcerts(ctx context.Context, rows []db.ConcertRow) error
	UpsertConcertSnapshot(ctx context.Context, snap db.ConcertSnapshot) error
}

// poolStore is the production concertStore — the package-level db helpers
// bound to the worker's pool.
type poolStore struct{ pool *pgxpool.Pool }

func (s poolStore) UpsertConcerts(ctx context.Context, rows []db.ConcertRow) error {
	return db.UpsertConcerts(ctx, s.pool, rows)
}

func (s poolStore) UpsertConcertSnapshot(ctx context.Context, snap db.ConcertSnapshot) error {
	return db.UpsertConcertSnapshot(ctx, s.pool, snap)
}

// persistScan writes a finished scan's results on a context detached from the
// scan's own.
//
// ctx here is the ScanBudget-bounded context, and the scan this most matters
// for is the one that used all of it: a cold 200-artist profile is exactly the
// result worth keeping, and it arrives at this point with ctx already expired.
// Writing on it failed both statements and returned the error to river, which
// retried the entire fan-out — spending the day's quota again to recompute a
// snapshot that had already been assembled and thrown away.
func persistScan(ctx context.Context, store concertStore, rows []db.ConcertRow, snap db.ConcertSnapshot) error {
	persistCtx, cancel := detached(ctx, persistBudget)
	defer cancel()
	if err := store.UpsertConcerts(persistCtx, rows); err != nil {
		return err
	}
	return store.UpsertConcertSnapshot(persistCtx, snap)
}

// isOnlyRateCapped reports whether quota exhaustion was the sole reason a
// scan came up short. If artists were also skipped outright, time alone
// won't fix it and the job should still retry.
func isOnlyRateCapped(e *concerts.IncompleteError) bool {
	return e.RateCapped && e.SkippedArtists == 0
}

// nextQuotaReset returns the instant the per-user daily ledger rolls to a
// new day. Mirrors internal/rate's UTC day bucket — if that ever changes,
// this has to move with it.
func nextQuotaReset(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

// scanQuotaFor sizes the per-source reservation for a scan. A profile artist
// can cost a resolution call plus an events call, so it reserves two; a pick
// already holds its attraction ID and costs only the events call, so it
// reserves one (docs/public-path.md §2.1). Reserving two for a pick would not
// overspend -- the block is handed back -- but it would hold twice the pick's
// cost against the user's daily cap and the shared account ceiling for the
// length of the scan, which is exactly when other users' scans are drawing on
// it. The reservation hands back whatever cache hits mean we never spend.
//
// A genre listing reserves ticketmaster.MaxGenrePages, its worst case on a
// cache miss; most scans in a shared cell spend none and hand it back.
func scanQuotaFor(targets []concerts.Target, genres int) int {
	n := genres * ticketmaster.MaxGenrePages
	for _, t := range targets {
		if t.AttractionID != "" {
			n++
		} else {
			n += 2
		}
	}
	return n
}

// targetsFor builds the list a scan searches: the user's picks first, then
// their Spotify affinity profile, to spotify.MaxScoredArtists in all
// (docs/public-path.md §3.5, §5).
//
// An account with no Spotify identity never reaches the affinity service at
// all. That is what lets a picks-only scan run with no Spotify client wired
// in, and it is the property the CF-30 test pins: such a user's scan requests
// no Spotify URL.
//
// A Spotify user whose connection was revoked (DELETE /me/spotify-connection)
// keeps their picks, so a disconnected user with picks gets a picks-only scan
// instead of a failed one. Any other affinity error still fails the scan: a
// transient Spotify failure must not quietly persist a complete snapshot
// missing every profile artist, which would then be trusted for the full
// staleness window.
//
// hasGenres counts as having picks for that last rule: a disconnected Spotify
// user with genre picks still has a feed to build.
func (w *ScanConcertsWorker) targetsFor(ctx context.Context, user db.User, hasGenres bool) ([]concerts.Target, error) {
	picks, err := db.ListArtistPicks(ctx, w.Pool, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load picks: %w", err)
	}
	var profile []spotify.ScoredArtist
	if user.SpotifyUserID != "" && w.Affinity != nil {
		artists, _, _, err := w.Affinity.LoadOrCompute(ctx, affinity.User{ID: user.ID, SpotifyUserID: user.SpotifyUserID})
		switch {
		case err == nil:
			profile = artists
		case errors.Is(err, auth.ErrSpotifyDisconnected) && (len(picks) > 0 || hasGenres):
			slog.Info("scan_concerts: spotify disconnected, scanning picks only", "user", user.ID, "picks", len(picks))
		default:
			return nil, err
		}
	}
	return mergeTargets(pickTargets(picks), profileTargets(profile), spotify.MaxScoredArtists), nil
}

// pickTargets turns hand picks into concert-search targets. Each carries its
// attraction ID, which skips artist_resolutions and name resolution in
// concerts.Search and keeps it out of the fallback chain, and a "tm:" ID,
// which is the identity the viewer's snapshot records and their bell
// subscribes to. The genre goes on the Target because tmEventToConcert copies
// Target.Genres onto every Concert it produces, and from there into this
// user's artist refs (CF-28) -- never into the shared concerts row.
func pickTargets(picks []db.ArtistPick) []concerts.Target {
	out := make([]concerts.Target, 0, len(picks))
	for _, p := range picks {
		t := concerts.Target{
			ID:           PickArtistID(p.AttractionID),
			Name:         p.Name,
			AttractionID: p.AttractionID,
		}
		if p.Genre != "" {
			t.Genres = []string{p.Genre}
		}
		out = append(out, t)
	}
	return out
}

// PickArtistID is the viewer-facing artist ID of a pick. Exported because the
// feed's reason line and the subscribe control must spell it identically.
func PickArtistID(attractionID string) string { return concerts.TicketmasterArtistID(attractionID) }

// mergeTargets puts picks first and fills from the profile up to limit,
// dropping a profile artist whose name matches a pick: the pick already
// searches that artist by ID for one call, where the profile entry would
// spend up to two on the same shows.
func mergeTargets(picks, profile []concerts.Target, limit int) []concerts.Target {
	out := make([]concerts.Target, 0, min(len(picks)+len(profile), limit))
	picked := make(map[string]struct{}, len(picks))
	for _, p := range picks {
		if len(out) == limit {
			return out
		}
		picked[concerts.Normalize(p.Name)] = struct{}{}
		out = append(out, p)
	}
	for _, a := range profile {
		if len(out) == limit {
			break
		}
		if _, dup := picked[concerts.Normalize(a.Name)]; dup {
			continue
		}
		out = append(out, a)
	}
	return out
}

// enqueueNotifications picks the subset of just-computed concerts that
// (a) are future-dated and (b) belong to a subscribed artist, then enqueues
// one job per opted-in channel carrying whatever that channel has not
// already sent.
//
// The candidate set is computed once and the *ledger filter* is per channel.
// Doing it the other way around — filtering once and fanning out — is the
// silent-suppression bug migration 0016 documents: whichever channel ran
// first would consume the other's candidates, and a user opted into both
// would receive exactly one, with no error anywhere.
func (w *ScanConcertsWorker) enqueueNotifications(ctx context.Context, user db.User, found []concerts.Concert) {
	wantsEmail := user.InstantNotifyOptIn && user.Email != ""
	wantsPush := user.PushOptIn
	if !wantsEmail && !wantsPush {
		return
	}
	subs, err := db.GetSubscribedArtistIDs(ctx, w.Pool, user.ID)
	if err != nil || len(subs) == 0 {
		return
	}
	now := time.Now()
	candidates := make([]string, 0)
	for _, c := range found {
		if !c.Date.After(now) {
			continue
		}
		if _, ok := subs[c.Artist.ID]; !ok {
			continue
		}
		candidates = append(candidates, c.DedupKey)
	}
	if len(candidates) == 0 {
		return
	}
	if wantsEmail {
		if keys := w.unsentFor(ctx, user, db.ChannelEmail, candidates); len(keys) > 0 {
			if _, err := w.River.Insert(ctx, SendInstantNotifyArgs{UserID: user.ID, DedupKeys: keys}, nil); err != nil {
				slog.Warn("instant notify: enqueue failed", "err", err, "user", user.ID)
			}
		}
	}
	if wantsPush {
		if keys := w.unsentFor(ctx, user, db.ChannelPush, candidates); len(keys) > 0 {
			if _, err := w.River.Insert(ctx, SendPushArgs{UserID: user.ID, DedupKeys: keys}, nil); err != nil {
				slog.Warn("push: enqueue failed", "err", err, "user", user.ID)
			}
		}
	}
}

// unsentFor returns the candidates this channel has not already delivered,
// in the order they were given.
func (w *ScanConcertsWorker) unsentFor(ctx context.Context, user db.User, ch db.Channel, candidates []string) []string {
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, ch, candidates)
	if err != nil {
		slog.Warn("notify: filter unsent failed", "err", err, "user", user.ID, "channel", string(ch))
		return nil
	}
	out := make([]string, 0, len(unsent))
	for _, k := range candidates {
		if _, ok := unsent[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// LocationKey produces the canonical string used as user_concert_snapshots.location_key
// and as part of concert_cache keys. Kept in one place so writers and readers
// stay aligned.
func LocationKey(loc concerts.Location) string {
	return strconv.FormatFloat(loc.Latitude, 'f', 4, 64) + "," +
		strconv.FormatFloat(loc.Longitude, 'f', 4, 64) + "," +
		strconv.Itoa(loc.RadiusMiles)
}

// FanoutAffinityRefreshWorker enqueues a per-user job for every user with a
// non-expired browser session in the last 14 days. This is a proxy for
// "active enough to be worth refreshing" — inactive users don't burn Spotify
// quota. Tune as usage patterns emerge.
type FanoutAffinityRefreshWorker struct {
	river.WorkerDefaults[FanoutAffinityRefreshArgs]
	Pool   *pgxpool.Pool
	Client *river.Client[pgx.Tx]
}

func (w *FanoutAffinityRefreshWorker) Work(ctx context.Context, _ *river.Job[FanoutAffinityRefreshArgs]) error {
	// Only accounts with a live Spotify connection. A picks-only account has
	// no profile to refresh, and a disconnected one has revoked the grant;
	// enqueueing either produced one failing job per user per night, from
	// auth.ErrSpotifyDisconnected (CF-32). The predicate is
	// db.User.HasSpotifyCredential's, spelled in SQL.
	const q = `
SELECT DISTINCT s.user_id FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.last_seen_at > now() - interval '14 days'
  AND u.spotify_user_id IS NOT NULL
  AND length(u.encrypted_refresh_token) > 0
`
	rows, err := w.Pool.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	slog.Info("fanout: refreshing users", "count", len(ids))
	for _, id := range ids {
		if _, err := w.Client.Insert(ctx, RefreshAffinityArgs{UserID: id}, nil); err != nil {
			slog.Warn("fanout enqueue failed", "user", id, "err", err)
		}
	}
	return nil
}

// SendDigestWorker composes and sends one user's daily digest. Diffs the
// current snapshot's dedup_keys against a small "seen" table (represented
// here by digest_last_sent_at as a time cutoff) and emails whatever is net-new.
type SendDigestWorker struct {
	river.WorkerDefaults[SendDigestArgs]
	Pool             *pgxpool.Pool
	Sender           MailSender
	UnsubscribeBase  string // e.g. https://your-domain.com — the /api/me/unsubscribe path is appended
	UnsubscribeToken func(userID uuid.UUID) string
	// Fallback is the server-wide default location, used when the user never
	// saved one. Must match what FanoutScanConcertsWorker uses or the digest
	// looks up a location_key no scan ever wrote.
	Fallback FallbackLocation
}

func (w *SendDigestWorker) Work(ctx context.Context, job *river.Job[SendDigestArgs]) error {
	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	if user.Email == "" || !user.DigestOptIn {
		return nil // opted out or no email — silent no-op
	}
	// Load the user's current snapshot (their canonical location). Digest
	// covers the location the user actually browses; multi-location users
	// get one digest for their most-recently-set location.
	//
	// A user with no saved location falls back to the server-wide default —
	// the same thing FanoutScanConcertsWorker does when it enqueues their
	// nightly scan. Returning early here instead meant an opted-in user who
	// never set a location was scanned every night and never emailed, with
	// nothing logged to say why.
	target := concerts.Location{
		Latitude:    w.Fallback.Latitude,
		Longitude:   w.Fallback.Longitude,
		RadiusMiles: w.Fallback.RadiusMiles,
	}
	loc, hit, err := db.GetUserLocation(ctx, w.Pool, user.ID)
	if err != nil {
		return err
	}
	if hit {
		target = concerts.Location{Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusMiles: loc.RadiusMiles}
	} else {
		slog.Info("digest: user has no saved location, using server default", "user", user.ID)
	}
	locKey := LocationKey(target)
	snap, hit, err := db.GetConcertSnapshot(ctx, w.Pool, user.ID, locKey)
	if err != nil || !hit {
		return nil
	}
	// Post-normalization: snapshot holds dedup_keys; join against the
	// shared concerts table to get full bodies.
	blobs, err := db.GetConcertsByDedupKeys(ctx, w.Pool, snap.DedupKeys)
	if err != nil {
		return err
	}
	all, err := concerts.AssembleByKey(snap.DedupKeys, blobs)
	if err != nil {
		return err
	}
	refs, err := concerts.DecodeArtistRefs(snap.ArtistRefs)
	if err != nil {
		return err
	}
	concerts.ApplyArtistRefs(all, refs)
	// First-ever digest caps at 60 days out so we don't dump an entire tour
	// year into one email. Detected by an exact count of prior sends: the
	// old "every candidate happens to be unsent" heuristic also fired for
	// an established user who had just moved cities, silently withholding
	// everything past the horizon from them.
	priorSends, err := db.CountDigestSent(ctx, w.Pool, user.ID, db.ChannelEmail)
	if err != nil {
		return err
	}
	firstEver := priorSends == 0

	// Build the candidate set: future-dated concerts only.
	now := time.Now()
	horizon := now.Add(60 * 24 * time.Hour)
	candidateKeys := make([]string, 0, len(all))
	byKey := make(map[string]concerts.Concert, len(all))
	for _, c := range all {
		if !c.Date.After(now) {
			continue
		}
		candidateKeys = append(candidateKeys, c.DedupKey)
		byKey[c.DedupKey] = c
	}
	// Exact "net new" — filter out anything we've already emailed this user.
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, db.ChannelEmail, candidateKeys)
	if err != nil {
		return err
	}
	fresh := make([]concerts.Concert, 0, len(unsent))
	sendKeys := make([]string, 0, len(unsent))
	for _, k := range candidateKeys { // preserve snapshot order
		if _, ok := unsent[k]; !ok {
			continue
		}
		c := byKey[k]
		if firstEver && c.Date.After(horizon) {
			continue
		}
		fresh = append(fresh, c)
		sendKeys = append(sendKeys, k)
	}
	if len(fresh) == 0 {
		slog.Info("digest: nothing new", "user", user.ID)
		return nil
	}
	// Cap the listing (CF-31): a genre pick in a dense city is hundreds of
	// shows. Only what is listed goes into sendKeys, so the held-back shows
	// are still unsent tomorrow and lead that digest; the email links to the
	// feed for them meanwhile.
	fresh, moreEvents := email.CapDigest(fresh, email.DigestMaxEvents)
	sendKeys = sendKeys[:0]
	for _, c := range fresh {
		sendKeys = append(sendKeys, c.DedupKey)
	}
	unsub := w.UnsubscribeBase + "/api/unsubscribe?token=" + w.UnsubscribeToken(user.ID)
	more := email.DigestMore{Events: moreEvents, URL: w.UnsubscribeBase + "/"}
	msg := email.RenderDigest(user.DisplayName, user.Email, fresh, unsub, more)
	// Send first, record only what landed. See sendAndRecord for why this
	// order, and why the earlier one turned a rejected recipient into shows
	// the user is never told about.
	if err := sendAndRecord(ctx, w.Sender, msg, sendKeys, func(ctx context.Context, keys []string) error {
		return db.RecordDigestSent(ctx, w.Pool, user.ID, db.ChannelEmail, keys)
	}); err != nil {
		slog.Warn("digest: send failed; sent-set left unrecorded so the retry re-sends",
			"err", err, "user", user.ID, "concerts", len(sendKeys))
		return err
	}
	// Both counts: the sent-set is per (artist, show) but the email reads one
	// entry per event, so a lone "concerts" number no longer matches what the
	// recipient sees.
	slog.Info("digest sent", "user", user.ID, "concerts", len(fresh), "events", concerts.CountEventKeys(fresh), "held_back_events", moreEvents)
	return nil
}

// SendInstantNotifyWorker delivers one immediate-notification email covering
// a specific set of dedup_keys. The scan worker enqueues this after each
// successful snapshot when the user has instant_notify_opt_in AND at least
// one net-new concert is by a subscribed artist. Idempotent-ish: we filter
// out anything already in user_digest_sent, so a river retry won't re-send.
type SendInstantNotifyWorker struct {
	river.WorkerDefaults[SendInstantNotifyArgs]
	Pool             *pgxpool.Pool
	Sender           MailSender
	UnsubscribeBase  string
	UnsubscribeToken func(userID uuid.UUID) string
}

func (w *SendInstantNotifyWorker) Work(ctx context.Context, job *river.Job[SendInstantNotifyArgs]) error {
	if len(job.Args.DedupKeys) == 0 {
		return nil
	}
	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	if user.Email == "" || !user.InstantNotifyOptIn {
		return nil // opted out between enqueue and run
	}
	// Filter out anything already sent (idempotent under retry).
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, db.ChannelEmail, job.Args.DedupKeys)
	if err != nil {
		return err
	}
	if len(unsent) == 0 {
		return nil
	}
	// Look up the concerts. Only the unsent keys need loading — no reason
	// to pull the whole snapshot when we already know exactly which
	// dedup_keys belong in this email. (Pre-normalization this fetched the
	// snapshot then filtered client-side; the concerts table lookup is
	// now the direct path.)
	// Preserve the enqueued order rather than ranging over the map: Go
	// randomizes map iteration, and AssembleByKey preserves whatever order
	// it's handed, so this used to produce emails listing shows at random.
	keys := make([]string, 0, len(unsent))
	for _, k := range job.Args.DedupKeys {
		if _, ok := unsent[k]; ok {
			keys = append(keys, k)
		}
	}
	blobs, err := db.GetConcertsByDedupKeys(ctx, w.Pool, keys)
	if err != nil {
		return err
	}
	fresh, err := concerts.AssembleByKey(keys, blobs)
	if err != nil {
		return err
	}
	if err := applyUserArtistRefs(ctx, w.Pool, user.ID, keys, fresh); err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	// Soonest show first — the digest groups by month for the same reason.
	sort.Slice(fresh, func(i, j int) bool {
		if !fresh[i].Date.Equal(fresh[j].Date) {
			return fresh[i].Date.Before(fresh[j].Date)
		}
		return fresh[i].Artist.Name < fresh[j].Artist.Name
	})
	sendKeys := make([]string, 0, len(fresh))
	for _, c := range fresh {
		sendKeys = append(sendKeys, c.DedupKey)
	}
	unsub := w.UnsubscribeBase + "/api/unsubscribe?token=" + w.UnsubscribeToken(user.ID)
	msg := email.RenderInstantNotify(user.DisplayName, user.Email, fresh, unsub)
	// Same ordering as the daily digest, through the same helper. These two
	// are one channel with two triggers and must not drift: a fix applied to
	// one of them and not the other is the same bug still shipping.
	if err := sendAndRecord(ctx, w.Sender, msg, sendKeys, func(ctx context.Context, keys []string) error {
		return db.RecordDigestSent(ctx, w.Pool, user.ID, db.ChannelEmail, keys)
	}); err != nil {
		slog.Warn("instant notify: send failed; sent-set left unrecorded so the retry re-sends",
			"err", err, "user", user.ID, "concerts", len(sendKeys))
		return err
	}
	slog.Info("instant notify sent", "user", user.ID, "concerts", len(fresh), "events", concerts.CountEventKeys(fresh))
	return nil
}

// JanitorWorker prunes stale rows across the app's data tables. Runs daily.
// Failures for one prune step don't block the others — best-effort cleanup.
type JanitorWorker struct {
	river.WorkerDefaults[JanitorArgs]
	Pool *pgxpool.Pool
}

func (w *JanitorWorker) Work(ctx context.Context, _ *river.Job[JanitorArgs]) error {
	type step struct {
		name string
		run  func(context.Context) (int64, error)
	}
	steps := []step{
		{"rate_ledger", func(c context.Context) (int64, error) { return db.PruneRateLedger(c, w.Pool, 90) }},
		{"rate_ledger_account", func(c context.Context) (int64, error) { return db.PruneRateLedgerAccount(c, w.Pool, 90) }},
		// Daily location slots. They only mean anything on the day they were
		// claimed, but nothing else removes them and one lands per location
		// per user per day.
		{"location_visits", func(c context.Context) (int64, error) { return db.PruneLocationVisits(c, w.Pool, 30) }},
		{"concert_cache", func(c context.Context) (int64, error) { return db.PruneConcertCache(c, w.Pool, 7) }},
		{"past_concerts", func(c context.Context) (int64, error) { return db.PrunePastConcerts(c, w.Pool, 7) }},
		{"oauth_handshakes", func(c context.Context) (int64, error) { return db.PruneExpiredHandshakes(c, w.Pool) }},
		// Single-use, so most rows vanish on redemption; these are the
		// abandoned logins.
		{"mobile_auth_codes", func(c context.Context) (int64, error) { return db.PruneExpiredMobileAuthCodes(c, w.Pool) }},
		// Email sign-in mail (CF-33). Kept two days past sending, not past
		// expiry, because the per-address and daily limits count these rows.
		{"email_login_tokens", func(c context.Context) (int64, error) { return db.PruneEmailLoginTokens(c, w.Pool) }},
		// Tokens APNs retired as permanently dead. The 30-day delay lets a
		// device that reinstalls re-register onto its existing row instead
		// of racing a delete.
		{"disabled_devices", func(c context.Context) (int64, error) { return db.PruneDisabledDevices(c, w.Pool, 30) }},
		// Sessions were the one table with an expiry and no reaper. Both
		// nightly fanout workers scan this table, so dead rows cost every
		// night, forever.
		{"expired_sessions", func(c context.Context) (int64, error) { return db.PruneExpiredSessions(c, w.Pool) }},
		{"stale_snapshots", w.pruneStaleSnapshots},
		{"old_digest_sent", func(c context.Context) (int64, error) { return db.PruneOldDigestSent(c, w.Pool, 180) }},
		// The two resolver caches only ever grew. Their readers already
		// ignore expired negatives, so this is about the rows, not
		// correctness — but they accumulate one per artist and one per city
		// we ever failed to resolve, forever. Positives are kept on purpose.
		{"expired_mb_negatives", func(c context.Context) (int64, error) { return db.PruneExpiredNegativeMBURLs(c, w.Pool) }},
		{"expired_geo_negatives", func(c context.Context) (int64, error) { return db.PruneExpiredNegativeGeo(c, w.Pool) }},
		// The third negative cache, and the one that grows fastest: a row per
		// artist anyone's profile has ever contained. concerts.needsTMResolution
		// already re-asks about these once they age past the TTL, so nothing
		// reads them; positives have a non-NULL attraction ID and are kept.
		{"expired_tm_negatives", func(c context.Context) (int64, error) {
			return db.PruneExpiredNegativeResolutions(c, w.Pool, concerts.NegativeResolutionTTL)
		}},
	}
	for _, s := range steps {
		n, err := s.run(ctx)
		if err != nil {
			slog.Warn("janitor step failed", "step", s.name, "err", err)
			continue
		}
		if n > 0 {
			slog.Info("janitor pruned", "step", s.name, "rows", n)
		}
	}
	return nil
}

// pruneStaleSnapshots drops snapshots for locations a user has moved away
// from. The set of keys to preserve is built here, in Go, using the same
// LocationKey formatter the scan worker writes with — the previous version
// rebuilt the key in SQL with round(...)::text, a second implementation of
// the format whose disagreement with the first would delete the snapshot a
// user is actively looking at.
func (w *JanitorWorker) pruneStaleSnapshots(ctx context.Context) (int64, error) {
	locs, err := db.ListCurrentLocations(ctx, w.Pool)
	if err != nil {
		return 0, err
	}
	keep := make([]db.CurrentLocationKey, 0, len(locs))
	for _, l := range locs {
		keep = append(keep, db.CurrentLocationKey{
			UserID: l.UserID,
			LocationKey: LocationKey(concerts.Location{
				Latitude:    l.Latitude,
				Longitude:   l.Longitude,
				RadiusMiles: l.RadiusMiles,
			}),
		})
	}
	return db.PruneStaleSnapshots(ctx, w.Pool, keep, 30)
}

// FanoutSendDigestWorker enqueues one SendDigestArgs per opted-in user with
// a stored email. Runs daily.
type FanoutSendDigestWorker struct {
	river.WorkerDefaults[FanoutSendDigestArgs]
	Pool   *pgxpool.Pool
	Client *river.Client[pgx.Tx]
}

func (w *FanoutSendDigestWorker) Work(ctx context.Context, _ *river.Job[FanoutSendDigestArgs]) error {
	const q = `
SELECT id FROM users
WHERE digest_opt_in = true AND email IS NOT NULL AND email <> ''
`
	rows, err := w.Pool.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	slog.Info("fanout: sending digests", "count", len(ids))
	for _, id := range ids {
		if _, err := w.Client.Insert(ctx, SendDigestArgs{UserID: id}, nil); err != nil {
			slog.Warn("digest fanout enqueue failed", "user", id, "err", err)
		}
	}
	return nil
}

// FanoutScanConcertsWorker enqueues one ScanConcertsArgs per active user
// (session in the last 14 days). Users without a saved location fall back
// to the server-wide default so they still get nightly refreshes. Enqueue
// times are spread across SpreadWindow so the batch doesn't thunder.
type FanoutScanConcertsWorker struct {
	river.WorkerDefaults[FanoutScanConcertsArgs]
	Pool     *pgxpool.Pool
	Client   *river.Client[pgx.Tx]
	Fallback FallbackLocation
}

// FallbackLocation is the process-wide default used for active users who
// haven't set a location. Set once at wire-up time in main.go.
type FallbackLocation struct {
	Latitude    float64
	Longitude   float64
	RadiusMiles int
}

// SpreadWindow is how long the fanout smears its per-user jobs across, using
// river's ScheduledAt. Prevents a thundering herd when the periodic tick fires.
const SpreadWindow = 60 * time.Minute

func (w *FanoutScanConcertsWorker) Work(ctx context.Context, _ *river.Job[FanoutScanConcertsArgs]) error {
	// LEFT JOIN so users without a saved user_locations row still get a
	// refresh — using the server-wide fallback location. Missing locations
	// otherwise mean brand-new users never get a nightly snapshot.
	//
	// A user with neither a Spotify identity nor a pick has nothing to scan
	// (CF-30), so they are not queued: each such job would take a worker slot
	// to discover that.
	const q = `
SELECT DISTINCT s.user_id, ul.latitude, ul.longitude, ul.radius_miles
FROM sessions s
JOIN users u ON u.id = s.user_id
LEFT JOIN user_locations ul ON ul.user_id = s.user_id
WHERE s.last_seen_at > now() - interval '14 days'
  AND (u.spotify_user_id IS NOT NULL
       OR EXISTS (SELECT 1 FROM user_artist_picks p WHERE p.user_id = s.user_id)
       OR EXISTS (SELECT 1 FROM user_genre_picks g WHERE g.user_id = s.user_id))
`
	rows, err := w.Pool.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	type target struct {
		id     uuid.UUID
		lat    *float64
		lng    *float64
		radius *int
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.lat, &t.lng, &t.radius); err != nil {
			return err
		}
		targets = append(targets, t)
	}
	slog.Info("fanout: scanning concerts", "count", len(targets))
	for i, t := range targets {
		lat, lng, radius := w.Fallback.Latitude, w.Fallback.Longitude, w.Fallback.RadiusMiles
		if t.lat != nil && t.lng != nil && t.radius != nil {
			lat, lng, radius = *t.lat, *t.lng, *t.radius
		}
		args := ScanConcertsArgs{UserID: t.id, Latitude: lat, Longitude: lng, RadiusMiles: radius}
		// Spread the batch across SpreadWindow so N users don't all hit
		// MB + Nominatim + TM in the same second.
		offset := time.Duration(0)
		if len(targets) > 1 {
			offset = SpreadWindow * time.Duration(i) / time.Duration(len(targets))
		}
		if _, err := w.Client.Insert(ctx, args, &river.InsertOpts{
			ScheduledAt: time.Now().Add(offset),
		}); err != nil {
			slog.Warn("scan fanout enqueue failed", "user", t.id, "err", err)
		}
	}
	return nil
}

// applyUserArtistRefs lays this user's own artist identity over rows loaded
// straight from the shared concerts table by dedup_key. The jobs that start
// from keys rather than a snapshot (instant-notify, push) need it as much as
// the feed does: the shared row is last-writer-wins and holds no artist ID or
// genres of anybody's (CF-28).
func applyUserArtistRefs(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, keys []string, cs []concerts.Concert) error {
	raw, err := db.GetArtistRefs(ctx, pool, userID, keys)
	if err != nil {
		return err
	}
	concerts.ApplyArtistRefs(cs, concerts.ArtistRefsFromRaw(raw))
	return nil
}

// profileTargets turns an affinity profile into concert-search targets.
// Profile artists carry no attraction ID, so concerts.Search resolves each
// by name through artist_resolutions, exactly as before CF-29. The
// conversion lives here, not in internal/concerts, because that package must
// not import internal/spotify (see concerts.Target).
func profileTargets(artists []spotify.ScoredArtist) []concerts.Target {
	out := make([]concerts.Target, len(artists))
	for i, a := range artists {
		out[i] = concerts.Target{ID: a.ID, Name: a.Name, Score: a.Score, Genres: a.Genres}
	}
	return out
}
