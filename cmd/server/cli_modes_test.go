package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
)

// The break-glass, tested.
//
// -mint-invite/-list-invites/-disable-invite are what an operator uses when
// the web app is down or no admin can sign in, and adding an admin console is
// only additive if they keep working. They are also the only reason invite
// administration is a mode of this binary at all: the api image is distroless,
// so `docker compose exec api` has no shell and no curl.
//
// -grant-admin is in the same category and is strictly more load-bearing,
// because it is the ONLY way the first admin can exist -- the console requires
// an admin to grant one, and migration 0022 leaves every account without the
// flag.
func cliTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(ctx, pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	// The CLI modes read DATABASE_URL through config.Load, exactly as they do
	// in the container.
	t.Setenv("DATABASE_URL", url)
	return pool
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// -mint-invite puts the code on stdout alone, with everything else on stderr,
// so that it is safe to pipe; that split is part of what is under test.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	code := fn()
	os.Stdout = orig
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(out), code
}

func TestMintInviteCLIStillWorks(t *testing.T) {
	pool := cliTestPool(t)

	out, code := captureStdout(t, func() int {
		return runInviteAdmin(inviteAdminArgs{mint: true, note: "cli test", uses: 1})
	})
	if code != 0 {
		t.Fatalf("-mint-invite exited %d, want 0", code)
	}
	minted := strings.TrimSpace(out)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM invite_codes WHERE code = $1`, minted)
	})
	// One line, the code, and nothing else: that is what makes it pipeable.
	if strings.Contains(minted, "\n") {
		t.Fatalf("-mint-invite wrote more than the code to stdout: %q", out)
	}
	if db.NormalizeInviteCode(minted) != minted {
		t.Fatalf("minted code %q is not in canonical form", minted)
	}

	// It is a real, single-use, usable row -- not just a string on stdout.
	codes, err := db.ListInviteCodes(context.Background(), pool)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *db.InviteCode
	for i := range codes {
		if codes[i].Code == minted {
			found = &codes[i]
		}
	}
	if found == nil {
		t.Fatalf("code %q was printed but not stored", minted)
	}
	if found.MaxRedemptions != 1 {
		t.Errorf("max_redemptions = %d, want 1", found.MaxRedemptions)
	}
	if !found.Usable(time.Now()) {
		t.Error("a freshly minted code is not usable")
	}

	// And the other two modes still run against it.
	if _, code := captureStdout(t, func() int {
		return runInviteAdmin(inviteAdminArgs{list: true})
	}); code != 0 {
		t.Errorf("-list-invites exited %d, want 0", code)
	}
	if _, code := captureStdout(t, func() int {
		return runInviteAdmin(inviteAdminArgs{disable: minted})
	}); code != 0 {
		t.Errorf("-disable-invite exited %d, want 0", code)
	}
	if found, err := db.ListInviteCodes(context.Background(), pool); err != nil {
		t.Fatalf("list after disable: %v", err)
	} else {
		for _, c := range found {
			if c.Code == minted && c.State(time.Now()) != db.InviteDisabled {
				t.Errorf("state after -disable-invite = %q, want %q", c.State(time.Now()), db.InviteDisabled)
			}
		}
	}
}

func TestGrantAdminCLIBootstrapsTheFirstAdmin(t *testing.T) {
	pool := cliTestPool(t)
	ctx := context.Background()
	id := uuid.New()
	spotifyID := "spotify-cli-admin-" + uuid.NewString()
	const insert = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, $3, $4, $5)`
	if _, err := pool.Exec(ctx, insert, id, spotifyID, "CLI Admin",
		[]byte("ciphertext"), []byte("nonce-123456")); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})

	if _, code := captureStdout(t, func() int {
		return runAdminAdmin(adminArgs{grant: spotifyID})
	}); code != 0 {
		t.Fatalf("-grant-admin exited %d, want 0", code)
	}
	u, err := db.GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if !u.IsAdmin {
		t.Fatal("-grant-admin did not set the flag")
	}

	// -list-admins prints the account it just granted. This is the clause
	// that makes the grant verifiable without a database session, which is
	// the whole reason it exists.
	out, code := captureStdout(t, func() int {
		return runAdminAdmin(adminArgs{list: true})
	})
	if code != 0 {
		t.Fatalf("-list-admins exited %d, want 0", code)
	}
	if !strings.Contains(out, spotifyID) {
		t.Fatalf("-list-admins output does not name the granted account:\n%s", out)
	}

	// And the door swings back.
	if _, code := captureStdout(t, func() int {
		return runAdminAdmin(adminArgs{revoke: spotifyID})
	}); code != 0 {
		t.Fatalf("-revoke-admin exited %d, want 0", code)
	}
	u, err = db.GetUserByID(ctx, pool, id)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.IsAdmin {
		t.Fatal("-revoke-admin did not clear the flag")
	}
}

// Granting the flag to a Spotify ID that has never signed in is the likeliest
// operator mistake, and it must be a non-zero exit rather than a silent
// success -- otherwise the operator goes looking for a broken console.
func TestGrantAdminFailsLoudlyForAnUnknownAccount(t *testing.T) {
	cliTestPool(t)
	if _, code := captureStdout(t, func() int {
		return runAdminAdmin(adminArgs{grant: "spotify-never-signed-in-" + uuid.NewString()})
	}); code == 0 {
		t.Fatal("-grant-admin for an unknown account exited 0; it must fail")
	}
}
