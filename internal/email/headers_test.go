package email

import (
	"mime"
	"net/mail"
	"strings"
	"testing"
)

func parseHeaders(t *testing.T, raw []byte) mail.Header {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("assembled message does not parse as RFC 5322: %v", err)
	}
	return m.Header
}

// Date and Message-ID are required headers, and their absence is a standing
// SpamAssassin penalty — a deliverability problem that shows up as "our email
// goes to spam" long before anyone thinks to look at the MIME.
func TestBuildMIMEHasRequiredHeaders(t *testing.T) {
	raw := buildMIME("ConcertFinder <notify@example.com>", Message{
		To: "user@example.net", Subject: "hello", Text: "t", HTML: "<p>h</p>",
	})
	h := parseHeaders(t, raw)
	if h.Get("Date") == "" {
		t.Error("missing Date header")
	}
	if _, err := h.Date(); err != nil {
		t.Errorf("Date header does not parse: %v", err)
	}
	id := h.Get("Message-ID")
	if id == "" {
		t.Fatal("missing Message-ID header")
	}
	// The domain should match the sender's, since a mismatch is itself a
	// spam signal.
	if !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("Message-ID domain should match the From domain, got %q", id)
	}
}

// Two messages must not share a Message-ID, or receivers treat the second as
// a duplicate of the first and silently drop it.
func TestMessageIDsAreUnique(t *testing.T) {
	m := Message{To: "u@example.net", Subject: "s", Text: "t", HTML: "<p>h</p>"}
	a := parseHeaders(t, buildMIME("notify@example.com", m)).Get("Message-ID")
	b := parseHeaders(t, buildMIME("notify@example.com", m)).Get("Message-ID")
	if a == b {
		t.Errorf("two messages shared a Message-ID: %q", a)
	}
}

// Headers must be ASCII. The digest subject contains a literal em dash, which
// sent raw is malformed and renders as mojibake in stricter clients.
func TestNonASCIISubjectIsEncoded(t *testing.T) {
	raw := buildMIME("notify@example.com", Message{
		To: "u@example.net", Subject: "ConcertFinder digest — 5 new shows",
		Text: "t", HTML: "<p>h</p>",
	})
	headerBlock, _, _ := strings.Cut(string(raw), "\r\n\r\n")
	for i := 0; i < len(headerBlock); i++ {
		if headerBlock[i] > 127 {
			t.Fatalf("raw non-ASCII byte in headers at %d: %q", i, headerBlock)
		}
	}
	// And it has to survive the round trip as the original text.
	got := parseHeaders(t, raw).Get("Subject")
	dec, err := new(mime.WordDecoder).DecodeHeader(got)
	if err != nil {
		t.Fatalf("decoding encoded-word subject: %v", err)
	}
	if dec != "ConcertFinder digest — 5 new shows" {
		t.Errorf("subject did not round-trip: got %q", dec)
	}
}

// Gmail and Yahoo's bulk-sender rules expect one-click unsubscribe. Without
// these headers the mail is penalized before a recipient ever sees it.
func TestListUnsubscribeHeaders(t *testing.T) {
	raw := buildMIME("notify@example.com", Message{
		To: "u@example.net", Subject: "s", Text: "t", HTML: "<p>h</p>",
		UnsubscribeURL: "https://example.com/api/unsubscribe?token=abc",
	})
	h := parseHeaders(t, raw)
	if got := h.Get("List-Unsubscribe"); got != "<https://example.com/api/unsubscribe?token=abc>" {
		t.Errorf("List-Unsubscribe = %q", got)
	}
	if got := h.Get("List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
		t.Errorf("List-Unsubscribe-Post = %q", got)
	}
}

func TestListUnsubscribeOmittedWhenNoURL(t *testing.T) {
	raw := buildMIME("notify@example.com", Message{
		To: "u@example.net", Subject: "s", Text: "t", HTML: "<p>h</p>",
	})
	if got := parseHeaders(t, raw).Get("List-Unsubscribe"); got != "" {
		t.Errorf("expected no List-Unsubscribe without a URL, got %q", got)
	}
}

// Display names and addresses come from Spotify. A newline in one would let
// it terminate its header and inject others (a Bcc, say).
func TestHeaderInjectionIsStripped(t *testing.T) {
	raw := buildMIME("notify@example.com", Message{
		To:      "victim@example.net\r\nBcc: attacker@evil.example",
		Subject: "hi\r\nX-Injected: yes",
		Text:    "t", HTML: "<p>h</p>",
	})
	h := parseHeaders(t, raw)
	if h.Get("Bcc") != "" {
		t.Error("CRLF in To injected a Bcc header")
	}
	if h.Get("X-Injected") != "" {
		t.Error("CRLF in Subject injected a header")
	}
}
