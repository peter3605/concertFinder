package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math/big"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
)

// EmailLinkPath is where a sign-in mail's link lands: a confirmation page on
// the web, and a universal link for the app (apple-app-site-association
// lists it). It is outside /api because it is a page a person opens.
const EmailLinkPath = "/app/auth/email"

// MailSender is the one method of email.Sender this needs; tests fake it.
type MailSender interface {
	Send(ctx context.Context, m email.Message) error
}

// EmailLogin serves email sign-in links and address verification (CF-33,
// docs/public-path.md section 4.2).
//
// Three rules hold it together:
//
//   - POST /api/auth/email/start answers 202 whatever the address, and does
//     the lookup and the send after answering, so neither the status nor the
//     time it takes says whether an account exists.
//   - The link opens a confirmation page and only a POST consumes it. Mail
//     security gateways fetch every URL in a message; a consuming GET would
//     sign the gateway in, or burn the link before its owner saw it. The
//     unsubscribe endpoint is built the same way for the same reason.
//   - An unknown address is a signup and goes through the invite gate, whose
//     redemption shares the transaction that consumes the token and creates
//     the user (db.ConsumeEmailLoginToken).
type EmailLogin struct {
	Pool   *pgxpool.Pool
	Sender MailSender
	// SiteBaseURL builds the link, e.g. https://concertfinder.app.
	SiteBaseURL    string
	CookieDomain   string
	PostLoginURL   string
	InviteRequired bool
	// DailyCap bounds mail across every caller in 24 hours; <= 0 disables
	// it. See config.Config.EmailLoginDailyCap.
	DailyCap int
	// StartLimiter is the per-IP bucket on /email/start, tighter than the
	// /api/auth one it sits inside because each call can send a mail. Nil
	// gets DefaultEmailStartLimiter.
	StartLimiter *IPRateLimit
	// Background runs the lookup-and-send after the 202 is written. Nil runs
	// it on a goroutine; tests run it inline.
	Background func(func())
	// OnLoginSuccess mirrors Deps.OnLoginSuccess. Nil is a no-op.
	OnLoginSuccess func(ctx context.Context, userID uuid.UUID)
	// Now is for tests; nil is time.Now.
	Now func() time.Time
}

// NewEmailStartLimiter is the per-IP bucket on /email/start: a burst of 5,
// refilling one a minute. One person signing in needs one or two.
func NewEmailStartLimiter() *IPRateLimit { return NewIPRateLimit(1.0/60, 5) }

// MountEmailAuth registers the unauthenticated routes. The caller mounts it
// inside /api/auth, so the subtree's IP limiter applies to all of them.
func (e *EmailLogin) MountEmailAuth(r chi.Router) {
	if e.StartLimiter == nil {
		e.StartLimiter = NewEmailStartLimiter()
	}
	r.With(e.StartLimiter.Middleware).Post("/email/start", e.handleStart)
	r.Post("/email/consume", e.handleConsume)
	r.Post("/email/code", e.handleCode)
}

// MountEmailMe registers the signed-in routes. The caller mounts it inside
// /api/me, after RequireUser and CSRF.
func (e *EmailLogin) MountEmailMe(r chi.Router) {
	r.Post("/email", e.handleSetAddress)
	r.Post("/email/verify", e.handleVerifyCode)
}

// HashEmailLoginToken maps a link token to email_login_tokens.token_hash.
// SHA-256 for the reason HashSessionToken gives: the input is 32 random
// bytes, so there is no keyspace for a work factor to slow down.
func HashEmailLoginToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

var errEmailLimited = errors.New("email sign-in limit reached")

func (e *EmailLogin) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// normalizeEmail accepts a bare address only -- no display name, no list --
// and lowercases it, which is the spelling user_identities.subject holds.
func normalizeEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || !strings.Contains(s[at+1:], ".") {
		return "", false
	}
	return strings.ToLower(s), true
}

func sixDigitCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func isSixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

type emailStartRequest struct {
	Email        string `json:"email"`
	Invite       string `json:"invite"`
	Client       string `json:"client"`
	AppChallenge string `json:"app_challenge"`
}

