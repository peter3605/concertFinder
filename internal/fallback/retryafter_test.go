package fallback

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// RFC 9110 §10.2.3 gives Retry-After two forms and a recipient has to
// understand both. Both clients in this package used to read delta-seconds
// only, so a 429 or 503 expressed as a date parsed to zero and fell through to
// the local backoff — 200ms for Songkick — against an upstream that had asked
// for minutes.
func TestParseRetryAfterBothRFC9110Forms(t *testing.T) {
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
		{"120", 120 * time.Second, "clamping is the caller's job — songkick caps at 30s, MusicBrainz at 15s"},
		{"0", 0, "zero is indistinguishable from absent here, so the caller backs off instead of hot-looping"},
		{"-3", 0, "a negative delay would fire the timer immediately, which is a hot loop against a rate limiter"},
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
		if got := parseRetryAfter(tc.header, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v (%s)", tc.header, got, tc.want, tc.why)
		}
	}
}

// The behavioural half for Songkick: a date-form Retry-After must actually
// suppress the fast retry, not merely parse. Observed within a short context
// rather than by waiting out the real 30s clamp — with the header honoured the
// client is still asleep when the deadline fires, so exactly one request went
// out. Without it, songkickBaseBackoff (200ms) puts a second on the wire.
func TestSongkickHonoursHTTPDateRetryAfter(t *testing.T) {
	// Long enough that a slow machine completes the first round trip inside
	// it, short enough that a 30s clamp is unambiguously still asleep.
	const window = 700 * time.Millisecond

	t.Run("an HTTP-date Retry-After suppresses the fast retry", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.Header().Set("Retry-After", time.Now().UTC().Add(2*time.Minute).Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		c := NewSongkickClient(songkickSecret, "")
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()

		if _, err := c.get(ctx, srv.URL); err == nil {
			t.Fatal("expected an error")
		}
		if got := n.Load(); got != 1 {
			t.Errorf("made %d requests in %v, want 1 -- an HTTP-date Retry-After fell through to the sub-second backoff", got, window)
		}
	})

	// The contrast, so the test above cannot pass by the client simply never
	// retrying anything.
	t.Run("no Retry-After falls back to sub-second backoff", func(t *testing.T) {
		var n atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		c := NewSongkickClient(songkickSecret, "")
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()

		if _, err := c.get(ctx, srv.URL); err == nil {
			t.Fatal("expected an error")
		}
		if got := n.Load(); got < 2 {
			t.Errorf("made %d requests in %v, want at least 2 -- with no usable header the retry should be fast", got, window)
		}
	})
}

// mbSleepBackoff is the MusicBrainz half. It differs from the other three
// clients: its fallback is 2s-8s rather than sub-second, so the date form
// failing there was a wrong wait rather than a hammering — but it still
// substituted our number for the server's. Capped at mbMaxRetryAfter because
// these lookups are globally serialized behind a 1 req/sec turnstile.
func TestMBSleepBackoffHonoursHTTPDateRetryAfter(t *testing.T) {
	// The header has to be SHORTER than the backoff it replaces, or the two
	// outcomes are indistinguishable. attempt 2's own backoff is 2s<<2 = 8s,
	// against a date ~2s out: honoured, this returns in 1-2s; ignored, in 8s.
	//
	// The gap is wide because HTTP-date carries whole seconds only — the
	// format truncates the sub-second part, so a date "n seconds out" is
	// really somewhere in (n-1, n]. Anything tighter measures that truncation
	// rather than the behaviour under test.
	hdr := time.Now().UTC().Add(2 * time.Second).Format(http.TimeFormat)

	start := time.Now()
	if !mbSleepBackoff(context.Background(), 2, hdr) {
		t.Fatal("mbSleepBackoff returned false with a live context")
	}
	elapsed := time.Since(start)

	if elapsed > 4*time.Second {
		t.Errorf("waited %v; an HTTP-date Retry-After was ignored in favour of the 8s backoff", elapsed)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("waited %v; the Retry-After was not waited out at all", elapsed)
	}
}
