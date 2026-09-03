package fallback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Songkick API key rides in the query string, and fallback.go logs
// SearchArtistEvents' error verbatim ("songkick failed", err).
const songkickSecret = "SUPERSECRETKEY"

func newTestSongkick(t *testing.T) *SongkickClient {
	t.Helper()
	return &SongkickClient{HTTP: &http.Client{Timeout: 2 * time.Second}, APIKey: songkickSecret}
}

func TestSongkickGetTransportErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	c := newTestSongkick(t)
	_, err := c.get(context.Background(), base+"/api/3.0/search/artists.json?apikey="+songkickSecret+"&query=x")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), songkickSecret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "/api/3.0/search/artists.json") {
		t.Fatalf("path should survive redaction: %q", err)
	}
}

func TestSongkickGetStatusErrorsRedactAPIKey(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError, http.StatusTooManyRequests} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", code)
		}))
		c := newTestSongkick(t)
		_, err := c.get(context.Background(), srv.URL+"/api/3.0/artists/1/calendar.json?apikey="+songkickSecret)
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected an error", code)
		}
		if strings.Contains(err.Error(), songkickSecret) {
			t.Fatalf("status %d: api key leaked into error: %q", code, err)
		}
	}
}
