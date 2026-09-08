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
//
// localDate is the calendar day at the venue, "2006-01-02". It is a string,
// and taken from the source rather than derived from an instant here, because
// this key is the primary key of the `concerts` table and half of
// user_saved_concerts' -- so anything that can move it silently orphans a
// save and re-notifies the user through user_digest_sent.
//
// This used to format date.UTC(), which moved it twice over. Ticketmaster
// sends dates.start.dateTime in UTC, so a 20:00 Pacific show on the 15th
// keyed to the 16th; and a timeTBA event, which has no dateTime, keyed off
// its localDate until TM published a set time and the key jumped. Passing the
// day in explicitly is what makes both impossible: there is no instant here
// to render in the wrong zone.
func DedupKey(artistName, localDate, venue, city string) string {
	h := sha256.New()
	h.Write([]byte(Normalize(artistName)))
	h.Write([]byte(localDate))
	h.Write([]byte(Normalize(venue)))
	h.Write([]byte(Normalize(city)))
	return hex.EncodeToString(h.Sum(nil))
}

// LocalDay is the venue-local day to key this concert by, and is what every
// call site should pass to DedupKey and EventKey rather than reading the
// field directly.
//
// The fallback carries rows that were persisted before LocalDate existed.
// For those, the instant's own wall clock reproduces the key they were
// already stored under -- a Ticketmaster row written in UTC renders its old
// UTC day -- so an existing snapshot keeps grouping and an existing save
// keeps matching. New rows carry a real LocalDate and get the right answer.
// The alternative, keying an empty day, would fold every date at a venue
// into a single event.
func (c Concert) LocalDay() string {
	if c.LocalDate != "" {
		return c.LocalDate
	}
	return LocalDateOf(c.Date)
}

// LocalDay is the venue-local day for a grouped event, with the same
// fallback as Concert.LocalDay for events assembled from rows persisted
// before LocalDate existed.
func (e Event) LocalDay() string {
	if e.LocalDate != "" {
		return e.LocalDate
	}
	return LocalDateOf(e.Date)
}

// LocalDayTime is LocalDay as a time.Time at midnight, for rendering a
// weekday, month or day-of-month to a person.
//
// Display only -- never key off it. It exists because the digest and the push
// body format a calendar day ("Mon Jan 2") and were formatting the instant
// instead, so an evening show on the US west coast was announced to the user
// under tomorrow's date. Parsing the day back is what keeps that rendering
// independent of whatever zone the instant happens to carry.
func (e Event) LocalDayTime() time.Time {
	if t, err := time.Parse("2006-01-02", e.LocalDay()); err == nil {
		return t
	}
	return e.Date
}

// LocalDateOf renders an instant's own wall clock as a dedup-key date.
//
// For a source that parses RFC3339 with a real offset -- Songkick and the
// JSON-LD extractor both do -- the parsed time already sits in the venue's
// zone, so its own calendar day is the local one. Note the absent .UTC():
// that call is precisely what this package had wrong.
func LocalDateOf(t time.Time) string {
	return t.Format("2006-01-02")
}

// EventKey identifies a show independent of who is playing it: the same
// composite as DedupKey with the artist left out. Two of the user's artists
// on the same night at the same room are one thing to attend.
//
// It takes the venue-local day for the same reason DedupKey does, and the
// day grain is deliberate beyond that: acts at one festival have different
// set times, so a finer grain would split exactly the bills this exists to
// merge. The cost is that a venue string naming a multi-room complex merges
// genuinely separate shows; that is accepted, because the alternative loses
// every festival.
//
// Keying on the UTC day was worse than a wrong label here. A festival whose
// sets straddle local midnight -- a 19:00 and a 23:30 slot on the US west
// coast -- rendered as two different UTC days and so as two separate events,
// which is the grouping bug this key was introduced to fix.
func EventKey(localDate, venue, city string) string {
	h := sha256.New()
	h.Write([]byte(localDate))
	h.Write([]byte(Normalize(venue)))
	h.Write([]byte(Normalize(city)))
	return hex.EncodeToString(h.Sum(nil))
}

// SortLinks sorts a link slice in-place by source priority, then URL.
func SortLinks(links []TicketLink) {
	sort.SliceStable(links, func(i, j int) bool {
		pi, pj := priorityOf(links[i].Source), priorityOf(links[j].Source)
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
		c.DedupKey = DedupKey(c.Artist.Name, c.LocalDay(), c.Venue, c.City)
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
