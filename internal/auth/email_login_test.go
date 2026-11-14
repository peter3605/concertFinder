package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/email"
)

const testSite = "https://concerts.example.com"

// fakeMail records what would have been sent.
type fakeMail struct {
	mu   sync.Mutex
	sent []email.Message
}

func (f *fakeMail) Send(_ context.Context, m email.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeMail) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

var (
	linkTokenRE = regexp.MustCompile(`token=([A-Za-z0-9_%-]+)`)
	codeRE      = regexp.MustCompile(`code: (\d{6})`)
)

// last returns the token and code of the newest mail.
func (f *fakeMail) last(t *testing.T) (token, code string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no mail was sent")
	}
	m := f.sent[len(f.sent)-1]
	tm := linkTokenRE.FindStringSubmatch(m.Text)
	cm := codeRE.FindStringSubmatch(m.Text)
	if tm == nil || cm == nil {
		t.Fatalf("mail carries no token or code: %q", m.Text)
	}
	tok, err := url.QueryUnescape(tm[1])
	if err != nil {
		t.Fatalf("unescape token: %v", err)
	}
	return tok, cm[1]
}

type emailFixture struct {
	pool *pgxpool.Pool
	mail *fakeMail
	e    *EmailLogin
	r    chi.Router
}

// newEmailFixture builds the real routes over a private, fully migrated
// schema. Private because the daily ceiling counts every row in the table.
func newEmailFixture(t *testing.T, inviteRequired bool) *emailFixture {
	t.Helper()
	pool := identityTestPool(t)
	applyUpMigrations(t, pool, 1, allMigrations)
	f := &emailFixture{pool: pool, mail: &fakeMail{}}
	f.e = &EmailLogin{
		Pool:           pool,
		Sender:         f.mail,
		SiteBaseURL:    testSite,
		PostLoginURL:   "/",
		InviteRequired: inviteRequired,
		DailyCap:       1000,
		// Generous: the per-IP bucket has its own test.
		StartLimiter: NewIPRateLimit(1000, 1000),
		// Inline, so a test can look for the mail as soon as the 202 lands.
		Background: func(fn func()) { fn() },
	}
	r := chi.NewRouter()
	r.Route("/api/auth", f.e.MountEmailAuth)
	r.Route("/api/me", func(r chi.Router) {
		r.Use(RequireUser(pool))
		f.e.MountEmailMe(r)
	})
	r.Get(EmailLinkPath, f.e.HandleConfirmPage)
	f.r = r
	return f
}

func (f *emailFixture) do(t *testing.T, method, path, contentType, body string, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: cookie})
	}
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

func (f *emailFixture) start(t *testing.T, addr, invite string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"email": addr, "invite": invite})
	return f.do(t, http.MethodPost, "/api/auth/email/start", "application/json", string(b), "")
}

func (f *emailFixture) consumeForm(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/email/consume",
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testSite)
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	return rec
}

