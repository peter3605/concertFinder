package concerts

import "testing"

// The case that decided the design. Ticketmaster returns a synthetic combined
// attraction first, so reading position naively reports USHER as supporting an
// act that does not exist.
func TestBillingSkipsTheCombinedPackageAttraction(t *testing.T) {
	lineup := []string{"Usher Raymond & Chris Brown", "USHER", "Chris Brown"}

	if got := billingOf(lineup, "USHER"); got != BillingHeadliner {
		t.Errorf("USHER billing = %q, want %q", got, BillingHeadliner)
	}
	if got := billingOf(lineup, "Chris Brown"); got != BillingSupport {
		t.Errorf("Chris Brown billing = %q, want %q", got, BillingSupport)
	}
}

// A real headliner whose name happens to contain a support act's must survive.
// One containment is a coincidence; the package pattern needs two.
func TestBillingKeepsALeadThatMerelyContainsOneOtherName(t *testing.T) {
	lineup := []string{"Foo Fighters", "Foo", "Bar"}

	if got := billingOf(lineup, "Foo Fighters"); got != BillingHeadliner {
		t.Errorf("Foo Fighters billing = %q, want %q", got, BillingHeadliner)
	}
	if got := billingOf(lineup, "Foo"); got != BillingSupport {
		t.Errorf("Foo billing = %q, want %q", got, BillingSupport)
	}
}

func TestBillingOrdinaryBill(t *testing.T) {
	lineup := []string{"Japanese Breakfast", "Mannequin Pussy"}

	if got := billingOf(lineup, "Japanese Breakfast"); got != BillingHeadliner {
		t.Errorf("headliner = %q, want %q", got, BillingHeadliner)
	}
	if got := billingOf(lineup, "Mannequin Pussy"); got != BillingSupport {
		t.Errorf("support = %q, want %q", got, BillingSupport)
	}
}

// Unknown is a real answer and must stay distinguishable from "support".
// Everything from the Phase 2 fallback chain arrives with no lineup at all,
// and an empty lineup must not quietly demote an artist to support.
func TestBillingUnknownWhenTheArtistIsNotOnTheList(t *testing.T) {
	if got := billingOf(nil, "Japanese Breakfast"); got != "" {
		t.Errorf("no lineup = %q, want unknown", got)
	}
	if got := billingOf([]string{"Someone Else"}, "Japanese Breakfast"); got != "" {
		t.Errorf("absent artist = %q, want unknown", got)
	}
}

// Matching goes through the dedup normalizer, so casing and punctuation
// differences between Spotify's spelling and Ticketmaster's do not silently
// turn a headliner into an unknown.
func TestBillingMatchesUnderNormalization(t *testing.T) {
	if got := billingOf([]string{"THE NATIONAL"}, "The National"); got != BillingHeadliner {
		t.Errorf("billing = %q, want %q", got, BillingHeadliner)
	}
}
