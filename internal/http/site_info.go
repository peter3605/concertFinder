package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// SiteInfoHandler serves GET /api/site-info: small public bootstrap payload
// consumed by the frontend to fill in operator-specific values (contact
// email, effective date) on static content pages. Unauthenticated.
type SiteInfoHandler struct {
	ContactEmail string
	// EffectiveDate is the "as of" date for privacy/terms text. Injected at
	// build/run time rather than hardcoded in the React source so the pages
	// stay honest as content evolves.
	EffectiveDate string
	// MinIOSBuild is the oldest iOS build this server still supports. The
	// app compares it on launch and can show a blocking "update required"
	// screen. This is the escape hatch that makes a breaking server change
	// survivable once builds are in people's pockets for months — it is
	// cheap to add now and impossible to add retroactively.
	MinIOSBuild int
	// InviteRequired tells a client whether to offer an invite-code box on
	// its sign-in screen. It is a hint for rendering, never the gate itself
	// -- the gate is in the auth callback, which is the only place that can
	// tell a signup from a returning user.
	InviteRequired bool
	// Pool and MaxSpotifyUsers answer spotify_signups_open: whether a new
	// Spotify account could be created right now (SPOTIFY_MAX_USERS, with
	// db.NoSpotifyCap meaning no cap). Like InviteRequired it only decides
	// whether the sign-up screen offers Spotify. The gate itself stays in
	// db.UpsertUserWithAdmission, which recounts under a lock, so a stale
	// answer here costs a refusal message, never an extra account. A nil
	// Pool reports false.
	Pool            *pgxpool.Pool
	MaxSpotifyUsers int
	// AppleSignIn reports whether /api/auth/apple/* is mounted. A rendering
	// hint like the two above: the routes are what decide, and a client that
	// guessed wrong would show a button that leads to a 404, not let anyone in.
	AppleSignIn bool
}

// spotifySignupsOpen fails closed: an unreadable count hides the Spotify
// button on the sign-up tab rather than sending a new person through
// Spotify's consent screen to be refused on the way back. Returning users
// are unaffected, because the sign-in tab offers Spotify unconditionally.
func spotifySignupsOpen(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, maxUsers int) bool {
	if maxUsers == db.NoSpotifyCap {
		return true
	}
	if maxUsers <= 0 {
		return false
	}
	n, err := db.CountSpotifyUsers(ctx, q)
	if err != nil {
		slog.Warn("site-info: spotify user count failed", "err", err)
		return false
	}
	return n < maxUsers
}

func (h *SiteInfoHandler) Get(w http.ResponseWriter, r *http.Request) {
	effective := h.EffectiveDate
	if effective == "" {
		effective = time.Now().UTC().Format("2006-01-02")
	}
	writeJSON(w, map[string]any{
		"contact_email":   h.ContactEmail,
		"effective_date":  effective,
		"min_ios_build":   h.MinIOSBuild,
		"invite_required": h.InviteRequired,
		// Additive, like invite_required: iOS builds decode this payload.
		"spotify_signups_open": h.spotifySignupsOpen(r.Context()),
		"apple_sign_in":        h.AppleSignIn,
	})
}

func (h *SiteInfoHandler) spotifySignupsOpen(ctx context.Context) bool {
	if h.Pool == nil {
		return h.MaxSpotifyUsers == db.NoSpotifyCap
	}
	return spotifySignupsOpen(ctx, h.Pool, h.MaxSpotifyUsers)
}