func (e *EmailLogin) handleStart(w http.ResponseWriter, r *http.Request) {
	var req emailStartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Refusing a malformed address says nothing about accounts.
	addr, ok := normalizeEmail(req.Email)
	if !ok {
		writeAuthError(w, http.StatusBadRequest, "enter a valid email address")
		return
	}
	appChallenge := ""
	if req.Client == ClientIOS {
		if req.AppChallenge == "" {
			writeAuthError(w, http.StatusBadRequest, "app_challenge is required for client=ios")
			return
		}
		appChallenge = req.AppChallenge
	}
	// The same read-only pre-check /login runs, and for the same reason: a
	// typo is caught before the mail rather than after the click. It is about
	// the code, not the address, so it cannot be used to probe for accounts.
	invite := db.NormalizeInviteCode(req.Invite)
	if e.InviteRequired && invite != "" {
		usable, err := db.InviteCodeUsable(r.Context(), e.Pool, invite)
		if err != nil {
			slog.Error("email start: invite pre-check failed", "err", err)
			writeAuthError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !usable {
			writeAuthError(w, http.StatusForbidden, "That invite code is not valid. It may have already been used, or it may have expired.")
			return
		}
	}

	writeJSONResponse(w, http.StatusAccepted, map[string]string{"status": "check_email"})

	run := e.Background
	if run == nil {
		run = func(f func()) { go f() }
	}
	run(func() {
		// Detached from the request, which is already answered.
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		e.startLogin(ctx, addr, invite, appChallenge)
	})
}

// startLogin decides whether to send and sends. Nothing here may change the
// response, which has already gone. It logs no address: a refused signup is
// somebody who has no account, and keeping their address would be retaining
// it for a non-user.
func (e *EmailLogin) startLogin(ctx context.Context, addr, invite, appChallenge string) {
	known, err := db.EmailIdentityExists(ctx, e.Pool, addr)
	if err != nil {
		slog.Error("email start: identity lookup failed", "err", err)
		return
	}
	switch {
	case known:
		// A returning user never spends a code.
		invite = ""
	case !e.InviteRequired:
		invite = ""
	case invite == "":
		slog.Info("email sign-in: unknown address and no invite code; nothing sent")
		return
	}
	if err := e.issue(ctx, addr, db.EmailPurposeLogin, uuid.Nil, invite, appChallenge); err != nil {
		if !errors.Is(err, errEmailLimited) {
			slog.Error("email sign-in: send failed", "err", err)
		}
	}
}

// issue applies the per-address and daily limits, records the token and
// sends the mail. The row is written before the send so a failed send still
// counts against the limits; a mail that might have gone out is one.
func (e *EmailLogin) issue(ctx context.Context, addr, purpose string, userID uuid.UUID, invite, appChallenge string) error {
	now := e.now()
	n, err := db.CountEmailLoginTokensSince(ctx, e.Pool, addr, now.Add(-time.Hour))
	if err != nil {
		return fmt.Errorf("count per-address: %w", err)
	}
	if n >= db.EmailLoginPerAddressHourly {
		slog.Info("email sign-in: per-address hourly limit reached; nothing sent", "purpose", purpose)
		return errEmailLimited
	}
	if e.DailyCap > 0 {
		total, err := db.CountAllEmailLoginTokensSince(ctx, e.Pool, now.Add(-24*time.Hour))
		if err != nil {
			return fmt.Errorf("count daily: %w", err)
		}
		if total >= e.DailyCap {
			slog.Warn("email sign-in: daily ceiling reached; nothing sent",
				"cap", e.DailyCap, "remedy", "raise EMAIL_LOGIN_DAILY_CAP if this is real demand")
			return errEmailLimited
		}
	}
	token, err := RandomString(32)
	if err != nil {
		return err
	}
	code, err := sixDigitCode()
	if err != nil {
		return err
	}
	th := HashEmailLoginToken(token)
	if err := db.InsertEmailLoginToken(ctx, e.Pool, db.EmailLoginToken{
		TokenHash:    th,
		CodeHash:     db.EmailCodeHash(th, code),
		Email:        addr,
		Purpose:      purpose,
		UserID:       userID,
		InviteCode:   invite,
		AppChallenge: appChallenge,
		ExpiresAt:    now.Add(db.EmailLoginTokenTTL),
	}); err != nil {
		return err
	}
	link := strings.TrimRight(e.SiteBaseURL, "/") + EmailLinkPath + "?token=" + url.QueryEscape(token)
	return e.Sender.Send(ctx, email.RenderSignIn(addr, link, code, email.SignInPurpose(purpose)))
}

// HandleConfirmPage is GET /app/auth/email. It renders a button and consumes
// nothing; db.PeekEmailLoginToken is read-only. See the type comment.
func (e *EmailLogin) HandleConfirmPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	purpose, ok := "", false
	if token != "" {
		var err error
		purpose, ok, err = db.PeekEmailLoginToken(r.Context(), e.Pool, HashEmailLoginToken(token))
		if err != nil {
			slog.Error("email confirm page: peek failed", "err", err)
			writeEmailPage(w, http.StatusInternalServerError, "Something went wrong", "Please try the link again in a moment.", "")
			return
		}
	}
	if !ok {
		writeExpiredPage(w)
		return
	}
	title, body, button := "Sign in to ConcertFinder", "Press the button to finish signing in on this device.", "Sign in"
	if purpose == db.EmailPurposeVerify {
		title, body, button = "Confirm your email address", "Press the button to confirm this address for your ConcertFinder account.", "Confirm address"
	}
	form := fmt.Sprintf(`<form method="POST" action="/api/auth/email/consume">
<input type="hidden" name="token" value="%s">
<button type="submit" style="font:inherit;padding:0.6rem 1.1rem;border:0;border-radius:6px;background:#1db954;color:#fff;cursor:pointer">%s</button>
</form>`, html.EscapeString(token), html.EscapeString(button))
	writeEmailPage(w, http.StatusOK, title, body, form)
}

