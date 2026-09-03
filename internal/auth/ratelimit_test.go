package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// userLimited wraps a UserRateLimit around a handler that records whether it
// ran, and returns a request builder for a given authenticated user.
func userLimited(t *testing.T, perSecond, burst float64) (http.Handler, *int) {
	t.Helper()
	reached := 0
	h := NewUserRateLimit(perSecond, burst).Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	return h, &reached
}

func requestAs(id uuid.UUID) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/me/artists/search?q=abc", nil)
	return r.WithContext(withUser(r.Context(), CurrentUser{ID: id}))
}

// The bucket has to be per user, not per process: /me/artists/search proxies
// to Spotify's /v1/search under the app's single client ID, so one account
// hammering the picker 429s everyone's searches upstream.
func TestUserRateLimitBoundsOneUserWithoutTouchingAnother(t *testing.T) {
	h, reached := userLimited(t, 1, 3)
	noisy, quiet := uuid.New(), uuid.New()

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requestAs(noisy))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d within the burst got %d", i+1, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestAs(noisy))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fourth request past a burst of 3 got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After: a throttled client with nothing to go on retries immediately")
	}

	// The other user's allowance is untouched. Sharing one bucket would make
	// this a global limit wearing a per-user label.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, requestAs(quiet))
	if rec.Code != http.StatusOK {
		t.Errorf("a second user was throttled by the first: got %d", rec.Code)
	}
	if *reached != 4 {
		t.Errorf("handler ran %d times, want 4", *reached)
	}
}

// Mounted outside RequireUser there is no user to key on. Failing closed is
// the only safe answer: keying everyone to one bucket would silently turn a
// per-user limit into a shared one.
func TestUserRateLimitRefusesAnUnauthenticatedRequest(t *testing.T) {
	h, reached := userLimited(t, 1, 10)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me/artists/search", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if *reached != 0 {
		t.Error("handler ran without an authenticated user")
	}
}

// An unswept map keyed by user id, in a process that runs for weeks between
// deploys, grows with the user table. Both limiters share the sweep, so
// testing it once covers both.
func TestIdleBucketsAreSweptAway(t *testing.T) {
	l := newTokenBuckets(1, 10)
	l.allow("someone")
	if len(l.buckets) != 1 {
		t.Fatalf("bucket count = %d, want 1", len(l.buckets))
	}

	// Age the entry past its TTL and make the amortized sweep due.
	l.mu.Lock()
	for _, b := range l.buckets {
		b.lastSeen = time.Now().Add(-bucketTTL - time.Minute)
	}
	l.nextGC = time.Time{}
	l.mu.Unlock()

	l.allow("someone-else")
	if _, ok := l.buckets["someone"]; ok {
		t.Error("an idle bucket survived the sweep")
	}
}
