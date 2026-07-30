package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// UnsubscribeTokenMaxAge is how long a signed unsubscribe link is valid.
// After that, the user has to log in and change prefs in the UI. Sends a
// clear signal that an intercepted-years-later email link can't nuke a
// subscription.
const UnsubscribeTokenMaxAge = 90 * 24 * time.Hour

// UnsubscribeHandler handles GET /api/me/unsubscribe?token=... — the link
// embedded in every digest email. Unauthenticated on purpose (users click
// from Gmail on their phone, no session cookie), so the token is HMAC-signed
// with the app's ENCRYPTION_KEY: only we can generate valid tokens.
type UnsubscribeHandler struct {
	Pool   *pgxpool.Pool
	Secret []byte // ENCRYPTION_KEY bytes
}

// Token returns a URL-safe token that binds a user ID + issued-at timestamp
// with an HMAC. Format:
//   base64(user_id_bytes || issued_at_unix_be) + "." + base64(hmac_sha256(...))
// The timestamp is included so tokens can be aged out; see UnsubscribeTokenMaxAge.
func (h *UnsubscribeHandler) Token(userID uuid.UUID) string {
	id, _ := userID.MarshalBinary()
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(time.Now().Unix()))
	payload := append(append([]byte{}, id...), ts[:]...)
	mac := hmac.New(sha256.New, h.Secret)
	mac.Write(payload)
	sum := mac.Sum(nil)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sum)
}

// Get flips digest_opt_in to false for the token's user and shows a plain
// confirmation page.
func (h *UnsubscribeHandler) Get(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	userID, ok := h.verify(token)
	if !ok {
		http.Error(w, "invalid or expired unsubscribe link", http.StatusBadRequest)
		return
	}
	if err := db.SetDigestOptIn(r.Context(), h.Pool, userID, false); err != nil {
		slog.Error("unsubscribe: db write failed", "err", err, "user", userID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html><body style="font-family:-apple-system,Helvetica,Arial,sans-serif;max-width:520px;margin:4rem auto;line-height:1.5;color:#222">
<h2>Unsubscribed</h2>
<p>You&rsquo;ll no longer receive daily digest emails from ConcertFinder.</p>
<p>You can turn them back on any time by logging in and toggling the digest option in settings.</p>
</body></html>`))
}

func (h *UnsubscribeHandler) verify(token string) (uuid.UUID, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return uuid.Nil, false
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(parts[0])
	if err != nil {
		return uuid.Nil, false
	}
	sigGiven, err := enc.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, false
	}
	mac := hmac.New(sha256.New, h.Secret)
	mac.Write(payload)
	sigExpect := mac.Sum(nil)
	if !hmac.Equal(sigGiven, sigExpect) {
		return uuid.Nil, false
	}
	// payload = 16 UUID bytes + 8 unix-seconds bytes
	if len(payload) != 24 {
		return uuid.Nil, false
	}
	var id uuid.UUID
	if err := id.UnmarshalBinary(payload[:16]); err != nil {
		return uuid.Nil, false
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(payload[16:])), 0)
	if time.Since(issued) > UnsubscribeTokenMaxAge {
		return uuid.Nil, false
	}
	return id, true
}
