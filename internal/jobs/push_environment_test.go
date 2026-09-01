package jobs

import (
	"testing"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/db"
	"github.com/peterho/concertfinder/internal/push"
)

func devs(envs ...string) []db.Device {
	out := make([]db.Device, 0, len(envs))
	for i, e := range envs {
		out = append(out, db.Device{DeviceToken: string(rune('a'+i)) + "-token", Environment: e})
	}
	return out
}

// The ordinary case now that one client reaches both hosts: an auth key
// issued as "Sandbox & Production" serves every device, and each token is
// routed to the host that minted it. Nothing is filtered out.
func TestForEnvironmentKeepsBothWhenTheKeyServesBoth(t *testing.T) {
	in := devs("sandbox", "production", "sandbox")
	kept := forEnvironment(in, []string{push.EnvSandbox, push.EnvProduction}, uuid.New())

	if len(kept) != len(in) {
		t.Fatalf("kept %d of %d devices; a both-environments key should skip none", len(kept), len(in))
	}
}

// A key restricted to one environment in Apple's portal. The other host
// answers InvalidProviderToken, which no retry improves, so those devices are
// skipped rather than sent.
func TestForEnvironmentDropsWhatTheKeyCannotServe(t *testing.T) {
	kept := forEnvironment(devs("sandbox", "production", "sandbox"), []string{push.EnvSandbox}, uuid.New())

	if len(kept) != 2 {
		t.Fatalf("kept %d devices, want 2", len(kept))
	}
	for _, d := range kept {
		if d.Environment != push.EnvSandbox {
			t.Errorf("kept a %q device with a sandbox-only key", d.Environment)
		}
	}
}

// Skipping must not be a write. The device has to start working again the
// moment the deployment gains a key that covers it, which is exactly what
// disabling it would have prevented.
func TestForEnvironmentKeepsNothingWhenNoneAreServed(t *testing.T) {
	kept := forEnvironment(devs("production", "production"), []string{push.EnvSandbox}, uuid.New())

	if len(kept) != 0 {
		t.Fatalf("kept %d devices, want 0", len(kept))
	}
}

// A client with no configured environments serves nothing, and the filter
// must agree — otherwise the send loop would push to a host derived from
// nothing at all.
func TestForEnvironmentWithNoServedEnvironmentsKeepsNothing(t *testing.T) {
	if kept := forEnvironment(devs("sandbox", "production"), nil, uuid.New()); len(kept) != 0 {
		t.Fatalf("kept %d devices with no served environments, want 0", len(kept))
	}
}

// Every notification must carry the device's own environment, not just its
// token. This is the catastrophic one: a sandbox token sent to the production
// host answers BadDeviceToken, the send loop reads that as an uninstall and
// calls db.DisableDevice, and the device is retired for good. One dropped
// assignment is enough, and it compiles.
func TestAddressedToCarriesBothHalvesOfTheAddress(t *testing.T) {
	base := push.Notification{CollapseID: "event-1"}
	for _, d := range devs("sandbox", "production") {
		got := addressedTo(base, d)
		if got.DeviceToken != d.DeviceToken {
			t.Errorf("token %q, want %q", got.DeviceToken, d.DeviceToken)
		}
		if got.Environment != d.Environment {
			t.Errorf("token %q addressed to %q, want %q — this token is only valid at its own host",
				d.DeviceToken, got.Environment, d.Environment)
		}
		if got.CollapseID != base.CollapseID {
			t.Errorf("rendered payload lost its collapse ID")
		}
	}
}

// Addressing must not mutate the shared notification. The loop renders one
// per event and addresses it per device, so a mutating version leaks the
// previous device's environment into the next send whenever a later
// assignment is dropped.
func TestAddressedToDoesNotMutateTheRenderedNotification(t *testing.T) {
	base := push.Notification{CollapseID: "event-1"}
	_ = addressedTo(base, db.Device{DeviceToken: "a", Environment: push.EnvSandbox})

	if base.DeviceToken != "" || base.Environment != "" {
		t.Errorf("addressing wrote back to the shared notification: token=%q env=%q",
			base.DeviceToken, base.Environment)
	}
}
