package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// fakeGenres stands in for Ticketmaster's Music catalog.
type fakeGenres struct {
	genres []ticketmaster.Genre
	err    error
	calls  int
}

func (f *fakeGenres) MusicGenres(context.Context) ([]ticketmaster.Genre, error) {
	f.calls++
	return f.genres, f.err
}

func catalog() *fakeGenres {
	return &fakeGenres{genres: []ticketmaster.Genre{
		{ID: "KnvZfZ7vAeA", Name: "Rock"},
		{ID: "KnvZfZ7vAev", Name: "Pop"},
		{ID: "KnvZfZ7vAvv", Name: "Alternative"},
		{ID: "KnvZfZ7vAvE", Name: "Jazz"},
	}}
}

// A unique address per test: these run against the shared test database.
func reviewTestAddr() string { return "review-" + uuid.NewString()[:8] + "@example.com" }

func count(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

// The account a reviewer lands in must already be able to produce a feed:
// an email identity for the configured address, a location and genre picks,
// and no Spotify identity, so nothing about it can reach Spotify.
func TestSeedReviewAccountCreatesASignInReadyAccount(t *testing.T) {
	pool := cliTestPool(t)
	ctx := context.Background()
	addr := reviewTestAddr()

	res, err := seedReviewAccount(ctx, pool, addr, catalog(), nil)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if !res.created {
		t.Fatal("first run did not report creating the account")
	}
	if n := count(t, pool, `SELECT count(*) FROM user_identities WHERE provider = 'email' AND subject = $1 AND user_id = $2`, addr, res.userID); n != 1 {
		t.Fatal("no email identity for the review address")
	}
	if n := count(t, pool, `SELECT count(*) FROM user_identities WHERE user_id = $1 AND provider <> 'email'`, res.userID); n != 0 {
		t.Fatal("review account holds a non-email identity")
	}
	loc, ok, err := db.GetUserLocation(ctx, pool, res.userID)
	if err != nil || !ok || loc != (db.UserLocation{UserID: res.userID, Latitude: 40.71, Longitude: -74.01, RadiusMiles: 25}) {
		t.Fatalf("location = %+v (found %v, err %v)", loc, ok, err)
	}
	got, err := db.ListGenrePicks(ctx, pool, res.userID)
	if err != nil {
		t.Fatalf("list genres: %v", err)
	}
	want := map[string]string{"KnvZfZ7vAeA": "Rock", "KnvZfZ7vAev": "Pop", "KnvZfZ7vAvv": "Alternative"}
	if len(got) != len(want) {
		t.Fatalf("genre picks = %+v, want %v", got, want)
	}
	for _, g := range got {
		if want[g.GenreID] != g.Name {
			t.Fatalf("unexpected pick %+v", g)
		}
	}
	// Nothing may ever be mailed to an address nobody reads.
	if n := count(t, pool, `SELECT count(*) FROM users WHERE id = $1 AND (digest_opt_in OR instant_notify_opt_in)`, res.userID); n != 0 {
		t.Fatal("review account is opted in to email")
	}
}

// A re-run keeps the account and puts its fixture back, so the operator can
// run it before every submission without thinking about what a previous
// reviewer changed.
func TestSeedReviewAccountIsIdempotent(t *testing.T) {
	pool := cliTestPool(t)
	ctx := context.Background()
	addr := reviewTestAddr()

	first, err := seedReviewAccount(ctx, pool, addr, catalog(), nil)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	// What a reviewer might do in between.
	if _, err := db.AddGenrePick(ctx, pool, first.userID, db.GenrePick{GenreID: "KnvZfZ7vAvE", Name: "Jazz"}); err != nil {
		t.Fatalf("add pick: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE user_locations SET radius_miles = 5 WHERE user_id = $1`, first.userID); err != nil {
		t.Fatalf("move: %v", err)
	}

	second, err := seedReviewAccount(ctx, pool, addr, catalog(), nil)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if second.created || second.userID != first.userID {
		t.Fatalf("re-run made a new account: %+v then %+v", first, second)
	}
	if n := count(t, pool, `SELECT count(*) FROM user_genre_picks WHERE user_id = $1`, first.userID); n != 3 {
		t.Fatalf("genre picks after re-run = %d, want the 3 seeded", n)
	}
	if n := count(t, pool, `SELECT radius_miles FROM user_locations WHERE user_id = $1`, first.userID); n != 25 {
		t.Fatalf("radius after re-run = %d, want 25", n)
	}
}

// Since email sign-in, an email-only account with this address is as likely
// to be a person's. Seeding it would replace their picks and hand App
// Review's code to their account.
func TestSeedReviewAccountRefusesSomeoneElsesAccount(t *testing.T) {
	pool := cliTestPool(t)
	ctx := context.Background()
	addr := reviewTestAddr()

	id := uuid.New()
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES ($1, 'A Person', ''::bytea, ''::bytea, $2)`, id, addr); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_identities (provider, subject, user_id) VALUES ('email', $1, $2)`, addr, id); err != nil {
		t.Fatalf("insert identity: %v", err)
	}

	if _, err := seedReviewAccount(ctx, pool, addr, catalog(), nil); err == nil {
		t.Fatal("seeded an account this mode did not create")
	}
	if n := count(t, pool, `SELECT count(*) FROM user_genre_picks WHERE user_id = $1`, id); n != 0 {
		t.Fatal("the person's account was given picks")
	}
}

// A genre missing from Ticketmaster's catalog, or a failed fetch, writes
// nothing: a half-seeded account is a thinner feed that nobody would notice.
func TestSeedReviewAccountWritesNothingWhenGenresFail(t *testing.T) {
	pool := cliTestPool(t)
	ctx := context.Background()

	for name, g := range map[string]*fakeGenres{
		"fetch fails":   {err: errors.New("upstream 500")},
		"genre missing": {genres: []ticketmaster.Genre{{ID: "KnvZfZ7vAeA", Name: "Rock"}}},
	} {
		t.Run(name, func(t *testing.T) {
			addr := reviewTestAddr()
			if _, err := seedReviewAccount(ctx, pool, addr, g, nil); err == nil {
				t.Fatal("seed succeeded without its genres")
			}
			if n := count(t, pool, `SELECT count(*) FROM user_identities WHERE subject = $1`, addr); n != 0 {
				t.Fatal("an account was created anyway")
			}
		})
	}
}
