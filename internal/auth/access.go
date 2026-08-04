package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// tokenSkew is how long before real expiry we stop handing out a cached
// access token, so a token can't expire mid-flight on a slow request.
const tokenSkew = time.Minute

// cachedToken is one user's live access token.
type cachedToken struct {
	token     string
	expiresAt time.Time
}

// TokenService issues Spotify access tokens for a stored user,
// transparently rotating the persisted refresh token when Spotify returns
// a new one (design §3.4).
//
// Access tokens are cached in memory until they expire. The original
// comment here claimed "at most one refresh per user per day" on the
// grounds that affinity has a 24h TTL — true when affinity was the only
// caller, but the artist-search proxy now calls this per request, and each
// call was a full user load + AES decrypt + round trip to Spotify's token
// endpoint (plus a write whenever Spotify rotated the refresh token).
// Tokens are never persisted; a restart just means one more refresh.
type TokenService struct {
	Pool       *pgxpool.Pool
	EncKey     []byte
	ClientID   string
	HTTPClient *http.Client

	mu     sync.Mutex
	tokens map[uuid.UUID]cachedToken
}

// cached returns a still-valid access token for the user, if we hold one.
func (s *TokenService) cached(userID uuid.UUID) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[userID]
	if !ok || time.Now().After(t.expiresAt.Add(-tokenSkew)) {
		return "", false
	}
	return t.token, true
}

func (s *TokenService) store(userID uuid.UUID, token string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens == nil {
		s.tokens = make(map[uuid.UUID]cachedToken)
	}
	s.tokens[userID] = cachedToken{token: token, expiresAt: expiresAt}
}

// Forget drops any cached token for a user. Called when the user is
// deleted so a recycled UUID can't inherit a stale entry.
func (s *TokenService) Forget(userID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, userID)
}

// AccessTokenFor returns a valid access token for the given user, reusing a
// cached one when it has meaningful life left and refreshing otherwise.
func (s *TokenService) AccessTokenFor(ctx context.Context, userID uuid.UUID) (string, error) {
	if tok, ok := s.cached(userID); ok {
		return tok, nil
	}
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
	s.store(userID, tok.AccessToken, tok.ExpiresAt)
	return tok.AccessToken, nil
}
