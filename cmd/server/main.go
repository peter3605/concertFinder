package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/fallback"
	"github.com/peterho/concertfinder/internal/geocoding"
	webhttp "github.com/peterho/concertfinder/internal/http"
	"github.com/peterho/concertfinder/internal/http/spa"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/search"
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
		searches     *search.Manager
		riverClient  *river.Client[pgx.Tx]
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
			Handshakes:    auth.NewHandshakeStore(),
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
		bitClient := bandsintown.NewClient(ticketHTTP, cfg.BandsintownAppID)

		affinityH = &webhttp.AffinityHandler{Service: affinitySvc}

		var fallbackChain concerts.Fallbacker
		if cfg.Phase2Enabled {
			fallbackChain = &fallback.Chain{
				Pool:     pool,
				Fetcher:  fallback.NewFetcher(pool),
				Brave:    fallback.NewBraveClient(cfg.BraveSearchAPIKey),
				Songkick: fallback.NewSongkickClient(cfg.SongkickAPIKey),
			}
			logger.Info("phase 2 fallbacks enabled",
				"min_score", cfg.Phase2MinScore,
				"brave_key_set", cfg.BraveSearchAPIKey != "",
				"songkick_key_set", cfg.SongkickAPIKey != "",
			)
		}

		fallbackLoc := concerts.Location{
			Latitude:    cfg.UserLatitude,
			Longitude:   cfg.UserLongitude,
			RadiusMiles: cfg.UserRadiusMiles,
		}
		searches = search.NewManager()

		concertsH = &webhttp.ConcertsHandler{
			Affinity:         affinitySvc,
			Pool:             pool,
			TM:               tmClient,
			BIT:              bitClient,
			FallbackLocation: fallbackLoc,
			Fallback:         fallbackChain,
			MinFallbackScore: cfg.Phase2MinScore,
			Searches:         searches,
		}
		locationH = &webhttp.LocationHandler{
			Pool:             pool,
			Geocoder:         geocoding.NewClient(""),
			FallbackLocation: fallbackLoc,
		}

		// Background jobs live in the same process — no separate worker
		// binary. See docs/design.md §10.2 (option 2 rationale in commit).
		workers := river.NewWorkers()
		river.AddWorker(workers, &jobs.RefreshAffinityWorker{Pool: pool, Affinity: affinitySvc})
		fanoutW := &jobs.FanoutAffinityRefreshWorker{Pool: pool}
		river.AddWorker(workers, fanoutW)
		periodic := []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(24*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return jobs.FanoutAffinityRefreshArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: false},
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
		fanoutW.Client = riverClient
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Route("/api", func(api chi.Router) {
		api.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})
		if authDeps == nil {
			return
		}
		api.Route("/auth", func(r chi.Router) { auth.Mount(r, authDeps) })
		api.Route("/me", func(r chi.Router) {
			r.Use(auth.RequireUser(pool))
			r.Get("/affinity", affinityH.Get)
			r.Get("/concerts", concertsH.Get)
			r.Get("/location", locationH.Get)
			r.Put("/location", locationH.Put)
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
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
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
	if searches != nil {
		searches.Shutdown()
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
