package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerToken(t *testing.T) {
	for _, tc := range []struct {
		name, header, want string
	}{
		{"absent", "", ""},
		{"bearer", "Bearer abc123", "abc123"},
		{"lowercase scheme", "bearer abc123", "abc123"},
		{"mixed case scheme", "BeArEr abc123", "abc123"},
		{"surrounding space", "Bearer   abc123  ", "abc123"},
		{"wrong scheme", "Basic abc123", ""},
		{"scheme only", "Bearer", ""},
		{"scheme and space only", "Bearer ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := BearerToken(r); got != tc.want {
				t.Fatalf("BearerToken(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// The mechanism a request authenticates by decides whether CSRF applies, so
// the resolution order is security-relevant, not a detail.
func TestSessionCredentialResolution(t *testing.T) {
	t.Run("bearer only", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer tok")
		id, mech := sessionCredential(r)
		if id != "tok" || mech != MechanismBearer {
			t.Fatalf("got (%q, %v), want (tok, MechanismBearer)", id, mech)
		}
	})

	t.Run("cookie only", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess"})
		id, mech := sessionCredential(r)
		if id != "sess" || mech != MechanismCookie {
			t.Fatalf("got (%q, %v), want (sess, MechanismCookie)", id, mech)
		}
	})

	// A request carrying both is a browser that also set a header. Honouring
	// the header is the safer read: it is the credential an attacker's page
	// cannot cause to be sent.
	t.Run("both prefers bearer", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", "Bearer tok")
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess"})
		id, mech := sessionCredential(r)
		if id != "tok" || mech != MechanismBearer {
			t.Fatalf("got (%q, %v), want (tok, MechanismBearer)", id, mech)
		}
	})

	t.Run("neither", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		id, mech := sessionCredential(r)
		if id != "" || mech != MechanismNone {
			t.Fatalf("got (%q, %v), want (\"\", MechanismNone)", id, mech)
		}
	})

	// An empty cookie is not a credential. Treating it as one would send an
	// empty string to the session lookup on every anonymous request.
	t.Run("empty cookie is not a credential", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: ""})
		if id, mech := sessionCredential(r); id != "" || mech != MechanismNone {
			t.Fatalf("got (%q, %v), want (\"\", MechanismNone)", id, mech)
		}
	})
}

// The exchange accepts a verifier only if it hashes to the stored challenge.
// This is what makes an intercepted one-time code useless on its own.
func TestAppChallengeVerification(t *testing.T) {
	verifier, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	challenge := ChallengeFromVerifier(verifier)

	if ChallengeFromVerifier(verifier) != challenge {
		t.Fatal("challenge derivation is not deterministic")
	}

	other, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if ChallengeFromVerifier(other) == challenge {
		t.Fatal("a different verifier produced the same challenge")
	}
	// The stored challenge must not itself be a usable verifier — that would
	// make anyone who intercepted the /login query able to complete the
	// exchange.
	if ChallengeFromVerifier(challenge) == challenge {
		t.Fatal("the challenge hashes to itself")
	}
}

func TestMobileCallbackURL(t *testing.T) {
	for _, tc := range []struct {
		name, base, want string
	}{
		{
			"plain",
			"https://example.com/app/auth/callback",
			"https://example.com/app/auth/callback?code=abc",
		},
		{
			// A base that already carries a query must not produce a second
			// '?', which would make the code unparseable.
			"existing query",
			"https://example.com/app/auth/callback?src=login",
			"https://example.com/app/auth/callback?src=login&code=abc",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Deps{MobileCallbackURL: tc.base}
			if got := d.mobileCallbackURL("abc"); got != tc.want {
				t.Fatalf("mobileCallbackURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// /login?client=ios without a challenge must be refused. Accepting it would
// mint a code nothing could redeem, and the app would hang on a login that
// already succeeded server-side.
func TestLoginRequiresAppChallengeForIOSClient(t *testing.T) {
	d := &Deps{MobileCallbackURL: "https://example.com/app/auth/callback"}
	req := httptest.NewRequest(http.MethodGet, "/login?client=ios", nil)
	rec := httptest.NewRecorder()
	d.handleLogin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// A deployment with no universal link configured must refuse the mobile flow
// rather than complete it into a session only a throwaway web context holds.
func TestLoginRefusesIOSClientWhenCallbackUnconfigured(t *testing.T) {
	d := &Deps{}
	req := httptest.NewRequest(http.MethodGet, "/login?client=ios&app_challenge=xyz", nil)
	rec := httptest.NewRecorder()
	d.handleLogin(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}
