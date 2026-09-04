package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// --- Pure unit tests. These run everywhere, with or without a database. ---

func TestNormalizeInviteCode(t *testing.T) {
	// Every one of these is a real shape a code arrives in: pasted from a
	// chat client, typed on a phone keyboard, read off a screenshot.
	cases := map[string]string{
		"CF-ABCD-EFGH":   "CF-ABCD-EFGH",
		"cf-abcd-efgh":   "CF-ABCD-EFGH",
		"  CF-ABCD-EFGH": "CF-ABCD-EFGH",
		"CF ABCD EFGH":   "CFABCDEFGH",
		"Cf-Abcd-Efgh":   "CF-ABCD-EFGH",
		"CF_ABCD_EFGH":   "CFABCDEFGH",
		"":               "",
	}
	for in, want := range cases {
		if got := NormalizeInviteCode(in); got != want {
			t.Errorf("NormalizeInviteCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeInviteCodeIsIdempotent(t *testing.T) {
	// The handler normalizes, the db layer normalizes again, and the CLI
	// normalizes before storing. Applying it twice must not change the
	// answer or those three would disagree about what got stored.
	for _, in := range []string{"cf-abcd-efgh", " CF ABCD ", "CF-2345-6789"} {
		once := NormalizeInviteCode(in)
		if twice := NormalizeInviteCode(once); twice != once {
			t.Errorf("not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestInviteCodeUsable(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name string
		code InviteCode
		want bool
	}{
		{"fresh single-use", InviteCode{MaxRedemptions: 1}, true},
		{"partly used multi-use", InviteCode{MaxRedemptions: 5, Redemptions: 2}, true},
		{"spent", InviteCode{MaxRedemptions: 1, Redemptions: 1}, false},
		{"over-spent", InviteCode{MaxRedemptions: 1, Redemptions: 4}, false},
		{"disabled", InviteCode{MaxRedemptions: 1, DisabledAt: &past}, false},
		{"expired", InviteCode{MaxRedemptions: 1, ExpiresAt: &past}, false},
		{"expiring later", InviteCode{MaxRedemptions: 1, ExpiresAt: &future}, true},
		// The boundary is exclusive on both sides of the SQL and the Go, so
		// a code expiring exactly now is expired in both.
		{"expiring exactly now", InviteCode{MaxRedemptions: 1, ExpiresAt: &now}, false},
	}
	for _, c := range cases {
		if got := c.code.Usable(now); got != c.want {
			t.Errorf("%s: Usable = %v, want %v", c.name, got, c.want)
		}
	}
}

// --- Database-backed. Skips without TEST_DATABASE_URL; CI provides one. ---

func mintTestInvite(t *testing.T, pool *pgxpool.Pool, uses int, expires *time.Time) InviteCode {
	t.Helper()
	code := "CF-TEST-" + uuid.NewString()[:8]
	c, err := CreateInviteCode(context.Background(), pool, code, "test", uses, expires)
	if err != nil {
		t.Fatalf("mint invite: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `UPDATE users SET invited_with = NULL WHERE invited_with = $1`, c.Code)
		_, _ = pool.Exec(ctx, `DELETE FROM invite_codes WHERE code = $1`, c.Code)
	})
	return c
}

func newTestUser(spotifyID string) User {
	return User{
		SpotifyUserID:         spotifyID,
		DisplayName:           "Test User",
		EncryptedRefreshToken: []byte("ciphertext"),
		RefreshTokenNonce:     []byte("nonce-123456"),
	}
}

func dropUser(t *testing.T, pool *pgxpool.Pool, spotifyID string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE spotify_user_id = $1`, spotifyID)
	})
}

// A signup with no code at all is refused when the gate is on, and nothing is
// written. The "nothing is written" half matters: a half-created user would
// be admitted for free on their next attempt, since the gate only fires when
// the row does not exist.
func TestAdmissionRefusesSignupWithoutCode(t *testing.T) {
	pool := testPool(t)
	spotifyID := "spotify-nocode-" + uuid.NewString()
	dropUser(t, pool, spotifyID)

	_, err := UpsertUserWithAdmission(context.Background(), pool, newTestUser(spotifyID), "", true)
	if !errors.Is(err, ErrInviteRequired) {
		t.Fatalf("err = %v, want ErrInviteRequired", err)
	}
	assertUserCount(t, pool, spotifyID, 0)
}

func TestAdmissionRefusesUnknownCode(t *testing.T) {
	pool := testPool(t)
	spotifyID := "spotify-badcode-" + uuid.NewString()
	dropUser(t, pool, spotifyID)

	_, err := UpsertUserWithAdmission(context.Background(), pool, newTestUser(spotifyID), "CF-NOPE-NOPE", true)
	if !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("err = %v, want ErrInviteInvalid", err)
	}
	assertUserCount(t, pool, spotifyID, 0)
}

func TestAdmissionAcceptsValidCodeAndSpendsItExactlyOnce(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	invite := mintTestInvite(t, pool, 1, nil)
	spotifyID := "spotify-good-" + uuid.NewString()
	dropUser(t, pool, spotifyID)

	u, err := UpsertUserWithAdmission(ctx, pool, newTestUser(spotifyID), invite.Code, true)
	if err != nil {
		t.Fatalf("admission: %v", err)
	}
	if u.InvitedWith != invite.Code {
		t.Errorf("InvitedWith = %q, want %q", u.InvitedWith, invite.Code)
	}
	assertRedemptions(t, pool, invite.Code, 1)

	// The same person logging in again is a returning user, not a second
	// signup: they must not be asked for a code and must not spend one.
	// This is the property that grandfathers every pre-0021 account.
	again, err := UpsertUserWithAdmission(ctx, pool, newTestUser(spotifyID), "", true)
	if err != nil {
		t.Fatalf("returning login refused: %v", err)
	}
	if again.ID != u.ID {
		t.Errorf("returning login changed the user id: %s then %s", u.ID, again.ID)
	}
	assertRedemptions(t, pool, invite.Code, 1)
}

// A one-use code seats exactly one person. The second signup is refused and,
// critically, leaves no user row behind.
func TestAdmissionRefusesSpentCode(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	invite := mintTestInvite(t, pool, 1, nil)

	first := "spotify-first-" + uuid.NewString()
	second := "spotify-second-" + uuid.NewString()
	dropUser(t, pool, first)
	dropUser(t, pool, second)

	if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(first), invite.Code, true); err != nil {
		t.Fatalf("first signup: %v", err)
	}
	_, err := UpsertUserWithAdmission(ctx, pool, newTestUser(second), invite.Code, true)
	if !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("second signup err = %v, want ErrInviteInvalid", err)
	}
	assertUserCount(t, pool, second, 0)
	assertRedemptions(t, pool, invite.Code, 1)
}

func TestAdmissionMultiUseCodeSeatsSeveral(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	invite := mintTestInvite(t, pool, 3, nil)

	for i := range 3 {
		id := "spotify-multi-" + uuid.NewString()
		dropUser(t, pool, id)
		if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(id), invite.Code, true); err != nil {
			t.Fatalf("signup %d: %v", i, err)
		}
	}
	assertRedemptions(t, pool, invite.Code, 3)

	overflow := "spotify-multi-overflow-" + uuid.NewString()
	dropUser(t, pool, overflow)
	if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(overflow), invite.Code, true); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("fourth signup err = %v, want ErrInviteInvalid", err)
	}
}

func TestAdmissionRefusesExpiredAndDisabledCodes(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	past := time.Now().UTC().Add(-time.Hour)
	expired := mintTestInvite(t, pool, 1, &past)
	expiredUser := "spotify-expired-" + uuid.NewString()
	dropUser(t, pool, expiredUser)
	if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(expiredUser), expired.Code, true); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("expired code err = %v, want ErrInviteInvalid", err)
	}

	disabled := mintTestInvite(t, pool, 1, nil)
	if err := DisableInviteCode(ctx, pool, disabled.Code); err != nil {
		t.Fatalf("disable: %v", err)
	}
	disabledUser := "spotify-disabled-" + uuid.NewString()
	dropUser(t, pool, disabledUser)
	if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(disabledUser), disabled.Code, true); !errors.Is(err, ErrInviteInvalid) {
		t.Errorf("disabled code err = %v, want ErrInviteInvalid", err)
	}
}

// With the gate off the callback behaves exactly as it did before migration
// 0021: signups succeed with no code and nothing is recorded against one.
// This is the switch an operator flips, so it is worth pinning.
func TestAdmissionOffAdmitsWithoutCode(t *testing.T) {
	pool := testPool(t)
	spotifyID := "spotify-open-" + uuid.NewString()
	dropUser(t, pool, spotifyID)

	u, err := UpsertUserWithAdmission(context.Background(), pool, newTestUser(spotifyID), "", false)
	if err != nil {
		t.Fatalf("open signup refused: %v", err)
	}
	if u.InvitedWith != "" {
		t.Errorf("InvitedWith = %q, want empty when the gate is off", u.InvitedWith)
	}
}

// The code a user types is not the code that was minted -- case and spacing
// differ -- and it still has to work. A regression here refuses a valid
// invite, which reads to the holder as "you sent me a broken code".
func TestAdmissionAcceptsUnnormalizedCode(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	invite := mintTestInvite(t, pool, 1, nil)
	spotifyID := "spotify-sloppy-" + uuid.NewString()
	dropUser(t, pool, spotifyID)

	sloppy := "  " + lower(invite.Code) + " "
	u, err := UpsertUserWithAdmission(ctx, pool, newTestUser(spotifyID), sloppy, true)
	if err != nil {
		t.Fatalf("admission with %q: %v", sloppy, err)
	}
	// Stored in canonical form, not as typed, so the operator's -list-invites
	// and the user's row agree.
	if u.InvitedWith != invite.Code {
		t.Errorf("InvitedWith = %q, want canonical %q", u.InvitedWith, invite.Code)
	}
}

// InviteCodeUsable is what /login consults. It must agree with the redemption
// about every reason a code is unusable, or a code accepted on the way out
// gets refused on the way back with no explanation.
func TestInviteCodeUsableAgreesWithRedemption(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	usable := mintTestInvite(t, pool, 1, nil)
	if ok, err := InviteCodeUsable(ctx, pool, usable.Code); err != nil || !ok {
		t.Errorf("fresh code: usable = %v, err = %v; want true, nil", ok, err)
	}
	// Spend it, then ask again.
	spent := "spotify-usable-" + uuid.NewString()
	dropUser(t, pool, spent)
	if _, err := UpsertUserWithAdmission(ctx, pool, newTestUser(spent), usable.Code, true); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if ok, err := InviteCodeUsable(ctx, pool, usable.Code); err != nil || ok {
		t.Errorf("spent code: usable = %v, err = %v; want false, nil", ok, err)
	}
	if ok, err := InviteCodeUsable(ctx, pool, "CF-DOES-NTXS"); err != nil || ok {
		t.Errorf("unknown code: usable = %v, err = %v; want false, nil", ok, err)
	}
}

func assertRedemptions(t *testing.T, pool *pgxpool.Pool, code string, want int) {
	t.Helper()
	var got int
	err := pool.QueryRow(context.Background(),
		`SELECT redemptions FROM invite_codes WHERE code = $1`, code).Scan(&got)
	if err != nil {
		t.Fatalf("read redemptions: %v", err)
	}
	if got != want {
		t.Errorf("redemptions = %d, want %d", got, want)
	}
}

func assertUserCount(t *testing.T, pool *pgxpool.Pool, spotifyID string, want int) {
	t.Helper()
	var got int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM users WHERE spotify_user_id = $1`, spotifyID).Scan(&got)
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if got != want {
		t.Errorf("users with spotify_user_id %s = %d, want %d", spotifyID, got, want)
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
