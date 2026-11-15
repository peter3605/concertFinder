package concerts

import (
	"testing"
	"time"
)

func viewerShow(id, name, venue string, day int) Concert {
	d := time.Date(2026, 9, 1+day, 20, 0, 0, 0, time.UTC)
	local := d.Format("2006-01-02")
	return Concert{
		Artist: ArtistRef{ID: id, Name: name}, Date: d, LocalDate: local,
		Venue: venue, City: "New York",
		DedupKey: DedupKey(name, local, venue, "New York"),
	}
}

func TestViewerMatchesChosen(t *testing.T) {
	v := ViewerMatches{
		Subscribed: map[string]struct{}{"tm:SUB": {}},
		Picked:     map[string]struct{}{"tm:PICK": {}},
	}
	cases := []struct {
		id   string
		want bool
	}{
		{"tm:SUB", true},                 // subscribed to an act found through a genre
		{"tm:PICK", true},                // picked by hand
		{"4Z8W4fKeB5YxbusRsdQVPb", true}, // a Spotify ID: profile artist
		{"tm:GENRE", false},              // genre listing only
		{"", false},
	}
	for _, tc := range cases {
		if got := v.Chosen(Concert{Artist: ArtistRef{ID: tc.id}}); got != tc.want {
			t.Errorf("Chosen(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
	// Nil maps are a degraded lookup, not a panic, and the profile half
	// still works without them.
	var empty ViewerMatches
	if !empty.Chosen(Concert{Artist: ArtistRef{ID: "spotifyid"}}) || empty.Chosen(Concert{Artist: ArtistRef{ID: "tm:X"}}) {
		t.Error("zero ViewerMatches misclassified")
	}
}

func TestViewerMatchesTag(t *testing.T) {
	v := ViewerMatches{
		Subscribed: map[string]struct{}{"a": {}},
		Picked:     map[string]struct{}{"tm:P": {}},
		Reasons:    map[string]string{"a": "You follow them", "tm:P": "#3 in your top artists"},
	}
	cs := []Concert{{Artist: ArtistRef{ID: "a"}}, {Artist: ArtistRef{ID: "tm:P"}}, {Artist: ArtistRef{ID: "tm:Q"}}}
	v.Reasons = map[string]string{"a": "You follow them"}
	v.Tag(cs)
	if !cs[0].Subscribed || cs[0].Reason != "You follow them" {
		t.Errorf("profile+subscribed act = %+v", cs[0])
	}
	if cs[1].Subscribed || cs[1].Reason != PickedReason {
		t.Errorf("picked act = %+v", cs[1])
	}
	if cs[2].Subscribed || cs[2].Reason != "" {
		t.Errorf("genre act = %+v", cs[2])
	}
}

// Whole events move together, and order is kept within each half. A festival
// with one chosen act and one genre act leads as one event.
func TestChosenFirstMovesWholeEventsAndKeepsOrder(t *testing.T) {
	cs := []Concert{
		viewerShow("tm:G1", "Genre One", "Room A", 0),
		viewerShow("tm:G2", "Genre Two", "Fest", 1),
		viewerShow("spotify1", "Chosen One", "Fest", 1), // same event as Genre Two
		viewerShow("tm:G3", "Genre Three", "Room B", 2),
		viewerShow("spotify2", "Chosen Two", "Room C", 3),
	}
	got := ChosenFirst(cs, ViewerMatches{}.Chosen)
	var names []string
	for _, c := range got {
		names = append(names, c.Artist.Name)
	}
	want := []string{"Genre Two", "Chosen One", "Chosen Two", "Genre One", "Genre Three"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}
}
