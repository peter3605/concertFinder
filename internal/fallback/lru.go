package fallback

import (
	"container/list"
	"sync"
)

// lruCache is a small, threadsafe LRU keyed on string. Values are opaque
// interface{}s so both the MusicBrainzClient (string URLs) and the
// VenueGeocoder (cachedGeo struct) can share it. The bound is a soft cap
// on entry count; when Set pushes it over, the least-recently-used entry
// is evicted.
type lruCache struct {
	mu    sync.Mutex
	max   int
	order *list.List
	items map[string]*list.Element
}

type lruEntry struct {
	key string
	val any
}

func newLRU(max int) *lruCache {
	if max <= 0 {
		max = 1000
	}
	return &lruCache{max: max, order: list.New(), items: map[string]*list.Element{}}
}

// Get returns (value, true) on hit, promotes the entry to most-recent.
func (c *lruCache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

// Set inserts or updates. Evicts oldest when over capacity.
func (c *lruCache) Set(key string, val any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*lruEntry).val = val
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&lruEntry{key: key, val: val})
	c.items[key] = el
	for c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest == nil {
			return
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*lruEntry).key)
	}
}
