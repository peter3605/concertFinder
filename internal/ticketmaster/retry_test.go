package ticketmaster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newRetryServer serves a handler and counts requests. These tests drive
// doGETRetry directly with a raw URL, the way client_test.go does, because
// the retry policy is a property of the transport layer rather than of
// either endpoint.
func newRetryServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request, n int32)) (*Client, *atomic.Int32, string) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(w, r, n.Add(1))
	}))
	t.Cleanup(srv.Close)
	c := NewClient(&http.Client{Timeout: 5 * time.Second}, secret)
	return c, &n, srv.URL + "/discovery/v2/events.json?apikey=" + secret
}

func TestDoGETRetryReturnsBodyAndStatusOnSuccess(t *testing.T) {
	c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"page":{"totalElements":0}}`))
	})
	body, status, err := c.doGETRetry(context.Background(), u)
	if err != nil {
		t.Fatalf("doGETRetry: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if string(body) != `{"page":{"totalElements":0}}` {
		t.Errorf("body = %q", body)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("made %d requests for a first-try success, want 1", got)
	}
}

// "Never retry other 4xx." A 401 from a revoked key and a 404 from a bad
// attractionId are both permanent, and retrying them three more times spends
// three more of the user's daily rate permits to learn the same thing.
func TestDoGETRetryDoesNotRetryOther4xx(t *testing.T) {
	for _, code := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusGone,
		http.StatusUnprocessableEntity,
	} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
				http.Error(w, "nope", code)
			})
			body, status, err := c.doGETRetry(context.Background(), u)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := n.Load(); got != 1 {
				t.Errorf("made %d requests, want 1 -- a 4xx other than 429 is permanent", got)
			}
			if status != code {
				t.Errorf("status = %d, want %d", status, code)
			}
			// The body comes back so the caller can see what upstream said;
			// the error text carries a truncated copy of it.
			if string(body) != "nope\n" {
				t.Errorf("body = %q, want the upstream body", body)
			}
			if !strings.Contains(err.Error(), "nope") {
				t.Errorf("error should quote the upstream body: %q", err)
			}
		})
	}
}

// "5xx exp backoff capped at 3 retries" -- four requests in total, then the
// error. Costs about a second of real backoff, which is the point: the
// sleeps are what a regression to an unbounded loop would blow through.
func TestDoGETRetryRetries5xxAtMostThreeTimes(t *testing.T) {
	c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		http.Error(w, "upstream down", http.StatusInternalServerError)
	})
	_, status, err := c.doGETRetry(context.Background(), u)
	if err == nil {
		t.Fatal("expected an error after the retries were exhausted")
	}
	if got := n.Load(); got != 4 {
		t.Errorf("made %d requests, want 4 (the initial call plus maxRetries=%d)", got, maxRetries)
	}
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should name the status: %q", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("api key leaked into error: %q", err)
	}
}

// The 429 contrast, and the reason CLAUDE.md spells the rule out: a usable
// Retry-After is honoured (clamped down to maxRetryAfter, never discarded),
// and only its absence falls back to sub-second exponential backoff. Getting
// this backwards turns a soft rate limit into a ban.
//
// Both halves are observed within a short context rather than by waiting out
// a real 30s clamp: with the header present the client is still asleep when
// the deadline fires, so exactly one request went out; without it, backoff is
// short enough that a second request is already on the wire.
func TestDoGETRetry429PrefersRetryAfterOverBackoff(t *testing.T) {
	// Long enough that a slow machine still completes the first round trip
	// inside it, short enough that a 30s clamp is unambiguously still asleep.
	const window = 600 * time.Millisecond

	t.Run("a long Retry-After suppresses the fast retry", func(t *testing.T) {
		c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
			// Two minutes: far beyond maxRetryAfter, so the clamp applies --
			// and 30s is still far beyond the test's window.
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		})
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()

		_, status, err := c.doGETRetry(ctx, u)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the context deadline (the client should still have been waiting)", err)
		}
		if status != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429", status)
		}
		if got := n.Load(); got != 1 {
			t.Errorf("made %d requests in %v, want 1 -- Retry-After was ignored in favour of the sub-second backoff", got, window)
		}
	})

	t.Run("no Retry-After falls back to sub-second backoff", func(t *testing.T) {
		c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
		ctx, cancel := context.WithTimeout(context.Background(), window)
		defer cancel()

		_, _, err := c.doGETRetry(ctx, u)
		if err == nil {
			t.Fatal("expected an error")
		}
		// baseBackoff is 100ms plus up to 100ms of jitter, so the second
		// attempt is always inside the window.
		if got := n.Load(); got < 2 {
			t.Errorf("made %d requests in %v, want at least 2 -- with no usable header the retry should be fast", got, window)
		}
	})
}

// The header is not merely respected, it is waited out and the request then
// retried. One second of real sleep, which is the smallest Retry-After the
// header can express.
func TestDoGETRetryWaitsOutRetryAfterThenRetries(t *testing.T) {
	c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, attempt int32) {
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	start := time.Now()
	body, status, err := c.doGETRetry(context.Background(), u)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("doGETRetry: %v", err)
	}
	if status != http.StatusOK || string(body) != `{"ok":true}` {
		t.Errorf("status = %d, body = %q; want the retried 200", status, body)
	}
	if got := n.Load(); got != 2 {
		t.Errorf("made %d requests, want 2", got)
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("returned after %v; a Retry-After of 1s must actually be waited out, not shortened to the backoff", elapsed)
	}
}

// A context that is already done never reaches the wire. The scan-wide
// deadline in concerts.Search cancels mid-fanout, and 200 artists each firing
// one doomed request is 200 wasted rate permits.
func TestDoGETRetryStopsBeforeTheFirstRequestOnACancelledContext(t *testing.T) {
	c, n, u := newRetryServer(t, func(w http.ResponseWriter, _ *http.Request, _ int32) {
		t.Error("a cancelled context still produced a request")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := c.doGETRetry(ctx, u)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := n.Load(); got != 0 {
		t.Errorf("made %d requests, want 0", got)
	}
}

func TestRetryAfterParsesOnlyDeltaSeconds(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
		why    string
	}{
		{"", 0, "absent header; the caller falls back to backoff"},
		{"5", 5 * time.Second, "the normal shape"},
		{"120", 120 * time.Second, "clamping to maxRetryAfter is the caller's job, not this function's"},
		{"0", 0, "zero is indistinguishable from absent here, so the caller backs off instead of hot-looping"},
		{"-3", 0, "a negative delay would make sleepFor fire immediately, which is a hot loop against a rate limiter"},
		{"3.5", 0, "RFC 7231 delta-seconds is an integer"},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0, "the HTTP-date form is legal and is NOT parsed; such a response backs off instead"},
	} {
		if got := retryAfter(tc.header); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v (%s)", tc.header, got, tc.want, tc.why)
		}
	}
}

// The three numbers CLAUDE.md's retry policy is stated in. They are asserted
// rather than merely defined because the policy is written down in prose
// somewhere a compiler will never read.
func TestRetryPolicyConstants(t *testing.T) {
	if maxRetries != 3 {
		t.Errorf("maxRetries = %d, want 3 (5xx backoff is capped at 3 retries)", maxRetries)
	}
	if maxRetryAfter != 30*time.Second {
		t.Errorf("maxRetryAfter = %v, want 30s (Retry-After is clamped to this, not discarded)", maxRetryAfter)
	}
	if baseBackoff != 100*time.Millisecond {
		t.Errorf("baseBackoff = %v, want 100ms (the sub-second fallback)", baseBackoff)
	}
}

// truncate bounds what an upstream body can contribute to an error string.
// The 4xx branch interpolates the response body, and a WAF or proxy sitting
// in front of Ticketmaster answers with a full HTML page.
func TestTruncateBoundsTheUpstreamBody(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"short bodies pass through", []byte("nope"), "nope"},
		{"exactly at the limit passes through", []byte(strings.Repeat("x", 200)), strings.Repeat("x", 200)},
		{"longer bodies are cut and marked", []byte(strings.Repeat("x", 201)), strings.Repeat("x", 200) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncate(tc.in); got != tc.want {
				t.Errorf("truncate(%d bytes) = %d bytes %q...", len(tc.in), len(got), got[:min(len(got), 16)])
			}
		})
	}
}
