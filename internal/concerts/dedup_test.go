package concerts

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"The Beatles", "beatles"},
		{"the beatles", "beatles"},
		{"THE BEATLES", "beatles"},
		{"A Perfect Circle", "perfect circle"},
		{"An Evening With...", "evening with"},
		{"  Radiohead  ", "radiohead"},
		{"Sigur Rós!", "sigur ros"},
		{"AC/DC", "ac dc"},
		{"", ""},
		{"the", "the"}, // no trailing space means it's not the article
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDedupKey_MatchesAcrossSources(t *testing.T) {
	const d = "2026-03-15"
	k1 := DedupKey("Radiohead", d, "Madison Square Garden", "New York")
	k2 := DedupKey("radiohead", d, "MADISON SQUARE GARDEN!", "new-york")
	if k1 != k2 {
		t.Errorf("expected equal dedup keys, got %s vs %s", k1, k2)
	}
}

func TestDedupKey_DifferentDate(t *testing.T) {
	const d1, d2 = "2026-03-15", "2026-03-16"
	if DedupKey("X", d1, "V", "C") == DedupKey("X", d2, "V", "C") {
		t.Fatal("dates differ by one day should produce different keys")
	}
}

func TestMerger_CombinesLinksSorted(t *testing.T) {
	m := NewMerger()
	d := time.Date(2026, 3, 15, 20, 0, 0, 0, time.UTC)

	m.Add(Concert{
		Artist: ArtistRef{ID: "a1", Name: "Radiohead"},
		Date:   d, Venue: "MSG", City: "New York",
		Links: []TicketLink{{Source: SourceBandsintown, URL: "https://bit/1?tracked=1"}},
	})
	m.Add(Concert{
		Artist: ArtistRef{ID: "a1", Name: "Radiohead"},
		Date:   d, Venue: "MSG", City: "New York",
		Links: []TicketLink{{Source: SourceTicketmaster, URL: "https://tm/1"}},
	})
	all := m.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 merged concert, got %d", len(all))
	}
	got := all[0]
	if len(got.Links) != 2 {
		t.Fatalf("expected 2 links, got %+v", got.Links)
	}
	if got.Links[0].Source != SourceTicketmaster {
		t.Errorf("TM must come first, got %s", got.Links[0].Source)
	}
	if got.Links[1].Source != SourceBandsintown {
		t.Errorf("BIT must come second, got %s", got.Links[1].Source)
	}
	// tracking params preserved verbatim
	if got.Links[1].URL != "https://bit/1?tracked=1" {
		t.Errorf("BIT tracking params dropped: %s", got.Links[1].URL)
	}
}

func TestMerger_AllReturnsIndependentSlices(t *testing.T) {
	// Regression for the audit fix: All() must deep-copy Links so a later Add
	// doesn't mutate a slice a caller is already iterating.
	when := time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)
	m := NewMerger()
	m.Add(Concert{
		Artist: ArtistRef{Name: "Cardinal Bloom"}, Date: when, Venue: "Union Stage", City: "DC",
		Links: []TicketLink{{Source: SourceTicketmaster, URL: "https://a"}},
	})
	snapshot := m.All()
	m.Add(Concert{
		Artist: ArtistRef{Name: "Cardinal Bloom"}, Date: when, Venue: "Union Stage", City: "DC",
		Links: []TicketLink{{Source: SourceBandsintown, URL: "https://b"}},
	})
	if len(snapshot[0].Links) != 1 {
		t.Fatalf("snapshot slice must not be extended by later Add; got %d links", len(snapshot[0].Links))
	}
}

func TestMerger_SortByDateThenName(t *testing.T) {
	m := NewMerger()
	m.Add(Concert{Artist: ArtistRef{Name: "Z"}, Date: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), Venue: "v", City: "c"})
	m.Add(Concert{Artist: ArtistRef{Name: "A"}, Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Venue: "v", City: "c"})
	m.Add(Concert{Artist: ArtistRef{Name: "M"}, Date: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Venue: "v2", City: "c"})
	got := m.All()
	if got[0].Artist.Name != "A" || got[1].Artist.Name != "M" || got[2].Artist.Name != "Z" {
		t.Errorf("wrong order: %+v", got)
	}
}

// A source that is no longer produced but still appears in stored rows must
// keep sorting below the live ones. sourcePriority is a map, so a bare
// lookup returns 0 for a miss — a higher priority than Ticketmaster's 2 —
// which would promote retired links to the top of every card instead of
// demoting them.
func TestUnknownSourceSortsLast(t *testing.T) {
	links := []TicketLink{
		{Source: Source("some-retired-source"), URL: "https://retired"},
		{Source: SourceTicketmaster, URL: "https://tm"},
		{Source: SourceOfficial, URL: "https://official"},
	}
	SortLinks(links)
	if links[0].Source != SourceOfficial || links[1].Source != SourceTicketmaster {
		t.Fatalf("known sources should lead in priority order, got %+v", links)
	}
	if links[2].Source != Source("some-retired-source") {
		t.Errorf("unknown source should sort last, got %+v", links)
	}
}

