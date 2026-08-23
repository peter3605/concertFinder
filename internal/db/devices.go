package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device is one APNs registration. A user can have several — an iPhone and an
// iPad, or one device across a reinstall — so the push worker fans out over
// every live row rather than assuming a single token.
type Device struct {
	UserID      uuid.UUID
	DeviceToken string
	Environment string // "sandbox" | "production"
	LastSeenAt  time.Time
	DisabledAt  *time.Time
}

// APNs environments. A token minted against one host is meaningless to the
// other, so this travels with every row rather than being read from config at
// send time — a TestFlight build and a debug build register different tokens
// for the same account.
const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

// UpsertDevice registers a token or refreshes an existing one. Idempotent by
// design: the app calls it on every launch, not just on first permission
// grant, because APNs rotates tokens without telling the user.
//
// A re-registration clears disabled_at. A token previously retired on a 410
// can legitimately come back — the user reinstalled, or re-granted permission
// — and leaving it disabled would silently mute that device forever.
func UpsertDevice(ctx context.Context, pool *pgxpool.Pool, d Device) error {
	const q = `
INSERT INTO user_devices (user_id, device_token, environment)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, device_token) DO UPDATE SET
  environment  = EXCLUDED.environment,
  last_seen_at = now(),
  disabled_at  = NULL
`
	_, err := pool.Exec(ctx, q, d.UserID, d.DeviceToken, d.Environment)
	return err
}

// DeleteDevice deregisters one token. Called on logout — the session ends but
// the OS-level registration does not, so without this the device keeps
// receiving pushes for an account nobody is signed into.
func DeleteDevice(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, token string) error {
	const q = `DELETE FROM user_devices WHERE user_id = $1 AND device_token = $2`
	_, err := pool.Exec(ctx, q, userID, token)
	return err
}

// ListLiveDevices returns the user's non-disabled tokens.
func ListLiveDevices(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) ([]Device, error) {
	const q = `
SELECT user_id, device_token, environment, last_seen_at
FROM user_devices
WHERE user_id = $1 AND disabled_at IS NULL
ORDER BY last_seen_at DESC
`
	rows, err := pool.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.UserID, &d.DeviceToken, &d.Environment, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DisableDevice retires a token APNs has rejected as permanently invalid
// (410 Gone, or BadDeviceToken). Stamped rather than deleted so the janitor
// prunes on its own schedule and a re-registration can revive the row.
func DisableDevice(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, token string) error {
	const q = `
UPDATE user_devices SET disabled_at = now()
WHERE user_id = $1 AND device_token = $2 AND disabled_at IS NULL
`
	_, err := pool.Exec(ctx, q, userID, token)
	return err
}

// PruneDisabledDevices drops tokens retired more than `days` ago. Janitor
// step. The delay is deliberate: a device that reinstalls within the window
// re-registers onto its existing row instead of racing a delete.
func PruneDisabledDevices(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM user_devices WHERE disabled_at IS NOT NULL AND disabled_at < now() - make_interval(days => $1)`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

// SetPushOptIn updates the user's push preference. Separate from the email
// flags on purpose — see migration 0016.
func SetPushOptIn(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, optIn bool) error {
	const q = `UPDATE users SET push_opt_in = $2, updated_at = now() WHERE id = $1`
	tag, err := pool.Exec(ctx, q, id, optIn)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoRows
	}
	return nil
}