// emailUser creates an account whose only identity is the given address.
func (f *emailFixture) emailUser(t *testing.T, addr string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := f.pool.Exec(ctx, `
INSERT INTO users (id, display_name, encrypted_refresh_token, refresh_token_nonce, email)
VALUES ($1, '', ''::bytea, ''::bytea, $2)`, id, addr); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
INSERT INTO user_identities (provider, subject, user_id) VALUES ('email', $1, $2)`, addr, id); err != nil {
		t.Fatalf("insert identity: %v", err)
	}
	return id
}

// spotifyUserWithSession creates a Spotify account with no address and a live
// session, returning the session token.
func (f *emailFixture) spotifyUserWithSession(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := f.pool.Exec(ctx, `
INSERT INTO users (id, spotify_user_id, display_name, encrypted_refresh_token, refresh_token_nonce)
VALUES ($1, $2, 'Spotify User', 'ct'::bytea, 'nonce'::bytea)`, id, "sp-"+id.String()); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	token := uuid.NewString() + uuid.NewString()
	if err := db.CreateSession(ctx, f.pool, db.Session{
		ID: uuid.NewString(), TokenHash: HashSessionToken(token), UserID: id,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id, token
}

func (f *emailFixture) scalar(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

func sessionCookie(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// The start endpoint must not say which addresses have accounts: known,
// unknown-without-invite and malformed-invite-free requests all answer 202,
// and only the first is mailed.
func TestEmailStartAlwaysAnswers202(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "known@example.com")

	for _, addr := range []string{"known@example.com", "stranger@example.com"} {
		if rec := f.start(t, addr, ""); rec.Code != http.StatusAccepted {
			t.Fatalf("start(%s) = %d, want 202", addr, rec.Code)
		}
	}
	if n := f.mail.count(); n != 1 {
		t.Fatalf("mails sent = %d, want 1 (known address only)", n)
	}
	if to := f.mail.sent[0].To; to != "known@example.com" {
		t.Errorf("mail went to %q", to)
	}
}

// Neither the link token nor the code is in the table; only their hashes.
func TestEmailTokensAreStoredOnlyAsHashes(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "hash@example.com")
	f.start(t, "hash@example.com", "")
	token, code := f.mail.last(t)

	var tokenHash, codeHash string
	var expires time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT token_hash, code_hash, expires_at FROM email_login_tokens WHERE email = $1`,
		"hash@example.com").Scan(&tokenHash, &codeHash, &expires); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if tokenHash == token || strings.Contains(tokenHash, token) || codeHash == code {
		t.Fatal("a raw token or code was stored")
	}
	if tokenHash != HashEmailLoginToken(token) {
		t.Error("token_hash is not HashEmailLoginToken(token)")
	}
	if codeHash != db.EmailCodeHash(tokenHash, code) {
		t.Error("code_hash is not EmailCodeHash(token_hash, code)")
	}
	if ttl := time.Until(expires); ttl > 15*time.Minute || ttl < 14*time.Minute {
		t.Errorf("expiry %v from now, want 15 minutes", ttl)
	}
}

// A mail security gateway fetching the link must not use it up: GET renders
// a page and consumes nothing, and the POST that follows still signs in.
// Then the link is spent.
func TestEmailLinkGETLeavesTokenUsable(t *testing.T) {
	f := newEmailFixture(t, true)
	uid := f.emailUser(t, "scan@example.com")
	f.start(t, "scan@example.com", "")
	token, _ := f.mail.last(t)

	for i := 0; i < 3; i++ {
		rec := f.do(t, http.MethodGet, EmailLinkPath+"?token="+url.QueryEscape(token), "", "", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `method="POST"`) {
			t.Fatalf("GET #%d = %d %q, want 200 with a form", i, rec.Code, rec.Body.String())
		}
	}
	if n := f.scalar(t, `SELECT count(*) FROM email_login_tokens WHERE consumed_at IS NOT NULL`); n != 0 {
		t.Fatalf("GET consumed %d tokens", n)
	}

	rec := f.consumeForm(t, token)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST = %d %q, want 303", rec.Code, rec.Body.String())
	}
	sess := sessionCookie(rec)
	if sess == "" {
		t.Fatal("no session cookie set")
	}
	su, err := db.GetSessionUserByTokenHash(context.Background(), f.pool, HashSessionToken(sess))
	if err != nil || su.User.ID != uid {
		t.Fatalf("session user = %v, %v; want %v", su.User.ID, err, uid)
	}

	if rec := f.consumeForm(t, token); rec.Code != http.StatusBadRequest {
		t.Fatalf("second POST = %d, want 400 (single use)", rec.Code)
	}
	if rec := f.do(t, http.MethodGet, EmailLinkPath+"?token="+url.QueryEscape(token), "", "", ""); strings.Contains(rec.Body.String(), `method="POST"`) {
		t.Error("a spent link still renders a sign-in button")
	}
}

// Fifteen minutes, then nothing.
func TestEmailTokenExpires(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "late@example.com")
	f.e.Now = func() time.Time { return time.Now().Add(-16 * time.Minute) }
	f.start(t, "late@example.com", "")
	token, code := f.mail.last(t)

	if rec := f.consumeForm(t, token); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired link = %d, want 400", rec.Code)
	}
	body := `{"email":"late@example.com","code":"` + code + `"}`
	if rec := f.do(t, http.MethodPost, "/api/auth/email/code", "application/json", body, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired code = %d, want 400", rec.Code)
	}
}

