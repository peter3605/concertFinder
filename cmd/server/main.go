package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
	"github.com/peterho/concertfinder/internal/fallback"
	"github.com/peterho/concertfinder/internal/geocoding"
	webhttp "github.com/peterho/concertfinder/internal/http"
	"github.com/peterho/concertfinder/internal/http/spa"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/push"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

func main() {
	// The api image is distroless — no shell, no curl, no wget — so a compose
	// healthcheck has nothing to run except this binary. `-healthcheck` probes
	// the already-running server in the same container and exits 0/1, which is
	// what `healthcheck:` in docker-compose.prod.yml invokes.
	healthcheck := flag.Bool("healthcheck", false,
		"probe the running server's /api/healthz and exit 0 (healthy) or 1 (not); for the container healthcheck")
	// Invite administration, for the same reason: the operator's only way
	// into a distroless container is this binary, so minting a code is a mode
	// of it rather than a second command that would have to be added to the
	// image. Run as
	//   docker compose exec api /server -mint-invite -note "alex"
	mintInvite := flag.Bool("mint-invite", false, "mint an invite code, print it, and exit")
	listInvites := flag.Bool("list-invites", false, "list invite codes and their state, then exit")
	disableInvite := flag.String("disable-invite", "", "revoke an invite code (keeps it readable) and exit")
	inviteNote := flag.String("note", "", "with -mint-invite: who the code is for (operator's own reference)")
	inviteUses := flag.Int("uses", 1, "with -mint-invite: how many signups the code admits")
	inviteDays := flag.Int("expires-days", 0, "with -mint-invite: days until the code expires (0 = never)")
	// Admin administration, and the bootstrap for the whole authorization
	// tier: the web console can only be reached by an admin, and migration
	// 0022 leaves every account a non-admin, so the first grant has to come
	// from outside the app. Accounts are named by their Spotify user ID
	// because the internal UUID is not something an operator can look up.
	//   docker compose exec api /server -grant-admin <spotify_user_id>
	grantAdmin := flag.String("grant-admin", "", "grant the admin flag to a Spotify user ID and exit")
	revokeAdmin := flag.String("revoke-admin", "", "remove the admin flag from a Spotify user ID and exit")
	listAdmins := flag.Bool("list-admins", false, "list admin accounts, then exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(runHealthcheck())
	}
	if *mintInvite || *listInvites || *disableInvite != "" {
		os.Exit(runInviteAdmin(inviteAdminArgs{
			mint:    *mintInvite,
			list:    *listInvites,
			disable: *disableInvite,
			note:    *inviteNote,
			uses:    *inviteUses,
			days:    *inviteDays,
		}))
	}
	if *grantAdmin != "" || *revokeAdmin != "" || *listAdmins {
		if *grantAdmin != "" && *revokeAdmin != "" {
			// Refused rather than resolved by precedence. Both flags name an
			// account and mean opposite things, so picking one silently is
			// how somebody revokes the only admin while reading the command
			// that granted it.
			fmt.Fprintln(os.Stderr, "server: -grant-admin and -revoke-admin are mutually exclusive")
			os.Exit(2)
		}
		os.Exit(runAdminAdmin(adminArgs{
			grant:  *grantAdmin,
			revoke: *revokeAdmin,
			list:   *listAdmins,
		}))
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	// Refuse to serve on a configuration that would fail silently once real
	// traffic arrives — see config.Validate for what each check has cost.
	// Reported all at once so a misconfigured deploy takes one round trip to
	// fix, not one per variable.
	if problems := cfg.Validate(); len(problems) > 0 {
		for _, p := range problems {
			logger.Error("invalid configuration", "err", p)
		}
		logger.Error("refusing to start", "problems", len(problems))
		os.Exit(1)
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := db.Connect(dbCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	dbCancel()
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Its own context, cancelled before the pool closes (defers run LIFO), so
	// the reporter never outlives what it reports on. dbCtx is no use here —
	// it is a 10s connect deadline and is already cancelled.
	statsCtx, stopStats := context.WithCancel(context.Background())
	defer stopStats()
	db.StartPoolStatsLogger(statsCtx, pool)

	// App + river migrations run at startup. Both are idempotent.
	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := db.Migrate(migCtx, pool, "migrations"); err != nil {
		migCancel()
		logger.Error("app migrations failed", "err", err)
		os.Exit(1)
	}
	riverDriver := riverpgxv5.New(pool)
	riverMigrator, err := rivermigrate.New(riverDriver, nil)
	if err != nil {
		migCancel()
		logger.Error("river migrator init failed", "err", err)
		os.Exit(1)
	}
	if _, err := riverMigrator.Migrate(migCtx, rivermigrate.DirectionUp, nil); err != nil {
		migCancel()
		logger.Error("river migrations failed", "err", err)
		os.Exit(1)
	}
	migCancel()

	// Wire dependencies before mounting routes. Hoisting these out of the
	// router closure means shutdown handlers below can see them.
	var (
		authDeps     *auth.Deps
		concertsH    *webhttp.ConcertsHandler
		affinityH    *webhttp.AffinityHandler
		locationH    *webhttp.LocationHandler
		savedH       *webhttp.SavedConcertsHandler
		accountH     *webhttp.AccountHandler
		subscribedH  *webhttp.SubscribedArtistsHandler
		emailPrefsH  *webhttp.EmailPrefsHandler
		unsubscribeH *webhttp.UnsubscribeHandler
		devicesH     *webhttp.DevicesHandler
		riverClient  *river.Client[pgx.Tx]
		signingKey   []byte
	)

	// Validate already rejected a bad key, so this cannot fire — but it used
	// to be a Warn that silently skipped wiring every auth and /me route,
	// leaving a site that served, passed health checks, and 404'd on login.
	// If the two ever disagree, stopping is the only safe answer.
	encKey, keyErr := auth.DecodeKey(cfg.EncryptionKey)
	if keyErr != nil {
		logger.Error("ENCRYPTION_KEY invalid; refusing to start without authentication", "err", keyErr)
		os.Exit(1)
	}
	{
		spotifyHTTP := &http.Client{Timeout: 30 * time.Second}
		spotifyClient := spotify.NewClient(spotifyHTTP)
		oauthHTTP := &http.Client{Timeout: 10 * time.Second}
		tokenSvc := &auth.TokenService{
			Pool:       pool,
			EncKey:     encKey,
			ClientID:   cfg.SpotifyClientID,
			HTTPClient: oauthHTTP,
		}
		authDeps = &auth.Deps{
			Pool:          pool,
			EncKey:        encKey,
			ClientID:      cfg.SpotifyClientID,
			RedirectURI:   cfg.SpotifyRedirectURI,
			CookieDomain:  cfg.SessionCookieDomain,
			Handshakes:    auth.NewDBHandshakeStore(pool),
			SpotifyClient: spotifyClient,
			HTTPClient:    oauthHTTP,
			PostLoginURL:  "/",
			// Empty on a deployment with no iOS configuration, which makes
			// /login?client=ios refuse rather than complete into a session
			// only a browser could use.
			MobileCallbackURL: cfg.MobileCallbackURL,
			// Gates signups, not logins. Defaults on; see
			// config.Config.InviteRequired.
			InviteRequired: cfg.InviteRequired,
		}

		affinitySvc := &affinity.Service{
			Pool:    pool,
			Tokens:  tokenSvc,
			Spotify: spotifyClient,
			TTL:     24 * time.Hour,
		}
		ticketHTTP := &http.Client{Timeout: 10 * time.Second}
		tmClient := ticketmaster.NewClient(ticketHTTP, cfg.TicketmasterAPIKey)

		affinityH = &webhttp.AffinityHandler{Service: affinitySvc}

		// Shared Nominatim client. Used by the location handler (user picks a
		// city) and by the fallback venue geocoder (turns "Baltimore, MD"
		// venues into coords when JSON-LD omits geo).
		//
		// Both Nominatim and MusicBrainz require a User-Agent that identifies
		// the operator and offers a way to reach them; the sanction for a bad
		// one is a block, not an error we'd see in a log. Build it from the
		// deployment's own base URL and contact address so it stays true.
		userAgent := fmt.Sprintf("ConcertFinder/1.0 (+%s; %s)", cfg.SiteBaseURL, cfg.ContactEmail)
		geocoder := geocoding.NewClient(userAgent)

		rateLedger := &rate.Ledger{
			Pool: pool,
			Caps: rate.Caps{
				Ticketmaster:        cfg.RateCapTMPerUserDaily,
				Songkick:            cfg.RateCapSongkickPerUserDaily,
				TicketmasterAccount: cfg.RateCapTMAccountDaily,
				SongkickAccount:     cfg.RateCapSongkickAccountDaily,
			},
		}
		logger.Info("rate ledger enabled",
			"tm_daily", cfg.RateCapTMPerUserDaily,
			"songkick_daily", cfg.RateCapSongkickPerUserDaily,
			"tm_account_daily", cfg.RateCapTMAccountDaily,
			"songkick_account_daily", cfg.RateCapSongkickAccountDaily,
			"max_artists_per_scan", spotify.MaxScoredArtists,
		)
		// How many users can do a full cold scan on one day before the shared
		// allowance is gone. This is the number that decides whether opening
		// the app to the public degrades everyone's feed, and it is not
		// obvious from either cap on its own.
		if cfg.RateCapTMAccountDaily > 0 && cfg.RateCapTMPerUserDaily > 0 {
			logger.Info("TM account allowance covers a bounded number of full scans per day",
				"full_scans_per_day", cfg.RateCapTMAccountDaily/cfg.RateCapTMPerUserDaily,
			)
			if cfg.RateCapTMAccountDaily < cfg.RateCapTMPerUserDaily {
				logger.Warn("TM account cap is below the per-user cap; no single user can finish a scan",
					"account", cfg.RateCapTMAccountDaily,
					"per_user", cfg.RateCapTMPerUserDaily,
				)
			}
		}
		// A cap below the cost of a COLD scan means a new user can never cover
		// their own profile: the scan runs out of quota partway and reports
		// itself incomplete. Measured against artists x CallsPerArtistColdScan
		// rather than against the artist count, because the artist count is
		// the warm cost and checking it is what let a 250 cap look fine while
		// covering 125 of 200 artists. Worth saying out loud rather than
		// leaving to be rediscovered from a thin concert list.
		coldScanCost := spotify.MaxScoredArtists * ticketmaster.CallsPerArtistColdScan
		if cfg.RateCapTMPerUserDaily > 0 && cfg.RateCapTMPerUserDaily < coldScanCost {
			logger.Warn("TM per-user daily cap is below the cost of a cold scan; a new user's first scan will be capped short",
				"cap", cfg.RateCapTMPerUserDaily,
				"cold_scan_cost", coldScanCost,
				"artists", spotify.MaxScoredArtists)
		}

		// Admission. Logged unconditionally and in both directions, because
		// both are silent from outside: a gate left off looks like a working
		// site right up until the shared Ticketmaster allowance is gone, and
		// a gate left on looks like a working site to everyone who already
		// has an account -- which includes the operator, who is therefore the
		// last person to notice that nobody new can join.
		if cfg.InviteRequired {
			coldScansPerDay := 0
			if scanReservation := spotify.MaxScoredArtists * ticketmaster.CallsPerArtistColdScan; scanReservation > 0 {
				coldScansPerDay = cfg.RateCapTMAccountDaily / scanReservation
			}
			logger.Info("signups require an invite code",
				"mint", "server -mint-invite -note '<who>'",
				"cold_scans_per_day", coldScansPerDay)
		} else {
			logger.Warn("signups are OPEN: anyone who can reach Spotify's consent screen can create an account",
				"set", "INVITE_REQUIRED=true to gate signups",
				"tm_account_daily", cfg.RateCapTMAccountDaily)
		}

		// The admin console is unreachable until somebody holds the flag, and
		// nothing about that failure is legible from the browser: /admin
		// answers 403, which is the same thing it says to an ordinary user.
		// One count at boot turns "the page is broken" into a line saying
		// exactly which command fixes it. Best-effort — an admin-flag count
		// is not a reason to refuse to serve.
		adminCtx, cancelAdminCount := context.WithTimeout(context.Background(), 5*time.Second)
		admins, err := db.ListAdmins(adminCtx, pool)
		cancelAdminCount()
		if err != nil {
			logger.Warn("could not count admin accounts", "err", err)
		} else if len(admins) == 0 {
			logger.Warn("no admin accounts exist; /admin is unreachable until one is granted",
				"grant", "server -grant-admin <spotify_user_id>")
		}

		var fallbackChain concerts.Fallbacker
		if cfg.Phase2Enabled {
			// URL resolution defaults to MusicBrainz (free, no API key). If a
			// Brave key is set, we fall back to that — kept for parity while
			// evaluating MusicBrainz coverage on real data.
			var resolver fallback.URLResolver = fallback.NewMusicBrainzClient(userAgent).WithPool(pool)
			if cfg.BraveSearchAPIKey != "" {
				resolver = fallback.NewBraveClient(cfg.BraveSearchAPIKey)
			}
			// One User-Agent for every outbound client, not just the two that
			// happened to take one. The artist-site fetcher and the Songkick
			// client each carried their own hardcoded string naming a GitHub
			// repository that does not exist — which is worse than none, since
			// it looks like an operator can be contacted and nobody can be.
			fallbackChain = &fallback.Chain{
				Pool:     pool,
				Fetcher:  fallback.NewFetcher(pool, userAgent),
				Resolver: resolver,
				Songkick: fallback.NewSongkickClient(cfg.SongkickAPIKey, userAgent),
				VenueGeo: fallback.NewVenueGeocoder(geocoder).WithPool(pool),
			}
			budget := time.Duration(cfg.Phase2FallbackBudgetSeconds) * time.Second
			if budget == 0 {
				budget = concerts.DefaultFallbackBudget
			}
			logger.Info("phase 2 fallbacks enabled",
				"min_score", cfg.Phase2MinScore,
				"url_resolver", fmt.Sprintf("%T", resolver),
				"songkick_key_set", cfg.SongkickAPIKey != "",
				"venue_geocoder", "nominatim",
				"fallback_budget", budget,
				"fallback_concurrency", cfg.Phase2FallbackConcurrency,
			)
		}

		fallbackLoc := concerts.Location{
			Latitude:    cfg.UserLatitude,
			Longitude:   cfg.UserLongitude,
			RadiusMiles: cfg.UserRadiusMiles,
		}

		locationH = &webhttp.LocationHandler{
			Pool:             pool,
			Geocoder:         geocoder,
			FallbackLocation: fallbackLoc,
		}

		// Factory returning a fresh SearchDeps for each scan job. TM +
		// fallback are all safe to share across concurrent jobs (their
		// internal state is either read-only config or independently locked).
		// Per-user quota is not wired here: the scan worker reserves it and
		// puts it on the context (see internal/rate).
		// Upstream response cache lifetime. Long enough that the SWR refresh
		// loop is served from cache between quota-spending scans.
		cacheTTL := concerts.DefaultCacheTTL
		if cfg.ConcertCacheTTLHours > 0 {
			cacheTTL = time.Duration(cfg.ConcertCacheTTLHours) * time.Hour
		}
		fallbackBudget := time.Duration(cfg.Phase2FallbackBudgetSeconds) * time.Second
		// ONE gate for the whole process. The fallback's resolvers share a
		// single 1 req/sec turnstile each, so concurrent scans would divide
		// that throughput while each still believing it had a full budget.
		// Constructed here, outside the closure, so every scan contends for
		// the same slot — a per-scan gate would be a no-op.
		fallbackGate := concerts.NewFallbackGate(cfg.Phase2FallbackConcurrency)
		searchDeps := func() concerts.SearchDeps {
			return concerts.SearchDeps{
				Pool:           pool,
				TM:             tmClient,
				CacheTTL:       cacheTTL,
				Parallelism:    10,
				Fallback:       fallbackChain,
				FallbackBudget: fallbackBudget,
				FallbackGate:   fallbackGate,
			}
		}

		// Email sender + unsubscribe handler. Sender falls back to log mode
		// automatically when EMAIL_DELIVERY_MODE isn't 'smtp', so local dev
		// runs the full digest render + enqueue path without a real relay.
		emailSender := &email.Sender{Cfg: email.Config{
			Mode:     email.Mode(cfg.EmailDeliveryMode),
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
		}}
		signingKey = auth.SigningKey(cfg.SigningKey, encKey)
		unsubscribeH = &webhttp.UnsubscribeHandler{Pool: pool, Secret: signingKey}
		emailPrefsH = &webhttp.EmailPrefsHandler{Pool: pool}

		// Background jobs live in the same process — no separate worker
		// binary. See docs/design.md §10.2 (option 2 rationale in commit).
		workers := river.NewWorkers()
		river.AddWorker(workers, &jobs.RefreshAffinityWorker{Pool: pool, Affinity: affinitySvc})
		scanWorker := &jobs.ScanConcertsWorker{
			Pool:             pool,
			Affinity:         affinitySvc,
			Deps:             searchDeps,
			MinFallbackScore: cfg.Phase2MinScore,
			Ledger:           rateLedger,
			// River back-reference wired after client construction (below).
		}
		river.AddWorker(workers, scanWorker)
		river.AddWorker(workers, &jobs.SendDigestWorker{
			Pool:             pool,
			Sender:           emailSender,
			UnsubscribeBase:  cfg.SiteBaseURL,
			UnsubscribeToken: unsubscribeH.Token,
			// Same fallback the scan fanout uses. Without it, an opted-in
			// user who never saved a location is scanned nightly and never
			// emailed.
			Fallback: jobs.FallbackLocation{
				Latitude:    cfg.UserLatitude,
				Longitude:   cfg.UserLongitude,
				RadiusMiles: cfg.UserRadiusMiles,
			},
		})
		river.AddWorker(workers, &jobs.SendInstantNotifyWorker{
			Pool:             pool,
			Sender:           emailSender,
			UnsubscribeBase:  cfg.SiteBaseURL,
			UnsubscribeToken: unsubscribeH.Token,
		})
		// APNs client. Absent configuration is not an error — a deployment
		// without an Apple developer account still runs everything else —
		// but a *partial* one is, and config.Validate has already rejected
		// that, so reaching here with some fields set means all are.
		var apnsClient *push.Client
		if cfg.APNSKeyID != "" {
			apnsClient, err = push.New(push.Config{
				KeyID:       cfg.APNSKeyID,
				TeamID:      cfg.APNSTeamID,
				BundleID:    cfg.APNSBundleID,
				P8Key:       cfg.APNSP8Key,
				Environment: cfg.APNSEnvironment,
			})
			if err != nil {
				// The key is malformed. Refusing to start beats running with
				// push silently disabled, which is indistinguishable from
				// nobody having opted in.
				logger.Error("APNs client init failed", "err", err)
				os.Exit(1)
			}
			// Log what the key is authorized for rather than the raw variable:
			// they differ whenever APNS_ENVIRONMENT names both, and the list
			// is what the worker filters devices on.
			logger.Info("push enabled",
				"environments", strings.Join(apnsClient.Environments(), ","),
				"bundle", cfg.APNSBundleID)
		}
		river.AddWorker(workers, &jobs.SendPushWorker{Pool: pool, APNs: apnsClient})
		river.AddWorker(workers, &jobs.JanitorWorker{Pool: pool})
		fanoutAff := &jobs.FanoutAffinityRefreshWorker{Pool: pool}
		fanoutScan := &jobs.FanoutScanConcertsWorker{Pool: pool, Fallback: jobs.FallbackLocation{
			Latitude:    cfg.UserLatitude,
			Longitude:   cfg.UserLongitude,
			RadiusMiles: cfg.UserRadiusMiles,
		}}
		fanoutDigest := &jobs.FanoutSendDigestWorker{Pool: pool}
		river.AddWorker(workers, fanoutAff)
		river.AddWorker(workers, fanoutScan)
		river.AddWorker(workers, fanoutDigest)
		// Wall-clock schedules, not 24h intervals — see jobs.DailyAt for why
		// an interval schedule silently stops running on a frequently
		// redeployed process. RunOnStart stays false because DailyAt computes
		// the next real occurrence, so a restart resumes the schedule instead
		// of resetting a countdown.
		if gap := cfg.ScanDigestGapHours(); gap < config.MinScanDigestGapHours {
			logger.Warn("daily digest runs too soon after the daily scan; it will report the previous day's results",
				"scan_hour_utc", cfg.DailyScanHourUTC,
				"digest_hour_utc", cfg.DailyDigestHourUTC,
				"gap_hours", gap,
				"min_gap_hours", config.MinScanDigestGapHours,
			)
		}
		periodic := []*river.PeriodicJob{
			river.NewPeriodicJob(
				jobs.DailyAt(cfg.DailyAffinityHourUTC, 0),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.FanoutAffinityRefreshArgs{}, nil },
				&river.PeriodicJobOpts{ID: "daily_affinity_refresh", RunOnStart: false},
			),
			river.NewPeriodicJob(
				jobs.DailyAt(cfg.DailyScanHourUTC, 0),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.FanoutScanConcertsArgs{}, nil },
				&river.PeriodicJobOpts{ID: "daily_scan_concerts", RunOnStart: false},
			),
			river.NewPeriodicJob(
				jobs.DailyAt(cfg.DailyDigestHourUTC, 0),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.FanoutSendDigestArgs{}, nil },
				&river.PeriodicJobOpts{ID: "daily_send_digest", RunOnStart: false},
			),
			river.NewPeriodicJob(
				jobs.DailyAt(cfg.DailyJanitorHourUTC, 0),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.JanitorArgs{}, nil },
				&river.PeriodicJobOpts{ID: "daily_janitor", RunOnStart: false},
			),
		}
		riverClient, err = river.NewClient[pgx.Tx](riverDriver, &river.Config{
			Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
			Workers:      workers,
			PeriodicJobs: periodic,
		})
		if err != nil {
			logger.Error("river client init failed", "err", err)
			os.Exit(1)
		}
		fanoutAff.Client = riverClient
		fanoutScan.Client = riverClient
		fanoutDigest.Client = riverClient
		scanWorker.River = riverClient

		concertsH = &webhttp.ConcertsHandler{
			Pool:               pool,
			River:              riverClient,
			FallbackLocation:   fallbackLoc,
			SnapshotStaleAfter: time.Duration(cfg.SnapshotStaleAfterHours) * time.Hour,
			// LRU-bounded per-(user, location, computed_at) cache. 200 entries
			// is plenty at single-instance scale — typically one live entry
			// per active user per location.
			SnapshotCache: webhttp.NewSnapshotCache(200),
			// Read-only use of the profile: the handler asks for reasons, not
			// for a computation. See ConcertsHandler.Affinity.
			Affinity: affinitySvc,
		}
		savedH = &webhttp.SavedConcertsHandler{Pool: pool, FallbackLocation: fallbackLoc}
		devicesH = &webhttp.DevicesHandler{Pool: pool}
		accountH = &webhttp.AccountHandler{Pool: pool, Tokens: tokenSvc, CookieDomain: cfg.SessionCookieDomain}
		subscribedH = &webhttp.SubscribedArtistsHandler{
			Pool:    pool,
			Spotify: spotifyClient,
			Tokens:  tokenSvc,
		}

		// Wire the login-success hook so new sessions trigger a pre-warm
		// snapshot job. Uses a detached background context so a browser
		// disconnect mid-callback doesn't cancel the enqueue.
		authDeps.OnLoginSuccess = func(_ context.Context, userID uuid.UUID) {
			ul, hit, err := db.GetUserLocation(context.Background(), pool, userID)
			if err != nil {
				logger.Warn("prewarm location lookup failed", "err", err, "user", userID)
				return
			}
			if !hit {
				// No location of the user's own yet, so the only thing to
				// scan is this deployment's USER_LATITUDE/USER_LONGITUDE —
				// a city the user has never mentioned. That scan takes up to
				// ScanBudget and reserves a chunk of a daily per-user cap
				// sized at roughly one scan, and the moment the user names a
				// real place the result is filed under a different
				// location_key and never read. The user waited through it to
				// be handed nothing.
				//
				// Both clients ask for a location before the first feed, and
				// the SWR read enqueues the scan when the answer arrives.
				logger.Info("prewarm skipped: user has no location yet", "user", userID)
				return
			}
			loc := concerts.Location{Latitude: ul.Latitude, Longitude: ul.Longitude, RadiusMiles: ul.RadiusMiles}
			args := jobs.ScanConcertsArgs{
				UserID:      userID,
				Latitude:    loc.Latitude,
				Longitude:   loc.Longitude,
				RadiusMiles: loc.RadiusMiles,
			}
			// Uniqueness comes from ScanConcertsArgs.InsertOpts (one scan in
			// flight per user+location); passing opts here would override it.
			if _, err := riverClient.Insert(context.Background(), args, nil); err != nil {
				logger.Warn("prewarm scan enqueue failed", "err", err, "user", userID)
			}
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))
	// gzip everything gzip-worthy. Level 5 is a well-tuned middle ground —
	// the concert-list JSON compresses ~5x, which is the payload that
	// dominates wire time. chi's Compress skips already-compressed content
	// types (images, video) automatically.
	r.Use(middleware.Compress(5))

	// Rate limiting lives here rather than in Caddy on purpose. The stock
	// caddy:2-alpine image ships no rate_limit directive — it is a
	// third-party module that has to be compiled in with xcaddy — and the
	// image is pinned by digest, so adding one means owning a custom build and
	// its update path. Middleware costs a map lookup and stays in the same
	// place as the handlers it protects.
	//
	// Three tiers, outermost first:
	//   - the whole /api subtree, generous, so no single address can flood any
	//     endpoint including the ones added later;
	//   - /api/auth, /api/healthz and /api/unsubscribe, each tighter for its
	//     own reason (below);
	//   - /me/artists/search, keyed by user rather than address, because the
	//     resource it spends is the app's single Spotify client ID.
	//
	// 20 req/s with burst 60 per address. A signed-in browser polls
	// /me/concerts every 10s during a refresh and the first paint of the feed
	// issues a handful of calls, so this is roughly two orders of magnitude
	// above real use — the aim is a ceiling, not a quota.
	apiLimiter := auth.NewIPRateLimit(20, 60)
	r.Route("/api", func(api chi.Router) {
		api.Use(apiLimiter.Middleware)
		// Unknown /api/* paths return a JSON 404 rather than falling through
		// to the SPA HTML handler — matters for API clients that would
		// otherwise see HTML and misdiagnose.
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		})
		api.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write([]byte(`{"error":"method_not_allowed"}`))
		})
		// Health means "can actually serve requests", which for every route
		// on this server means reaching Postgres. Reporting ok without
		// checking it is backwards: a restart loop or an alert would see a
		// green light while every real endpoint 500s.
		//
		// Which is also why it needs its own, tighter limit: each call is a
		// round trip to a Neon compute pinned at 0.25 CU, and the free plan is
		// a compute-hour budget rather than a request budget. 1/s with burst
		// 10 is far above the two callers that legitimately exist — the
		// container healthcheck and scripts/verify-deploy.sh — while leaving
		// them room to retry.
		healthLimiter := auth.NewIPRateLimit(1, 10)
		api.With(healthLimiter.Middleware).Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			w.Header().Set("Content-Type", "application/json")
			if err := pool.Ping(ctx); err != nil {
				logger.Warn("healthz: database unreachable", "err", err)
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "reason": "database"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		// "Popular shows near you" for a visitor with no session: the login
		// page, App Review's first screen, and the backdrop behind the iOS
		// first-run flow. Served entirely from concert_cache — it cannot
		// reach an upstream API and cannot touch the rate ledger, which is
		// the property that makes an unauthenticated concert endpoint safe
		// to have at all.
		//
		// Its own bucket, tighter than /api's, because it is unauthenticated
		// and its refresh decodes thousands of cached payloads. 2/s with
		// burst 20 is far above a page that calls it once on load.
		discoverLimiter := auth.NewIPRateLimit(2, 20)
		// One instance for the process. The handler holds the decoded
		// candidate set (DiscoverRefreshInterval), so a per-request one would
		// be an empty cache every time, i.e. no cache at all.
		discoverH := &webhttp.DiscoverHandler{Pool: pool}
		api.With(discoverLimiter.Middleware).Get("/discover", discoverH.Get)
		api.Get("/site-info", (&webhttp.SiteInfoHandler{
			ContactEmail:  cfg.ContactEmail,
			EffectiveDate: "2026-07-29",
			MinIOSBuild:   cfg.MinIOSBuild,
			// Additive field: tells both clients whether to show an invite
			// box on the sign-in screen. Additive because /api/me/* and this
			// payload are consumed by builds already on people's phones.
			InviteRequired: cfg.InviteRequired,
		}).Get)
		// No nil guard on authDeps here any more. There used to be one, and it
		// was the mechanism by which a bad ENCRYPTION_KEY produced a site with
		// no authentication rather than a process that refused to start.
		// Startup now fails on that config, so these routes always mount.
		//
		// Rate-limit the OAuth start (/login and /callback are the state-mutating
		// bits of the auth flow). 5 req/s with burst 20 per source IP — plenty
		// for real users, spam-hostile.
		authLimiter := auth.NewIPRateLimit(5, 20)
		api.Route("/auth", func(r chi.Router) {
			r.Use(authLimiter.Middleware)
			auth.Mount(r, authDeps)
		})
		// Unauthenticated: HMAC-signed token in the URL is proof of identity
		// for one-click unsubscribe from a mobile mail client with no session.
		// GET only renders a confirmation page — the opt-out itself is on
		// POST, so link-scanning mail gateways can't unsubscribe people by
		// prefetching. POST is also what RFC 8058 one-click sends, which is
		// what the List-Unsubscribe-Post header on our mail advertises.
		//
		// Its own bucket because it is the other unauthenticated mutation on
		// the server and its guard is an HMAC verification, which is cheap but
		// not free, followed by a write. 2/s with burst 20 comfortably covers
		// a mail gateway prefetching links and a person clicking twice.
		unsubLimiter := auth.NewIPRateLimit(2, 20)
		api.With(unsubLimiter.Middleware).Get("/unsubscribe", unsubscribeH.Get)
		api.With(unsubLimiter.Middleware).Post("/unsubscribe", unsubscribeH.Post)
		// One instance for the process, mounted per route below. A limiter
		// constructed inside a handler is a fresh empty bucket on every
		// request, i.e. no limiter at all.
		//
		// 1 req/s with burst 10 per user. The picker is debounced client-side,
		// so this is many times a real typist; what it stops is one account
		// spending the whole app's Spotify /v1/search allowance, which is
		// enforced against our client ID and would 429 every other user's
		// searches with nothing in our logs to connect the two.
		artistSearchLimiter := auth.NewUserRateLimit(1, 10)
		api.Route("/me", func(r chi.Router) {
			r.Use(auth.RequireUser(pool))
			r.Use(auth.CSRF(signingKey))
			r.Get("/affinity", affinityH.Get)
			r.Get("/concerts", concertsH.Get)
			r.Post("/concerts/refresh", concertsH.Refresh)
			r.Get("/location", locationH.Get)
			r.Put("/location", locationH.Put)
			r.Get("/saved-concerts", savedH.List)
			r.Post("/saved-concerts", savedH.Post)
			r.Delete("/saved-concerts/{dedupKey}", savedH.Delete)
			r.Get("/subscribed-artists", subscribedH.List)
			r.Post("/subscribed-artists/{artistID}", subscribedH.Post)
			r.Delete("/subscribed-artists/{artistID}", subscribedH.Delete)
			// Mounted inside RequireUser so the limiter has a user to key on.
			r.With(artistSearchLimiter.Middleware).Get("/artists/search", subscribedH.SearchArtists)
			r.Post("/devices", devicesH.Post)
			r.Delete("/devices/{token}", devicesH.Delete)
			r.Put("/email-prefs", emailPrefsH.Put)
			r.Delete("/account", accountH.Delete)
			// Disconnect Spotify without deleting the account. Guideline
			// 5.1.1(v) asks for a revoke mechanism inside the app, and
			// account deletion was the only one (plan §10.1.2).
			r.Delete("/spotify-connection", accountH.DisconnectSpotify)
		})
		// The operator's console: mint and revoke invite codes without an SSM
		// round trip. Web only — no admin field appears in /api/me/* or
		// /api/auth/me, so the mobile contract does not learn that this tier
		// exists (see internal/http/admin.go).
		//
		// Three middlewares, in this order and all at Route level. RequireUser
		// resolves the session; CSRF must come after it, because its bearer
		// exemption reads the mechanism RequireUser recorded; RequireAdmin is
		// innermost and is installed by AdminHandler.Mount rather than here,
		// so no route can be registered beside these ones without it.
		//
		// One limiter for the process. Minting writes a row per call, and
		// "behind auth" is not a bound.
		mintLimiter := auth.NewUserRateLimit(1, 10)
		api.Route("/admin", func(r chi.Router) {
			r.Use(auth.RequireUser(pool))
			r.Use(auth.CSRF(signingKey))
			(&webhttp.AdminHandler{Pool: pool}).Mount(r, mintLimiter)
		})
	})

	// Universal-link association. Outside /api and registered before the SPA
	// catch-all, because Apple requires this exact path with no redirect and
	// no extension — falling through to the SPA would serve HTML with a 200
	// and iOS would simply decline to associate the domain, silently.
	r.Get("/.well-known/apple-app-site-association", (&webhttp.AASAHandler{AppID: cfg.IOSAppID}).Get)

	spaHandler := spa.Handler()
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { spaHandler.ServeHTTP(w, r) })

	if riverClient != nil {
		if err := riverClient.Start(context.Background()); err != nil {
			logger.Error("river start failed", "err", err)
			os.Exit(1)
		}
		logger.Info("river client running")
	}

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: r,
		// Bound every phase of a connection, not just the header read — a
		// slow or stalled client must not be able to hold one open
		// indefinitely. WriteTimeout is generous because /me/concerts can
		// serialize a few thousand concerts on a cold cache.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
	if riverClient != nil {
		if err := riverClient.Stop(shutdownCtx); err != nil {
			logger.Error("river stop error", "err", err)
		}
	}
}

