package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/picks"
	"github.com/peterho/concertfinder/internal/rate"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// The App Review account (CF-38, docs/public-path.md §6.2).
//
// Guideline 2.1 wants a demo account a reviewer can sign in to and find
// working, and a reviewer has no mailbox and is not on the Spotify allowlist.
// REVIEW_LOGIN_EMAIL + REVIEW_LOGIN_CODE answer the sign-in half
// (auth.EmailLogin.ReviewEmail). This answers the other half: an account that
// already has an email identity for that address, a location and genre picks,
// so the first sign-in lands on a feed that fills, not on onboarding.
//
// Genre picks rather than artist picks because a genre listing is a city's
// whole calendar for that genre and is never empty in a dense market, where
// any particular artist may have no dates this quarter. New York because it is
// the densest market and the first discover seed city.
//
// It is a mode of the server binary for the reason -grant-admin is: the image
// is distroless, so this binary is the only thing an operator can run in it.
//
//	docker compose exec api /server -seed-review-account

var (
	reviewLocation = db.UserLocation{Latitude: 40.71, Longitude: -74.01, RadiusMiles: 25}
	// Matched by name against Ticketmaster's own catalog, so the IDs stored
	// are the ones listings are fetched with.
	reviewGenres = []string{"Rock", "Pop", "Alternative"}
)

// reviewDisplayName is what the web header shows the reviewer, and also how
// a re-run recognises an account it made (seedReviewAccount).
const reviewDisplayName = "App Review"

// runSeedReviewAccount loads config as far as the database and Ticketmaster,
// seeds the account and reports what it did. Like the other operator modes,
// an unrelated missing variable does not stop it.
func runSeedReviewAccount() int {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-review-account: config load: %v\n", err)
		return 1
	}
	switch {
	case cfg.DatabaseURL == "":
		fmt.Fprintln(os.Stderr, "seed-review-account: DATABASE_URL is not set")
		return 1
	case cfg.ReviewLoginEmail == "":
		fmt.Fprintln(os.Stderr, "seed-review-account: REVIEW_LOGIN_EMAIL is not set; there is no review address to seed")
		return 1
	case cfg.TicketmasterAPIKey == "":
		fmt.Fprintln(os.Stderr, "seed-review-account: TICKETMASTER_API_KEY is not set; genre picks are validated against Ticketmaster's catalog")
		return 1
	}
	if cfg.ReviewLoginCode == "" {
		// Seeding first is a legitimate order, so this is not a failure. It
		// is worth saying, because the account is unreachable until it is set.
		fmt.Fprintln(os.Stderr, "seed-review-account: note: REVIEW_LOGIN_CODE is not set, so App Review cannot sign in yet")
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-review-account: connect: %v\n", err)
		return 1
	}
	defer pool.Close()

	ledger := &rate.Ledger{Pool: pool, Caps: rate.Caps{TicketmasterAccount: cfg.RateCapTMAccountDaily}}
	tm := ticketmaster.NewClient(&http.Client{Timeout: 10 * time.Second}, cfg.TicketmasterAPIKey)
	res, err := seedReviewAccount(ctx, pool, cfg.ReviewLoginEmail, tm, ledger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "seed-review-account: %v\n", err)
		return 1
	}
	// The address is not printed: deploy and exec output can end up in logs,
	// and the operator already knows which address they configured.
	verb := "updated"
	if res.created {
		verb = "created"
	}
	fmt.Printf("review account %s: location %.2f,%.2f (%d mi), genres %s\n",
		verb, reviewLocation.Latitude, reviewLocation.Longitude, reviewLocation.RadiusMiles,
		strings.Join(res.genres, ", "))
	return 0
}

type seedResult struct {
	userID  uuid.UUID
	created bool
	genres  []string
}

