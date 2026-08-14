// Package email is a small transport for outbound notification emails
// (currently just the daily digest). Speaks plain SMTP so it works against
// SES, Postmark, Resend, Mailgun, or a local MailHog with only env-var
// changes — no provider SDK. Design §11 keeps AWS out of /internal.
package email

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Mode governs whether we actually connect to an SMTP server or just log
// the assembled message. "log" is the local-dev default so a running app
// without SMTP config still exercises the full render + enqueue path.
type Mode string

const (
	ModeSMTP Mode = "smtp"
	ModeLog  Mode = "log"
)

// Config is all the knobs needed to talk to an SMTP relay.
type Config struct {
	Mode     Mode
	Host     string
	Port     int
	Username string
	Password string
	From     string // e.g. "notifications@yourdomain.com"
}

// Sender ships messages via SMTP (or logs them, per Mode).
type Sender struct {
	Cfg Config
}

// Message is one email to one recipient. Both plaintext and HTML bodies are
// required — plaintext for accessibility / spam filters, HTML for the pretty
// version.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// UnsubscribeURL populates the List-Unsubscribe headers. Required in
	// practice: Gmail and Yahoo's bulk-sender rules expect one-click
	// unsubscribe on marketing/notification mail, and its absence costs
	// inbox placement before any recipient ever sees the message.
	UnsubscribeURL string
}

// Send delivers the message. In ModeLog it writes the assembled MIME to
// slog and returns; in ModeSMTP it dials the configured server with
// STARTTLS. Errors are returned unwrapped so callers can log them with
// context.
func (s *Sender) Send(ctx context.Context, m Message) error {
	if m.To == "" {
		return fmt.Errorf("email: empty recipient")
	}
	if s.Cfg.From == "" {
		return fmt.Errorf("email: SMTP_FROM not configured")
	}

	raw := buildMIME(s.Cfg.From, m)

	if s.Cfg.Mode == ModeLog || s.Cfg.Mode == "" {
		slog.Info("email (log mode)", "to", m.To, "subject", m.Subject, "bytes", len(raw))
		// Log first ~500 chars of the plaintext for eyeballing.
		preview := m.Text
		if len(preview) > 500 {
			preview = preview[:500] + "…"
		}
		slog.Info("email body preview", "text", preview)
		return nil
	}

	addr := net.JoinHostPort(s.Cfg.Host, fmt.Sprintf("%d", s.Cfg.Port))
	auth := smtp.PlainAuth("", s.Cfg.Username, s.Cfg.Password, s.Cfg.Host)

	// STARTTLS-only path (port 587 style). Plain smtp.SendMail handles the
	// STARTTLS upgrade internally when the server offers it.
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, s.Cfg.From, []string{m.To}, raw)
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("smtp send: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("smtp send timeout")
	}
}

// buildMIME hand-rolls a multipart/alternative message with both text and
// HTML parts. Small enough that the stdlib mime/multipart machinery is
// overkill.
//
// Header hygiene here is deliverability, not pedantry:
//   - Date and Message-ID are required by RFC 5322 and their absence is a
//     standing SpamAssassin penalty (MISSING_DATE / MISSING_MID).
//   - Subject goes through RFC 2047 encoding. Headers must be ASCII, and the
//     digest subject carries a literal em dash — sent raw it is malformed and
//     renders as mojibake in stricter clients.
//   - List-Unsubscribe + List-Unsubscribe-Post give Gmail and Yahoo the
//     one-click unsubscribe their bulk-sender rules expect; the POST target is
//     the same handler the confirmation page submits to.
//
// Every interpolated value is stripped of CR/LF first. Display names and
// addresses originate at Spotify, and a newline in one would otherwise let it
// inject arbitrary headers.
func buildMIME(from string, m Message) []byte {
	boundary := "concertfinder-boundary-" + fmt.Sprintf("%d", time.Now().UnixNano())
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", sanitizeHeader(from))
	fmt.Fprintf(&b, "To: %s\r\n", sanitizeHeader(m.To))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitizeHeader(m.Subject)))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: %s\r\n", messageID(from))
	if u := sanitizeHeader(m.UnsubscribeURL); u != "" {
		fmt.Fprintf(&b, "List-Unsubscribe: <%s>\r\n", u)
		b.WriteString("List-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n", boundary)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(m.Text)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(m.HTML)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}

// sanitizeHeader strips CR and LF so an interpolated value cannot terminate
// its header and start a new one.
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(v))
}

// messageID builds a globally-unique Message-ID, taking the domain from the
// From address so it matches the sending domain (mismatched domains are
// themselves a spam signal).
func messageID(from string) string {
	domain := "concertfinder.local"
	if addr, err := mail.ParseAddress(from); err == nil {
		if i := strings.LastIndex(addr.Address, "@"); i >= 0 && i+1 < len(addr.Address) {
			domain = addr.Address[i+1:]
		}
	} else if i := strings.LastIndex(from, "@"); i >= 0 && i+1 < len(from) {
		domain = strings.Trim(from[i+1:], "<> ")
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), sanitizeHeader(domain))
	}
	return fmt.Sprintf("<%s.%d@%s>", hex.EncodeToString(buf[:]), time.Now().UnixNano(), sanitizeHeader(domain))
}