// runHealthcheck probes the local server's /api/healthz and returns a process
// exit code. It exists because `docker compose up -d` returns as soon as a
// container is *started*, not once it stays up: with config.Validate's hard
// exit on a bad .env and `restart: unless-stopped`, one wrong variable used to
// produce a crash-looping api container, an SSM "Success", and a green
// workflow, with the site down the whole time and nothing saying so. A
// healthcheck is what lets `up -d --wait` fail instead.
//
// It deliberately reads LISTEN_ADDR straight from the environment rather than
// going through config.Load: the probe's job is to report on the server, and
// re-running config parsing here would let it fail for reasons that have
// nothing to do with whether the server is answering.
//
// /api/healthz pings Postgres, so this reports unhealthy when the database is
// unreachable. That marks the container unhealthy without restarting it —
// Docker's restart policies act on exit, not on health — so an RDS blip
// surfaces in `docker compose ps` rather than turning into a restart loop.
func runHealthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: LISTEN_ADDR %q is not host:port: %v\n", addr, err)
		return 1
	}
	// ":8080", "0.0.0.0:8080" and "[::]:8080" all mean "every interface";
	// from inside the container the way to reach that is loopback.
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/api/healthz"

	// Comfortably above the handler's own 2s database-ping timeout, so a slow
	// but working database reads as unhealthy only when it really is.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: build request: %v\n", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: GET %s: %v\n", url, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: GET %s returned %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}

func requestLogger(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			// X-CF-Client identifies the calling client, e.g.
			// "ios/1.0.0 (build 42)". Logged from day one on purpose: when
			// something breaks for app users only, this is the field that
			// separates them from browser traffic, and it cannot be added
			// retroactively to logs already written.
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"req_id", middleware.GetReqID(r.Context()),
			}
			if c := r.Header.Get("X-CF-Client"); c != "" {
				// Bounded: this is attacker-controlled input on its way into
				// a log line.
				if len(c) > 64 {
					c = c[:64]
				}
				attrs = append(attrs, "client", c)
			}
			l.Info("http", attrs...)
		})
	}
}
