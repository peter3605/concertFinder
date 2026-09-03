package auth

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// tokenBuckets is the shared core of the limiters below: a map of keyed token
// buckets with an amortized idle sweep. Single-instance semantics —
// multi-replica deploys should replace this with a shared counter (Postgres or
// Redis).
//
// It stays unexported and is wrapped by two named types rather than being
// offered directly with a key function. What differs between an IP bucket and
// a user bucket is only what the key means, and that is precisely the thing a
// call site must not get wrong: a per-IP limit on an endpoint that needed a
// per-user one lets one account behind a phone network's NAT throttle a whole
// city, and the reverse lets an unauthenticated flood through.
type tokenBuckets struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64       // tokens per second
	burst   float64       // max bucket capacity
	ttl     time.Duration // idle time before we drop a key
	nextGC  time.Time     // amortizes the sweep; see gcLocked
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// gcInterval bounds how often the idle-bucket sweep runs. The sweep is
// O(tracked keys) under the global mutex, so doing it on every request — as
// this used to — puts a full map walk on the auth hot path.
const gcInterval = time.Minute

// bucketTTL is how long an untouched bucket is kept. It is what makes the map
// bounded by *active* callers rather than by every caller the process has ever
// seen, which matters equally for both key spaces: this process runs for weeks
// between deploys, so an unswept map keyed by user id is a leak that grows
// with the user table.
const bucketTTL = 10 * time.Minute

func newTokenBuckets(perSecond, burst float64) *tokenBuckets {
	return &tokenBuckets{
		buckets: map[string]*bucket{},
		rate:    perSecond,
		burst:   burst,
		ttl:     bucketTTL,
	}
}

// allow returns true if the key is under its bucket cap and consumes one
// token. Non-blocking.
func (l *tokenBuckets) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// Refill.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
	b.last = now
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// gcLocked drops buckets nobody has touched in ttl. Rate-limited to once
// per gcInterval: entries live ttl anyway, so sweeping more often just
// burns time holding the lock. Caller must hold l.mu.
func (l *tokenBuckets) gcLocked(now time.Time) {
	if now.Before(l.nextGC) {
		return
	}
	l.nextGC = now.Add(gcInterval)
	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.ttl {
			delete(l.buckets, key)
		}
	}
}

// tooManyRequests is the one refusal both limiters emit. Retry-After is
// advisory here — the bucket refills continuously rather than at a boundary —
// but a client with nothing to go on retries immediately, which is how a
// throttled caller becomes a busy loop.
func tooManyRequests(w http.ResponseWriter, retryAfterSeconds int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

// IPRateLimit is a per-remote-IP token bucket. Handy for putting a lid on
// /login spam without pulling in a full rate-limit library or Redis.
type IPRateLimit struct{ *tokenBuckets }

func NewIPRateLimit(perSecond, burst float64) *IPRateLimit {
	return &IPRateLimit{newTokenBuckets(perSecond, burst)}
}

// Allow returns true if the caller is under their bucket cap and consumes one
// token. Non-blocking.
func (l *IPRateLimit) Allow(ip string) bool { return l.allow(ip) }

// Middleware wraps a handler with rate limiting on the caller's IP. Uses
// chi.middleware.RealIP-populated r.RemoteAddr, which Caddy makes trustworthy
// by *overwriting* True-Client-IP / X-Real-IP / X-Forwarded-For rather than
// passing them through — without that a client picks its own bucket.
func (l *IPRateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(r.RemoteAddr) {
			tooManyRequests(w, 60)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// UserRateLimit is the same bucket keyed by authenticated user rather than by
// source IP.
//
// It exists for endpoints where the cost of a request is borne by a resource
// the whole deployment shares, so an IP bucket is the wrong shape twice over:
// one user can spread a flood across many addresses, and many users legitimately
// arrive from one. /me/artists/search is the case in hand — it proxies straight
// to Spotify's /v1/search, whose rate limit applies to our client ID, so one
// account hammering the picker gets *every* user's Spotify calls 429'd.
type UserRateLimit struct{ *tokenBuckets }

func NewUserRateLimit(perSecond, burst float64) *UserRateLimit {
	return &UserRateLimit{newTokenBuckets(perSecond, burst)}
}

// Allow returns true if the user is under their bucket cap and consumes one
// token. Non-blocking.
func (l *UserRateLimit) Allow(userID string) bool { return l.allow(userID) }

// Middleware wraps a handler with per-user rate limiting. It must be mounted
// *inside* RequireUser: without a resolved user there is no key, and the
// middleware fails closed with a 401 rather than silently degrading to one
// shared bucket for everyone — which would be a global limit wearing a
// per-user label.
func (l *UserRateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !l.Allow(u.ID.String()) {
			tooManyRequests(w, 10)
			return
		}
		next.ServeHTTP(w, r)
	})
}
