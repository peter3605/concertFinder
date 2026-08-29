package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testAppID = "L3MY7DN27B.com.concertfinder.ph"

func serveAASA(t *testing.T, appID string) *httptest.ResponseRecorder {
	t.Helper()
	h := &AASAHandler{AppID: appID}
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest(http.MethodGet, "/.well-known/apple-app-site-association", nil))
	return rec
}

// An unconfigured deployment must 404. Serving an association that names an
// empty app is worse than serving nothing, because iOS caches the file and
// learns that the domain does not belong to the app.
func TestAASAUnconfiguredIs404(t *testing.T) {
	if got := serveAASA(t, "").Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// Two services, not one. applinks is what routes the callback link into the
// app; webcredentials is what lets ASWebAuthenticationSession's
// https(host:path:) callback end the auth sheet. Dropping webcredentials makes
// iOS refuse to start the session, and the app hangs on the Spotify page with
// no error -- which is exactly how it shipped the first time.
func TestAASANamesAppInBothServices(t *testing.T) {
	rec := serveAASA(t, testAppID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Applinks struct {
			Details []struct {
				AppIDs     []string         `json:"appIDs"`
				Components []map[string]any `json:"components"`
			} `json:"details"`
		} `json:"applinks"`
		WebCredentials struct {
			Apps []string `json:"apps"`
		} `json:"webcredentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(body.Applinks.Details) != 1 || len(body.Applinks.Details[0].AppIDs) != 1 ||
		body.Applinks.Details[0].AppIDs[0] != testAppID {
		t.Errorf("applinks appIDs = %v, want [%s]", body.Applinks.Details, testAppID)
	}
	if len(body.WebCredentials.Apps) != 1 || body.WebCredentials.Apps[0] != testAppID {
		t.Errorf("webcredentials apps = %v, want [%s]", body.WebCredentials.Apps, testAppID)
	}

	// The claim stays scoped to the OAuth return path. "*" would pull the
	// privacy and terms pages -- which App Review opens in a browser -- into
	// the app.
	comps := body.Applinks.Details[0].Components
	if len(comps) != 1 || comps[0]["/"] != "/app/auth/callback" {
		t.Errorf("components = %v, want only /app/auth/callback", comps)
	}
}
