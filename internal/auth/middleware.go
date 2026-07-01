package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

type ctxKey struct{}

type CurrentUser struct {
	ID            uuid.UUID
	SpotifyUserID string
	DisplayName   string
	SessionID     string
}

func withUser(ctx context.Context, u CurrentUser) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFromContext returns the authenticated user, or (zero, false) if none.
func UserFromContext(ctx context.Context) (CurrentUser, bool) {
	u, ok := ctx.Value(ctxKey{}).(CurrentUser)
	return u, ok
}

// RequireUser is middleware that resolves cf_session → user and attaches it
// to the request context. On miss, responds 401.
func RequireUser(pool *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(SessionCookieName)
			if err != nil || c.Value == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			sess, err := db.GetSessionByID(r.Context(), pool, c.Value)
			if err != nil {
				if errors.Is(err, db.ErrNoRows) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				http.Error(w, "session lookup failed", http.StatusInternalServerError)
				return
			}
			user, err := db.GetUserByID(r.Context(), pool, sess.UserID)
			if err != nil {
				http.Error(w, "user lookup failed", http.StatusInternalServerError)
				return
			}
			// Best-effort touch; do not block on error.
			_ = db.TouchSession(r.Context(), pool, sess.ID)

			ctx := withUser(r.Context(), CurrentUser{
				ID:            user.ID,
				SpotifyUserID: user.SpotifyUserID,
				DisplayName:   user.DisplayName,
				SessionID:     sess.ID,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
