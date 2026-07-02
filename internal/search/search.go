package search

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/concerts"
)

// Search is one in-flight or recently completed fan-out. Merger is
// thread-safe; readers snapshot it via Merger.All().
type Search struct {
	ID        uuid.UUID
	StartedAt time.Time
	Merger    *concerts.Merger

	done       chan struct{}
	cancel     context.CancelFunc
	finishedAt atomic.Int64 // unix nanos, 0 while running
}

// IsComplete reports whether the fan-out has finished.
func (s *Search) IsComplete() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *Search) finishedTime() time.Time {
	ns := s.finishedAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// WaitFor blocks until the search completes or ctx expires, whichever first.
// Returns whether the search completed.
func (s *Search) WaitFor(ctx context.Context) bool {
	select {
	case <-s.done:
		return true
	case <-ctx.Done():
		return false
	}
}
