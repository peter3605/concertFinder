package ticketmaster

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The API key rides in the query string, so http.Client.Do's *url.Error names
// it on every transport failure — and those errors are logged verbatim by
// internal/concerts/search.go. At a 10s timeout across 200 artists this is the
// common path, not the rare one.
const secret = "SUPERSECRETKEY"

func TestDoGETRetryTransportErrorRedactsAPIKey(t *testing.T) {
	// A server that hangs up immediately gives us a real *url.Error from
	// net/http rather than a synthesized one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()

	c := NewClient(&http.Client{Timeout: 2 * time.Second}, secret)
	_, _, err := c.doGETRetry(context.Background(), base+"/discovery/v2/events.json?apikey="+secret)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
	if !strings.Contains(err.Error(), "/discovery/v2/events.json") {
		t.Fatalf("path should survive redaction: %q", err)
	}
}

func TestDoGETRetryStatusErrorsRedactAPIKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
	}{
		{"4xx", http.StatusForbidden},
		{"5xx", http.StatusInternalServerError},
		{"429", http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", tc.code)
			}))
			defer srv.Close()

			c := NewClient(&http.Client{Timeout: 2 * time.Second}, secret)
			_, _, err := c.doGETRetry(context.Background(), srv.URL+"/discovery/v2/attractions.json?apikey="+secret)
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("api key leaked into error: %q", err)
			}
		})
	}
}

// ResolveAttraction wraps doGETRetry's error with the artist name; check the
// redaction survives that wrapping, since that is the form callers log.
func TestCallerWrappingKeepsRedaction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(&http.Client{Timeout: 2 * time.Second}, secret)
	_, _, err := c.doGETRetry(context.Background(), srv.URL+"/attractions.json?keyword=x&apikey="+secret)
	if err == nil {
		t.Fatal("expected an error")
	}
	wrapped := fmt.Errorf("resolve attraction %q: %w", "Boygenius", err)
	if strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("api key leaked through the caller's wrapping: %q", wrapped)
	}
}

func TestRedactPath(t *testing.T) {
	cases := map[string]string{
		"https://app.ticketmaster.com/discovery/v2/events.json?apikey=" + secret: "/discovery/v2/events.json",
		"://bad-url?apikey=" + secret:                                            "://bad-url",
		"/discovery/v2/events.json":                                              "/discovery/v2/events.json",
	}
	for in, want := range cases {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}