// seedReviewAccount makes the review account match the fixture, and is safe
// to run again: an existing account keeps its ID, sessions and saves, and only
// its location and genre picks are put back.
//
// It never takes over an address some other account already signs in with by
// email -- that would hand the review code to a real user's account -- and
// never touches users.email on an existing account for the same reason.
func seedReviewAccount(ctx context.Context, pool *pgxpool.Pool, addr string, genres picks.GenreLister, ledger *rate.Ledger) (seedResult, error) {
	// Resolve the genres before writing anything, so a Ticketmaster failure
	// leaves no half-seeded account behind.
	chosen, err := reviewGenrePicks(ctx, genres, ledger)
	if err != nil {
		return seedResult{}, err
	}

	res := seedResult{}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return seedResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `
SELECT user_id FROM user_identities WHERE provider = 'email' AND subject = $1 FOR UPDATE`, addr).Scan(&res.userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// The same shape admitEmailTx gives an email signup: no Spotify ID
		// and the empty credential, which AccessTokenFor answers with
		// ErrSpotifyDisconnected before any network call. No invite is
		// redeemed -- the operator is the gate here -- and the email
		// opt-ins keep their column defaults (off), so nothing is ever
		// mailed to this address.
		res.userID, res.created = uuid.New(), true
		if _, err := tx.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES ($1, NULL, $3, ''::bytea, ''::bytea, $2)`, res.userID, addr, reviewDisplayName); err != nil {
			return seedResult{}, fmt.Errorf("insert review user: %w", err)
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id) VALUES ('email', $1, $2)`, addr, res.userID); err != nil {
			return seedResult{}, fmt.Errorf("insert review identity: %w", err)
		}
	case err != nil:
		return seedResult{}, fmt.Errorf("look up review identity: %w", err)
	default:
		// An account already signs in with this address. Seeding it is only
		// right if this mode made it: since email sign-in, an email-only
		// account is as likely to be a person's, and re-seeding would replace
		// their location and picks and hand App Review's code to it. The
		// display name is the mark, and a second identity disqualifies it.
		var ours bool
		if err := tx.QueryRow(ctx, `
SELECT u.display_name = $2
   AND NOT EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = u.id AND i.provider <> 'email')
FROM users u WHERE u.id = $1`, res.userID, reviewDisplayName).Scan(&ours); err != nil {
			return seedResult{}, fmt.Errorf("check existing account: %w", err)
		}
		if !ours {
			return seedResult{}, errors.New("that address already signs in to an account this mode did not create; choose a REVIEW_LOGIN_EMAIL nobody uses")
		}
	}

	if _, err := tx.Exec(ctx, `
INSERT INTO user_locations (user_id, latitude, longitude, radius_miles, updated_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (user_id) DO UPDATE SET
  latitude = EXCLUDED.latitude, longitude = EXCLUDED.longitude,
  radius_miles = EXCLUDED.radius_miles, updated_at = now()`,
		res.userID, reviewLocation.Latitude, reviewLocation.Longitude, reviewLocation.RadiusMiles); err != nil {
		return seedResult{}, fmt.Errorf("set review location: %w", err)
	}
	// Replaced rather than added to, so a re-run restores the fixture even if
	// a reviewer changed the picks, and never trips db.MaxGenrePicks.
	if _, err := tx.Exec(ctx, `DELETE FROM user_genre_picks WHERE user_id = $1`, res.userID); err != nil {
		return seedResult{}, fmt.Errorf("clear review genres: %w", err)
	}
	for _, g := range chosen {
		if _, err := tx.Exec(ctx, `
INSERT INTO user_genre_picks (user_id, genre_id, name) VALUES ($1, $2, $3)`, res.userID, g.ID, g.Name); err != nil {
			return seedResult{}, fmt.Errorf("add review genre %s: %w", g.Name, err)
		}
		res.genres = append(res.genres, g.Name)
	}
	if err := tx.Commit(ctx); err != nil {
		return seedResult{}, fmt.Errorf("commit review account: %w", err)
	}
	return res, nil
}

// reviewGenrePicks finds reviewGenres in Ticketmaster's catalog. The one call
// is charged to the account-wide ledger, as the discover seed's are: it is a
// real spend against the shared allowance that belongs to no user.
func reviewGenrePicks(ctx context.Context, genres picks.GenreLister, ledger *rate.Ledger) ([]ticketmaster.Genre, error) {
	res, err := ledger.ReserveAccount(ctx, rate.SourceTicketmaster, 1)
	if err != nil {
		return nil, fmt.Errorf("reserve Ticketmaster call: %w", err)
	}
	if !res.Take() {
		return nil, errors.New("today's Ticketmaster allowance is spent; run this again after 00:00 UTC")
	}
	catalog, err := genres.MusicGenres(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch Ticketmaster genres: %w", err)
	}
	byName := make(map[string]ticketmaster.Genre, len(catalog))
	for _, g := range catalog {
		byName[strings.ToLower(g.Name)] = g
	}
	out := make([]ticketmaster.Genre, 0, len(reviewGenres))
	for _, name := range reviewGenres {
		g, ok := byName[strings.ToLower(name)]
		if !ok {
			// Refused rather than skipped: a review account seeded with fewer
			// genres than intended is a thinner feed nobody would notice.
			return nil, fmt.Errorf("Ticketmaster has no top-level Music genre named %q", name)
		}
		out = append(out, g)
	}
	return out, nil
}
