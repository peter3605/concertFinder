package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The unsubscribe link must not act on GET. Mail security gateways (Outlook
// Safe Links and friends) fetch every URL in an inbound message, and a
// state-changing GET honors that fetch — unsubscribing people who never
// clicked anything.
//
// Pool is deliberately nil: the handler would panic on any database call, so
// this asserts "changes nothing" rather than trusting the code to be read
// correctly later.
func TestUnsubscribeGetDoesNotMutate(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	tok := h.Token(uuid.New())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/unsubscribe?token="+tok, nil)
	h.Get(rec, req) // panics if it touches the nil pool

	if rec.Code != 200 {
		t.Fatalf("expected a confirmation page, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `method="POST"`) {
		t.Error("confirmation page must submit via POST")
	}
	if !strings.Contains(body, tok) {
		t.Error("confirmation page must carry the token through to the POST")
	}
}

func TestUnsubscribeGetRejectsBadToken(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/unsubscribe?token=garbage", nil))
	if rec.Code != 400 {
		t.Errorf("expected 400 for an unverifiable token, got %d", rec.Code)
	}
}

// One-click unsubscribe (RFC 8058) POSTs the URL with the token still in the
// query string, while the confirmation page posts it as a form field. Both
// have to verify.
func TestUnsubscribePostAcceptsTokenFromQueryOrForm(t *testing.T) {
	h := &UnsubscribeHandler{Secret: []byte("test-signing-key-32-bytes-long!!")}
	uid := uuid.New()
	tok := h.Token(uid)

	fromQuery := httptest.NewRequest("POST", "/api/unsubscribe?token="+tok, nil)
	if got, ok := h.verify(fromQuery.URL.Query().Get("token")); !ok || got != uid {
		t.Error("token in the query string must verify (one-click path)")
	}

	form := httptest.NewRequest("POST", "/api/unsubscribe",
		strings.NewReader("token="+tok))
	form.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := form.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.verify(form.PostFormValue("token")); !ok || got != uid {
		t.Error("token in the form body must verify (confirmation-page path)")
	}
}
