package spotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries       = 3
	maxRetryAfter    = 30 * time.Second
	baseBackoff      = 100 * time.Millisecond
	maxResponseBytes = 4 << 20 // 4 MiB per page
)

// doGETRetry performs an authenticated GET with the retry policy from
// design §8.2: honor 429 Retry-After (capped 30s), exponential backoff with
// jitter on 5xx (max 3 retries), no retry on other 4xx. All waits respect ctx.
func (c *Client) doGETRetry(ctx context.Context, url, accessToken string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !sleepBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !sleepBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}

		switch {
		case resp.StatusCode/100 == 2:
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("spotify 429")
			// Honor Retry-After, clamped to maxRetryAfter. Clamping must
			// shorten the wait toward 30s, never collapse it to the
			// sub-second backoff: an upstream asking for 120s used to fall
			// into the `d > maxRetryAfter` branch and get retried in ~100ms,
			// which is the fastest way to turn a soft limit into a ban.
			if d := retryAfter(resp.Header.Get("Retry-After"), time.Now()); d > 0 {
				if d > maxRetryAfter {
					d = maxRetryAfter
				}
				if !sleepFor(ctx, d) {
					return nil, ctx.Err()
				}
				continue
			}
			// No usable Retry-After — fall back to exponential backoff.
			if !sleepBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		case resp.StatusCode/100 == 5:
			lastErr = fmt.Errorf("spotify %d: %s", resp.StatusCode, truncate(body))
			if !sleepBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		default:
			return nil, fmt.Errorf("spotify %d: %s", resp.StatusCode, truncate(body))
		}
	}
	if lastErr == nil {
		lastErr = errors.New("spotify: retries exhausted")
	}
	return nil, lastErr
}

// retryAfter parses a Retry-After header measured against now. RFC 9110
// §10.2.3 defines two forms and a recipient has to understand both:
// delay-seconds, and an HTTP-date. Zero means "nothing usable here", which
// sends the caller to its exponential backoff.
//
// The date form is not an exotic spelling — it is what a CDN or proxy in
// front of the API emits — and dropping it was not a no-op: an upstream
// asking for a 90-second pause fell through to the sub-second backoff and got
// retried in ~100ms, which is the same failure the clamp above exists to
// prevent, arriving by a different route.
//
// A date already in the past yields zero rather than a negative duration.
// Negative would make sleepFor fire immediately, i.e. hot-loop against the
// limiter that just refused us.
func retryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	// http.ParseTime accepts all three formats RFC 9110 requires: IMF-fixdate,
	// the obsolete RFC 850 form, and asctime.
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	if d := t.Sub(now); d > 0 {
		return d
	}
	return 0
}

func sleepBackoff(ctx context.Context, attempt int) bool {
	if attempt >= maxRetries {
		return false
	}
	d := baseBackoff << attempt // 100ms, 200ms, 400ms
	d += time.Duration(rand.Int63n(int64(100 * time.Millisecond)))
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return sleepFor(ctx, d)
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truncate(b []byte) string {
	const n = 200
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
