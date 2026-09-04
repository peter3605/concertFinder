package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/peterho/concertfinder/internal/config"
	"github.com/peterho/concertfinder/internal/db"
)

// inviteAdminArgs is the parsed shape of the -mint-invite / -list-invites /
// -disable-invite flags. See main() for why invite administration is a mode of
// the server binary rather than its own command.
type inviteAdminArgs struct {
	mint    bool
	list    bool
	disable string
	note    string
	uses    int
	days    int
}

// runInviteAdmin performs one invite operation and returns a process exit
// code. It loads config only far enough to reach the database — an operator
// minting a code should not be blocked by, say, a missing APNs key.
func runInviteAdmin(a inviteAdminArgs) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "invite: config load: %v\n", err)
		return 1
	}
	if cfg.DatabaseURL == "" {
		fmt.Fprintln(os.Stderr, "invite: DATABASE_URL is not set")
		return 1
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invite: connect: %v\n", err)
		return 1
	}
	defer pool.Close()

	switch {
	case a.disable != "":
		code := db.NormalizeInviteCode(a.disable)
		if err := db.DisableInviteCode(ctx, pool, code); err != nil {
			fmt.Fprintf(os.Stderr, "invite: disable %s: %v\n", code, err)
			return 1
		}
		fmt.Printf("disabled %s\n", code)
		return 0

	case a.list:
		codes, err := db.ListInviteCodes(ctx, pool)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invite: list: %v\n", err)
			return 1
		}
		if len(codes) == 0 {
			fmt.Println("no invite codes yet — mint one with -mint-invite")
			return 0
		}
		now := time.Now()
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CODE\tUSED\tSTATE\tEXPIRES\tNOTE")
		for _, c := range codes {
			// db.InviteCode.State, not a switch of our own. The admin console
			// renders the same four words from the same function, so a code
			// this calls "spent" cannot be "expired" in the browser.
			state := c.State(now)
			expires := "never"
			if c.ExpiresAt != nil {
				expires = c.ExpiresAt.UTC().Format("2006-01-02")
			}
			fmt.Fprintf(tw, "%s\t%d/%d\t%s\t%s\t%s\n",
				c.Code, c.Redemptions, c.MaxRedemptions, state, expires, c.Note)
		}
		return flushOrFail(tw)

	case a.mint:
		var expiresAt *time.Time
		if a.days > 0 {
			t := time.Now().UTC().AddDate(0, 0, a.days)
			expiresAt = &t
		}
		code, err := db.NewInviteCode()
		if err != nil {
			fmt.Fprintf(os.Stderr, "invite: generate: %v\n", err)
			return 1
		}
		created, err := db.CreateInviteCode(ctx, pool, code, a.note, a.uses, expiresAt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invite: mint: %v\n", err)
			return 1
		}
		// The code itself on stdout and alone on its line, so this is safe to
		// pipe. Everything else goes to stderr.
		fmt.Fprintf(os.Stderr, "minted for %q — %d use(s), expires %s\n",
			created.Note, created.MaxRedemptions, expiresDescription(created.ExpiresAt))
		fmt.Println(created.Code)
		return 0
	}
	return 0
}

func expiresDescription(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.UTC().Format("2006-01-02")
}

func flushOrFail(tw *tabwriter.Writer) int {
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(os.Stderr, "invite: write output: %v\n", err)
		return 1
	}
	return 0
}