// A form POST from another site must not sign this browser in (login CSRF).
func TestEmailConsumeRefusesCrossSiteForm(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "csrf@example.com")
	f.start(t, "csrf@example.com", "")
	token, _ := f.mail.last(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/email/consume",
		strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	f.r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || sessionCookie(rec) != "" {
		t.Fatalf("cross-site POST = %d, cookie %q; want 403 and none", rec.Code, sessionCookie(rec))
	}
	if rec := f.consumeForm(t, token); rec.Code != http.StatusSeeOther {
		t.Fatalf("the refused POST spent the token: same-site POST = %d", rec.Code)
	}
}

// An unknown address is a signup. Without a code nothing is sent; with one,
// the code is spent in the transaction that creates the user, and only when
// the link is used.
func TestEmailSignupRedeemsInviteAtConsumption(t *testing.T) {
	f := newEmailFixture(t, true)
	ctx := context.Background()
	code, err := db.NewInviteCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateInviteCode(ctx, f.pool, code, "email test", 1, nil); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	if rec := f.start(t, "new@example.com", code); rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d", rec.Code)
	}
	if n := f.scalar(t, `SELECT redemptions FROM invite_codes WHERE code = $1`, code); n != 0 {
		t.Fatalf("redemptions after start = %d, want 0: an unclicked link must spend nothing", n)
	}
	token, _ := f.mail.last(t)
	if rec := f.consumeForm(t, token); rec.Code != http.StatusSeeOther {
		t.Fatalf("consume = %d %q", rec.Code, rec.Body.String())
	}
	if n := f.scalar(t, `SELECT redemptions FROM invite_codes WHERE code = $1`, code); n != 1 {
		t.Fatalf("redemptions = %d, want 1", n)
	}
	if n := f.scalar(t, `
SELECT count(*) FROM users u JOIN user_identities i ON i.user_id = u.id
WHERE i.provider = 'email' AND i.subject = 'new@example.com'
  AND u.email = 'new@example.com' AND u.spotify_user_id IS NULL AND u.invited_with = $1`, code); n != 1 {
		t.Fatalf("new email account rows = %d, want 1", n)
	}

	// The code is used up: a second stranger is refused at the pre-check.
	if rec := f.start(t, "other@example.com", code); rec.Code != http.StatusForbidden {
		t.Fatalf("start with spent code = %d, want 403", rec.Code)
	}
}

// The pre-check is read-only, so a code can be used up between the mail and
// the click. The consumption is then refused, no user is created, and the
// token stays unspent rather than half-applied.
func TestEmailSignupRefusedWhenInviteSpentBeforeClick(t *testing.T) {
	f := newEmailFixture(t, true)
	ctx := context.Background()
	code, _ := db.NewInviteCode()
	if _, err := db.CreateInviteCode(ctx, f.pool, code, "race", 1, nil); err != nil {
		t.Fatal(err)
	}
	f.start(t, "first@example.com", code)
	first, _ := f.mail.last(t)
	f.start(t, "second@example.com", code)
	second, _ := f.mail.last(t)

	if rec := f.consumeForm(t, second); rec.Code != http.StatusSeeOther {
		t.Fatalf("second consume = %d", rec.Code)
	}
	if rec := f.consumeForm(t, first); rec.Code != http.StatusForbidden {
		t.Fatalf("first consume after code spent = %d, want 403", rec.Code)
	}
	if n := f.scalar(t, `SELECT count(*) FROM user_identities WHERE subject = 'first@example.com'`); n != 0 {
		t.Fatal("a user was admitted without a redeemable invite")
	}
	if n := f.scalar(t, `SELECT count(*) FROM email_login_tokens WHERE email = 'first@example.com' AND consumed_at IS NOT NULL`); n != 0 {
		t.Fatal("the refused consumption committed")
	}
}

// With the gate off an unknown address signs up with no code.
func TestEmailSignupWithGateOff(t *testing.T) {
	f := newEmailFixture(t, false)
	f.start(t, "open@example.com", "")
	token, _ := f.mail.last(t)
	if rec := f.consumeForm(t, token); rec.Code != http.StatusSeeOther {
		t.Fatalf("consume = %d", rec.Code)
	}
	if n := f.scalar(t, `SELECT count(*) FROM user_identities WHERE provider = 'email' AND subject = 'open@example.com'`); n != 1 {
		t.Fatal("no email identity created")
	}
}

