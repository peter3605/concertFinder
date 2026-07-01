package ticketmaster

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

const APIBase = "https://app.ticketmaster.com/discovery/v2"

const (
	maxRetries       = 3
	maxRetryAfter    = 30 * time.Second
	baseBackoff      = 100 * time.Millisecond
	maxResponseBytes = 4 << 20
)

// Client wraps the Ticketmaster Discovery API. API key is passed as ?apikey=
// on every request per TM's convention.
type Client struct {
	HTTP   *http.Client
	APIKey string
}

func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{HTTP: httpClient, APIKey: apiKey}
}

// doGETRetry executes a GET with the design §8.2 retry policy.
func (c *Client) doGETRetry(ctx context.Context, url string) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !sleepBackoff(ctx, attempt) {
				return nil, 0, lastErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !sleepBackoff(ctx, attempt) {
				return nil, 0, lastErr
			}
			continue
		}
		switch {
		case resp.StatusCode/100 == 2:
			return body, resp.StatusCode, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			d := retryAfter(resp.Header.Get("Retry-After"))
			if d == 0 || d > maxRetryAfter {
				if !sleepBackoff(ctx, attempt) {
					return nil, resp.StatusCode, fmt.Errorf("tm 429: retries exhausted")
				}
			} else if !sleepFor(ctx, d) {
				return nil, resp.StatusCode, ctx.Err()
			}
			lastErr = fmt.Errorf("tm 429")
			continue
		case resp.StatusCode/100 == 5:
			lastErr = fmt.Errorf("tm %d", resp.StatusCode)
			if !sleepBackoff(ctx, attempt) {
				return nil, resp.StatusCode, lastErr
			}
			continue
		default:
			return body, resp.StatusCode, fmt.Errorf("tm %d: %s", resp.StatusCode, truncate(body))
		}
	}
	if lastErr == nil {
		lastErr = errors.New("tm: retries exhausted")
	}
	return nil, 0, lastErr
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
	d := baseBackoff << attempt
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
