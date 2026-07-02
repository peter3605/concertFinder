package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/geocoding"
)

// LocationHandler serves GET/PUT /me/location.
type LocationHandler struct {
	Pool      *pgxpool.Pool
	Geocoder  *geocoding.Client
	FallbackLocation concerts.Location // used when user has no row yet
}

type locationDTO struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	RadiusMiles int     `json:"radius_miles"`
	DisplayName string  `json:"display_name,omitempty"`
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
		})
		return
	}
	writeJSON(w, locationDTO{Latitude: l.Latitude, Longitude: l.Longitude, RadiusMiles: l.RadiusMiles})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	default:
		http.Error(w, "supply query OR (latitude, longitude)", http.StatusBadRequest)
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
