package db

import (
	"context"
	"testing"
)

// The test plan §8 asks for by name: "a user opted into both channels
// receives both — it is the failure mode that produces no error."
//
// Before migration 0016, user_digest_sent was keyed (user_id, dedup_key) with
// no channel. Email digest and instant-notify share it deliberately — one
// email per show, whichever path finds it first. Push cannot join that
// unchanged:
//
//   - if the push worker WRITES those rows, the email is suppressed;
//   - if it READS them, whichever worker ran first consumes the other's
//     candidates.
//
// Both are invisible. No error is raised, nothing is logged, and the only
// symptom is a notification that never arrives on one of two channels the
// user explicitly asked for. These tests are the standing proof that the
// channel column actually separates them.

func TestBothChannelsDeliverTheSameShow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{
		email:         "both@example.com",
		instantNotify: true,
		push:          true,
	})
	const dedup = "show-both-channels"

	// Email runs first and records its send.
	emailUnsent, err := FilterUnsentDedupKeys(ctx, pool, user, ChannelEmail, []string{dedup})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := emailUnsent[dedup]; !ok {
		t.Fatal("a never-sent show should be unsent on the email channel")
	}
	if err := RecordDigestSent(ctx, pool, user, ChannelEmail, []string{dedup}); err != nil {
		t.Fatal(err)
	}

	// Push runs second. It must still consider the show unsent — this is the
	// assertion the whole migration exists for.
	pushUnsent, err := FilterUnsentDedupKeys(ctx, pool, user, ChannelPush, []string{dedup})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pushUnsent[dedup]; !ok {
		t.Fatal("email delivery consumed the push candidate: the user opted into " +
			"both channels and would receive only one, with no error anywhere")
	}
	if err := RecordDigestSent(ctx, pool, user, ChannelPush, []string{dedup}); err != nil {
		t.Fatal(err)
	}

	// And the ledger now records exactly one send per channel.
	for _, ch := range []Channel{ChannelEmail, ChannelPush} {
		n, err := CountDigestSent(ctx, pool, user, ch)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("CountDigestSent(%s) = %d, want 1", ch, n)
		}
	}
}

// The other half: within one channel, suppression must still work. The point
// of the ledger is that a show is not sent twice.
func TestOneChannelSuppressesItsOwnRepeat(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com", instantNotify: true})
	const dedup = "show-repeat"

	if err := RecordDigestSent(ctx, pool, user, ChannelEmail, []string{dedup}); err != nil {
		t.Fatal(err)
	}
	unsent, err := FilterUnsentDedupKeys(ctx, pool, user, ChannelEmail, []string{dedup})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := unsent[dedup]; ok {
		t.Fatal("an already-emailed show came back unsent on the same channel")
	}
}

// The daily digest and instant-notify are two triggers for ONE channel, and
// they must keep suppressing each other — that behaviour predates push and is
// intended. Both pass ChannelEmail, so this is really a guard against someone
// later "fixing" them into separate channels and doubling every user's mail.
func TestDigestAndInstantNotifyShareTheEmailChannel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com", digest: true, instantNotify: true})
	const dedup = "show-shared-email"

	// Instant-notify sends it.
	if err := RecordDigestSent(ctx, pool, user, ChannelEmail, []string{dedup}); err != nil {
		t.Fatal(err)
	}
	// The daily digest must not send it again.
	unsent, err := FilterUnsentDedupKeys(ctx, pool, user, ChannelEmail, []string{dedup})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := unsent[dedup]; ok {
		t.Fatal("the digest would re-send a show instant-notify already emailed")
	}
}

// RecordDigestSent is called before delivery and re-run on river retries, so
// it has to be idempotent per channel or a retry would error on the primary
// key and fail a job that already did its work.
func TestRecordDigestSentIsIdempotentPerChannel(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com", push: true})
	keys := []string{"a", "b"}

	for i := 0; i < 3; i++ {
		if err := RecordDigestSent(ctx, pool, user, ChannelPush, keys); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	n, err := CountDigestSent(ctx, pool, user, ChannelPush)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(keys) {
		t.Errorf("CountDigestSent = %d after three identical writes, want %d", n, len(keys))
	}
}

// Deleting a user must take their device registrations with them, or a
// deleted account's phone keeps receiving pushes with nothing left to turn
// them off from. That is an ON DELETE CASCADE in migration 0016, which is the
// kind of thing that is easy to drop in a later migration without noticing.
func TestDeletingAUserRemovesTheirDevices(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com", push: true})

	if err := UpsertDevice(ctx, pool, Device{
		UserID: user, DeviceToken: "token-abc", Environment: EnvSandbox,
	}); err != nil {
		t.Fatal(err)
	}
	devices, err := ListLiveDevices(ctx, pool, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("ListLiveDevices = %d, want 1", len(devices))
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM user_devices WHERE user_id = $1`, user).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d device rows survived the user; a deleted account's phone would keep receiving pushes", remaining)
	}
}

// A token APNs retired is stamped rather than deleted, so a reinstall can
// revive the row; re-registering must clear disabled_at or that device is
// muted forever.
func TestReRegisteringRevivesADisabledDevice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := insertTestUser(t, pool, userOptIns{email: "e@example.com", push: true})
	const token = "token-revive"

	if err := UpsertDevice(ctx, pool, Device{UserID: user, DeviceToken: token, Environment: EnvProduction}); err != nil {
		t.Fatal(err)
	}
	if err := DisableDevice(ctx, pool, user, token); err != nil {
		t.Fatal(err)
	}
	live, err := ListLiveDevices(ctx, pool, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("a disabled device is still live: %d rows", len(live))
	}

	// The app re-registers on next launch.
	if err := UpsertDevice(ctx, pool, Device{UserID: user, DeviceToken: token, Environment: EnvProduction}); err != nil {
		t.Fatal(err)
	}
	live, err = ListLiveDevices(ctx, pool, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Errorf("re-registering did not revive the device: %d live rows, want 1", len(live))
	}
}
