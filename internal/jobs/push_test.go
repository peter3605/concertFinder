package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/push"
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

// stubAPNs answers per device token, so one round can mix a delivery, a dead
// token and a transient rejection the way a real one does.
type stubAPNs struct {
	errs map[string]error
	sent []push.Notification
}

func (s *stubAPNs) Send(_ context.Context, n push.Notification) error {
	s.sent = append(s.sent, n)
	return s.errs[n.DeviceToken]
}

func oneEvent(dedup ...string) []concerts.Event {
	acts := make([]concerts.Act, 0, len(dedup))
	for i, dk := range dedup {
		acts = append(acts, act("Artist "+string(rune('A'+i)), dk))
	}
	return []concerts.Event{{
		EventKey: "ek1",
		Date:     time.Date(2026, 9, 12, 20, 0, 0, 0, time.UTC),
		Venue:    "9:30 Club",
		City:     "Washington",
		Acts:     acts,
	}}
}

// The failure this exists to stop: the ledger was written before the sends, so
// a rejection APNs would have accepted a moment later burned the key anyway.
// River's retry then found it already recorded and pushed nothing, and the
// user was never told about the show — no error, no log, nothing to notice.
func TestSendRoundHoldsBackKeysAfterATransientFailure(t *testing.T) {
	dev := db.Device{DeviceToken: "a-token", Environment: push.EnvSandbox}
	sender := &stubAPNs{errs: map[string]error{"a-token": errors.New("connection reset")}}

	res := sendRound(context.Background(), sender, oneEvent("dk1", "dk2"), []db.Device{dev}, uuid.New(),
		func(db.Device) { t.Error("retired a device over a transient error; it would never be pushed to again") })

	if len(res.Delivered) != 0 {
		t.Errorf("Delivered = %v, want none — recording these keys is what loses the notification", res.Delivered)
	}
	if res.Transient != 1 {
		t.Errorf("Transient = %d, want 1 so the job fails and river retries", res.Transient)
	}
}

// A token APNs calls dead never succeeds, so its keys are recorded: the
// alternative is a job that fails forever over a device that no longer exists.
func TestSendRoundRecordsKeysRefusedForADeadToken(t *testing.T) {
	dev := db.Device{DeviceToken: "a-token", Environment: push.EnvSandbox}
	sender := &stubAPNs{errs: map[string]error{
		"a-token": &push.Error{StatusCode: http.StatusGone, Reason: "Unregistered"},
	}}
	var retired []string

	res := sendRound(context.Background(), sender, oneEvent("dk1", "dk2"), []db.Device{dev}, uuid.New(),
		func(d db.Device) { retired = append(retired, d.DeviceToken) })

	if len(res.Delivered) != 2 {
		t.Errorf("Delivered = %v, want both keys", res.Delivered)
	}
	if res.Transient != 0 {
		t.Errorf("Transient = %d, want 0 — no retry improves a dead token", res.Transient)
	}
	if len(retired) != 1 || retired[0] != "a-token" {
		t.Errorf("retired = %v, want the dead token", retired)
	}
}

// Every act on a bill travels together. Recording the acts that happened to
// land while the event as a whole must be re-sent would leave the retry
// pushing a card missing half its lineup.
func TestSendRoundHoldsBackAWholeEventWhenOneDeviceFails(t *testing.T) {
	devices := []db.Device{
		{DeviceToken: "ok-token", Environment: push.EnvSandbox},
		{DeviceToken: "bad-token", Environment: push.EnvSandbox},
	}
	sender := &stubAPNs{errs: map[string]error{"bad-token": errors.New("i/o timeout")}}

	res := sendRound(context.Background(), sender, oneEvent("dk1", "dk2"), devices, uuid.New(), func(db.Device) {})

	if res.Sent != 1 {
		t.Errorf("Sent = %d, want 1 (the healthy device did receive it)", res.Sent)
	}
	if len(res.Delivered) != 0 {
		t.Errorf("Delivered = %v, want none while the event is still owed to a device", res.Delivered)
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

// An event with no acts is not a bad notification, it is a panic:
// notificationFor indexes Acts[0], and a panic inside Work takes the
// goroutine river is running the job on rather than failing the job, so every
// event queued behind it in the same batch is lost silently. withActs is what
// keeps that to a skipped event and a log line.
func TestWithActsDropsEmptyEventsAndKeepsTheRest(t *testing.T) {
	events := []concerts.Event{
		{EventKey: "ek1", Acts: []concerts.Act{act("Turnstile", "dk1")}},
		{EventKey: "ek2"},
		{EventKey: "ek3", Acts: []concerts.Act{act("Ceremony", "dk3")}},
	}

	kept := withActs(events, uuid.New())

	if len(kept) != 2 {
		t.Fatalf("kept %d events, want 2", len(kept))
	}
	if kept[0].EventKey != "ek1" || kept[1].EventKey != "ek3" {
		t.Errorf("kept = %q, %q; want ek1, ek3", kept[0].EventKey, kept[1].EventKey)
	}
	// The point of the guard: what survives can be handed to notificationFor.
	for _, ev := range kept {
		notificationFor(ev)
	}
}
