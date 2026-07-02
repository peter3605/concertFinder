// Package search implements design §6.1's streaming pattern: /me/concerts
// starts a search that keeps running past the HTTP request, and the
// frontend polls the same endpoint with the returned search ID to pick up
// concerts that completed after the initial 15s window.
//
// Storage is in-process — Phase 2 is single-instance. If we ever run
// multiple API replicas, this needs to move to Postgres.
package search

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/spotify"
)

const (
	// SearchBudget bounds how long a detached search may run past the
	// initiating request. Beyond this, in-flight artist goroutines cancel.
	SearchBudget = 60 * time.Second

	// KeepAfterComplete is how long we retain a finished search so slow
	// clients can still pull the final snapshot.
	KeepAfterComplete = 5 * time.Minute

	// ReapEvery is the sweep interval for the manager's GC goroutine.
	ReapEvery = 30 * time.Second
)

// Manager owns the lifecycle of in-flight searches.
type Manager struct {
	mu       sync.RWMutex
	searches map[uuid.UUID]*Search
	stopOnce sync.Once
	stop     chan struct{}
}

func NewManager() *Manager {
	m := &Manager{searches: map[uuid.UUID]*Search{}, stop: make(chan struct{})}
	go m.reapLoop()
	return m
}

// Shutdown stops the reaper. Existing detached searches finish naturally via
// their own contexts.
func (m *Manager) Shutdown() {
	m.stopOnce.Do(func() { close(m.stop) })
}

// Start kicks off a new search on a detached context bounded by SearchBudget.
// The returned Search's Merger receives concerts as artist goroutines finish.
func (m *Manager) Start(deps concerts.SearchDeps, artists []spotify.ScoredArtist, loc concerts.Location) *Search {
	id := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), SearchBudget)
	s := &Search{
		ID:        id,
		StartedAt: time.Now(),
		Merger:    concerts.NewMerger(),
		done:      make(chan struct{}),
		cancel:    cancel,
	}
	m.mu.Lock()
	m.searches[id] = s
	m.mu.Unlock()

	go func() {
		defer close(s.done)
		defer cancel()
		_ = concerts.StreamSearch(ctx, deps, artists, loc, s.Merger)
		s.finishedAt.Store(time.Now().UnixNano())
	}()
	return s
}

func (m *Manager) Get(id uuid.UUID) (*Search, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.searches[id]
	return s, ok
}

func (m *Manager) reapLoop() {
	t := time.NewTicker(ReapEvery)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reap()
		}
	}
}

func (m *Manager) reap() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.searches {
		// Drop searches that finished long ago, or that have been running past
		// SearchBudget + a small grace (in case cancel raced with completion).
		if s.IsComplete() {
			if now.Sub(s.finishedTime()) > KeepAfterComplete {
				delete(m.searches, id)
			}
		} else if now.Sub(s.StartedAt) > SearchBudget+time.Minute {
			s.cancel()
			delete(m.searches, id)
		}
	}
}
