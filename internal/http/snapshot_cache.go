package http

import (
	"container/list"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/peterho/concertfinder/internal/concerts"
)

// SnapshotCache caches the fully-assembled []Concert per (user, location,
// computed_at). Post-normalization (migration 0012) serving GET
// /me/concerts requires a DB batch fetch + N JSON unmarshals + assembly;
// this in-process LRU eliminates that work when the same snapshot is
// requested again before computed_at moves.
//
// The cache is keyed on user+location+computed_at, so it self-invalidates
// when the scan worker writes a new snapshot with a fresh timestamp — no
// pub/sub needed. Cache misses just do the full assembly path.
//
// Threadsafe. Bound is a soft cap; entries above the cap get LRU-evicted.
type SnapshotCache struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[string]*list.Element
}

type snapshotCacheEntry struct {
	key      string
	concerts []concerts.Concert
}

func NewSnapshotCache(max int) *SnapshotCache {
	if max <= 0 {
		max = 200
	}
	return &SnapshotCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

func snapKey(userID uuid.UUID, locationKey string, computedAt time.Time) string {
	return userID.String() + "|" + locationKey + "|" + computedAt.UTC().Format(time.RFC3339Nano)
}

// Get returns the cached assembled concerts for a snapshot, if any.
func (c *SnapshotCache) Get(userID uuid.UUID, locationKey string, computedAt time.Time) ([]concerts.Concert, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[snapKey(userID, locationKey, computedAt)]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*snapshotCacheEntry).concerts, true
}

// Put stores an assembled snapshot. Evicts oldest when over capacity.
func (c *SnapshotCache) Put(userID uuid.UUID, locationKey string, computedAt time.Time, cs []concerts.Concert) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := snapKey(userID, locationKey, computedAt)
	if el, ok := c.items[key]; ok {
		el.Value.(*snapshotCacheEntry).concerts = cs
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&snapshotCacheEntry{key: key, concerts: cs})
	c.items[key] = el
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*snapshotCacheEntry).key)
	}
}
