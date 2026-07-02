package jobs

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/affinity"
	"github.com/peterho/concertfinder/internal/db"
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
