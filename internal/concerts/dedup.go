package concerts

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize applies the design §6 normalization: lowercase, fold diacritics
// (so "Sigur Rós" and "Sigur Ros" collide), strip punctuation, drop a leading
// article, collapse whitespace.
func Normalize(s string) string {
	s = strings.ToLower(s)
	s = foldDiacritics(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		default:
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	out := strings.TrimSpace(b.String())
	for _, art := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(out, art) {
			out = strings.TrimSpace(out[len(art):])
			break
		}
	}
	return out
}

// foldDiacritics decomposes to NFD and drops combining marks so accented
// Latin letters fall back to their ASCII base.
func foldDiacritics(s string) string {
	dec := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(dec))
	for _, r := range dec {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DedupKey composes the design §6 sha256 identifier.
func DedupKey(artistName string, date time.Time, venue, city string) string {
	h := sha256.New()
	h.Write([]byte(Normalize(artistName)))
	h.Write([]byte(date.UTC().Format("2006-01-02")))
	h.Write([]byte(Normalize(venue)))
	h.Write([]byte(Normalize(city)))
	return hex.EncodeToString(h.Sum(nil))
}

// EventKey identifies a show independent of who is playing it: the same
// composite as DedupKey with the artist left out. Two of the user's artists
// on the same night at the same room are one thing to attend.
//
// The date is truncated to a UTC day for the same reason DedupKey does it —
// and note that a finer grain would defeat the purpose here, since acts at
// one festival have different set times. The cost is that a venue string
// naming a multi-room complex merges genuinely separate shows; that is
// accepted, because the alternative loses every festival.
func EventKey(date time.Time, venue, city string) string {
	h := sha256.New()
	h.Write([]byte(date.UTC().Format("2006-01-02")))
	h.Write([]byte(Normalize(venue)))
	h.Write([]byte(Normalize(city)))
	return hex.EncodeToString(h.Sum(nil))
}

// SortLinks sorts a link slice in-place by source priority, then URL.
func SortLinks(links []TicketLink) {
	sort.SliceStable(links, func(i, j int) bool {
		pi, pj := sourcePriority[links[i].Source], sourcePriority[links[j].Source]
		if pi != pj {
			return pi < pj
		}
		return links[i].URL < links[j].URL
	})
}

// Merger accumulates concerts, deduping incrementally. Safe for concurrent
// use — streaming search has a fan-out goroutine calling Add while the HTTP
// handler calls All() for snapshots.
type Merger struct {
	mu    sync.RWMutex
	byKey map[string]*Concert
}

func NewMerger() *Merger {
	return &Merger{byKey: map[string]*Concert{}}
}

// Add merges a candidate into the store. Later adds contribute their ticket
// link to any existing canonical record and enrich empty fields.
func (m *Merger) Add(c Concert) {
	if c.DedupKey == "" {
		c.DedupKey = DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.byKey[c.DedupKey]
	if !ok {
		SortLinks(c.Links)
		copyC := c
		m.byKey[c.DedupKey] = &copyC
		return
	}
	for _, link := range c.Links {
		if !containsURL(existing.Links, link.URL) {
			existing.Links = append(existing.Links, link)
		}
	}
	SortLinks(existing.Links)
	if existing.State == "" {
		existing.State = c.State
	}
	if existing.Country == "" {
		existing.Country = c.Country
	}
	if existing.Latitude == 0 && existing.Longitude == 0 {
		existing.Latitude = c.Latitude
		existing.Longitude = c.Longitude
	}
	if existing.Artist.ID == "" {
		existing.Artist.ID = c.Artist.ID
	}
	if existing.Artist.Name == "" {
		existing.Artist.Name = c.Artist.Name
	}
}

// All returns concerts sorted ascending by date, then artist name. Values —
// including the Links slice — are deep-copied under the read lock so a
// concurrent Add appending to the stored Concert can't race with a caller
// iterating the returned slice.
func (m *Merger) All() []Concert {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Concert, 0, len(m.byKey))
	for _, c := range m.byKey {
		cp := *c
		if len(c.Links) > 0 {
			cp.Links = append([]TicketLink(nil), c.Links...)
		}
		if len(c.Artist.Genres) > 0 {
			cp.Artist.Genres = append([]string(nil), c.Artist.Genres...)
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		return out[i].Artist.Name < out[j].Artist.Name
	})
	return out
}

func containsURL(links []TicketLink, url string) bool {
	for _, l := range links {
		if l.URL == url {
			return true
		}
	}
	return false
}
