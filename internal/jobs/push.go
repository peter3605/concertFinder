package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/push"
)

// PushMaxAttempts bounds river's retries for a push delivery. See
// SendPushArgs.InsertOpts.
const PushMaxAttempts = 3

// SendPushWorker delivers APNs notifications covering a set of dedup_keys.
// Mirrors SendInstantNotifyWorker — same trigger, same net-new filtering —
// but on its own ledger channel so the two do not consume each other's
// candidates. See migration 0016.
type SendPushWorker struct {
	river.WorkerDefaults[SendPushArgs]
	Pool *pgxpool.Pool
	// APNs is nil when the deployment has no push configuration. The worker
	// then no-ops rather than failing: a server without APNs credentials
	// should still run every other job.
	APNs *push.Client
}

func (w *SendPushWorker) Work(ctx context.Context, job *river.Job[SendPushArgs]) error {
	if w.APNs == nil || len(job.Args.DedupKeys) == 0 {
		return nil
	}
	user, err := db.GetUserByID(ctx, w.Pool, job.Args.UserID)
	if err != nil {
		return err
	}
	// Opted out between enqueue and run.
	if !user.PushOptIn {
		return nil
	}
	devices, err := db.ListLiveDevices(ctx, w.Pool, user.ID)
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		return nil
	}
	devices = forEnvironment(devices, w.APNs.Environments(), user.ID)
	if len(devices) == 0 {
		return nil
	}

	// Net-new on the push channel specifically. Filtering without the
	// channel would let whichever of the email and push workers ran first
	// swallow the other's candidates — the silent-suppression bug migration
	// 0016 exists to prevent.
	unsent, err := db.FilterUnsentDedupKeys(ctx, w.Pool, user.ID, db.ChannelPush, job.Args.DedupKeys)
	if err != nil {
		return err
	}
	if len(unsent) == 0 {
		return nil
	}
	// Preserve the enqueued order; Go randomizes map iteration and
	// AssembleByKey keeps whatever order it is handed.
	keys := make([]string, 0, len(unsent))
	for _, k := range job.Args.DedupKeys {
		if _, ok := unsent[k]; ok {
			keys = append(keys, k)
		}
	}
	blobs, err := db.GetConcertsByDedupKeys(ctx, w.Pool, keys)
	if err != nil {
		return err
	}
	fresh, err := concerts.AssembleByKey(keys, blobs)
	if err != nil {
		return err
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Slice(fresh, func(i, j int) bool {
		if !fresh[i].Date.Equal(fresh[j].Date) {
			return fresh[i].Date.Before(fresh[j].Date)
		}
		return fresh[i].Artist.Name < fresh[j].Artist.Name
	})

	// Group before notifying, for the same reason the email renderers do: a
	// festival that matched six artists is one night out, and six separate
	// pushes for it reads as spam. One notification per event.
	events := concerts.GroupEvents(fresh)

	sendKeys := make([]string, 0, len(fresh))
	for _, c := range fresh {
		sendKeys = append(sendKeys, c.DedupKey)
	}
	// Record before sending, matching the digest's at-most-once trade-off:
	// a crash between the two costs a notification, whereas recording after
	// would re-push the whole set on every retry.
	if err := db.RecordDigestSent(ctx, w.Pool, user.ID, db.ChannelPush, sendKeys); err != nil {
		return err
	}

	var sent, retired int
	for _, ev := range withActs(events, user.ID) {
		n := notificationFor(ev)
		for _, d := range devices {
			err := w.APNs.Send(ctx, addressedTo(n, d))
			if err == nil {
				sent++
				continue
			}
			var apnsErr *push.Error
			if errors.As(err, &apnsErr) && apnsErr.IsUnregistered() {
				// The app was uninstalled or the token was reissued. An
				// environment mismatch cannot land here: each token now goes
				// to its own host, and a host the key is not authorized for
				// is refused before the request. So reaching here really does
				// mean the token is dead.
				if derr := db.DisableDevice(ctx, w.Pool, user.ID, d.DeviceToken); derr != nil {
					slog.Warn("push: disable device failed", "err", derr, "user", user.ID)
				}
				retired++
				continue
			}
			// Transient: let river retry the job. The ledger already
			// records these keys, so a retry re-pushes nothing.
			slog.Warn("push: send failed", "err", err, "user", user.ID)
		}
	}
	slog.Info("push sent", "user", user.ID, "events", len(events), "deliveries", sent, "devices_retired", retired)
	return nil
}

