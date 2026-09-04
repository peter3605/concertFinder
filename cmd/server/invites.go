package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
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

// inviteAlphabet deliberately omits I, L, O, U, 0 and 1. These codes get read
// down a phone, typed off a screenshot and pasted out of a message with a
// capitalisation-correcting keyboard in the way, so the characters that are
// indistinguishable in a sans-serif font are simply not minted. U is dropped
// as well, which is Crockford's convention and costs nothing.
const inviteAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// inviteGroups x inviteGroupLen characters of entropy: 8 characters from a
// 30-symbol alphabet is ~39 bits. That is far short of a password, and it does
// not need to be one -- a guessed code buys a signup slot, not an account,
// because redeeming it still requires a full Spotify OAuth grant from the
// guesser. What it does need is to survive the /api/auth rate limiter's 5/s
// standing between an attacker and the guess, which at 39 bits it does by
// several billion years.
const (
	inviteGroups   = 2
	inviteGroupLen = 4
)

// newInviteCode returns a code in the form CF-XXXX-XXXX.
//
// It reads from crypto/rand and refuses to fall back to anything weaker: a
// math/rand code would be predictable from the mint time, and the failure
// would look exactly like a working code.
func newInviteCode() (string, error) {
	buf := make([]byte, inviteGroups*inviteGroupLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	groups := make([]string, 0, inviteGroups+1)
	groups = append(groups, "CF")
	for g := range inviteGroups {
		var sb strings.Builder
		for i := range inviteGroupLen {
			// Modulo bias across a 30-symbol alphabet over 256 values is
			// negligible at this entropy level and this is not a key.
			sb.WriteByte(inviteAlphabet[int(buf[g*inviteGroupLen+i])%len(inviteAlphabet)])
		}
		groups = append(groups, sb.String())
	}
	return strings.Join(groups, "-"), nil
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
			state := "usable"
			switch {
			case c.DisabledAt != nil:
				state = "disabled"
			case c.ExpiresAt != nil && !c.ExpiresAt.After(now):
				state = "expired"
			case c.Redemptions >= c.MaxRedemptions:
				state = "spent"
			}
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
		code, err := newInviteCode()
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
