package picks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// Ticketmaster returned 309 attractions for "Ghost" and the band itself, as
// "GHOST", was not among the first ten (docs/public-path.md §1.1). Ranking
// over the size=200 page must bring it to the top.
func TestRank_PromotesExactMatchFromDeepInThePage(t *testing.T) {
	var in []ticketmaster.AttractionCandidate
	for i := range 150 {
		in = append(in, ticketmaster.AttractionCandidate{
			ID:       fmt.Sprintf("noise-%d", i),
			Name:     fmt.Sprintf("Ghost Town Revue %d", i),
			Upcoming: 50, // busier than the real band, so position alone cannot explain the result
		})
	}
	in = append(in[:120], append([]ticketmaster.AttractionCandidate{{ID: "K8vGHOST", Name: "GHOST", Genre: "Rock", Upcoming: 3}}, in[120:]...)...)

	got := Rank("Ghost", in)
	if len(got) != ResultLimit {
		t.Fatalf("got %d results, want %d", len(got), ResultLimit)
	}
	if got[0].AttractionID != "K8vGHOST" || !got[0].Exact {
		t.Fatalf("first result = %+v, want the exact match GHOST", got[0])
	}
	for _, r := range got[1:] {
		if r.Exact {
			t.Errorf("%q marked exact for query Ghost", r.Name)
		}
	}
}

// "Beyonce" typed without the accent: Ticketmaster returned "Beyoncé" as the
// second result with no exact match.
func TestRank_FoldsDiacritics(t *testing.T) {
	in := []ticketmaster.AttractionCandidate{
		{ID: "a", Name: "Beyonce Tribute Night", Upcoming: 9},
		{ID: "K8vBEY", Name: "Beyoncé", Upcoming: 1},
		{ID: "c", Name: "Beyonce Brunch", Upcoming: 4},
	}
	got := Rank("Beyonce", in)
	if got[0].AttractionID != "K8vBEY" || !got[0].Exact {
		t.Fatalf("first result = %+v, want Beyoncé as an exact match", got[0])
	}
	if got[len(got)-1].AttractionID != "a" {
		t.Errorf("tribute act not last: %+v", got)
	}
}

