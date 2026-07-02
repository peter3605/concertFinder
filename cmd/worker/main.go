// Worker binary: runs river-backed background jobs. Currently one periodic
// schedule (daily fanout of affinity refreshes to active users) plus the
// per-user compute job it enqueues.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/jobs"
	"github.com/peterho/concertfinder/internal/spotify"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, "migrations"); err != nil {
		logger.Error("app migrations failed", "err", err)
		os.Exit(1)
	}
	driver := riverpgxv5.New(pool)
	migrator, err := rivermigrate.New(driver, nil)
	if err != nil {
		logger.Error("river migrator init failed", "err", err)
		os.Exit(1)
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		logger.Error("river migrations failed", "err", err)
		os.Exit(1)
	}

	encKey, err := auth.DecodeKey(cfg.EncryptionKey)
	if err != nil {
		logger.Error("ENCRYPTION_KEY invalid", "err", err)
		os.Exit(1)
	}
	spotifyClient := spotify.NewClient(&http.Client{Timeout: 30 * time.Second})
	tokenSvc := &auth.TokenService{
		Pool:       pool,
		EncKey:     encKey,
		ClientID:   cfg.SpotifyClientID,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}
	affinitySvc := &affinity.Service{
		Pool:    pool,
		Tokens:  tokenSvc,
		Spotify: spotifyClient,
		TTL:     24 * time.Hour,
	}

	workers := river.NewWorkers()
	river.AddWorker(workers, &jobs.RefreshAffinityWorker{Pool: pool, Affinity: affinitySvc})

	// Fanout needs the client itself to enqueue per-user jobs; set the field
	// after we construct the client below.
	fanoutW := &jobs.FanoutAffinityRefreshWorker{Pool: pool}
	river.AddWorker(workers, fanoutW)

	periodic := []*river.PeriodicJob{
		river.NewPeriodicJob(
			river.PeriodicInterval(24*time.Hour),
			func() (river.JobArgs, *river.InsertOpts) { return jobs.FanoutAffinityRefreshArgs{}, nil },
			&river.PeriodicJobOpts{RunOnStart: false},
		),
	}

	client, err := river.NewClient[pgx.Tx](driver, &river.Config{
		Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers:      workers,
		PeriodicJobs: periodic,
	})
	if err != nil {
		logger.Error("river client init failed", "err", err)
		os.Exit(1)
	}
	fanoutW.Client = client

	if err := client.Start(ctx); err != nil {
		logger.Error("river start failed", "err", err)
		os.Exit(1)
	}
	logger.Info("worker running")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdown, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := client.Stop(shutdown); err != nil {
		logger.Error("river stop error", "err", err)
	}
}
