package auth

import (
	"net/http"
	"sync"
	"time"
)

// IPRateLimit is a per-remote-IP token bucket. Handy for putting a lid on
// /login spam without pulling in a full rate-limit library or Redis. Single-
// instance semantics — multi-replica deploys should replace this with a
// shared counter (Postgres or Redis).
type IPRateLimit struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64       // tokens per second
	burst   float64       // max bucket capacity
	ttl     time.Duration // idle time before we drop an IP
	nextGC  time.Time     // amortizes the sweep; see gcLocked
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

// gcInterval bounds how often the idle-bucket sweep runs. The sweep is
// O(tracked IPs) under the global mutex, so doing it on every request — as
// this used to — puts a full map walk on the auth hot path.
const gcInterval = time.Minute

func NewIPRateLimit(perSecond, burst float64) *IPRateLimit {
	return &IPRateLimit{
		buckets: map[string]*bucket{},
		rate:    perSecond,
		burst:   burst,
		ttl:     10 * time.Minute,
	}
}

// Allow returns true if the caller is under their bucket cap and consumes one
// token. Non-blocking.
func (l *IPRateLimit) Allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gcLocked(now)
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
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
func (l *IPRateLimit) gcLocked(now time.Time) {
	if now.Before(l.nextGC) {
		return
	}
	l.nextGC = now.Add(gcInterval)
	for ip, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.ttl {
			delete(l.buckets, ip)
		}
	}
}

// Middleware wraps a handler with rate limiting on the caller's IP. Uses
// chi.middleware.RealIP-populated r.RemoteAddr.
func (l *IPRateLimit) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(r.RemoteAddr) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