// The bug this file's DedupKey change exists to fix, stated end to end.
//
// Ticketmaster sends dates.start.dateTime in UTC. A 20:00 Pacific show on the
// 15th is 03:00Z on the 16th, and keying off that instant filed the show under
// a day it is not on -- visible to the user as a concert listed under the
// wrong date, and to the janitor's past-show floor as a show that outlives
// itself by a day.
func TestDedupKeyUsesTheVenuesDayNotTheUTCDay(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*60*60)
	evening := time.Date(2026, 9, 15, 20, 0, 0, 0, pacific)

	if got := LocalDateOf(evening); got != "2026-09-15" {
		t.Fatalf("LocalDateOf = %q, want 2026-09-15 (the instant is 03:00Z on the 16th)", got)
	}
	if evening.UTC().Format("2006-01-02") != "2026-09-16" {
		t.Fatal("fixture no longer straddles the UTC day boundary; it is not testing anything")
	}

	local := DedupKey("Phoebe Bridgers", LocalDateOf(evening), "The Greek", "Los Angeles")
	utc := DedupKey("Phoebe Bridgers", evening.UTC().Format("2006-01-02"), "The Greek", "Los Angeles")
	if local == utc {
		t.Fatal("the two days produce the same key; DedupKey has stopped reading the day it is given")
	}
}

// A timeTBA event gaining a set time must not move its key. This is the half
// with teeth: dedup_key is the primary key of `concerts` and half of
// user_saved_concerts', so a moved key orphans the save silently and
// user_digest_sent re-notifies about a show already sent.
func TestDedupKeyIsStableWhenASetTimeIsPublished(t *testing.T) {
	const day = "2026-09-15"
	pacific := time.FixedZone("PDT", -7*60*60)

	tba := Concert{
		Artist: ArtistRef{Name: "Japanese Breakfast"},
		// Before the time is announced, TM gives only localDate, which
		// parses to midnight UTC.
		Date:      time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
		LocalDate: day,
		Venue:     "The Greek", City: "Los Angeles",
	}
	announced := tba
	announced.Date = time.Date(2026, 9, 15, 20, 0, 0, 0, pacific)

	before := DedupKey(tba.Artist.Name, tba.LocalDay(), tba.Venue, tba.City)
	after := DedupKey(announced.Artist.Name, announced.LocalDay(), announced.Venue, announced.City)
	if before != after {
		t.Error("publishing a set time moved the dedup key; every existing save for this show is now orphaned")
	}
	if EventKey(tba.LocalDay(), tba.Venue, tba.City) != EventKey(announced.LocalDay(), announced.Venue, announced.City) {
		t.Error("publishing a set time moved the event key; the show would split into two cards")
	}
}

// The key must survive the round trips it actually makes: concert_cache holds
// marshalled ticketmaster.Events and concerts.data holds a marshalled
// Concert. A time.Time carries its offset through JSON but not through a
// timestamptz column, which is why the day is stored as a string.
func TestLocalDateSurvivesAJSONRoundTrip(t *testing.T) {
	pacific := time.FixedZone("PDT", -7*60*60)
	c := Concert{
		Artist:    ArtistRef{Name: "Alvvays"},
		Date:      time.Date(2026, 9, 15, 20, 0, 0, 0, pacific),
		LocalDate: "2026-09-15",
		Venue:     "The Greek", City: "Los Angeles",
	}
	c.DedupKey = DedupKey(c.Artist.Name, c.LocalDay(), c.Venue, c.City)

	blob, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Concert
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.LocalDay() != c.LocalDay() {
		t.Errorf("local day changed across JSON: %q -> %q", c.LocalDay(), back.LocalDay())
	}
	if got := DedupKey(back.Artist.Name, back.LocalDay(), back.Venue, back.City); got != c.DedupKey {
		t.Error("recomputing the key after a round trip produced a different key")
	}
}

// Rows written before LocalDate existed have none, and they must keep the key
// they were already stored under -- their save in user_saved_concerts still
// points at it. For a Ticketmaster row that means rendering its UTC instant,
// which is the old, wrong-but-consistent answer.
func TestLocalDayFallsBackToTheStoredInstantForLegacyRows(t *testing.T) {
	legacy := Concert{
		Artist: ArtistRef{Name: "Radiohead"},
		Date:   time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC), // 20:00 PT on the 15th
		Venue:  "The Greek", City: "Los Angeles",
	}
	if got := legacy.LocalDay(); got != "2026-09-16" {
		t.Errorf("LocalDay = %q, want 2026-09-16 -- a legacy row must reproduce its existing key, not be silently re-dated", got)
	}
	// And an empty day must never be what gets keyed: that would fold every
	// date at this venue into one event.
	if legacy.LocalDay() == "" {
		t.Fatal("empty local day")
	}
}
