package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/geocoding"
	"github.com/peterho/concertfinder/internal/jobs"
)

// secondsUntilNextUTCDay is how long until the daily allowances reset. Never
// less than one second, so a Retry-After of "0" can't invite an immediate
// retry that is guaranteed to fail again.
func secondsUntilNextUTCDay(now time.Time) int {
	next := startOfUTCDay(now).Add(24 * time.Hour)
	if s := int(next.Sub(now).Seconds()); s > 0 {
		return s
	}
	return 1
}

// LocationHandler serves GET/PUT /me/location.
type LocationHandler struct {
	Pool             *pgxpool.Pool
	Geocoder         *geocoding.Client
	FallbackLocation concerts.Location // used when user has no row yet
}

type locationDTO struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	RadiusMiles int     `json:"radius_miles"`
	DisplayName string  `json:"display_name,omitempty"`
	// IsDefault marks a response the user never chose: the process-wide
	// USER_LATITUDE/USER_LONGITUDE fallback, served because they have no
	// user_locations row yet. Without this the client cannot distinguish
	// "somewhere I picked" from "wherever this deployment happens to be
	// configured for", so it silently presented one operator's default as the
	// user's own location and scanned there — the fallback is a real city, so
	// nothing about the response looked wrong.
	//
	// The frontend uses it to decide whether to ask the browser for real
	// coordinates on first login. It is deliberately absent (omitempty) on a
	// stored location, so a user who has chosen a location is never re-prompted.
	IsDefault bool `json:"is_default,omitempty"`
}

func (h *LocationHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	l, hit, err := db.GetUserLocation(r.Context(), h.Pool, u.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !hit {
		writeJSON(w, locationDTO{
			Latitude:    h.FallbackLocation.Latitude,
			Longitude:   h.FallbackLocation.Longitude,
			RadiusMiles: h.FallbackLocation.RadiusMiles,
			IsDefault:   true,
		})
		return
	}
	writeJSON(w, locationDTO{Latitude: l.Latitude, Longitude: l.Longitude, RadiusMiles: l.RadiusMiles})
}

// coordPrecision is the number of decimal places a saved coordinate keeps.
// Two places is ~1.1km, which is far finer than any radius this app offers can
// distinguish — the minimum is 1 mile — and coarse enough that repeated saves
// from one place land on one value.
const coordPrecision = 2

// roundCoord quantises a coordinate before it is stored.
//
// jobs.LocationKey formats coordinates to 4 decimal places (~11m) and that key
// is the identity of a snapshot. Browser geolocation returns a slightly
// different fix every time — wifi and IP positioning drift by tens or hundreds
// of metres between calls — so two saves from the same desk produced
// 38.8335,-77.2102 and 38.8333,-77.2103: two keys, two snapshots, and two
// full cold scans about 10 metres apart.
//
// That is expensive rather than merely untidy. A cold scan reserves ~2
// Ticketmaster calls per artist across 200 artists, so each spurious key costs
// ~400 of a 500/day per-user cap; two of them exhausts the day and every
// subsequent scan reports itself incomplete.
//
// Rounding happens here, at the point of save, rather than inside
// LocationKey: the stored coordinate is what the scan actually searches, so
// quantising only the key would leave the snapshot filed under one location
// while the search ran at another.
func roundCoord(v float64) float64 {
	p := math.Pow(10, coordPrecision)
	return math.Round(v*p) / p
}

type putLocationRequest struct {
	Query       string   `json:"query,omitempty"`
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	RadiusMiles int      `json:"radius_miles"`
}

// Put accepts either {query: "New York, NY"} (geocode) or explicit
// {latitude, longitude}. radius_miles is required in both.
func (h *LocationHandler) Put(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req putLocationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.RadiusMiles < 1 || req.RadiusMiles > 500 {
		http.Error(w, "radius_miles must be 1..500", http.StatusBadRequest)
		return
	}
	var (
		lat, lng    float64
		displayName string
	)
	switch {
	case req.Latitude != nil && req.Longitude != nil:
		lat, lng = *req.Latitude, *req.Longitude
		// radius_miles has been bounded since this endpoint shipped and the
		// coordinates were not, which is backwards: the radius only widens a
		// search, while a coordinate is half of the snapshot identity and
		// therefore of what gets scanned.
		if !validCoords(lat, lng) {
			http.Error(w, "latitude must be -90..90 and longitude -180..180", http.StatusBadRequest)
			return
		}
	case req.Query != "":
		res, err := h.Geocoder.Search(r.Context(), req.Query)
		if err != nil {
			if errors.Is(err, geocoding.ErrNotFound) {
				http.Error(w, "no match for that location", http.StatusNotFound)
				return
			}
			slog.Error("geocode failed", "err", err, "query", req.Query)
			http.Error(w, "geocode failed", http.StatusBadGateway)
			return
		}
		lat, lng, displayName = res.Latitude, res.Longitude, res.DisplayName
		// Nominatim is a third party and its output is the same input to the
		// same scan identity as a client-supplied pair, so it gets the same
		// check rather than the benefit of the doubt.
		if !validCoords(lat, lng) {
			slog.Warn("geocode returned an out-of-range point", "query", req.Query, "lat", lat, "lng", lng)
			http.Error(w, "no usable match for that location", http.StatusNotFound)
			return
		}
	default:
		http.Error(w, "supply query OR (latitude, longitude)", http.StatusBadRequest)
		return
	}
	lat, lng = roundCoord(lat), roundCoord(lng)

	// Claim one of today's location slots before writing anything. A scan is
	// keyed by (user, location_key), so every new location is a fresh
	// five-minute job holding one of five worker slots for the whole
	// deployment — cycling coordinates starves every other user's scans,
	// digests and pushes, with each individual job looking perfectly
	// ordinary. Recorded against the rounded coordinates and the radius, i.e.
	// exactly the key the scan will use, so a location already opened today
	// costs nothing to return to.
	locKey := jobs.LocationKey(concerts.Location{Latitude: lat, Longitude: lng, RadiusMiles: req.RadiusMiles})
	switch allowed, err := db.RecordLocationVisit(r.Context(), h.Pool, u.ID, locKey, maxDailyLocations); {
	case err != nil:
		slog.Error("location visit accounting failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case !allowed:
		// Retry-After to the next UTC midnight, which is when the allowance
		// resets — the same boundary the upstream quota ledger rolls on.
		w.Header().Set("Retry-After", strconv.Itoa(secondsUntilNextUTCDay(time.Now())))
		http.Error(w,
			"too many different locations today; returning to a location you already used today is always allowed",
			http.StatusTooManyRequests)
		return
	}

	if err := db.UpsertUserLocation(r.Context(), h.Pool, db.UserLocation{
		UserID:      u.ID,
		Latitude:    lat,
		Longitude:   lng,
		RadiusMiles: req.RadiusMiles,
	}); err != nil {
		slog.Error("save location failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, locationDTO{Latitude: lat, Longitude: lng, RadiusMiles: req.RadiusMiles, DisplayName: displayName})
}
