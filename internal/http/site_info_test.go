package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/testdb"
)

func siteInfoSignupsOpen(t *testing.T, h *SiteInfoHandler) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/api/site-info", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	open, ok := body["spotify_signups_open"].(bool)
	if !ok {
		t.Fatalf("spotify_signups_open missing or not a bool: %v", body)
	}
	return open
}

// spotify_signups_open is what decides whether the sign-up tab offers
// Spotify. It must flip exactly at SPOTIFY_MAX_USERS, the same count the
// callback refuses at, or the page offers a button that ends in a refusal.
func TestSiteInfoSpotifySignupsOpenAtAndBelowCap(t *testing.T) {
	pool := testdb.Isolated(t, "../../migrations")
	ctx := context.Background()
	addSpotifyUser := func(i int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES (gen_random_uuid(), $1, '', 'ct'::bytea, 'n'::bytea)`, fmt.Sprintf("sp-%d", i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// An account with no Spotify identity holds no Spotify place.
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES (gen_random_uuid(), '', ''::bytea, ''::bytea, 'someone@example.com')`); err != nil {
		t.Fatalf("insert email user: %v", err)
	}

	h := &SiteInfoHandler{Pool: pool, MaxSpotifyUsers: 2}
	addSpotifyUser(1)
	if !siteInfoSignupsOpen(t, h) {
		t.Fatal("1 of 2 Spotify places used: want open")
	}
	addSpotifyUser(2)
	if siteInfoSignupsOpen(t, h) {
		t.Fatal("2 of 2 Spotify places used: want closed")
	}

	if siteInfoSignupsOpen(t, &SiteInfoHandler{Pool: pool, MaxSpotifyUsers: 0}) {
		t.Fatal("SPOTIFY_MAX_USERS=0 means no new Spotify accounts: want closed")
	}
	if !siteInfoSignupsOpen(t, &SiteInfoHandler{Pool: pool, MaxSpotifyUsers: db.NoSpotifyCap}) {
		t.Fatal("no cap: want open")
	}
}

// Without a database the hint fails closed rather than offering a button
// that may end in a refusal after Spotify's consent screen.
func TestSiteInfoSpotifySignupsOpenWithoutPool(t *testing.T) {
	if siteInfoSignupsOpen(t, &SiteInfoHandler{MaxSpotifyUsers: 5}) {
		t.Fatal("no pool: want closed")
	}
}

// apple_sign_in follows whether the Apple routes are mounted, so the sign-in
// page never offers a button that leads to a 404.
func TestSiteInfoAppleSignIn(t *testing.T) {
	for _, mounted := range []bool{false, true} {
		rec := httptest.NewRecorder()
		(&SiteInfoHandler{AppleSignIn: mounted}).Get(rec, httptest.NewRequest(http.MethodGet, "/api/site-info", nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got, ok := body["apple_sign_in"].(bool)
		if !ok || got != mounted {
			t.Errorf("mounted=%v: apple_sign_in = %v (present %v), want %v", mounted, body["apple_sign_in"], ok, mounted)
		}
	}
}
