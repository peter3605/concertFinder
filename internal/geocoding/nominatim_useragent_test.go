package geocoding

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The User-Agent is a contract with Nominatim, not a formality: their usage
// policy requires a way to reach the operator, and their stated remedy for one
// they cannot act on is a block. That failure arrives as silence — venue
// geocoding simply stops resolving, with no error this process raises.
//
// This guard is deliberately a copy of the one in internal/fallback rather
// than a shared helper. That package's TestPackageUserAgentDefaultsAgree looks
// comprehensive and cannot see this constant, which is exactly how this file
// kept a repository URL for as long as it did. A test that lives beside the
// thing it guards is the point; importing internal/fallback here to dedupe a
// string assertion would buy a package dependency for nothing.
func TestDefaultUserAgentNamesAReachableAddress(t *testing.T) {
	if strings.Contains(defaultUserAgent, "github.com") {
		t.Errorf("default UA %q points at a code repository, not a contact address", defaultUserAgent)
	}
	if !strings.Contains(defaultUserAgent, "concertfinder.app") {
		t.Errorf("default UA %q names no address anyone can actually reach", defaultUserAgent)
	}
}

// A caller that supplies a User-Agent must get it; one that supplies nothing
// must still send something. main.go always supplies one, so the second half
// is the case nobody exercises in production and the one that rots.
func TestNewClientUserAgent(t *testing.T) {
	const ua = "ConcertFinder/1.0 (+https://example.test; ops@example.test)"
	if got := NewClient(ua).UserAgent; got != ua {
		t.Errorf("UA = %q, want %q", got, ua)
	}
	if got := NewClient("").UserAgent; got != defaultUserAgent {
		t.Errorf("empty UA fell back to %q, want the package default %q", got, defaultUserAgent)
	}
}

// The header has to reach the wire, not just the struct — the policy is about
// what Nominatim receives. Drive the real Search and intercept at the
// transport, so this exercises the code under test rather than asserting that
// a header this test set itself came back: Search builds its own request
// against NominatimEndpoint, and a stub RoundTripper is what lets that happen
// with no network and no injectable-endpoint change to production code.
type capturingRT struct {
	gotUA   string
	gotHost string
}

func (rt *capturingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.gotUA = r.Header.Get("User-Agent")
	rt.gotHost = r.URL.Host
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`[{"display_name":"Baltimore","lat":"39.29","lon":"-76.61"}]`)),
		Header:     make(http.Header),
	}, nil
}

func TestSearchSendsTheUserAgent(t *testing.T) {
	const ua = "ConcertFinder/1.0 (+https://example.test; ops@example.test)"
	rt := &capturingRT{}
	c := NewClient(ua)
	c.HTTP = &http.Client{Transport: rt}

	if _, err := c.Search(context.Background(), "Baltimore, MD"); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rt.gotUA != ua {
		t.Errorf("Nominatim saw User-Agent %q, want %q", rt.gotUA, ua)
	}
	if rt.gotHost != "nominatim.openstreetmap.org" {
		t.Errorf("Search hit host %q, want nominatim.openstreetmap.org", rt.gotHost)
	}

	// The default has to reach the wire too — this is the path main.go never
	// takes and therefore the one that rots unwatched.
	rt2 := &capturingRT{}
	d := NewClient("")
	d.HTTP = &http.Client{Transport: rt2}
	if _, err := d.Search(context.Background(), "Baltimore, MD"); err != nil {
		t.Fatalf("Search with default UA: %v", err)
	}
	if rt2.gotUA != defaultUserAgent {
		t.Errorf("default path sent %q, want %q", rt2.gotUA, defaultUserAgent)
	}
	if strings.Contains(rt2.gotUA, "github.com") {
		t.Errorf("default path sent a code-repository URL on the wire: %q", rt2.gotUA)
	}
}
