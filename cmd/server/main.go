package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/spotify"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := db.Connect(dbCtx, cfg.DatabaseURL)
	dbCancel()
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

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
		riverClient  *river.Client[pgx.Tx]
		signingKey   []byte
	)

	encKey, keyErr := auth.DecodeKey(cfg.EncryptionKey)
	if keyErr != nil {
		logger.Warn("auth routes disabled: ENCRYPTION_KEY missing or invalid", "err", keyErr)
	} else {
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
		geocoder := geocoding.NewClient("")

		rateLedger := &rate.Ledger{
			Pool: pool,
			Caps: rate.Caps{
				Ticketmaster: cfg.RateCapTMPerUserDaily,
				Songkick:     cfg.RateCapSongkickPerUserDaily,
			},
		}
		logger.Info("rate ledger enabled",
			"tm_daily", cfg.RateCapTMPerUserDaily,
			"songkick_daily", cfg.RateCapSongkickPerUserDaily,
			"max_artists_per_scan", spotify.MaxScoredArtists,
		)
		// A cap below the artist count means a user can never cover their own
		// profile: every scan runs out of quota partway and reports itself
		// incomplete. Worth saying out loud rather than leaving to be
		// rediscovered from a thin concert list.
		if cfg.RateCapTMPerUserDaily > 0 && cfg.RateCapTMPerUserDaily < spotify.MaxScoredArtists {
			logger.Warn("TM per-user daily cap is below the per-scan artist count; scans will be capped short",
				"cap", cfg.RateCapTMPerUserDaily, "artists", spotify.MaxScoredArtists)
		}

		var fallbackChain concerts.Fallbacker
		if cfg.Phase2Enabled {
			// URL resolution defaults to MusicBrainz (free, no API key). If a
			// Brave key is set, we fall back to that — kept for parity while
			// evaluating MusicBrainz coverage on real data.
			var resolver fallback.URLResolver = fallback.NewMusicBrainzClient("").WithPool(pool)
			if cfg.BraveSearchAPIKey != "" {
				resolver = fallback.NewBraveClient(cfg.BraveSearchAPIKey)
			}
			fallbackChain = &fallback.Chain{
				Pool:     pool,
				Fetcher:  fallback.NewFetcher(pool),
				Resolver: resolver,
				Songkick: fallback.NewSongkickClient(cfg.SongkickAPIKey),
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
		}
		savedH = &webhttp.SavedConcertsHandler{Pool: pool, FallbackLocation: fallbackLoc}
		accountH = &webhttp.AccountHandler{Pool: pool, Tokens: tokenSvc}
		subscribedH = &webhttp.SubscribedArtistsHandler{
			Pool:    pool,
			Spotify: spotifyClient,
			Tokens:  tokenSvc,
		}

		// Wire the login-success hook so new sessions trigger a pre-warm
		// snapshot job. Uses a detached background context so a browser
		// disconnect mid-callback doesn't cancel the enqueue.
		authDeps.OnLoginSuccess = func(_ context.Context, userID uuid.UUID) {
			loc := fallbackLoc
			if ul, hit, err := db.GetUserLocation(context.Background(), pool, userID); err == nil && hit {
				loc = concerts.Location{Latitude: ul.Latitude, Longitude: ul.Longitude, RadiusMiles: ul.RadiusMiles}
			}
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

	r.Route("/api", func(api chi.Router) {
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
		api.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		api.Get("/site-info", (&webhttp.SiteInfoHandler{
			ContactEmail:  cfg.ContactEmail,
			EffectiveDate: "2026-07-29",
		}).Get)
		if authDeps == nil {
			return
		}
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
		api.Get("/unsubscribe", unsubscribeH.Get)
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
			r.Get("/artists/search", subscribedH.SearchArtists)
			r.Put("/email-prefs", emailPrefsH.Put)
			r.Delete("/account", accountH.Delete)
		})
	})

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

func requestLogger(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			l.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"req_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
