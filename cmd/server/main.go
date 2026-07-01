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

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/bandsintown"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/fallback"
	webhttp "github.com/peterho/concertfinder/internal/http"
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

	migCtx, migCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := db.Migrate(migCtx, pool, "migrations"); err != nil {
		migCancel()
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	migCancel()

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	if encKey, err := auth.DecodeKey(cfg.EncryptionKey); err != nil {
		logger.Warn("auth routes disabled: ENCRYPTION_KEY missing or invalid", "err", err)
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
		deps := &auth.Deps{
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
		r.Route("/auth", func(r chi.Router) { auth.Mount(r, deps) })

		affinitySvc := &webhttp.AffinityService{
			Pool:    pool,
			Tokens:  tokenSvc,
			Spotify: spotifyClient,
			TTL:     24 * time.Hour,
		}
		ticketHTTP := &http.Client{Timeout: 10 * time.Second}
		tmClient := ticketmaster.NewClient(ticketHTTP, cfg.TicketmasterAPIKey)
		bitClient := bandsintown.NewClient(ticketHTTP, cfg.BandsintownAppID)

		affinityH := &webhttp.AffinityHandler{Service: affinitySvc}

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

		concertsH := &webhttp.ConcertsHandler{
			Affinity: affinitySvc,
			Pool:     pool,
			TM:       tmClient,
			BIT:      bitClient,
			Location: concerts.Location{
				Latitude:    cfg.UserLatitude,
				Longitude:   cfg.UserLongitude,
				RadiusMiles: cfg.UserRadiusMiles,
			},
			Fallback:         fallbackChain,
			MinFallbackScore: cfg.Phase2MinScore,
		}
		r.Route("/me", func(r chi.Router) {
			r.Use(auth.RequireUser(pool))
			r.Get("/affinity", affinityH.Get)
			r.Get("/concerts", concertsH.Get)
		})
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", "err", err)
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