// The top three for "Taylor Swift" were tributes. The real artist comes
// first, and every tribute sinks below every non-tribute even when it has
// more upcoming shows.
func TestRank_DemotesTributeActs(t *testing.T) {
	in := []ticketmaster.AttractionCandidate{
		{ID: "t1", Name: "Taylor Swift Tribute - Red Night", Upcoming: 40},
		{ID: "t2", Name: "The Taylor Swift Experience", Upcoming: 35},
		{ID: "t3", Name: "Swiftie Night: A Salute to Taylor Swift", Upcoming: 30},
		{ID: "K8vTS", Name: "Taylor Swift", Upcoming: 2},
		{ID: "n1", Name: "Taylor Swift Karaoke Brunch", Upcoming: 1},
	}
	got := Rank("taylor swift", in)
	want := []string{"K8vTS", "n1", "t1", "t2", "t3"}
	for i, id := range want {
		if got[i].AttractionID != id {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	}
}

// Duplicate exact entries exist ("Disclosure" has two); the one with shows
// goes first so the default choice is the live one.
func TestRank_BreaksExactTiesByUpcoming(t *testing.T) {
	in := []ticketmaster.AttractionCandidate{
		{ID: "dead", Name: "Disclosure", Genre: "Rock", Upcoming: 0},
		{ID: "live", Name: "Disclosure", Genre: "Dance/Electronic", Upcoming: 2},
	}
	got := Rank("Disclosure", in)
	if got[0].AttractionID != "live" || !got[1].Exact {
		t.Fatalf("order = %+v", got)
	}
}

// A name made only of punctuation normalizes to nothing; it must not make
// every other punctuation-only name an exact match.
func TestRank_PunctuationOnlyQuery(t *testing.T) {
	in := []ticketmaster.AttractionCandidate{
		{ID: "x", Name: "???"},
		{ID: "chk", Name: "!!! (chk chk chk)"},
		{ID: "bang", Name: "!!!"},
	}
	got := Rank("!!!", in)
	if got[0].AttractionID != "bang" || !got[0].Exact {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Exact || got[2].Exact {
		t.Errorf("non-matching names marked exact: %+v", got)
	}
}

type countingSearcher struct {
	calls int
	byID  int
	out   []ticketmaster.AttractionCandidate
}

func (c *countingSearcher) SearchAttractions(context.Context, string) ([]ticketmaster.AttractionCandidate, error) {
	c.calls++
	return c.out, nil
}

func (c *countingSearcher) GetAttraction(_ context.Context, id string) (ticketmaster.AttractionCandidate, error) {
	c.byID++
	for _, a := range c.out {
		if a.ID == id {
			return a, nil
		}
	}
	return ticketmaster.AttractionCandidate{}, ticketmaster.ErrAttractionNotFound
}

// An attraction a search just showed is added without another call; one the
// server has not seen costs exactly one by-ID call, and only when the caller
// agrees to pay for it.
func TestService_LookupPrefersWhatASearchShowed(t *testing.T) {
	tm := &countingSearcher{out: []ticketmaster.AttractionCandidate{
		{ID: "K8vSEEN", Name: "Horsegirl", Genre: "Rock"},
		{ID: "K8vUNSEEN", Name: "Cola", Genre: "Rock"},
	}}
	s := &Service{TM: tm}
	ctx := context.Background()
	tm.out = tm.out[:1]
	if _, err := s.Search(ctx, "Horsegirl"); err != nil {
		t.Fatal(err)
	}
	tm.out = append(tm.out, ticketmaster.AttractionCandidate{ID: "K8vUNSEEN", Name: "Cola", Genre: "Rock"})

	paid := 0
	pay := func() bool { paid++; return true }
	r, fetched, err := s.Lookup(ctx, "K8vSEEN", pay)
	if err != nil || fetched || r.Name != "Horsegirl" || r.Genre != "Rock" {
		t.Fatalf("seen lookup = %+v fetched=%v err=%v", r, fetched, err)
	}
	if tm.byID != 0 || paid != 0 {
		t.Fatalf("seen attraction cost a call: byID=%d paid=%d", tm.byID, paid)
	}

	if _, _, err := s.Lookup(ctx, "K8vUNSEEN", func() bool { return false }); err != ErrQuota {
		t.Fatalf("refused quota: err = %v, want ErrQuota", err)
	}
	if tm.byID != 0 {
		t.Fatal("upstream called after the caller refused to pay")
	}
	r, fetched, err = s.Lookup(ctx, "K8vUNSEEN", pay)
	if err != nil || !fetched || r.Name != "Cola" || tm.byID != 1 || paid != 1 {
		t.Fatalf("unseen lookup = %+v fetched=%v err=%v byID=%d paid=%d", r, fetched, err, tm.byID, paid)
	}
}

// One upstream call per distinct query per CacheTTL, and case or spacing does
// not make a query distinct.
func TestService_CachesPerQueryUntilTTL(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	tm := &countingSearcher{out: []ticketmaster.AttractionCandidate{{ID: "K8vGEESE", Name: "Geese"}}}
	s := &Service{TM: tm, now: func() time.Time { return now }}

	if _, ok := s.Cached("Geese"); ok {
		t.Fatal("cache hit before any search")
	}
	if _, err := s.Search(context.Background(), "Geese"); err != nil {
		t.Fatal(err)
	}
	if r, ok := s.Cached("  geese "); !ok || len(r) != 1 {
		t.Fatalf("cache miss for the same query re-cased: %v %v", r, ok)
	}
	now = now.Add(CacheTTL)
	if _, ok := s.Cached("Geese"); ok {
		t.Fatal("cache hit at CacheTTL; entries must expire")
	}
	if tm.calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", tm.calls)
	}
}

func ids(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.AttractionID
	}
	return out
}
