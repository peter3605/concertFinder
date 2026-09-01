package ticketmaster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

// NewClient panics on a nil httpClient — http.DefaultClient has no timeout
// and would let a hung TM response block one search fan-out goroutine
// indefinitely.
func NewClient(httpClient *http.Client, apiKey string) *Client {
	if httpClient == nil {
		panic("ticketmaster.NewClient: httpClient must be non-nil (set an explicit timeout)")
	}
	return &Client{HTTP: httpClient, APIKey: apiKey}
}

// redactPath reduces a request URL to its path. The Ticketmaster API key
// travels in the query string (?apikey=), so anything that echoes a whole URL
// leaks the credential.
func redactPath(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Path
	}
	// Unparseable, so cut conservatively: everything from the first '?' is
	// query, and a URL with no '?' has no query to leak.
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// redactURLError strips the query string out of a *url.Error. http.Client.Do
// returns one on every transport failure and its Error() string is the full
// request URL — including ?apikey= — which callers log verbatim
// (internal/concerts/search.go). With a 10s timeout across a 200-artist
// fanout those errors are routine, not exceptional. Errors that are not
// *url.Error pass through untouched.
func redactURLError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := *ue
	redacted.URL = redactPath(ue.URL)
	return &redacted
}

// doGETRetry executes a GET with the design §8.2 retry policy. Every error it
// returns names the path only — see redactURLError.
func (c *Client) doGETRetry(ctx context.Context, rawURL string) ([]byte, int, error) {
	path := redactPath(rawURL)
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("tm %s: %w", path, redactURLError(err))
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = redactURLError(err)
			if !sleepBackoff(ctx, attempt) {
				return nil, 0, fmt.Errorf("tm %s: %w", path, lastErr)
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			lastErr = redactURLError(readErr)
			if !sleepBackoff(ctx, attempt) {
				return nil, 0, fmt.Errorf("tm %s: %w", path, lastErr)
			}
			continue
		}
		switch {
		case resp.StatusCode/100 == 2:
			return body, resp.StatusCode, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = errors.New("429")
			// Honor Retry-After, clamped to maxRetryAfter — clamping
			// shortens the wait toward 30s, it does not discard it. See
			// spotify/http.go for why the previous form was wrong.
			if d := retryAfter(resp.Header.Get("Retry-After")); d > 0 {
				if d > maxRetryAfter {
					d = maxRetryAfter
				}
				if !sleepFor(ctx, d) {
					return nil, resp.StatusCode, ctx.Err()
				}
				continue
			}
			if !sleepBackoff(ctx, attempt) {
				return nil, resp.StatusCode, fmt.Errorf("tm %s: 429: retries exhausted", path)
			}
			continue
		case resp.StatusCode/100 == 5:
			lastErr = fmt.Errorf("%d", resp.StatusCode)
			if !sleepBackoff(ctx, attempt) {
				return nil, resp.StatusCode, fmt.Errorf("tm %s: %w", path, lastErr)
			}
			continue
		default:
			return body, resp.StatusCode, fmt.Errorf("tm %s: %d: %s", path, resp.StatusCode, c.scrubKey(truncate(body)))
		}
	}
	if lastErr == nil {
		return nil, 0, fmt.Errorf("tm %s: retries exhausted", path)
	}
	return nil, 0, fmt.Errorf("tm %s: %w", path, lastErr)
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

// scrubKey removes the API key from a string built out of an upstream response
// body, which is the one path P0-1's URL redaction does not cover.
//
// Ticketmaster's own error bodies do not echo the request, but this error is
// raised for any unexpected 4xx, and a 4xx does not always come from
// Ticketmaster: a WAF, a caching proxy or a corporate gateway in front of it
// answers with an HTML error page, and those routinely quote the full request
// URL they refused — query string included. The body is then interpolated into
// an error that internal/concerts/search.go logs verbatim.
//
// Matching the client's own key exactly, rather than a `apikey=\w+` pattern,
// means this cannot half-match a key containing an unexpected character or
// miss one the upstream URL-encoded differently than we sent it.
func (c *Client) scrubKey(s string) string {
	if c.APIKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.APIKey, "REDACTED")
}

func truncate(b []byte) string {
	const n = 200
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
