package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// RFC 9110 §10.2.3 gives Retry-After two forms and a recipient has to
// understand both. The date form used to return 0 here, which sent doGETRetry
// to a ~100ms backoff against an upstream that had just asked for minutes —
// the "soft limit into a ban" failure the clamp beside it already guards
// against, arriving by a different route.
//
// Kept deliberately in step with internal/ticketmaster's copy of this test:
// the two parsers are separate on purpose (each external API owns its own
// client), so nothing but a matching pair of tests keeps them honest.
func TestRetryAfterParsesBothRFC9110Forms(t *testing.T) {
	// A fixed reference instant so the date cases are arithmetic, not a race
	// with the wall clock.
	now := time.Date(2026, 10, 21, 7, 28, 0, 0, time.UTC)

	for _, tc := range []struct {
		header string
		want   time.Duration
		why    string
	}{
		{"", 0, "absent header; the caller falls back to backoff"},
		{"5", 5 * time.Second, "delta-seconds, the normal shape"},
		{"120", 120 * time.Second, "clamping to maxRetryAfter is the caller's job, not this function's"},
		{"0", 0, "zero is indistinguishable from absent here, so the caller backs off instead of hot-looping"},
		{"-3", 0, "a negative delay would make sleepFor fire immediately, which is a hot loop against a rate limiter"},
		{"3.5", 0, "delta-seconds is an integer; a float is neither form and means nothing"},
		{"not a date either", 0, "unparseable as both forms; the caller backs off"},

		// The HTTP-date form. All three spellings http.ParseTime accepts,
		// because RFC 9110 requires a recipient to understand all three.
		{"Wed, 21 Oct 2026 07:30:00 GMT", 2 * time.Minute, "IMF-fixdate, the form everything actually emits"},
		{"Wednesday, 21-Oct-26 07:30:00 GMT", 2 * time.Minute, "the obsolete RFC 850 form"},
		{"Wed Oct 21 07:30:00 2026", 2 * time.Minute, "the asctime form"},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0, "a date equal to now is not a wait"},
		{"Wed, 21 Oct 2026 07:00:00 GMT", 0, "a date in the past yields zero, never a negative duration"},
	} {
		if got := retryAfter(tc.header, now); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v (%s)", tc.header, got, tc.want, tc.why)
		}
	}
}

// The behavioural half: a date-form Retry-After must actually suppress the
// fast retry, not merely parse. Observed within a short context rather than by
// waiting out the real 30s clamp — with the header honoured the client is
// still asleep when the deadline fires, so exactly one request went out.
func TestDoGETRetryHonoursHTTPDateRetryAfter(t *testing.T) {
	const window = 600 * time.Millisecond

	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		w.Header().Set("Retry-After", time.Now().UTC().Add(2*time.Minute).Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(&http.Client{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), window)
	defer cancel()

	if _, err := c.doGETRetry(ctx, srv.URL, "token"); err == nil {
		t.Fatal("expected an error")
	}
	if got := n.Load(); got != 1 {
		t.Errorf("made %d requests in %v, want 1 -- an HTTP-date Retry-After fell through to the sub-second backoff", got, window)
	}
}
