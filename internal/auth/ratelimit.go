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
}

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

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

func (l *IPRateLimit) gcLocked(now time.Time) {
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