type emailConsumeRequest struct {
	Token    string `json:"token"`
	Verifier string `json:"verifier"`
}

// handleConsume is POST /api/auth/email/consume: the confirmation page's
// form (cookie session, redirect) or the app with the universal link's token
// and its verifier (bearer token in the response).
func (e *EmailLogin) handleConsume(w http.ResponseWriter, r *http.Request) {
	if isJSON(r) {
		var req emailConsumeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.Token == "" {
			writeAuthError(w, http.StatusBadRequest, "invalid body")
			return
		}
		res, err := db.ConsumeEmailLoginToken(r.Context(), e.Pool, HashEmailLoginToken(req.Token), e.InviteRequired,
			func(t db.EmailLoginToken) error {
				if t.Purpose == db.EmailPurposeVerify {
					return nil
				}
				return checkAppChallenge(t, req.Verifier)
			})
		if err != nil {
			e.writeConsumeJSONError(w, err)
			return
		}
		if res.Token.Purpose == db.EmailPurposeVerify {
			writeJSONResponse(w, http.StatusOK, map[string]any{"verified": true})
			return
		}
		e.writeBearerSession(w, r, res.UserID)
		return
	}

	// Login CSRF: a page elsewhere must not be able to post its own token and
	// sign this browser in to somebody else's account.
	if !sameOriginForm(r, e.SiteBaseURL) {
		writeEmailPage(w, http.StatusForbidden, "Sign-in refused", "Open the link from your email again.", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	_ = r.ParseForm()
	token := r.PostFormValue("token")
	if token == "" {
		writeExpiredPage(w)
		return
	}
	res, err := db.ConsumeEmailLoginToken(r.Context(), e.Pool, HashEmailLoginToken(token), e.InviteRequired, nil)
	if err != nil {
		e.writeConsumePageError(w, err)
		return
	}
	if res.Token.Purpose == db.EmailPurposeVerify {
		writeEmailPage(w, http.StatusOK, "Address confirmed",
			"This address can now sign you in to ConcertFinder, and email updates can go to it.",
			`<p><a href="/settings">Back to settings</a></p>`)
		return
	}
	sessToken, expires, err := e.createSession(r.Context(), res.UserID)
	if err != nil {
		slog.Error("email consume: create session failed", "err", err)
		writeEmailPage(w, http.StatusInternalServerError, "Something went wrong", "Please request a new link.", "")
		return
	}
	setSessionCookie(w, e.CookieDomain, sessToken, expires)
	e.loginSucceeded(r.Context(), res.UserID)
	target := e.PostLoginURL
	if target == "" {
		target = "/"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

type emailCodeRequest struct {
	Email    string `json:"email"`
	Code     string `json:"code"`
	Verifier string `json:"verifier"`
}

// handleCode is POST /api/auth/email/code: the 6-digit code, for a mail
// opened on another device or an app the link did not open. With a verifier
// it answers with a bearer session; without, it sets the cookie.
//
// JSON only, which is also what keeps it from being a login-CSRF target: a
// cross-site page cannot send application/json without a CORS preflight this
// server never grants.
func (e *EmailLogin) handleCode(w http.ResponseWriter, r *http.Request) {
	if !isJSON(r) {
		writeAuthError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}
	var req emailCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr, ok := normalizeEmail(req.Email)
	code := strings.TrimSpace(req.Code)
	if !ok || !isSixDigits(code) {
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}
	var accept func(db.EmailLoginToken) error
	if req.Verifier != "" {
		accept = func(t db.EmailLoginToken) error { return checkAppChallenge(t, req.Verifier) }
	}
	res, err := db.ConsumeEmailLoginCode(r.Context(), e.Pool, addr, code, db.EmailPurposeLogin, uuid.Nil, e.InviteRequired, accept)
	if err != nil {
		e.writeConsumeJSONError(w, err)
		return
	}
	if req.Verifier != "" {
		e.writeBearerSession(w, r, res.UserID)
		return
	}
	token, expires, err := e.createSession(r.Context(), res.UserID)
	if err != nil {
		slog.Error("email code: create session failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "internal error")
		return
	}
	setSessionCookie(w, e.CookieDomain, token, expires)
	e.loginSucceeded(r.Context(), res.UserID)
	w.WriteHeader(http.StatusNoContent)
}

type setAddressRequest struct {
	Email string `json:"email"`
}

// handleSetAddress is POST /api/me/email: a signed-in user asks to confirm
// an address. Nothing about the account changes until the link or code is
// used; then the address becomes users.email and an email identity.
//
// This is what replaces "log in again to grant email access", which no
// re-login could satisfy once Spotify stopped returning the address.
func (e *EmailLogin) handleSetAddress(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req setAddressRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr, ok := normalizeEmail(req.Email)
	if !ok {
		writeAuthError(w, http.StatusBadRequest, "enter a valid email address")
		return
	}
	err := e.issue(r.Context(), addr, db.EmailPurposeVerify, u.ID, "", "")
	switch {
	case errors.Is(err, errEmailLimited):
		w.Header().Set("Retry-After", "3600")
		writeAuthError(w, http.StatusTooManyRequests, "too many confirmation emails; try again later")
		return
	case err != nil:
		slog.Error("set email address: send failed", "err", err, "user", u.ID)
		writeAuthError(w, http.StatusBadGateway, "could not send the confirmation email")
		return
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"status": "check_email"})
}

type verifyCodeRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// handleVerifyCode is POST /api/me/email/verify: the code from a
// confirmation mail, narrowed to the signed-in account's own requests.
func (e *EmailLogin) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFromContext(r.Context())
	if !ok {
		writeAuthError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req verifyCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeAuthError(w, http.StatusBadRequest, "invalid body")
		return
	}
	addr, ok := normalizeEmail(req.Email)
	code := strings.TrimSpace(req.Code)
	if !ok || !isSixDigits(code) {
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}
	if _, err := db.ConsumeEmailLoginCode(r.Context(), e.Pool, addr, code, db.EmailPurposeVerify, u.ID, e.InviteRequired, nil); err != nil {
		e.writeConsumeJSONError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errChallengeMismatch = errors.New("app verifier does not match")

// checkAppChallenge binds a bearer session to the app that started the
// sign-in, as the OAuth mobile exchange does. A refusal rolls back, so the
// token stays usable from the mail on a browser.
func checkAppChallenge(t db.EmailLoginToken, verifier string) error {
	if t.AppChallenge == "" || verifier == "" {
		return errChallengeMismatch
	}
	got := ChallengeFromVerifier(verifier)
	if subtle.ConstantTimeCompare([]byte(got), []byte(t.AppChallenge)) != 1 {
		return errChallengeMismatch
	}
	return nil
}

// createSession writes a session row holding only the token's hash and
// returns the token, which exists nowhere else.
func (e *EmailLogin) createSession(ctx context.Context, userID uuid.UUID) (string, time.Time, error) {
	token, err := RandomString(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(SessionCreatedTTL)
	if err := db.CreateSession(ctx, e.Pool, db.Session{
		ID:        uuid.NewString(),
		TokenHash: HashSessionToken(token),
		UserID:    userID,
		ExpiresAt: expires,
	}); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (e *EmailLogin) writeBearerSession(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	token, _, err := e.createSession(r.Context(), userID)
	if err != nil {
		slog.Error("email sign-in: create session failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "internal error")
		return
	}
	su, err := db.GetSessionUserByTokenHash(r.Context(), e.Pool, HashSessionToken(token))
	if err != nil {
		slog.Error("email sign-in: read session failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "internal error")
		return
	}
	e.loginSucceeded(r.Context(), userID)
	writeJSONResponse(w, http.StatusOK, mobileExchangeResponse{
		SessionToken: token,
		ExpiresAt:    su.Session.ExpiresAt,
		User:         userPayload(su.User),
	})
}

func (e *EmailLogin) loginSucceeded(ctx context.Context, userID uuid.UUID) {
	if e.OnLoginSuccess != nil {
		e.OnLoginSuccess(ctx, userID)
	}
}

func (e *EmailLogin) writeConsumeJSONError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrEmailTokenInvalid), errors.Is(err, errChallengeMismatch):
		writeAuthError(w, http.StatusBadRequest, "invalid or expired code")
	case errors.Is(err, db.ErrInviteRequired):
		slog.Info("email signup refused: no invite code")
		writeAuthError(w, http.StatusForbidden, "invite_required")
	case errors.Is(err, db.ErrInviteInvalid):
		slog.Info("email signup refused: invite not redeemable")
		writeAuthError(w, http.StatusForbidden, "invite_invalid")
	case errors.Is(err, db.ErrEmailTaken):
		writeAuthError(w, http.StatusConflict, "that address already signs in to another account")
	default:
		slog.Error("email sign-in: consume failed", "err", err)
		writeAuthError(w, http.StatusInternalServerError, "internal error")
	}
}

func (e *EmailLogin) writeConsumePageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrEmailTokenInvalid):
		writeExpiredPage(w)
	case errors.Is(err, db.ErrInviteRequired), errors.Is(err, db.ErrInviteInvalid):
		slog.Info("email signup refused at consume", "reason", err.Error())
		writeEmailPage(w, http.StatusForbidden, "Invite needed",
			"That invite code is no longer valid. It may have just been used up, or expired. Ask whoever sent it for another, then start again from the sign-in page.",
			`<p><a href="/">Back to sign in</a></p>`)
	case errors.Is(err, db.ErrEmailTaken):
		writeEmailPage(w, http.StatusConflict, "Address already in use",
			"That address already signs in to a different ConcertFinder account, so it cannot be added to this one.",
			`<p><a href="/settings">Back to settings</a></p>`)
	default:
		slog.Error("email consume failed", "err", err)
		writeEmailPage(w, http.StatusInternalServerError, "Something went wrong", "Please request a new link.", "")
	}
}

func isJSON(r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "application/json"
}

// sameOriginForm reports whether a form POST came from this site's own page.
// Browsers send Origin on every form POST; Sec-Fetch-Site covers the rest.
// A request with neither is not a browser being driven by another page.
func sameOriginForm(r *http.Request, siteBaseURL string) bool {
	if o := r.Header.Get("Origin"); o != "" {
		want, err := url.Parse(siteBaseURL)
		if err != nil {
			return false
		}
		got, err := url.Parse(o)
		if err != nil {
			return false // includes the opaque "null" origin
		}
		return strings.EqualFold(want.Scheme, got.Scheme) && strings.EqualFold(want.Host, got.Host)
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

func writeExpiredPage(w http.ResponseWriter) {
	writeEmailPage(w, http.StatusBadRequest, "This link has expired",
		"Sign-in links work once and only for 15 minutes. Request a new one from the sign-in page.",
		`<p><a href="/">Back to sign in</a></p>`)
}

// writeEmailPage renders the small server pages this flow lands on. no-store
// and no-referrer because the token is in the URL that produced them.
func writeEmailPage(w http.ResponseWriter, status int, title, body, extra string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title></head>
<body style="font-family:-apple-system,Helvetica,Arial,sans-serif;max-width:520px;margin:4rem auto;padding:0 16px;line-height:1.5;color:#222">
<h2>%s</h2>
<p>%s</p>
%s
</body></html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(body), extra)
}
