package spotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
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
			d := retryAfter(resp.Header.Get("Retry-After"))
			if d == 0 || d > maxRetryAfter {
				if !sleepBackoff(ctx, attempt) {
					return nil, fmt.Errorf("429 after backoff exhausted")
				}
			} else {
				if !sleepFor(ctx, d) {
					return nil, ctx.Err()
				}
			}
			lastErr = fmt.Errorf("spotify 429")
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

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
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
