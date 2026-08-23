package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

type ctxKey struct{}
type fullUserKey struct{}
type mechanismKey struct{}

// Mechanism records how a request proved its identity. CSRF reads it: the
// attack it defends against is a browser attaching a cookie to a request the
// user did not intend, which cannot happen when the credential arrived in a
// header the browser will not send ambiently. See CSRF for why this — and not
// a User-Agent sniff — is the only safe signal.
type Mechanism int

const (
	// MechanismNone means no authentication was resolved.
	MechanismNone Mechanism = iota
	// MechanismCookie is the browser path: cf_session, ambient authority.
	MechanismCookie
	// MechanismBearer is the native-client path: Authorization: Bearer.
	MechanismBearer
)

type CurrentUser struct {
	ID            uuid.UUID
	SpotifyUserID string
	DisplayName   string
	SessionID     string
}

func withUser(ctx context.Context, u CurrentUser) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// withFullUser stashes the whole db.User next to CurrentUser so downstream
// handlers that need every field (email, digest prefs, encrypted token) can
// skip a second GetUserByID query. Keeping the two contexts distinct lets
// handlers that only need the identity subset stay lightweight.
func withFullUser(ctx context.Context, u db.User) context.Context {
	return context.WithValue(ctx, fullUserKey{}, u)
}

// UserFromContext returns the authenticated user, or (zero, false) if none.
func UserFromContext(ctx context.Context) (CurrentUser, bool) {
	u, ok := ctx.Value(ctxKey{}).(CurrentUser)
	return u, ok
}

// FullUserFromContext returns the complete db.User the middleware fetched
// during session resolution, or (zero, false) if no request went through
// RequireUser.
func FullUserFromContext(ctx context.Context) (db.User, bool) {
	u, ok := ctx.Value(fullUserKey{}).(db.User)
	return u, ok
}

func withMechanism(ctx context.Context, m Mechanism) context.Context {
	return context.WithValue(ctx, mechanismKey{}, m)
}

// MechanismFromContext reports how RequireUser authenticated the request.
// Returns MechanismNone when the request did not pass through RequireUser,
// which is the safe default: CSRF treats anything that is not explicitly
// MechanismBearer as needing the token check.
func MechanismFromContext(ctx context.Context) Mechanism {
	m, ok := ctx.Value(mechanismKey{}).(Mechanism)
	if !ok {
		return MechanismNone
	}
	return m
}

// BearerToken extracts the credential from an Authorization header, or ""
// when the header is absent or is not a Bearer challenge. The scheme match is
// case-insensitive per RFC 7235 §2.1.
func BearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// sessionCredential resolves the session ID a request is presenting, and by
// which mechanism. The header is checked first so a native client is never
// at the mercy of a stray cookie, but the order is not load-bearing: a
// request carrying both is a browser that also set a header, and honouring
// the header there is strictly the safer read (it is the one an attacker
// cannot cause).
func sessionCredential(r *http.Request) (string, Mechanism) {
	if tok := BearerToken(r); tok != "" {
		return tok, MechanismBearer
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value, MechanismCookie
	}
	return "", MechanismNone
}

// SessionTouchInterval is how stale last_seen_at must get before we write
// it again. The column feeds a 14-day "recently active" window in the
// nightly fanout workers, so minute-level precision is worthless — but
// writing it on every request meant the concerts page, which polls every
// 10s, dirtied a row 6 times a minute per user. At that rate the write
// amplification and autovacuum churn cost more than the signal is worth.
const SessionTouchInterval = 15 * time.Minute

// RequireUser is middleware that resolves a session → user and attaches it
// to the request context. On miss, responds 401.
//
// The credential is read from Authorization: Bearer first and cf_session
// second. Both carry the same value — the session ID is a 32-byte random
// string and *is* the credential in either form — so the native client needs
// no new table, no second token type, and no separate expiry. What differs is
// only the mechanism, which is recorded on the context for CSRF to read.
func RequireUser(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sessID, mech := sessionCredential(r)
			if sessID == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// One round trip for session + user; both are needed on every
			// authenticated request.
			su, err := db.GetSessionUser(r.Context(), pool, sessID)
			if err != nil {
				if errors.Is(err, db.ErrNoRows) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				http.Error(w, "session lookup failed", http.StatusInternalServerError)
				return
			}
			sess, user := su.Session, su.User

			// Best-effort, throttled touch; do not block on error. We
			// already know last_seen_at from the join, so most requests
			// skip the write entirely rather than issuing a conditional
			// UPDATE that would round-trip anyway.
			if time.Since(sess.LastSeenAt) > SessionTouchInterval {
				_ = db.TouchSession(r.Context(), pool, sess.ID)
			}

			ctx := withUser(r.Context(), CurrentUser{
				ID:            user.ID,
				SpotifyUserID: user.SpotifyUserID,
				DisplayName:   user.DisplayName,
				SessionID:     sess.ID,
			})
			ctx = withFullUser(ctx, user)
			ctx = withMechanism(ctx, mech)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