// The code signs in, and five wrong guesses retire it.
func TestEmailCodeSignsInAndAttemptsAreCapped(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "code@example.com")
	f.start(t, "code@example.com", "")
	_, code := f.mail.last(t)

	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	post := func(c string) *httptest.ResponseRecorder {
		return f.do(t, http.MethodPost, "/api/auth/email/code", "application/json",
			`{"email":"code@example.com","code":"`+c+`"}`, "")
	}
	for i := 0; i < db.EmailCodeMaxAttempts; i++ {
		if rec := post(wrong); rec.Code != http.StatusBadRequest {
			t.Fatalf("wrong guess #%d = %d, want 400", i+1, rec.Code)
		}
	}
	if rec := post(code); rec.Code != http.StatusBadRequest {
		t.Fatalf("right code after %d misses = %d, want 400", db.EmailCodeMaxAttempts, rec.Code)
	}

	f.start(t, "code@example.com", "")
	_, fresh := f.mail.last(t)
	rec := post(fresh)
	if rec.Code != http.StatusNoContent || sessionCookie(rec) == "" {
		t.Fatalf("fresh code = %d, cookie %q; want 204 and a session", rec.Code, sessionCookie(rec))
	}
}

// Three mails per address per hour.
func TestEmailPerAddressLimit(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "flood@example.com")
	for i := 0; i < db.EmailLoginPerAddressHourly+2; i++ {
		if rec := f.start(t, "flood@example.com", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("start #%d = %d, want 202 even when limited", i+1, rec.Code)
		}
	}
	if n := f.mail.count(); n != db.EmailLoginPerAddressHourly {
		t.Fatalf("mails = %d, want %d", n, db.EmailLoginPerAddressHourly)
	}
}

// The daily ceiling bounds every caller together.
func TestEmailDailyCeiling(t *testing.T) {
	f := newEmailFixture(t, true)
	f.e.DailyCap = 2
	for _, a := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		f.emailUser(t, a)
		f.start(t, a, "")
	}
	if n := f.mail.count(); n != 2 {
		t.Fatalf("mails = %d, want 2", n)
	}
}

// The per-IP bucket on /email/start. No database needed: the limiter answers
// before the handler, and the handler refuses a malformed address before any
// lookup.
func TestEmailStartPerIPLimit(t *testing.T) {
	e := &EmailLogin{StartLimiter: NewIPRateLimit(1.0/60, 5), Background: func(fn func()) { fn() }}
	r := chi.NewRouter()
	r.Route("/api/auth", e.MountEmailAuth)
	send := func(ip string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/email/start", strings.NewReader(`{"email":"not-an-address"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = ip
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 5; i++ {
		if c := send("203.0.113.7"); c != http.StatusBadRequest {
			t.Fatalf("request #%d = %d, want 400 (under the limit)", i+1, c)
		}
	}
	if c := send("203.0.113.7"); c != http.StatusTooManyRequests {
		t.Fatalf("sixth request = %d, want 429", c)
	}
	if c := send("198.51.100.9"); c != http.StatusBadRequest {
		t.Fatalf("another address = %d, want its own bucket", c)
	}
}

// A signed-in Spotify user confirms an address: it becomes users.email and an
// email identity, so the account can now sign in by email.
func TestVerifyAddressAddsEmailIdentity(t *testing.T) {
	f := newEmailFixture(t, true)
	uid, sess := f.spotifyUserWithSession(t)

	if rec := f.do(t, http.MethodPost, "/api/me/email", "application/json", `{"email":"Me@Example.com"}`, sess); rec.Code != http.StatusAccepted {
		t.Fatalf("set address = %d %q", rec.Code, rec.Body.String())
	}
	if n := f.scalar(t, `SELECT count(*) FROM users WHERE id = $1 AND email IS NOT NULL`, uid); n != 0 {
		t.Fatal("address stored before it was confirmed")
	}
	_, code := f.mail.last(t)
	if rec := f.do(t, http.MethodPost, "/api/me/email/verify", "application/json",
		`{"email":"me@example.com","code":"`+code+`"}`, sess); rec.Code != http.StatusNoContent {
		t.Fatalf("verify = %d %q", rec.Code, rec.Body.String())
	}
	if n := f.scalar(t, `SELECT count(*) FROM users WHERE id = $1 AND email = 'me@example.com'`, uid); n != 1 {
		t.Fatal("users.email not set")
	}
	if n := f.scalar(t, `SELECT count(*) FROM user_identities WHERE provider = 'email' AND subject = 'me@example.com' AND user_id = $1`, uid); n != 1 {
		t.Fatal("no email identity added")
	}

	// And it now signs in to that same account.
	f.start(t, "me@example.com", "")
	token, _ := f.mail.last(t)
	rec := f.consumeForm(t, token)
	su, err := db.GetSessionUserByTokenHash(context.Background(), f.pool, HashSessionToken(sessionCookie(rec)))
	if err != nil || su.User.ID != uid {
		t.Fatalf("email sign-in reached %v (%v), want %v", su.User.ID, err, uid)
	}
}

// An address that already signs in to one account cannot be added to
// another. Accounts are never merged by address.
func TestVerifyAddressRefusesAnotherAccountsAddress(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "taken@example.com")
	uid, sess := f.spotifyUserWithSession(t)
	f.do(t, http.MethodPost, "/api/me/email", "application/json", `{"email":"taken@example.com"}`, sess)
	token, _ := f.mail.last(t)
	if rec := f.consumeForm(t, token); rec.Code != http.StatusConflict {
		t.Fatalf("verify taken address = %d, want 409", rec.Code)
	}
	if n := f.scalar(t, `SELECT count(*) FROM user_identities WHERE user_id = $1 AND provider = 'email'`, uid); n != 0 {
		t.Fatal("address linked to a second account")
	}
}

