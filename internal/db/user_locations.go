package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type UserLocation struct {
	UserID      uuid.UUID
	Latitude    float64
	Longitude   float64
	RadiusMiles int
}

// GetUserLocation returns (row, true, nil) on hit, (zero, false, nil) on miss.
func GetUserLocation(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (UserLocation, bool, error) {
	const q = `SELECT user_id, latitude, longitude, radius_miles FROM user_locations WHERE user_id = $1`
	var l UserLocation
	err := pool.QueryRow(ctx, q, userID).Scan(&l.UserID, &l.Latitude, &l.Longitude, &l.RadiusMiles)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return UserLocation{}, false, nil
		}
		return UserLocation{}, false, err
	}
	return l, true, nil
}

// RecordLocationVisit claims one of the user's daily location slots and
// reports whether they may use this location today.
//
// The thing being bounded is a *set*, not a count: a scan is keyed by
// (user, location_key), so what costs the deployment is the number of distinct
// keys an account opens, and revisiting one it already opened today costs
// nothing at all. Storing set membership is what makes that true for free —
// someone toggling between home and work spends two slots on their first
// morning and none ever again that day. A counter (a second use of
// rate_ledger, say) could not tell the two cases apart and would lock a
// commuter out by lunchtime.
//
// One statement, so the count and the insert cannot straddle a concurrent
// write from the same user's other tab. Postgres evaluates every CTE against
// the statement's snapshot, so the second EXISTS cannot see the row the first
// one just wrote — which is why the two are OR'd rather than one being enough.
func RecordLocationVisit(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, locationKey string, dailyCap int) (bool, error) {
	const q = `
WITH today AS (SELECT (now() AT TIME ZONE 'utc')::date AS d),
inserted AS (
  INSERT INTO user_location_visits (user_id, day, location_key)
  -- Explicit casts because the parameters first appear inside an
  -- INSERT ... SELECT, where Postgres resolves types from the SELECT rather
  -- than from the target columns and would otherwise refuse the statement
  -- with "could not determine data type of parameter".
  SELECT $1::uuid, today.d, $2::text FROM today
  WHERE (
    SELECT count(*) FROM user_location_visits v, today
     WHERE v.user_id = $1 AND v.day = today.d
  ) < $3::int
  ON CONFLICT (user_id, day, location_key) DO NOTHING
  RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM inserted)
    OR EXISTS (
      SELECT 1 FROM user_location_visits v, today
       WHERE v.user_id = $1 AND v.day = today.d AND v.location_key = $2
    )
`
	var allowed bool
	if err := pool.QueryRow(ctx, q, userID, locationKey, dailyCap).Scan(&allowed); err != nil {
		return false, err
	}
	return allowed, nil
}

// PruneLocationVisits drops visit rows older than the retention window
// (days). They are daily slots and mean nothing once their day is over, but
// nothing else removes them and one accumulates per location per user per
// day.
func PruneLocationVisits(ctx context.Context, pool *pgxpool.Pool, days int) (int64, error) {
	const q = `DELETE FROM user_location_visits WHERE day < (now() - make_interval(days => $1))::date`
	tag, err := pool.Exec(ctx, q, days)
	return tag.RowsAffected(), err
}

func UpsertUserLocation(ctx context.Context, pool *pgxpool.Pool, l UserLocation) error {
	const q = `
INSERT INTO user_locations (user_id, latitude, longitude, radius_miles, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE SET
  latitude     = EXCLUDED.latitude,
  longitude    = EXCLUDED.longitude,
  radius_miles = EXCLUDED.radius_miles,
  updated_at   = now()
`
	_, err := pool.Exec(ctx, q, l.UserID, l.Latitude, l.Longitude, l.RadiusMiles)
	return err
}
