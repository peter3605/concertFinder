package spa

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// A built SPA, minus everything that does not affect routing.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>ConcertFinder</title>")},
		"assets/index-abc123.js": {Data: []byte("console.log(1)")},
		"robots.txt":             {Data: []byte("User-agent: *\n")},
		"favicon.svg":            {Data: []byte("<svg/>")},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// The half that breaks the site: every real static file must still be served,
// with the caching the build depends on.
func TestHandlerServesRealAssets(t *testing.T) {
	h := handlerFor(testFS())

	for _, tc := range []struct {
		path string
		body string
	}{
		{"/assets/index-abc123.js", "console.log(1)"},
		{"/robots.txt", "User-agent: *\n"},
		{"/favicon.svg", "<svg/>"},
	} {
		rec := get(t, h, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tc.path, rec.Code)
		}
		if got := rec.Body.String(); got != tc.body {
			t.Errorf("%s: body = %q, want %q", tc.path, got, tc.body)
		}
	}

	if got := get(t, h, "/assets/index-abc123.js").Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("hashed bundle Cache-Control = %q", got)
	}
}

// A path naming a file we do not have is a 404. Serving index.html with a 200
// told crawlers /robots.txt existed and handed them HTML, and made a missing
// bundle chunk look like a syntax error in a script rather than a 404.
func TestHandlerNotFoundForMissingFiles(t *testing.T) {
	h := handlerFor(testFS())

	for _, p := range []string{"/favicon.ico", "/sitemap.xml", "/assets/index-stale.js", "/apple-touch-icon.png"} {
		if code := get(t, h, p).Code; code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", p, code)
		}
	}
}

// Extension-free paths stay with the router, including ones it does not know:
// this handler cannot tell a real route from a typo without duplicating the
// route table, so App.tsx's catch-all renders the 404 page instead.
func TestHandlerServesIndexForRoutes(t *testing.T) {
	h := handlerFor(testFS())

	for _, p := range []string{"/", "/saved", "/app/auth/callback", "/no-such-route"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", p, rec.Code)
		}
		if rec.Body.String() != "<!doctype html><title>ConcertFinder</title>" {
			t.Errorf("%s: did not serve index.html", p)
		}
	}
}
