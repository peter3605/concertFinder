package auth

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/db"
)

// The auth half of the Go/Swift contract check that internal/http's
// TestGoldenFixtures describes. It lives here because the payload is built by
// this package's unexported writeBearerSession/userPayload, and a fixture
// built from a hand-written literal would test the literal.
//
// Every app sign-in decodes this one body: the Spotify mobile exchange,
// Apple native, the email link and the email code (CF-37). Regenerate with:
//
//	go test ./internal/auth -run TestGoldenSessionFixtures -update

var updateGolden = flag.Bool("update", false, "rewrite the golden fixtures the Swift tests decode")

const fixtureDir = "../../ios/ConcertFinderTests/Fixtures"

func TestGoldenSessionFixtures(t *testing.T) {
	expires := time.Date(2027, 2, 19, 21, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		user db.User
	}{
		// An account that signed in with email and has never had Spotify:
		// spotify_user_id is the empty string, not absent, and the name is
		// empty until onboarding asks.
		{"session-email", db.User{
			ID:          uuid.MustParse("6f1c2d3e-4b5a-4c6d-8e7f-901a2b3c4d5e"),
			Email:       "listener@example.com",
			DigestOptIn: true,
			AuthMethods: []string{"email"},
		}},
		// Apple with a hidden address: a private-relay email and a name from
		// the first authorization.
		{"session-apple", db.User{
			ID:          uuid.MustParse("7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"),
			DisplayName: "Sam Rivera",
			Email:       "x7k2p9@privaterelay.appleid.com",
			AuthMethods: []string{"apple", "email"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := mobileExchangeResponse{
				SessionToken: "c2Vzc2lvbi10b2tlbi1leGFtcGxlLW5vdC1hLXJlYWwtb25l",
				ExpiresAt:    expires,
				User:         userPayload(tc.user),
			}
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetIndent("", "  ")
			if err := enc.Encode(payload); err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := buf.Bytes()
			path := filepath.Join(fixtureDir, tc.name+".json")
			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v (run: go test ./internal/auth -run TestGoldenSessionFixtures -update)", path, err)
			}
			if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
				t.Errorf("golden fixture %s is stale -- the session payload changed.\n"+
					"Regenerate: go test ./internal/auth -run TestGoldenSessionFixtures -update\n"+
					"then run the Swift tests.\n--- got ---\n%s\n--- want ---\n%s", tc.name, got, want)
			}
		})
	}
}
