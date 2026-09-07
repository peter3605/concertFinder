package jobs

import (
	"context"

	"github.com/peterho/concertfinder/internal/email"
)

// EmailMaxAttempts bounds river's retries for a digest or instant-notify
// delivery.
//
// It exists because of sendAndRecord below, and the connection is not
// obvious. While the ledger was written before the send, a failed send was
// retried exactly once in effect: the retry found every key already recorded,
// computed an empty set, logged "nothing new" and returned nil. River's
// default of 25 attempts was therefore invisible. Recording after the send
// makes those retries real SMTP connections, so a deterministic rejection --
// an unverified recipient while SES is in sandbox, say -- would otherwise be
// two dozen refused connections per user per night. Three is the same number
// SendPushArgs settled on, for the same reason.
const EmailMaxAttempts = 3

// mailSender is the slice of *email.Sender the digest workers use. An
// interface so the ordering in sendAndRecord -- which is the half that decides
// whether a user is ever told about a show -- is testable without an SMTP
// server, exactly as pushSender exists for the APNs side.
type mailSender interface {
	Send(ctx context.Context, m email.Message) error
}

// recordSent writes the already-sent ledger for one channel and user. Passed
// as a function rather than a pool so the caller keeps ownership of which
// db.Channel is being written -- that argument is mandatory at every call site
// on purpose (see CLAUDE.md), and hiding it in here would undo that.
type recordSent func(ctx context.Context, dedupKeys []string) error

// sendAndRecord delivers one message and marks its dedup keys as sent ONLY if
// the send actually landed.
//
// The order is the whole point, and it reverses a deliberate earlier choice,
// so here is why. The ledger used to be written first, accepting an
// at-most-once send: if SMTP failed, the keys were already spent and the mail
// was simply never sent again. That trade is defensible when failures are
// transient and rare -- an occasional missed email against duplicates on every
// retry.
//
// It stops being defensible when the failure is deterministic. With SES in
// sandbox only one verified recipient can receive anything, so for every other
// user the send is rejected every single night: the keys were burned, the mail
// never arrived, and leaving the sandbox later recovered nothing because those
// shows were already marked delivered. A one-in-a-hundred miss had become a
// total, permanent, silent loss, and nothing in the code could tell the
// difference.
//
// It was silent because of the retry, which is worth spelling out: the send
// failed, the worker returned the error, river retried, FilterUnsentDedupKeys
// excluded the keys just written, the candidate set came back empty, and the
// job logged "nothing new" and succeeded. A green job that sent nothing.
//
// The reversed trade is that a crash between the send and the record costs a
// duplicate email rather than a lost one. That is the milder direction --
// SendPushWorker made the same reversal, where CollapseID absorbs the
// duplicate; email has no equivalent and simply wears it. A repeated digest is
// annoying, a missing one is invisible, and only one of the two can be
// noticed and complained about.
//
// Deliberately NOT done here: classifying SMTP errors as permanent or
// transient in order to keep the old ordering for the transient ones. SES
// answers 554 MessageRejected for an unverified recipient, and telling that
// apart from a temporary refusal reliably is a much larger job that optimises
// for the rarer failure.
func sendAndRecord(ctx context.Context, sender mailSender, msg email.Message, dedupKeys []string, record recordSent) error {
	if err := sender.Send(ctx, msg); err != nil {
		return err
	}
	if len(dedupKeys) == 0 {
		return nil
	}
	// A failure here means the mail went out and the ledger does not know.
	// Returning the error lets river retry, which re-sends -- the duplicate
	// the trade above accepts, rather than a show nobody is told about.
	return record(ctx, dedupKeys)
}
