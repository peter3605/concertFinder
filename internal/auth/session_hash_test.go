package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The stored value used to *be* the credential: sessions.id held the raw
// token, which is both the cf_session cookie and the iOS bearer token. Every
// nightly pg_dump in S3 was therefore a file of working logins. What replaced
// it has to be one-way and it has to be stable, or lookups stop matching a
// cookie the browser is still sending.
func TestHashSessionTokenIsStableOneWayAndOpaque(t *testing.T) {
	const token = "a-32-byte-random-looking-token!!"

	got := HashSessionToken(token)
	if got != HashSessionToken(token) {
		t.Fatal("hashing is not deterministic; every request would fail to resolve its own session")
	}
	if got == token || strings.Contains(got, token) {
		t.Fatalf("the token survives into the stored value: %q", got)
	}
	if len(got) != 64 {
		t.Errorf("len = %d, want 64 hex characters of SHA-256", len(got))
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Errorf("not hex-encoded: %v", err)
	}
	if HashSessionToken(token) == HashSessionToken(token+"x") {
		t.Error("two different tokens hash alike")
	}
}

// sha256("") is a perfectly good 64-character hash, so a request presenting no
// credential must be turned away before anything hashes it — otherwise it
// arrives at the lookup looking like a well-formed one. Pool is nil: any
// database call panics, which is the assertion.
func TestRequireUserRejectsAnEmptyCredentialWithoutQuerying(t *testing.T) {
	h := RequireUser(nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran for a request carrying no session")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me/concerts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}
