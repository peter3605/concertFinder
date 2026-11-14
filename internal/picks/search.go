// Package picks is the artist picker: one Ticketmaster attraction search per
// submitted query, ranked so the artist a person typed is the one they see
// first (docs/public-path.md §1.1).
//
// It lives outside internal/ticketmaster because ranking compares names under
// concerts.Normalize, and internal/concerts already imports ticketmaster.
package picks

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/peterho/concertfinder/internal/concerts"
	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// ResultLimit is how many ranked candidates a search returns.
const ResultLimit = 10

// CacheTTL is how long a query's ranked results are reused. Attraction names
// and IDs do not move day to day; upcoming counts do, slowly, and only order
// ties.
const CacheTTL = 24 * time.Hour

// maxCachedQueries bounds the in-process cache. A query is ten small rows, so
// this is a few megabytes at most.
const maxCachedQueries = 5000

// Result is one candidate shown in the picker.
type Result struct {
	AttractionID string `json:"attraction_id"`
	Name         string `json:"name"`
	Genre        string `json:"genre,omitempty"`
	ImageURL     string `json:"image_url,omitempty"`
	Upcoming     int    `json:"upcoming"`
	// Exact is true when the name matches the query under folding, so a
	// client can mark the likely answer.
	Exact bool `json:"exact"`
}

// Searcher is the Ticketmaster surface the picker needs. *ticketmaster.Client
// satisfies it.
type Searcher interface {
	SearchAttractions(ctx context.Context, query string) ([]ticketmaster.AttractionCandidate, error)
	GetAttraction(ctx context.Context, id string) (ticketmaster.AttractionCandidate, error)
}

// Service answers picker searches through an in-process cache, and remembers
// every attraction it has shown so that adding one of them as a pick needs no
// further call.
type Service struct {
	TM Searcher

	mu    sync.Mutex
	cache map[string]cacheEntry
	// known is every attraction a search returned within CacheTTL, by ID.
	// It is what lets a pick take its name from Ticketmaster rather than
	// from the client (see Lookup).
	known map[string]knownEntry
	now   func() time.Time
}

type knownEntry struct {
	r  Result
	at time.Time
}

type cacheEntry struct {
	results []Result
	at      time.Time
}

// CacheKey is the cache identity of a query: the same search typed with
// different case or spacing is one upstream call.
func CacheKey(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

// Cached returns a query's ranked results when they are inside CacheTTL. The
// HTTP handler asks this first so a repeat search spends no quota.
func (s *Service) Cached(query string) ([]Result, bool) {
	key := CacheKey(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.cache[key]
	if !ok || s.clock().Sub(e.at) >= CacheTTL {
		return nil, false
	}
	return e.results, true
}

// Search makes one upstream call and caches the ranked result. The caller is
// responsible for having charged quota for it.
func (s *Service) Search(ctx context.Context, query string) ([]Result, error) {
	raw, err := s.TM.SearchAttractions(ctx, query)
	if err != nil {
		return nil, err
	}
	ranked := Rank(query, raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]cacheEntry)
	}
	now := s.clock()
	for _, r := range ranked {
		s.rememberLocked(r, now)
	}
	if len(s.cache) >= maxCachedQueries {
		for k, e := range s.cache {
			if now.Sub(e.at) >= CacheTTL {
				delete(s.cache, k)
			}
		}
		// Still full of live entries: drop one arbitrarily. A cache miss
		// costs one call; unbounded growth costs the process.
		for k := range s.cache {
			if len(s.cache) < maxCachedQueries {
				break
			}
			delete(s.cache, k)
		}
	}
	s.cache[CacheKey(query)] = cacheEntry{results: ranked, at: now}
	return ranked, nil
}

