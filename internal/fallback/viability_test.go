package fallback

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Phase 2 fallback chain costs a globally-serialized 1 req/sec resolver,
// a scan-wide budget, a process-wide concurrency gate, and two cache tables.
// design.md §12.2 asks whether that complexity is earning anything, and the
// honest answer has been "nobody measured." Widening the budget from 60s to
// 120s removed the exhaustion warning and changed the concert count by zero,
// which is suggestive but not an answer: it says the budget wasn't the
// binding constraint, not what is.
//
// This measures the funnel the chain actually walks:
//
//	artists escalated
//	  → MusicBrainz knows an official homepage      (mb_url_cache)
//	    → the site is reachable and lets us in      (robots.txt, DNS, TLS)
//	      → the page carries any JSON-LD at all
//	        → the JSON-LD contains MusicEvent
//	          → the MusicEvents parse into concerts
//
// Every stage but the first is a silent drop in production: an artist whose
// site 404s and an artist whose site publishes nothing both look like "no
// shows found." That is the same class of blindness that let Bandsintown 403
// for months (design §5.3), and it is why this is worth a real number.
//
// Opt-in. It makes live requests to a hundred-odd third-party sites, so it is
// skipped unless a DSN is supplied:
//
//	CF_VIABILITY_DSN='postgres://…' go test ./internal/fallback/ \
//	  -run TestJSONLDViability -v -timeout 30m
//
// Requests go through the production Fetcher, so robots.txt, the blocklist,
// the 3s per-host interval, and the 12h page cache all apply — a second run
// costs almost nothing and reports the same numbers.
func TestJSONLDViability(t *testing.T) {
	dsn := os.Getenv("CF_VIABILITY_DSN")
	if dsn == "" {
		t.Skip("set CF_VIABILITY_DSN to run the fallback viability measurement")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	resolved, negatives, err := loadResolvedHomepages(ctx, pool)
	if err != nil {
		t.Fatalf("load mb_url_cache: %v", err)
	}
	if len(resolved) == 0 {
		t.Fatal("mb_url_cache has no resolved homepages — run a scan first")
	}

	fetcher := NewFetcher(pool, "")
	results := probeAll(ctx, t, fetcher, resolved)
	reportFunnel(t, len(resolved), negatives, results)
}

type probeResult struct {
	artist  string
	site    string
	outcome outcome
	// pathTried is the probe path that produced the outcome, so a site that
	// only publishes on /tour is distinguishable from one that publishes on
	// its homepage.
	pathTried string
	events    int
	detail    string
}

type outcome int

const (
	outcomeBlocked outcome = iota // robots.txt or blocklist said no
	outcomeUnreachable
	outcomeNoJSONLD
	outcomeJSONLDNoEvents
	outcomeEvents
)

func (o outcome) String() string {
	switch o {
	case outcomeBlocked:
		return "blocked (robots/blocklist)"
	case outcomeUnreachable:
		return "unreachable"
	case outcomeNoJSONLD:
		return "reachable, no JSON-LD"
	case outcomeJSONLDNoEvents:
		return "JSON-LD but no MusicEvent"
	case outcomeEvents:
		return "MusicEvent found"
	}
	return "?"
}

func loadResolvedHomepages(ctx context.Context, pool *pgxpool.Pool) (map[string]string, int, error) {
	const q = `SELECT artist_key, official_url FROM mb_url_cache WHERE official_url <> '' ORDER BY artist_key`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, u string
		if err := rows.Scan(&k, &u); err != nil {
			return nil, 0, err
		}
		out[k] = u
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var negatives int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM mb_url_cache WHERE official_url = ''`).Scan(&negatives); err != nil {
		return nil, 0, err
	}
	return out, negatives, nil
}

// probeAll walks each artist's site through ProbeTourPaths exactly as
// tryOfficialSite does. Concurrency is over *distinct hosts* — the Fetcher
// serializes per host at 3s, so parallelism here costs no politeness.
func probeAll(ctx context.Context, t *testing.T, f *Fetcher, sites map[string]string) []probeResult {
	keys := make([]string, 0, len(sites))
	for k := range sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	const workers = 8
	var (
		mu   sync.Mutex
		out  []probeResult
		wg   sync.WaitGroup
		next = make(chan string)
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range next {
				r := probeSite(ctx, f, k, sites[k])
				mu.Lock()
				out = append(out, r)
				done := len(out)
				mu.Unlock()
				if done%20 == 0 {
					t.Logf("probed %d/%d", done, len(sites))
				}
			}
		}()
	}
	for _, k := range keys {
		select {
		case next <- k:
		case <-ctx.Done():
		}
	}
	close(next)
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].artist < out[j].artist })
	return out
}

// probeSite walks *every* path, deliberately not applying the no-JSON-LD
// early exit that tryOfficialSite uses. That exit is justified by this
// measurement, so the measurement has to keep covering the full space —
// otherwise it could never detect the assumption going stale.
func probeSite(ctx context.Context, f *Fetcher, artist, site string) probeResult {
	res := probeResult{artist: artist, site: site, outcome: outcomeUnreachable}
	base, err := url.Parse(site)
	if err != nil {
		res.detail = "unparseable URL"
		return res
	}
	base.Path = strings.TrimRight(base.Path, "/")

	var (
		sawReachable bool
		sawJSONLD    bool
		blocked      bool
		lastErr      string
	)
	for _, p := range ProbeTourPaths {
		u := *base
		u.Path = strings.TrimRight(base.Path, "/") + p
		page, err := f.GetPage(ctx, u.String())
		if err != nil {
			if errors.Is(err, ErrDisallowed) {
				blocked = true
			} else {
				lastErr = truncateStr([]byte(err.Error()), 120)
			}
			continue
		}
		sawReachable = true
		if countJSONLDBlocks(page) > 0 {
			sawJSONLD = true
		}
		// Same call the production chain makes, so "found events" here means
		// the chain would have found them too.
		events := ExtractMusicEvents(page, u.String(), artist)
		if len(events) > 0 {
			res.outcome = outcomeEvents
			res.events = len(events)
			res.pathTried = pathLabel(p)
			return res
		}
	}
	switch {
	case sawJSONLD:
		res.outcome = outcomeJSONLDNoEvents
	case sawReachable:
		res.outcome = outcomeNoJSONLD
	case blocked:
		res.outcome = outcomeBlocked
	default:
		res.outcome = outcomeUnreachable
		res.detail = lastErr
	}
	return res
}

func pathLabel(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func reportFunnel(t *testing.T, resolved, negatives int, results []probeResult) {
	byOutcome := map[outcome][]probeResult{}
	for _, r := range results {
		byOutcome[r.outcome] = append(byOutcome[r.outcome], r)
	}
	looked := resolved + negatives
	pct := func(n, of int) string {
		if of == 0 {
			return "  n/a"
		}
		return fmt.Sprintf("%5.1f%%", 100*float64(n)/float64(of))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== Phase 2 fallback viability ===\n\n")
	fmt.Fprintf(&b, "artists MusicBrainz was asked about   %4d\n", looked)
	fmt.Fprintf(&b, "  no official homepage on MB          %4d  %s\n", negatives, pct(negatives, looked))
	fmt.Fprintf(&b, "  homepage resolved                   %4d  %s\n\n", resolved, pct(resolved, looked))

	for _, o := range []outcome{outcomeEvents, outcomeJSONLDNoEvents, outcomeNoJSONLD, outcomeUnreachable, outcomeBlocked} {
		rs := byOutcome[o]
		fmt.Fprintf(&b, "  %-28s %4d  %s of resolved, %s of asked\n",
			o, len(rs), pct(len(rs), resolved), pct(len(rs), looked))
	}

	hits := byOutcome[outcomeEvents]
	totalEvents := 0
	byPath := map[string]int{}
	for _, r := range hits {
		totalEvents += r.events
		byPath[r.pathTried]++
	}
	fmt.Fprintf(&b, "\nMusicEvent entities extracted         %4d across %d artists\n", totalEvents, len(hits))
	if len(hits) > 0 {
		paths := make([]string, 0, len(byPath))
		for p := range byPath {
			paths = append(paths, p)
		}
		sort.Slice(paths, func(i, j int) bool { return byPath[paths[i]] > byPath[paths[j]] })
		fmt.Fprintf(&b, "which probe path hit:")
		for _, p := range paths {
			fmt.Fprintf(&b, "  %s=%d", p, byPath[p])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "\n-- artists with extractable events --\n")
	for _, r := range hits {
		fmt.Fprintf(&b, "  %-34s %2d events  %s%s\n", r.artist, r.events, r.site, r.pathTried)
	}

	fmt.Fprintf(&b, "\n-- unreachable (each is a silent zero in production) --\n")
	for _, r := range byOutcome[outcomeUnreachable] {
		fmt.Fprintf(&b, "  %-34s %s  %s\n", r.artist, r.site, r.detail)
	}
	if blocked := byOutcome[outcomeBlocked]; len(blocked) > 0 {
		fmt.Fprintf(&b, "\n-- blocked by robots.txt or blocklist --\n")
		for _, r := range blocked {
			fmt.Fprintf(&b, "  %-34s %s\n", r.artist, r.site)
		}
	}

	t.Log(b.String())

	// Not an assertion about the world — a tripwire. Every stage of this
	// funnel fails silently in production, so if a future run finds nothing
	// at all, that should surface as a failure rather than as a quiet log
	// nobody reads.
	if len(hits) == 0 {
		t.Errorf("no artist site yielded a single MusicEvent across %d resolved homepages; "+
			"the JSON-LD tier is contributing nothing", resolved)
	}
}
