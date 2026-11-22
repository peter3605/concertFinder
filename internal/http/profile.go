package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/auth"
	"github.com/peterho/concertfinder/internal/db"
)

// maxDisplayNameRunes bounds a self-chosen name. It is rendered in the header,
// in mail greetings and in the delete confirmation, none of which wants a
// paragraph.
const maxDisplayNameRunes = 64

// ProfileHandler exposes PUT /me/profile, which sets the display name.
//
// Spotify sign-in has always supplied one. Email sign-in (CF-33) never does,
// and Apple sends a name only on the first authorization and only if the
// person shares it, so an account could exist with none. That was not
// cosmetic: DELETE /me/account confirms against the display name and refuses
// an empty one, so such an account had no way to delete itself. Onboarding
// asks for a name, and this is where it lands.
//
// A Spotify account's name is rewritten from Spotify at each sign-in
// (db.UpsertUserWithAdmission), so the web client offers this only to
// accounts without a Spotify identity rather than accept an edit that the
// next login silently undoes.
type ProfileHandler struct {
	Pool *pgxpool.Pool
}

type profileRequest struct {
	DisplayName string `json:"display_name"`
}

// cleanDisplayName trims a submitted name and rejects what should never be
// stored: nothing at all, too much, or control characters. The last matters
// because the name reaches mail headers; sanitizeHeader strips CR/LF there,
// but refusing them at the door keeps the stored value honest everywhere else.
func cleanDisplayName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxDisplayNameRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

func (h *ProfileHandler) Put(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.UserFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req profileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	name, ok := cleanDisplayName(req.DisplayName)
	if !ok {
		http.Error(w, "display_name must be 1 to 64 characters", http.StatusBadRequest)
		return
	}
	if err := db.SetDisplayName(r.Context(), h.Pool, u.ID, name); err != nil {
		slog.Error("display name update failed", "err", err, "user", u.ID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, profileRequest{DisplayName: name})
}