// addressedTo stamps a rendered notification with one device's address.
//
// A copy, and both fields together, on purpose. The token and the environment
// are minted as a pair and neither means anything at the other host, so
// setting the token alone -- which the previous shape of this loop allowed,
// by mutating one Notification per device -- sends a sandbox token to the
// production host. That answers BadDeviceToken, which Error.IsUnregistered
// reports as a dead token, which the loop below retires permanently. The user
// stops hearing from the app and nothing says why.
//
// Taking a copy also means no field can survive from the previous device.
func addressedTo(n push.Notification, d db.Device) push.Notification {
	n.DeviceToken = d.DeviceToken
	n.Environment = d.Environment
	return n
}

// notificationFor renders one event as a push. The payload deliberately
// carries keys rather than the event: APNs caps at 4KB, and the app resolves
// event_key against the feed it already has.
// forEnvironment keeps only the tokens this deployment's APNs key is
// authorized to send to.
//
// Usually that is all of them: an auth key issued as "Sandbox & Production"
// serves both, and each token is routed to its own host. The filter matters
// for a key restricted to one environment, where the other host answers
// InvalidProviderToken and the job fails for a reason no retry improves.
//
// It is also the last line of defence for the older hazard. A token belongs
// to exactly one environment and the wrong host answers BadDeviceToken, a
// rejection indistinguishable from an uninstall — and the send loop retires a
// device permanently on it. That is how a deployment flipped to production
// for TestFlight used to disable every lingering sandbox device for good,
// with the user simply never hearing from the app again.
//
// Skipping is loud and reversible: nothing is written, so the device starts
// working the moment the deployment gains a key that covers it.
func forEnvironment(devices []db.Device, served []string, userID uuid.UUID) []db.Device {
	serves := make(map[string]bool, len(served))
	for _, e := range served {
		serves[e] = true
	}
	kept := make([]db.Device, 0, len(devices))
	skipped := 0
	for _, d := range devices {
		if serves[d.Environment] {
			kept = append(kept, d)
			continue
		}
		skipped++
	}
	if skipped > 0 {
		slog.Warn("push: skipping devices in an APNs environment this key does not serve",
			"user", userID, "serves", strings.Join(served, ","), "skipped", skipped, "kept", len(kept))
	}
	return kept
}

// withActs drops events carrying no acts. GroupEvents folds rows sharing an
// event_key so everything it returns should have at least one, which makes
// this defence rather than a case we expect -- but the failure it prevents is
// out of proportion to the check. notificationFor indexes Acts[0] three
// times, and a panic there does not fail the job: it takes down the goroutine
// river runs Work on, losing the notifications for every event after this one
// in the same batch, and the user is told nothing. Skipping costs one.
func withActs(events []concerts.Event, userID uuid.UUID) []concerts.Event {
	kept := make([]concerts.Event, 0, len(events))
	for _, ev := range events {
		if len(ev.Acts) == 0 {
			slog.Warn("push: skipping event with no acts", "user", userID, "event", ev.EventKey)
			continue
		}
		kept = append(kept, ev)
	}
	return kept
}

// Callers must pass an event with at least one act; withActs guarantees it.
func notificationFor(ev concerts.Event) push.Notification {
	title := ev.Acts[0].Artist.Name
	if len(ev.Acts) > 1 {
		title = fmt.Sprintf("%s and %d more", title, len(ev.Acts)-1)
	}
	body := fmt.Sprintf("%s · %s", ev.Venue, ev.Date.Format("Mon, Jan 2"))
	if ev.City != "" {
		body = fmt.Sprintf("%s, %s · %s", ev.Venue, ev.City, ev.Date.Format("Mon, Jan 2"))
	}
	dedupKey := ev.Acts[0].DedupKey
	return push.Notification{
		// Collapse on the event so a re-push for the same show replaces an
		// undelivered one rather than stacking.
		CollapseID: ev.EventKey,
		Payload: push.Payload{
			APS: push.APS{
				Alert:    push.Alert{Title: title, Body: body},
				Sound:    "default",
				ThreadID: "new-concerts",
			},
			EventKey:  ev.EventKey,
			DedupKey:  dedupKey,
			Artist:    ev.Acts[0].Artist.Name,
			Venue:     ev.Venue,
			EventDate: ev.Date.Format("2006-01-02"),
		},
	}
}
