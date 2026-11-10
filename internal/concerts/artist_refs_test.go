package concerts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEncodeSharedRowCarriesNoArtistIdentity(t *testing.T) {
	c := Concert{
		Artist:   ArtistRef{ID: "sp1", Name: "Phoebe Bridgers", Genres: []string{"indie folk"}},
		Date:     time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC),
		Venue:    "9:30 Club",
		City:     "Washington",
		DedupKey: "k",
		Links:    []TicketLink{{Source: SourceTicketmaster, URL: "https://tm.example/e"}},
	}
	b, err := EncodeSharedRow(c)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(b, &row); err != nil {
		t.Fatal(err)
	}
	if string(row["artist"]) != `{"name":"Phoebe Bridgers"}` {
		t.Errorf("shared row artist = %s, want the name alone", row["artist"])
	}
	// Everything that is not artist identity survives, so the row still
	// decodes into a whole Concert for the readers.
	var back Concert
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Venue != c.Venue || back.DedupKey != c.DedupKey || len(back.Links) != 1 || !back.Date.Equal(c.Date) {
		t.Errorf("shared row lost event fields: %+v", back)
	}
	if strings.Contains(string(b), "sp1") || strings.Contains(string(b), "indie folk") {
		t.Errorf("shared row %s carries the artist's ID or genres", b)
	}
}

func TestApplyArtistRefsNeverFallsBackToTheRow(t *testing.T) {
	// A row as it stands before the strip migration: still carrying the last
	// writer's identity.
	cs := []Concert{
		{DedupKey: "mine", Artist: ArtistRef{ID: "theirs", Name: "a", Genres: []string{"their genre"}}},
		{DedupKey: "unknown", Artist: ArtistRef{ID: "theirs", Name: "b", Genres: []string{"their genre"}}},
	}
	refs := ArtistRefs{"mine": {ID: "mine-id", Name: "A", Genres: []string{"my genre"}}}
	ApplyArtistRefs(cs, refs)

	if cs[0].Artist.ID != "mine-id" || cs[0].Artist.Name != "A" || cs[0].Artist.Genres[0] != "my genre" {
		t.Errorf("own ref not applied: %+v", cs[0].Artist)
	}
	if cs[1].Artist.ID != "" || cs[1].Artist.Genres != nil {
		t.Errorf("a row with no ref for this viewer kept the row's identity: %+v", cs[1].Artist)
	}
	if cs[1].Artist.Name != "b" {
		t.Errorf("name is shared and should survive, got %q", cs[1].Artist.Name)
	}

	// The overlay copies: mutating a result must not reach into the refs,
	// which a snapshot cache may hand to the next request.
	cs[0].Artist.Genres[0] = "mutated"
	if refs["mine"].Genres[0] != "my genre" {
		t.Error("ApplyArtistRefs aliased the ref's genre slice")
	}
}

func TestRefsOfRoundTripsThroughTheSnapshotColumn(t *testing.T) {
	found := []Concert{{DedupKey: "k", Artist: ArtistRef{ID: "tm:K8", Name: "A", Genres: []string{"Rock"}}}}
	b, err := json.Marshal(RefsOf(found))
	if err != nil {
		t.Fatal(err)
	}
	refs, err := DecodeArtistRefs(b)
	if err != nil {
		t.Fatal(err)
	}
	if refs["k"].ID != "tm:K8" || refs["k"].Genres[0] != "Rock" {
		t.Errorf("round trip lost the ref: %+v", refs)
	}
	if empty, err := DecodeArtistRefs(nil); err != nil || len(empty) != 0 {
		t.Errorf("empty column = %v, %v; want an empty set", empty, err)
	}
	if got := ArtistRefsFromRaw(map[string][]byte{"k": []byte(`{"id":"x"}`), "bad": []byte(`nope`), "nil": nil}); len(got) != 1 || got["k"].ID != "x" {
		t.Errorf("ArtistRefsFromRaw = %+v, want only the decodable ref", got)
	}
}
