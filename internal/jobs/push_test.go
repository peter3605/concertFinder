package jobs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
)

func act(name, dedup string) concerts.Act {
	return concerts.Act{
		Artist:   concerts.ArtistRef{ID: name, Name: name},
		DedupKey: dedup,
	}
}

func TestNotificationForSingleAct(t *testing.T) {
	ev := concerts.Event{
		EventKey: "ek1",
		Date:     time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		Venue:    "9:30 Club",
		City:     "Washington",
		Acts:     []concerts.Act{act("Turnstile", "dk1")},
	}
	n := notificationFor(ev)

	if n.Payload.APS.Alert.Title != "Turnstile" {
		t.Errorf("title = %q, want %q", n.Payload.APS.Alert.Title, "Turnstile")
	}
	if !strings.Contains(n.Payload.APS.Alert.Body, "9:30 Club") {
		t.Errorf("body %q does not name the venue", n.Payload.APS.Alert.Body)
	}
	// Collapsing on the event key is what stops a re-push for the same show
	// stacking a second banner on the device.
	if n.CollapseID != "ek1" {
		t.Errorf("CollapseID = %q, want the event key", n.CollapseID)
	}
	if n.Payload.EventKey != "ek1" || n.Payload.DedupKey != "dk1" {
		t.Errorf("payload keys = (%q, %q), want (ek1, dk1)", n.Payload.EventKey, n.Payload.DedupKey)
	}
}

// A festival the user matched six artists at is one night out. Six separate
// pushes for it reads as spam, so the worker groups before notifying and the
// title says how many others are on the bill.
func TestNotificationForMultiActBill(t *testing.T) {
	ev := concerts.Event{
		EventKey: "ek2",
		Date:     time.Date(2026, 7, 4, 18, 0, 0, 0, time.UTC),
		Venue:    "Merriweather Post Pavilion",
		City:     "Columbia",
		Acts: []concerts.Act{
			act("Turnstile", "dk1"),
			act("Snail Mail", "dk2"),
			act("Beach House", "dk3"),
		},
	}
	n := notificationFor(ev)

	if !strings.HasPrefix(n.Payload.APS.Alert.Title, "Turnstile") {
		t.Errorf("title = %q, want it to lead with the first act", n.Payload.APS.Alert.Title)
	}
	if !strings.Contains(n.Payload.APS.Alert.Title, "2 more") {
		t.Errorf("title = %q, want it to count the other two acts", n.Payload.APS.Alert.Title)
	}
}

// APNs rejects payloads over 4KB, and the rejection is per-notification —
// a festival card would fail silently for exactly the users it matters most
// to. The payload carries keys the app resolves against the feed it already
// has, so its size must not scale with the bill.
func TestNotificationPayloadStaysSmall(t *testing.T) {
	acts := make([]concerts.Act, 0, 40)
	for i := 0; i < 40; i++ {
		acts = append(acts, act(strings.Repeat("Long Artist Name ", 4), "dedup-key-that-is-not-short"))
	}
	ev := concerts.Event{
		EventKey: "ek3",
		Date:     time.Date(2026, 7, 4, 18, 0, 0, 0, time.UTC),
		Venue:    strings.Repeat("Venue ", 20),
		City:     "Columbia",
		Acts:     acts,
	}
	body, err := json.Marshal(notificationFor(ev).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 4096 {
		t.Fatalf("payload is %d bytes, over the APNs 4KB cap", len(body))
	}
}

func TestPushMaxAttemptsIsBounded(t *testing.T) {
	// River's default is 25. A device that is gone stays gone, and the
	// worker retires those tokens itself, so the retryable remainder is
	// transient and a handful is enough.
	opts := SendPushArgs{}.InsertOpts()
	if opts.MaxAttempts != PushMaxAttempts {
		t.Fatalf("MaxAttempts = %d, want %d", opts.MaxAttempts, PushMaxAttempts)
	}
	if PushMaxAttempts <= 0 || PushMaxAttempts > 5 {
		t.Fatalf("PushMaxAttempts = %d, want a small positive bound", PushMaxAttempts)
	}
}
