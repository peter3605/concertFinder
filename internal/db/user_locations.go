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
