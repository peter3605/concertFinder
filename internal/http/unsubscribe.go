package http

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"html"
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
//
//	base64(user_id_bytes || issued_at_unix_be) + "." + base64(hmac_sha256(...))
//
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

// Get renders a confirmation page with a button that POSTs back here. It
// deliberately changes nothing.
//
// Unsubscribing on GET is how you get people silently unsubscribed without
// them ever clicking: corporate mail security (Outlook Safe Links, scanning
// gateways, some antivirus) fetches every URL in an inbound message to check
// it, and a state-changing GET happily honors that fetch. The mutation lives
// on POST, which those scanners do not issue — and which is also exactly what
// RFC 8058 one-click unsubscribe sends, so the List-Unsubscribe-Post header
// on our outgoing mail lands on the same handler with no extra work.
func (h *UnsubscribeHandler) Get(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, ok := h.verify(token); !ok {
		http.Error(w, "invalid or expired unsubscribe link", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unsubscribe</title></head>
<body style="font-family:-apple-system,Helvetica,Arial,sans-serif;max-width:520px;margin:4rem auto;line-height:1.5;color:#222">
<h2>Unsubscribe from ConcertFinder email?</h2>
<p>This turns off both the daily digest and instant new-show alerts. Your account and saved shows are not affected.</p>
<form method="POST" action="/api/unsubscribe">
<input type="hidden" name="token" value="%s">
<button type="submit" style="font:inherit;padding:0.6rem 1.1rem;border:0;border-radius:6px;background:#1db954;color:#fff;cursor:pointer">Unsubscribe me</button>
</form>
</body></html>`, html.EscapeString(token))
}

// Post performs the opt-out. Reads the token from the query string or the
// form body, so both the confirmation page above and a mail client's
// one-click POST (which repeats the URL verbatim) work.
func (h *UnsubscribeHandler) Post(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		_ = r.ParseForm()
		token = r.PostFormValue("token")
	}
	userID, ok := h.verify(token)
	if !ok {
		http.Error(w, "invalid or expired unsubscribe link", http.StatusBadRequest)
		return
	}
	// Both flags, not just the digest — see db.OptOutAllEmail.
	if err := db.OptOutAllEmail(r.Context(), h.Pool, userID); err != nil {
		slog.Error("unsubscribe: db write failed", "err", err, "user", userID)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	slog.Info("unsubscribed from all email", "user", userID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unsubscribed</title></head>
<body style="font-family:-apple-system,Helvetica,Arial,sans-serif;max-width:520px;margin:4rem auto;line-height:1.5;color:#222">
<h2>Unsubscribed</h2>
<p>You&rsquo;ll no longer receive daily digests or instant new-show alerts from ConcertFinder.</p>
<p>You can turn either back on any time by logging in and changing your email settings.</p>
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
