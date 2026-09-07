package jobs

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
)

// fakeMailSender records what it was asked to send and fails on demand.
type fakeMailSender struct {
	err   error
	calls int
}

func (f *fakeMailSender) Send(_ context.Context, _ email.Message) error {
	f.calls++
	return f.err
}

var errSMTP = errors.New("554 message rejected: email address is not verified")

// THE ONE THAT MATTERS. A rejected send must leave the ledger untouched, so
// the next run still considers those shows unsent.
//
// The failure this guards is silent in every direction: the mail does not
// arrive, the keys say it did, the retry computes an empty candidate set and
// succeeds, and the user is simply never told about those concerts. Nothing
// logs an error and nothing reports a lost message.
func TestFailedSendDoesNotRecordTheLedger(t *testing.T) {
	sender := &fakeMailSender{err: errSMTP}
	recorded := false

	err := sendAndRecord(context.Background(), sender, email.Message{To: "a@example.com"},
		[]string{"key-1", "key-2"},
		func(context.Context, []string) error { recorded = true; return nil })

	if !errors.Is(err, errSMTP) {
		t.Fatalf("err = %v, want the sender's error propagated", err)
	}
	if recorded {
		t.Fatal("the ledger was written for a send that failed; those shows are now lost forever")
	}
	if sender.calls != 1 {
		t.Errorf("sender called %d times, want 1", sender.calls)
	}
}

// The other half: a send that lands must record exactly its own keys, or the
// next run mails the same shows again.
func TestSuccessfulSendRecordsExactlyItsKeys(t *testing.T) {
	sender := &fakeMailSender{}
	var got []string

	keys := []string{"key-1", "key-2", "key-3"}
	if err := sendAndRecord(context.Background(), sender, email.Message{To: "a@example.com"}, keys,
		func(_ context.Context, k []string) error { got = k; return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !reflect.DeepEqual(got, keys) {
		t.Fatalf("recorded %v, want %v", got, keys)
	}
}

// A record that fails after the mail went out must surface, not be swallowed.
// River then retries, which re-sends — the duplicate this ordering deliberately
// accepts. Swallowing it would put us back to a delivered mail whose keys are
// unrecorded AND a job that claims success, i.e. a guaranteed duplicate on the
// next scheduled run with no way to tell it happened.
func TestRecordFailureIsReported(t *testing.T) {
	sender := &fakeMailSender{}
	recordErr := errors.New("ledger write failed")

	err := sendAndRecord(context.Background(), sender, email.Message{To: "a@example.com"},
		[]string{"key-1"},
		func(context.Context, []string) error { return recordErr })

	if !errors.Is(err, recordErr) {
		t.Fatalf("err = %v, want the record error", err)
	}
}

// Nothing to record is not a reason to touch the ledger.
func TestNoKeysWritesNothing(t *testing.T) {
	sender := &fakeMailSender{}
	recorded := false
	if err := sendAndRecord(context.Background(), sender, email.Message{To: "a@example.com"}, nil,
		func(context.Context, []string) error { recorded = true; return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if recorded {
		t.Error("wrote the ledger with no keys")
	}
	if sender.calls != 1 {
		t.Errorf("sender called %d times, want 1 — an empty key set still means a message was requested", sender.calls)
	}
}

// --- Database-backed: the same property against the real table. ---
//
// The tests above prove the write is never *issued*. This one proves the
// consequence the story actually cares about: after a rejected send,
// db.FilterUnsentDedupKeys still reports those keys as unsent, so tomorrow's
// digest picks them up again.

func emailTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(ctx, pool, "../../migrations"); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertLedgerTestUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	const q = `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES ($1, $2, 'Ledger Test', $3, $4, $5)`
	if _, err := pool.Exec(context.Background(), q, id, "spotify-ledger-"+id.String(),
		[]byte("ciphertext"), []byte("nonce-123456"), "ledger-"+id.String()+"@example.com"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

func TestRejectedSendLeavesKeysUnsentInTheDatabase(t *testing.T) {
	ctx := context.Background()
	pool := emailTestPool(t)
	userID := insertLedgerTestUser(t, pool)
	keys := []string{"dedup-a", "dedup-b"}

	record := func(ctx context.Context, k []string) error {
		return db.RecordDigestSent(ctx, pool, userID, db.ChannelEmail, k)
	}

	// SES in sandbox rejecting an unverified recipient.
	err := sendAndRecord(ctx, &fakeMailSender{err: errSMTP}, email.Message{To: "a@example.com"}, keys, record)
	if err == nil {
		t.Fatal("expected the send to fail")
	}

	unsent, err := db.FilterUnsentDedupKeys(ctx, pool, userID, db.ChannelEmail, keys)
	if err != nil {
		t.Fatalf("filter unsent: %v", err)
	}
	for _, k := range keys {
		if _, ok := unsent[k]; !ok {
			t.Errorf("key %q is marked sent after a rejected send; that show is lost", k)
		}
	}

	// And the successful path does mark them, so the fix has not simply
	// disabled the ledger — which would mail the same shows every night.
	if err := sendAndRecord(ctx, &fakeMailSender{}, email.Message{To: "a@example.com"}, keys, record); err != nil {
		t.Fatalf("successful send: %v", err)
	}
	unsent, err = db.FilterUnsentDedupKeys(ctx, pool, userID, db.ChannelEmail, keys)
	if err != nil {
		t.Fatalf("filter unsent: %v", err)
	}
	if len(unsent) != 0 {
		t.Errorf("keys still unsent after a successful send: %v", unsent)
	}
}