// The app's JSON consumption needs the verifier for the challenge it started
// with; a wrong one is refused and leaves the link usable.
func TestEmailBearerConsumeNeedsVerifier(t *testing.T) {
	f := newEmailFixture(t, true)
	f.emailUser(t, "app@example.com")
	verifier, _ := GenerateVerifier()
	b, _ := json.Marshal(map[string]string{
		"email": "app@example.com", "client": ClientIOS, "app_challenge": ChallengeFromVerifier(verifier),
	})
	if rec := f.do(t, http.MethodPost, "/api/auth/email/start", "application/json", string(b), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("start = %d", rec.Code)
	}
	token, _ := f.mail.last(t)

	other, _ := GenerateVerifier()
	if rec := f.do(t, http.MethodPost, "/api/auth/email/consume", "application/json",
		`{"token":"`+token+`","verifier":"`+other+`"}`, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong verifier = %d, want 400", rec.Code)
	}
	rec := f.do(t, http.MethodPost, "/api/auth/email/consume", "application/json",
		`{"token":"`+token+`","verifier":"`+verifier+`"}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("right verifier = %d %q", rec.Code, rec.Body.String())
	}
	var resp mobileExchangeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.SessionToken == "" {
		t.Fatalf("no session token: %v %q", err, rec.Body.String())
	}
}

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"  Person@Example.COM ":   "person@example.com",
		"a.b+tag@sub.example.org": "a.b+tag@sub.example.org",
	} {
		if got, ok := normalizeEmail(in); !ok || got != want {
			t.Errorf("normalizeEmail(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "plain", "Name <a@example.com>", "a@b, c@d.com", "a@localhost", "@example.com",
		strings.Repeat("a", 250) + "@example.com"} {
		if got, ok := normalizeEmail(bad); ok {
			t.Errorf("normalizeEmail(%q) accepted as %q", bad, got)
		}
	}
}

func TestSignInMailCarriesNoUnsubscribeAndNamesExpiry(t *testing.T) {
	m := email.RenderSignIn("x@example.com", testSite+EmailLinkPath+"?token=abc", "123456", email.SignInLogin)
	if m.UnsubscribeURL != "" {
		t.Error("transactional sign-in mail must not carry List-Unsubscribe")
	}
	if !strings.Contains(m.Text, "123456") || !strings.Contains(m.Text, "15 minutes") || !strings.Contains(m.HTML, "token=abc") {
		t.Errorf("mail missing code, expiry or link: %q", m.Text)
	}
}
