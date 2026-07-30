package fallback

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// MusicBrainz replaces Brave Search for URL resolution. Free, no API key,
// legitimate (community database with structured URL relationships).
//
// Two-step lookup:
//  1. GET /ws/2/artist?query=<name>&fmt=json → top-scored MBID
//  2. GET /ws/2/artist/<mbid>?inc=url-rels&fmt=json → filter relations for
//     type == "official homepage"
//
// ToS notes (https://musicbrainz.org/doc/MusicBrainz_API):
//   - User-Agent must identify the application. Anonymous requests are 403'd.
//   - 1 req/sec/IP hard limit. Enforced client-side by rateLimiter.

const musicBrainzBase = "https://musicbrainz.org/ws/2"

const mbMinRequestGap = 1100 * time.Millisecond // small margin over 1 req/sec

// MusicBrainzClient satisfies URLResolver.
type MusicBrainzClient struct {
	HTTP      *http.Client
	UserAgent string
	limiter   *rateLimiter

	// Pool is optional: when set, MB lookups are cached in mb_url_cache too,
	// so restarts and replicas share the resolution work. In-memory cache
	// stays as a hot layer above it — reads that hit in-memory skip the DB
	// round trip entirely.
	Pool *pgxpool.Pool

	// cacheMu protects the in-memory hot cache. RWMutex because most reads
	// after warm-up are hits.
	cacheMu sync.RWMutex
	cache   map[string]string
}

// NewMusicBrainzClient constructs a client with an in-memory-only cache.
// Call WithPool to add DB persistence.
func NewMusicBrainzClient(userAgent string) *MusicBrainzClient {
	if userAgent == "" {
		userAgent = "ConcertFinder/0.1 (https://github.com/peter3605/concertFinder)"
	}
	return &MusicBrainzClient{
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		UserAgent: userAgent,
		limiter:   &rateLimiter{minGap: mbMinRequestGap},
		cache:     map[string]string{},
	}
}

// WithPool enables DB-backed persistent caching. Returns the same client for
// chaining.
func (c *MusicBrainzClient) WithPool(pool *pgxpool.Pool) *MusicBrainzClient {
	c.Pool = pool
	return c
}

type mbArtistSearchResp struct {
	Artists []struct {
		ID    string `json:"id"`
		Score int    `json:"score"`
		Name  string `json:"name"`
	} `json:"artists"`
}

type mbArtistDetailResp struct {
	Relations []struct {
		Type string `json:"type"`
		URL  struct {
			Resource string `json:"resource"`
		} `json:"url"`
	} `json:"relations"`
}

// ResolveOfficialURL returns the artist's official homepage URL from
// MusicBrainz, or "" if we couldn't find one. Two-tier cache:
//  1. In-memory hot cache (process-local, wiped on restart).
//  2. mb_url_cache in Postgres (shared across restarts and replicas).
// Only the raw MB fetch respects the 1 req/sec rate limit; cache hits skip
// it entirely.
func (c *MusicBrainzClient) ResolveOfficialURL(ctx context.Context, artistName string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(artistName))
	if key == "" {
		return "", nil
	}
	c.cacheMu.RLock()
	if cached, ok := c.cache[key]; ok {
		c.cacheMu.RUnlock()
		return cached, nil
	}
	c.cacheMu.RUnlock()

	// Warm layer: DB hit? Fill in-memory cache and return.
	if c.Pool != nil {
		if urlStr, hit, err := db.GetMBURL(ctx, c.Pool, key); err == nil && hit {
			c.cacheMu.Lock()
			c.cache[key] = urlStr
			c.cacheMu.Unlock()
			return urlStr, nil
		}
	}

	mbid, err := c.searchArtist(ctx, artistName)
	if err != nil {
		return "", err
	}
	var resolved string
	if mbid != "" {
		resolved, err = c.officialHomepage(ctx, mbid)
		if err != nil {
			return "", err
		}
	}
	c.cacheMu.Lock()
	c.cache[key] = resolved
	c.cacheMu.Unlock()
	if c.Pool != nil {
		_ = db.SaveMBURL(ctx, c.Pool, key, resolved)
	}
	return resolved, nil
}

