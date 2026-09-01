package push

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A throwaway P-256 key, generated for this test alone. It authenticates
// nothing: New only has to parse it to reach the routing under test.
const testPEM = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgvQ841JcnsNiHz4hP
5GHgUgnGBV2yQWV629qWVNwe9qChRANCAAQVoSHm75ZiQYWT2zatetV5UuOEzBBx
kMZR6433DNIyoIRhrk+hJHGObKSESdS/dnx8oTndKJpBYoYQKnBrFpy1
-----END PRIVATE KEY-----`

func testClient(t *testing.T, env string) *Client {
	t.Helper()
	c, err := New(Config{KeyID: "K", TeamID: "T", BundleID: "B", P8Key: testPEM, Environment: env})
	if err != nil {
		t.Fatalf("New(%q): %v", env, err)
	}
	return c
}

// recordURL swaps in a transport that answers every request with 200 and
// remembers where it was sent, so routing is observable without a network.
func recordURL(c *Client, got *string) {
	c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*got = r.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The change this file exists for. One client, both hosts, chosen by the
// device rather than by configuration -- so a deployment serving TestFlight
// and a developer's debug build reaches both instead of having to pick.
func TestSendRoutesEachDeviceToItsOwnHost(t *testing.T) {
	c := testClient(t, "sandbox,production")
	for _, tc := range []struct{ env, wantHost string }{
		{EnvSandbox, HostSandbox},
		{EnvProduction, HostProduction},
	} {
		var url string
		recordURL(c, &url)
		err := c.Send(context.Background(), Notification{
			DeviceToken: "abc", Environment: tc.env,
		})
		if err != nil {
			t.Fatalf("send to %s: %v", tc.env, err)
		}
		if want := tc.wantHost + "/3/device/abc"; url != want {
			t.Errorf("%s device sent to %q, want %q", tc.env, url, want)
		}
	}
}

// An unset Environment must not fall through to a host. A sandbox token sent
// to the production host answers BadDeviceToken, IsUnregistered reports that
// as a dead token, and SendPushWorker retires the device permanently -- so a
// caller who forgets this field would cost a user their notifications for
// good. Failing the send costs one notification instead.
func TestSendRefusesANotificationWithNoEnvironment(t *testing.T) {
	c := testClient(t, "sandbox,production")
	var url string
	recordURL(c, &url)

	err := c.Send(context.Background(), Notification{DeviceToken: "abc"})
	if err == nil {
		t.Fatal("a notification with no environment was sent somewhere")
	}
	if url != "" {
		t.Errorf("request went to %q; nothing should have been sent", url)
	}
}

// A key restricted to one environment in Apple's portal is rejected by the
// other host with InvalidProviderToken -- not a device problem, and not
// something a retry improves. The worker filters on Serves first, so this is
// a guard on the client itself.
func TestSendRefusesAnEnvironmentTheKeyDoesNotServe(t *testing.T) {
	c := testClient(t, "sandbox")
	var url string
	recordURL(c, &url)

	err := c.Send(context.Background(), Notification{DeviceToken: "abc", Environment: EnvProduction})
	if err == nil {
		t.Fatal("sent to production with a sandbox-only key")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("error should name the refused environment, got %q", err)
	}
	if url != "" {
		t.Errorf("request went to %q; nothing should have been sent", url)
	}
}

func TestParseEnvironments(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		// Unset means production, matching config.Load's default.
		{"", []string{EnvProduction}},
		{"sandbox", []string{EnvSandbox}},
		{"production", []string{EnvProduction}},
		{"sandbox,production", []string{EnvSandbox, EnvProduction}},
		// What an operator actually types into a .env file.
		{" Sandbox , Production ", []string{EnvSandbox, EnvProduction}},
		{"production,production", []string{EnvProduction}},
	} {
		got, err := ParseEnvironments(tc.in)
		if err != nil {
			t.Errorf("ParseEnvironments(%q): %v", tc.in, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("ParseEnvironments(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// A typo must not quietly become one environment or the other: either
// direction is a silent no-op for half the fleet.
func TestParseEnvironmentsRejectsAnythingElse(t *testing.T) {
	for _, in := range []string{"development", "prod", "sandbox,", "both", "sandbox production"} {
		if got, err := ParseEnvironments(in); err == nil {
			t.Errorf("ParseEnvironments(%q) accepted it as %v", in, got)
		}
	}
}

func TestServesReflectsTheConfiguredKey(t *testing.T) {
	both := testClient(t, "sandbox,production")
	if !both.Serves(EnvSandbox) || !both.Serves(EnvProduction) {
		t.Errorf("a both-environments key serves %v", both.Environments())
	}
	sandboxOnly := testClient(t, "sandbox")
	if sandboxOnly.Serves(EnvProduction) {
		t.Error("a sandbox-only key claims to serve production")
	}
}

// The safe direction for a hand-built client: send nothing, rather than send
// to a host that would retire the tokens.
func TestZeroValueClientServesNothing(t *testing.T) {
	var c Client
	if c.Serves(EnvSandbox) || c.Serves(EnvProduction) || len(c.Environments()) != 0 {
		t.Errorf("zero-value client serves %v", c.Environments())
	}
}
