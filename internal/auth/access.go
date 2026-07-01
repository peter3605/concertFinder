package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// TokenService issues fresh Spotify access tokens for a stored user,
// transparently rotating the persisted refresh token when Spotify returns
// a new one (design §3.4).
type TokenService struct {
	Pool       *pgxpool.Pool
	EncKey     []byte
	ClientID   string
	HTTPClient *http.Client
}

// AccessTokenFor returns a fresh access token for the given user. It always
// performs a refresh; access tokens are not cached in Phase 1 because the
// affinity profile has a 24h TTL, so at most one refresh per user per day.
func (s *TokenService) AccessTokenFor(ctx context.Context, userID uuid.UUID) (string, error) {
	user, err := db.GetUserByID(ctx, s.Pool, userID)
	if err != nil {
		return "", fmt.Errorf("load user: %w", err)
	}
	rt, err := DecryptToken(s.EncKey, user.EncryptedRefreshToken, user.RefreshTokenNonce)
	if err != nil {
		return "", fmt.Errorf("decrypt refresh token: %w", err)
	}
	tok, err := RefreshAccessToken(ctx, s.HTTPClient, s.ClientID, string(rt))
	if err != nil {
		return "", fmt.Errorf("refresh access token: %w", err)
	}
	if tok.RefreshToken != "" && tok.RefreshToken != string(rt) {
		ct, nonce, err := EncryptToken(s.EncKey, []byte(tok.RefreshToken))
		if err != nil {
			return "", fmt.Errorf("encrypt rotated refresh token: %w", err)
		}
		if err := db.UpdateRefreshToken(ctx, s.Pool, user.ID, ct, nonce); err != nil {
			return "", fmt.Errorf("persist rotated refresh token: %w", err)
		}
	}
	return tok.AccessToken, nil
}
