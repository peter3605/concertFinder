package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/peterho/concertfinder/internal/apple"
	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// AppleRevokeMaxAttempts is generous on purpose. This job is the only path
// by which a deleted account's Apple grant is withdrawn, and nobody is
// waiting on it, so it should outlast an Apple outage or a rotated key that
// takes a day to fix. River's backoff (attempt^4 seconds) puts the last of
// these several days out.
const AppleRevokeMaxAttempts = 15

// RevokeAppleTokenArgs revokes one queued Apple refresh token
// (apple_revocations, migration 0029). It is inserted by db.DeleteUser's
// enqueue hook inside the transaction that deletes the account, so the job
// and its queue row commit together. It carries only the row's id: the
// token stays in the encrypted column and never enters river_job's args.
type RevokeAppleTokenArgs struct {
	RevocationID uuid.UUID `json:"revocation_id"`
}

func (RevokeAppleTokenArgs) Kind() string { return "revoke_apple_token" }

func (RevokeAppleTokenArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		MaxAttempts: AppleRevokeMaxAttempts,
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
				rivertype.JobStateRetryable,
			},
		},
	}
}

// AppleRevoker is the one call the worker makes; *apple.Client satisfies it.
type AppleRevoker interface {
	Revoke(ctx context.Context, clientID, refreshToken string) error
}

// RevokeAppleTokenWorker calls Apple's /auth/revoke for a queued token.
// Success, or Apple saying the token is already invalid, removes the row.
// Anything else is recorded on the row and returned, so river retries it.
type RevokeAppleTokenWorker struct {
	river.WorkerDefaults[RevokeAppleTokenArgs]
	Pool   *pgxpool.Pool
	Apple  AppleRevoker
	EncKey []byte
}

func (w *RevokeAppleTokenWorker) Timeout(*river.Job[RevokeAppleTokenArgs]) time.Duration {
	return 30 * time.Second
}

func (w *RevokeAppleTokenWorker) Work(ctx context.Context, job *river.Job[RevokeAppleTokenArgs]) error {
	id := job.Args.RevocationID
	rev, ok, err := db.GetAppleRevocation(ctx, w.Pool, id)
	if err != nil {
		return err
	}
	if !ok {
		return nil // already done
	}
	if w.Apple == nil {
		// Apple was configured when the token was stored and is not now.
		// Erroring keeps the row and lets river retry, so restoring the
		// configuration still revokes it.
		_ = db.RecordAppleRevocationFailure(ctx, w.Pool, id, "Sign in with Apple is not configured")
		return fmt.Errorf("revoke apple token %s: Sign in with Apple is not configured", id)
	}
	token, err := auth.DecryptToken(w.EncKey, rev.EncryptedRefreshToken, rev.RefreshTokenNonce)
	if err != nil {
		// Not retryable by waiting, but kept: the row is the evidence.
		_ = db.RecordAppleRevocationFailure(ctx, w.Pool, id, "decrypt: "+err.Error())
		return river.JobCancel(fmt.Errorf("revoke apple token %s: decrypt: %w", id, err))
	}
	err = w.Apple.Revoke(ctx, rev.ClientID, string(token))
	if err != nil && !apple.IsInvalidGrant(err) {
		_ = db.RecordAppleRevocationFailure(ctx, w.Pool, id, err.Error())
		slog.Warn("apple revoke failed; will retry", "revocation", id, "attempt", job.Attempt, "err", err)
		return fmt.Errorf("revoke apple token %s: %w", id, err)
	}
	if err := db.CompleteAppleRevocation(ctx, w.Pool, id); err != nil {
		return err
	}
	slog.Info("apple token revoked", "revocation", id, "already_invalid", err != nil)
	return nil
}