func (c *MusicBrainzClient) searchArtist(ctx context.Context, name string) (string, error) {
	q := url.Values{}
	q.Set("query", name)
	q.Set("fmt", "json")
	q.Set("limit", "5")
	body, err := c.get(ctx, musicBrainzBase+"/artist?"+q.Encode())
	if err != nil {
		return "", err
	}
	var out mbArtistSearchResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("mb search decode: %w", err)
	}
	// MusicBrainz returns matches sorted by score already. Take the top hit
	// but require a strong score to avoid pulling in similarly-named artists.
	for _, a := range out.Artists {
		if a.Score >= 90 {
			return a.ID, nil
		}
	}
	return "", nil
}

func (c *MusicBrainzClient) officialHomepage(ctx context.Context, mbid string) (string, error) {
	q := url.Values{}
	q.Set("inc", "url-rels")
	q.Set("fmt", "json")
	body, err := c.get(ctx, musicBrainzBase+"/artist/"+mbid+"?"+q.Encode())
	if err != nil {
		return "", err
	}
	var out mbArtistDetailResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("mb detail decode: %w", err)
	}
	for _, r := range out.Relations {
		if r.Type == "official homepage" && r.URL.Resource != "" && isLikelyOfficial(r.URL.Resource) {
			return r.URL.Resource, nil
		}
	}
	return "", nil
}

// mbMaxAttempts is the number of times to try a single MB request before
// giving up. MusicBrainz responds with 503 ("web server is currently busy")
// whenever it feels squeezed even if we're inside the documented 1 req/sec
// limit, so retry is not optional.
const mbMaxAttempts = 4

func (c *MusicBrainzClient) get(ctx context.Context, u string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < mbMaxAttempts; attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/json")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			if !mbSleepBackoff(ctx, attempt, "") {
				return nil, lastErr
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if !mbSleepBackoff(ctx, attempt, "") {
				return nil, lastErr
			}
			continue
		}
		switch {
		case resp.StatusCode/100 == 2:
			return body, nil
		case resp.StatusCode == http.StatusServiceUnavailable, resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("mb %d: busy", resp.StatusCode)
			if !mbSleepBackoff(ctx, attempt, resp.Header.Get("Retry-After")) {
				return nil, lastErr
			}
		case resp.StatusCode == http.StatusNotFound:
			// "Not found" is a valid empty answer, not an error.
			return []byte("{}"), nil
		default:
			return nil, fmt.Errorf("mb %d: %s", resp.StatusCode, truncateStr(body, 200))
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("mb: retries exhausted")
	}
	return nil, lastErr
}

// mbSleepBackoff waits before the next attempt. Honors Retry-After when
// present, else exponential backoff (2s, 4s, 8s) with a small jitter. Returns
// false if ctx expires during the wait.
func mbSleepBackoff(ctx context.Context, attempt int, retryAfter string) bool {
	if attempt >= mbMaxAttempts-1 {
		return false
	}
	d := 2 * time.Second << attempt
	if retryAfter != "" {
		// Retry-After is typically seconds. Cap at 15s so we don't stall the
		// whole search on a single misbehaving artist.
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
			d = time.Duration(secs) * time.Second
			if d > 15*time.Second {
				d = 15 * time.Second
			}
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// rateLimiter enforces a minimum gap between consecutive requests. Enough for
// MusicBrainz's 1 req/sec limit; not accurate enough for high-throughput use.
type rateLimiter struct {
	mu      sync.Mutex
	lastReq time.Time
	minGap  time.Duration
}

func (r *rateLimiter) Wait(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if wait := r.minGap - time.Since(r.lastReq); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.lastReq = time.Now()
	return nil
}

func truncateStr(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
