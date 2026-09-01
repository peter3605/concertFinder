package http

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// DevicesHandler serves POST /me/devices and DELETE /me/devices/{token}: the
// APNs registration the push worker fans out over.
type DevicesHandler struct {
	Pool *pgxpool.Pool
}

type deviceRequest struct {
	DeviceToken string `json:"device_token"`
	// Environment must match the build's aps-environment entitlement. The
	// client sends it rather than the server assuming, because a TestFlight
	// build and a debug build of the same version register against different
	// APNs hosts and a token from one is invalid at the other.
	Environment string `json:"environment"`
}

// Post registers or refreshes a device token. Idempotent: the app calls it on
// every launch, not just on first permission grant, because APNs rotates
// tokens silently and a stale one fails as BadDeviceToken.
func (h *DevicesHandler) Post(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req deviceRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.DeviceToken == "" {
		http.Error(w, "device_token is required", http.StatusBadRequest)
		return
	}
	// Default rather than reject: an older build that predates the field
	// should still register, and production is the safe assumption for a
	// shipped app.
	env := req.Environment
	if env != db.EnvSandbox && env != db.EnvProduction {
		env = db.EnvProduction
	}
	if err := db.UpsertDevice(r.Context(), h.Pool, db.Device{
		UserID:      u.ID,
		DeviceToken: req.DeviceToken,
		Environment: env,
	}); err != nil {
		slog.Error("device register failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Delete deregisters a token, called on logout. The session ends but the
// OS-level registration does not, so without this the device keeps receiving
// pushes for an account nobody is signed into.
func (h *DevicesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := chi.URLParam(r, "token")
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	if err := db.DeleteDevice(r.Context(), h.Pool, u.ID, token); err != nil {
		slog.Error("device deregister failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
