package jobs

import (
	"testing"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/db"
)

func devs(envs ...string) []db.Device {
	out := make([]db.Device, 0, len(envs))
	for i, e := range envs {
		out = append(out, db.Device{DeviceToken: string(rune('a'+i)) + "-token", Environment: e})
	}
	return out
}

// The bug this exists for: a deployment flipped to production for TestFlight
// would send every lingering sandbox token to the production host, get
// BadDeviceToken, and permanently disable the device -- so the user simply
// stops hearing from the app, with nothing surfaced anywhere.
func TestForEnvironmentDropsTheOtherEnvironment(t *testing.T) {
	kept := forEnvironment(devs("sandbox", "production", "sandbox"), "sandbox", uuid.New())

	if len(kept) != 2 {
		t.Fatalf("kept %d devices, want 2", len(kept))
	}
	for _, d := range kept {
		if d.Environment != "sandbox" {
			t.Errorf("kept a %q device while sending to sandbox", d.Environment)
		}
	}
}

// Skipping must not be a write. The device has to start working again the
// moment the app re-registers with a matching token, which is exactly what
// disabling it would have prevented.
func TestForEnvironmentKeepsNothingWhenAllMismatch(t *testing.T) {
	kept := forEnvironment(devs("production", "production"), "sandbox", uuid.New())

	if len(kept) != 0 {
		t.Fatalf("kept %d devices, want 0", len(kept))
	}
}

func TestForEnvironmentKeepsEverythingWhenAligned(t *testing.T) {
	in := devs("production", "production")
	if got := len(forEnvironment(in, "production", uuid.New())); got != len(in) {
		t.Errorf("kept %d, want %d", got, len(in))
	}
}
