package concerts

import (
	"context"
	"sync"
	"sync/atomic"
)

// FallbackScanState is what the fallback chain learns during one scan and
// shares across that scan's artists. Search attaches one to the fallback
// context; the chain is a single process-wide value, so anything it must
// remember for "the rest of this scan" has to travel here rather than live
// on the chain.
type FallbackScanState struct {
	// Songkick goes dead for the scan on its first 401/403 (CF-B34).
	Songkick SourceGate
}

type fallbackStateKey struct{}

// WithFallbackScanState returns a context carrying s.
func WithFallbackScanState(ctx context.Context, s *FallbackScanState) context.Context {
	return context.WithValue(ctx, fallbackStateKey{}, s)
}

// FallbackScanStateFrom returns the scan's state, or nil outside a scan.
// Every method on a nil state's gates behaves as "no gate".
func FallbackScanStateFrom(ctx context.Context) *FallbackScanState {
	s, _ := ctx.Value(fallbackStateKey{}).(*FallbackScanState)
	return s
}

// SongkickGate is nil-safe: a nil state yields a nil gate, which admits
// everything.
func (s *FallbackScanState) SongkickGate() *SourceGate {
	if s == nil {
		return nil
	}
	return &s.Songkick
}

// SourceGate stops a scan calling a source that has refused our credential.
//
// The first caller through is a probe: the scan's other artists wait for
// its outcome rather than racing it, which is what makes "one request per
// scan" true with ten artists in flight. If the probe reports the source
// dead, every later Enter returns false. Any other outcome — results, no
// results, a 5xx, a refused permit — opens the gate, and a later caller can
// still report it dead if the key is revoked mid-scan.
//
// Dying is not incompleteness. The fallback is best-effort enrichment, and
// an artist skipped here falls through to the next tier exactly as one
// refused a permit does.
type SourceGate struct {
	mu      sync.Mutex
	settled chan struct{} // nil until the probe starts; closed when it reports
	dead    atomic.Bool
}

// Enter reports whether the caller may call the source. When it returns
// true, the caller must invoke report exactly once, as soon as it knows
// whether the source refused the credential; report(true) marks it dead.
// A caller that waits for the probe and is cancelled is turned away.
func (g *SourceGate) Enter(ctx context.Context) (ok bool, report func(dead bool)) {
	if g == nil {
		return true, func(bool) {}
	}
	g.mu.Lock()
	if g.settled == nil {
		g.settled = make(chan struct{})
		g.mu.Unlock()
		var once sync.Once
		return true, func(dead bool) {
			once.Do(func() {
				if dead {
					g.dead.Store(true)
				}
				close(g.settled)
			})
		}
	}
	settled := g.settled
	g.mu.Unlock()
	select {
	case <-settled:
	case <-ctx.Done():
		return false, func(bool) {}
	}
	if g.dead.Load() {
		return false, func(bool) {}
	}
	return true, func(dead bool) {
		if dead {
			g.dead.Store(true)
		}
	}
}

// Dead reports whether the source refused the credential during this scan.
func (g *SourceGate) Dead() bool {
	return g != nil && g.dead.Load()
}
