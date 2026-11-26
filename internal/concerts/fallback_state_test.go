package concerts

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// deadSongkickFallbacker is a chain whose Songkick tier met a 401: it marks
// the scan's gate dead the way fallback.Chain does.
type deadSongkickFallbacker struct{ stubFallbacker }

func (f *deadSongkickFallbacker) FindEvents(ctx context.Context, a Target, l Location) []Concert {
	if ok, report := FallbackScanStateFrom(ctx).SongkickGate().Enter(ctx); ok {
		report(true)
	}
	return f.stubFallbacker.FindEvents(ctx, a, l)
}

// summaryLine runs a one-artist scan and returns the "fallback summary"
// record it logged.
func summaryLine(t *testing.T, fb Fallbacker) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	d := statsDeps(t, fb)
	if _, err := Search(context.Background(), d, []Target{statsArtist(t)}, Location{RadiusMiles: 50}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "fallback summary" {
			return rec
		}
	}
	t.Fatalf("no fallback summary logged; got:\n%s", buf.String())
	return nil
}

// The summary carries songkick_unauthorized on every scan, true or false,
// so a dead key is one line per scan rather than a WARN per artist (CF-B34).
func TestFallbackSummaryReportsSongkickUnauthorized(t *testing.T) {
	if got := summaryLine(t, &deadSongkickFallbacker{})["songkick_unauthorized"]; got != true {
		t.Errorf("dead key: songkick_unauthorized = %v, want true", got)
	}
	if got := summaryLine(t, &stubFallbacker{})["songkick_unauthorized"]; got != false {
		t.Errorf("working key: songkick_unauthorized = %v, want false (logged unconditionally)", got)
	}
}

func TestSourceGate(t *testing.T) {
	t.Run("nil admits everything", func(t *testing.T) {
		var s *FallbackScanState
		ok, report := s.SongkickGate().Enter(context.Background())
		report(true)
		if !ok || s.SongkickGate().Dead() {
			t.Fatal("a nil gate must admit and never report dead")
		}
	})

	t.Run("waiters follow the probe", func(t *testing.T) {
		var g SourceGate
		ok, report := g.Enter(context.Background())
		if !ok {
			t.Fatal("the probe was refused")
		}
		got := make(chan bool)
		go func() {
			ok, _ := g.Enter(context.Background())
			got <- ok
		}()
		select {
		case <-got:
			t.Fatal("a waiter entered before the probe reported")
		case <-time.After(50 * time.Millisecond):
		}
		report(true)
		if <-got {
			t.Fatal("a waiter entered after the probe reported the source dead")
		}
		report(false) // a second report from the probe changes nothing
		if !g.Dead() {
			t.Fatal("a repeated report revived the gate")
		}
	})

	t.Run("an open gate can still die mid-scan", func(t *testing.T) {
		var g SourceGate
		_, probe := g.Enter(context.Background())
		probe(false)
		ok, report := g.Enter(context.Background())
		if !ok {
			t.Fatal("an open gate refused a caller")
		}
		report(true)
		if ok, _ := g.Enter(context.Background()); ok || !g.Dead() {
			t.Fatal("a key revoked mid-scan did not close the gate")
		}
	})

	t.Run("a cancelled waiter is turned away", func(t *testing.T) {
		var g SourceGate
		g.Enter(context.Background()) // probe that never reports
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if ok, _ := g.Enter(ctx); ok {
			t.Fatal("a cancelled waiter entered")
		}
	})
}
