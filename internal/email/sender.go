// Package email is a small transport for outbound notification emails
// (currently just the daily digest). Speaks plain SMTP so it works against
// SES, Postmark, Resend, Mailgun, or a local MailHog with only env-var
// changes — no provider SDK. Design §11 keeps AWS out of /internal.
package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
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
func buildMIME(from string, m Message) []byte {
	boundary := "concertfinder-boundary-" + fmt.Sprintf("%d", time.Now().UnixNano())
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.Subject)
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

// verifyTLS is unused today — kept as documentation of what a stricter
// TLS-configured client would look like if we drop the smtp.SendMail
// convenience wrapper.
var _ = tls.VersionTLS12