// Lookup returns what Ticketmaster says about an attraction: from a recent
// search when one showed it, otherwise by one by-ID call. fetch reports
// whether a call was made, so the caller can charge quota for exactly that.
//
// This is why a pick's name never comes from the client. The name becomes
// the artist on every concert the pick finds, and the artist name is part of
// the shared dedup_key, so a client-chosen name would let one account file a
// show under any artist it liked in the row every other viewer reads.
func (s *Service) Lookup(ctx context.Context, attractionID string, allowFetch func() bool) (r Result, fetched bool, err error) {
	s.mu.Lock()
	e, ok := s.known[attractionID]
	now := s.clock()
	s.mu.Unlock()
	if ok && now.Sub(e.at) < CacheTTL {
		return e.r, false, nil
	}
	if !allowFetch() {
		return Result{}, false, ErrQuota
	}
	a, err := s.TM.GetAttraction(ctx, attractionID)
	if err != nil {
		return Result{}, true, err
	}
	r = Result{AttractionID: a.ID, Name: a.Name, Genre: a.Genre, ImageURL: a.ImageURL, Upcoming: a.Upcoming}
	s.mu.Lock()
	s.rememberLocked(r, now)
	s.mu.Unlock()
	return r, true, nil
}

// ErrQuota is Lookup's answer when the caller declined to pay for a call.
var ErrQuota = errors.New("picks: no quota for an attraction lookup")

func (s *Service) rememberLocked(r Result, now time.Time) {
	if s.known == nil {
		s.known = make(map[string]knownEntry)
	}
	// Bounded like the query cache: ten per query at most.
	if len(s.known) >= maxCachedQueries*ResultLimit {
		for k, e := range s.known {
			if now.Sub(e.at) >= CacheTTL {
				delete(s.known, k)
			}
		}
		for k := range s.known {
			if len(s.known) < maxCachedQueries*ResultLimit {
				break
			}
			delete(s.known, k)
		}
	}
	r.Exact = false // a property of the query, not of the attraction
	s.known[r.AttractionID] = knownEntry{r: r, at: now}
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Rank orders Ticketmaster's candidates for a picker and keeps ResultLimit.
//
//  1. Exact matches under concerts.Normalize, which folds diacritics
//     ("Beyonce" finds "Beyoncé") and punctuation ("Sunn O)))" finds
//     "Sunn O )))"). Ticketmaster does not put these first: "Ghost" has its
//     exact entry outside the first 10 of 309.
//  2. Everything that does not look like a tribute act, ahead of what does.
//     The top three results for "Taylor Swift" are tributes.
//  3. More upcoming events first, which is what separates duplicate exact
//     entries ("Disclosure" has one with 2 events and one with 0).
//  4. Ticketmaster's own order.
func Rank(query string, in []ticketmaster.AttractionCandidate) []Result {
	q := matchKey(query)
	type scored struct {
		r       Result
		tribute bool
		pos     int
	}
	all := make([]scored, 0, len(in))
	for i, a := range in {
		exact := q != "" && matchKey(a.Name) == q
		all = append(all, scored{
			r: Result{
				AttractionID: a.ID,
				Name:         a.Name,
				Genre:        a.Genre,
				ImageURL:     a.ImageURL,
				Upcoming:     a.Upcoming,
				Exact:        exact,
			},
			tribute: !exact && looksLikeTribute(a.Name),
			pos:     i,
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.r.Exact != b.r.Exact {
			return a.r.Exact
		}
		if a.tribute != b.tribute {
			return !a.tribute
		}
		if a.r.Upcoming != b.r.Upcoming {
			return a.r.Upcoming > b.r.Upcoming
		}
		return a.pos < b.pos
	})
	n := min(len(all), ResultLimit)
	out := make([]Result, n)
	for i := range n {
		out[i] = all[i].r
	}
	return out
}

// matchKey is the comparison form of a name. concerts.Normalize reduces a
// name made only of punctuation ("!!!") to nothing, which would make every
// such name "equal"; those compare on their trimmed lowercase instead.
func matchKey(s string) string {
	if n := concerts.Normalize(s); n != "" {
		return n
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// tributeMarkers are words that, in a name that is not an exact match for
// what was typed, almost always mean a tribute or cover act. A real artist
// with one of these in its name still ranks first whenever it is what the
// person searched for, because exact matches are never demoted.
var tributeMarkers = []string{
	"tribute",
	"salute",
	"experience",
	"music of",
	"celebrating",
	"celebration of",
	"remembering",
	"cover band",
	"recreation",
	"re creation",
	"in the style of",
	"legacy of",
}

func looksLikeTribute(name string) bool {
	n := " " + concerts.Normalize(name) + " "
	for _, m := range tributeMarkers {
		if strings.Contains(n, " "+m+" ") {
			return true
		}
	}
	return false
}
