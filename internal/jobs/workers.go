package jobs

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/rate"
)

// RefreshAffinityWorker recomputes one user's profile.
type RefreshAffinityWorker struct {
	river.WorkerDefaults[RefreshAffinityArgs]
	Pool     *pgxpool.Pool
	Affinity *affinity.Service
}

func (w *RefreshAffinityWorker) Work(ctx context.Context, job *river.Job[RefreshAffinityArgs]) error {
	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	if _, err := w.Affinity.Compute(ctx, affinity.User{ID: user.ID, SpotifyUserID: user.SpotifyUserID}); err != nil {
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
	// Stash the user ID so ledger checks downstream can charge the right
	// account (design §8.3). Every fresh TM/BIT/Songkick call reads this.
	ctx = rate.NewContext(ctx, job.Args.UserID)

	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	artists, _, _, err := w.Affinity.LoadOrCompute(ctx, affinity.User{ID: user.ID, SpotifyUserID: user.SpotifyUserID})
	if err != nil {
		return err
	}
	loc := concerts.Location{
		Latitude:    job.Args.Latitude,
		Longitude:   job.Args.Longitude,
		RadiusMiles: job.Args.RadiusMiles,
	}
	deps := w.Deps()
	deps.MinFallbackScore = w.MinFallbackScore

	found, err := concerts.Search(ctx, deps, artists, loc)
	if err != nil {
		return err
	}
	// Normalized snapshots (migration 0012): upsert each concert into the
	// shared `concerts` table, then persist only the array of dedup_keys
	// on the user's snapshot row. Storage is now shared across users —
	// two users following the same Taylor Swift show write one row, not two.
	dedupKeys := make([]string, len(found))
	rows := make([]db.ConcertRow, len(found))
	for i, c := range found {
		body, err := json.Marshal(c)
		if err != nil {
			return err
		}
		rows[i] = db.ConcertRow{DedupKey: c.DedupKey, Data: body, EventDate: c.Date}
		dedupKeys[i] = c.DedupKey
	}
	if err := db.UpsertConcerts(ctx, w.Pool, rows); err != nil {
		return err
	}
	err = db.UpsertConcertSnapshot(ctx, w.Pool, db.ConcertSnapshot{
		UserID:      user.ID,
		LocationKey: LocationKey(loc),
		DedupKeys:   dedupKeys,
		ComputedAt:  time.Now(),
	})
	if err != nil {
		return err
	}
	slog.Info("scan_concerts done", "user", user.ID, "location", LocationKey(loc), "concerts", len(found))

	// Instant-notify hook. Only fires when the user opted in AND is
	// subscribed to at least one artist that had a net-new concert. Errors
	// are logged but don't fail the scan.
	if w.River != nil && user.InstantNotifyOptIn && user.Email != "" {
		w.enqueueInstantNotify(ctx, user, found)
	}
	return nil
}

// enqueueInstantNotify picks the subset of just-computed concerts that
// (a) are future-dated, (b) belong to a subscribed artist, and (c) have
// never been in a previous digest/instant email. If any survive, enqueue
// a SendInstantNotify job with just those dedup_keys.
func (w *ScanConcertsWorker) enqueueInstantNotify(ctx context.Context, user db.User, found []concerts.Concert) {
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
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, candidates)
	if err != nil {
		slog.Warn("instant notify: filter unsent failed", "err", err, "user", user.ID)
		return
	}
	toSend := make([]string, 0, len(unsent))
	for _, k := range candidates {
		if _, ok := unsent[k]; ok {
			toSend = append(toSend, k)
		}
	}
	if len(toSend) == 0 {
		return
	}
	_, err = w.River.Insert(ctx, SendInstantNotifyArgs{UserID: user.ID, DedupKeys: toSend}, nil)
	if err != nil {
		slog.Warn("instant notify: enqueue failed", "err", err, "user", user.ID)
	}
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
	const q = `
SELECT DISTINCT user_id FROM sessions
WHERE last_seen_at > now() - interval '14 days'
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
	Sender           *email.Sender
	UnsubscribeBase  string // e.g. https://your-domain.com — the /api/me/unsubscribe path is appended
	UnsubscribeToken func(userID uuid.UUID) string
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
	loc, hit, err := db.GetUserLocation(ctx, w.Pool, user.ID)
	if err != nil || !hit {
		return nil
	}
	locKey := LocationKey(concerts.Location{Latitude: loc.Latitude, Longitude: loc.Longitude, RadiusMiles: loc.RadiusMiles})
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
	// Build the candidate set: future-dated concerts only. First-ever digest
	// also caps at 60 days out so we don't dump an entire tour year into
	// one email. First-ever is detected by "no prior rows in
	// user_digest_sent" — checked below via len(unsent) == len(candidates).
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
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, candidateKeys)
	if err != nil {
		return err
	}
	// If everything is unsent, this is the first-ever digest for this user;
	// clip to the 60-day horizon.
	firstEver := len(unsent) == len(candidateKeys)
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
	// Idempotency: record the send BEFORE we hit SMTP. If the send fails
	// we're accepting an at-most-once send (occasional missed emails) over
	// duplicate-send-on-retry, which is the less annoying failure mode for
	// end users. A retry would find these keys already in user_digest_sent
	// and skip them.
	if err := db.RecordDigestSent(ctx, w.Pool, user.ID, sendKeys); err != nil {
		return err
	}
	unsub := w.UnsubscribeBase + "/api/unsubscribe?token=" + w.UnsubscribeToken(user.ID)
	msg := email.RenderDigest(user.DisplayName, user.Email, fresh, unsub)
	if err := w.Sender.Send(ctx, msg); err != nil {
		slog.Warn("digest: SMTP send failed after recording sent-set", "err", err, "user", user.ID)
		return err
	}
	slog.Info("digest sent", "user", user.ID, "concerts", len(fresh))
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
	Sender           *email.Sender
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
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, job.Args.DedupKeys)
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
	keys := make([]string, 0, len(unsent))
	for k := range unsent {
		keys = append(keys, k)
	}
	blobs, err := db.GetConcertsByDedupKeys(ctx, w.Pool, keys)
	if err != nil {
		return err
	}
	fresh, err := concerts.AssembleByKey(keys, blobs)
	if err != nil {
		return err
	}
	sendKeys := make([]string, 0, len(fresh))
	for _, c := range fresh {
		sendKeys = append(sendKeys, c.DedupKey)
	}
	if len(fresh) == 0 {
		return nil
	}
	// Record the send BEFORE hitting SMTP; same at-most-once trade-off as
	// the daily digest.
	if err := db.RecordDigestSent(ctx, w.Pool, user.ID, sendKeys); err != nil {
		return err
	}
	unsub := w.UnsubscribeBase + "/api/unsubscribe?token=" + w.UnsubscribeToken(user.ID)
	msg := email.RenderInstantNotify(user.DisplayName, user.Email, fresh, unsub)
	if err := w.Sender.Send(ctx, msg); err != nil {
		slog.Warn("instant notify: SMTP send failed after recording sent-set", "err", err, "user", user.ID)
		return err
	}
	slog.Info("instant notify sent", "user", user.ID, "concerts", len(fresh))
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
		{"concert_cache", func(c context.Context) (int64, error) { return db.PruneConcertCache(c, w.Pool, 7) }},
		{"past_concerts", func(c context.Context) (int64, error) { return db.PrunePastConcerts(c, w.Pool, 7) }},
		{"oauth_handshakes", func(c context.Context) (int64, error) { return db.PruneExpiredHandshakes(c, w.Pool) }},
		{"stale_snapshots", func(c context.Context) (int64, error) { return db.PruneStaleSnapshots(c, w.Pool, 30) }},
		{"old_digest_sent", func(c context.Context) (int64, error) { return db.PruneOldDigestSent(c, w.Pool, 180) }},
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
	const q = `
SELECT DISTINCT s.user_id, ul.latitude, ul.longitude, ul.radius_miles
FROM sessions s
LEFT JOIN user_locations ul ON ul.user_id = s.user_id
WHERE s.last_seen_at > now() - interval '14 days'
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
