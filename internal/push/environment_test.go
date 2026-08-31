package push

import "testing"

// A throwaway P-256 key, generated for this test alone. It authenticates
// nothing: New only has to parse it to reach the host selection under test.
const testPEM = `-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQgvQ841JcnsNiHz4hP
5GHgUgnGBV2yQWV629qWVNwe9qChRANCAAQVoSHm75ZiQYWT2zatetV5UuOEzBBx
kMZR6433DNIyoIRhrk+hJHGObKSESdS/dnx8oTndKJpBYoYQKnBrFpy1
-----END PRIVATE KEY-----`

// Environment is derived from Host so the two cannot disagree about where
// requests go -- the worker filters devices on this value, so a stale second
// field would silently drop every device instead of every wrong one.
func TestClientEnvironmentTracksHost(t *testing.T) {
	if got := (&Client{Host: HostSandbox}).Environment(); got != "sandbox" {
		t.Errorf("sandbox host reported %q", got)
	}
	if got := (&Client{Host: HostProduction}).Environment(); got != "production" {
		t.Errorf("production host reported %q", got)
	}
}

// New must map the config string to the host, since that is what Environment
// then reports back to the worker.
func TestNewSelectsHostFromEnvironment(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"sandbox", HostSandbox},
		{"production", HostProduction},
		{"", HostProduction},
	} {
		c, err := New(Config{
			KeyID: "K", TeamID: "T", BundleID: "B",
			P8Key: testPEM, Environment: tc.env,
		})
		if err != nil {
			t.Fatalf("env %q: %v", tc.env, err)
		}
		if c.Host != tc.want {
			t.Errorf("env %q -> host %q, want %q", tc.env, c.Host, tc.want)
		}
	}
}
