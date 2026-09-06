package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
)

// adminArgs is the parsed shape of -grant-admin / -revoke-admin /
// -list-admins.
//
// These are modes of the server binary for the same reason the invite flags
// are: the api image is distroless, so `docker compose exec api` has no shell
// and no curl, and this binary is the only executable in there. They are also
// the ONLY way the first admin can exist -- the web console requires an admin
// to grant one, and at migration time there are none -- so this is the
// bootstrap, not a convenience.
type adminArgs struct {
	grant  string
	revoke string
	list   bool
}

// runAdminAdmin performs one admin-flag operation and returns a process exit
// code. Config is loaded only as far as the database, matching runInviteAdmin:
// an operator granting themselves the flag should not be stopped by an
// unrelated missing key.
func runAdminAdmin(a adminArgs) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin: config load: %v\n", err)
		return 1
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(os.Stderr, "admin: DATABASE_URL is not set")
		return 1
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "admin: connect: %v\n", err)
		return 1
	}
	defer pool.Close()

	switch {
	case a.grant != "" || a.revoke != "":
		spotifyID, grant := a.grant, true
		if a.revoke != "" {
			spotifyID, grant = a.revoke, false
		}
		acct, err := db.SetAdmin(ctx, pool, spotifyID, grant)
		if err != nil {
			if errors.Is(err, db.ErrNoRows) {
				// The likeliest reason by far, so say it rather than leaving
				// the operator to wonder whether the write failed: the flag
				// lives on a users row, and there is no row until that
				// Spotify account has signed in at least once.
				fmt.Fprintf(os.Stderr,
					"admin: no user with spotify_user_id %q — they must sign in once before they can be granted the flag\n",
					spotifyID)
				return 1
			}
			fmt.Fprintf(os.Stderr, "admin: set admin %s: %v\n", spotifyID, err)
			return 1
		}
		verb := "granted"
		if !grant {
			verb = "revoked"
		}
		fmt.Printf("%s admin for %s (%s)\n", verb, acct.SpotifyUserID, acct.DisplayName)
		return 0

	case a.list:
		admins, err := db.ListAdmins(ctx, pool)
		if err != nil {
			fmt.Fprintf(os.Stderr, "admin: list: %v\n", err)
			return 1
		}
		if len(admins) == 0 {
			fmt.Println("no admins yet — grant one with -grant-admin <spotify_user_id>")
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SPOTIFY ID\tNAME\tJOINED")
		for _, a := range admins {
			fmt.Fprintf(tw, "%s\t%s\t%s\n",
				a.SpotifyUserID, a.DisplayName, a.CreatedAt.UTC().Format("2006-01-02"))
		}
		return flushOrFail(tw)
	}
	return 0
}
