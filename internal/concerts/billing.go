package concerts

import "strings"

// Billing is where an act sits on the bill.
//
// **This is inferred, not reported.** Ticketmaster publishes no billing
// order: across a 400-event sample the union of every field on an attraction
// object is `_links, aliases, classifications, draftStatus, externalLinks,
// id, images, locale, name, test, type, upcomingEvents, url` -- there is no
// headliner flag, no support role, and nothing ordinal. The only available
// signal is position in the attraction array, which is a convention rather
// than a contract.
//
// So BillingSupport means "not first in a list that was never promised to be
// ordered". Treat it as a hint in the UI, never as a fact worth arguing with
// a user about, and never render it as authoritative copy like "opening for".
//
// The empty string is the third state and the common one: unknown. Every act
// found through the Phase 2 fallback chain lands there, because neither
// Songkick nor JSON-LD carries an ordered lineup either.
const (
	BillingHeadliner = "headliner"
	BillingSupport   = "support"
)

// billingOf infers artist's slot from its position in Ticketmaster's
// attraction list. Returns "" when the artist is not in the list at all,
// which happens whenever the event came from a source other than TM.
func billingOf(lineup []string, artist string) string {
	ordered := dropCombinedLead(lineup)
	target := Normalize(artist)
	if target == "" {
		return ""
	}
	for i, name := range ordered {
		if Normalize(name) != target {
			continue
		}
		if i == 0 {
			return BillingHeadliner
		}
		return BillingSupport
	}
	return ""
}

// dropCombinedLead removes a synthetic package attraction from the front of
// the lineup.
//
// Ticketmaster sometimes lists a combined act first and the real performers
// after it. The observed case:
//
//	"The R&B Tour - Starring Usher Raymond & Chris Brown"
//	  -> ["Usher Raymond & Chris Brown", "USHER", "Chris Brown"]
//
// Read positionally and untouched, that says USHER is supporting an act that
// does not exist. A lead entry whose name contains two or more of the other
// entries is that pattern, and dropping it makes the rest of the list line up
// with reality.
//
// Two or more, not one: a genuine headliner whose name happens to contain a
// support act's ("Foo Fighters" above "Foo") must not be discarded.
func dropCombinedLead(lineup []string) []string {
	if len(lineup) < 3 {
		return lineup
	}
	lead := Normalize(lineup[0])
	if lead == "" {
		return lineup
	}
	contained := 0
	for _, other := range lineup[1:] {
		n := Normalize(other)
		if n == "" || n == lead {
			continue
		}
		if strings.Contains(lead, n) {
			contained++
		}
	}
	if contained >= 2 {
		return lineup[1:]
	}
	return lineup
}
