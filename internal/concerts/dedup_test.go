package concerts

import (
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
	d := time.Date(2026, 3, 15, 20, 0, 0, 0, time.UTC)
	k1 := DedupKey("Radiohead", d, "Madison Square Garden", "New York")
	k2 := DedupKey("radiohead", d, "MADISON SQUARE GARDEN!", "new-york")
	if k1 != k2 {
		t.Errorf("expected equal dedup keys, got %s vs %s", k1, k2)
	}
}

func TestDedupKey_DifferentDate(t *testing.T) {
	d1 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
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
