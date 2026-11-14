package concerts

// Target is one artist to search for: the unit of work concerts.Search fans
// out over.
//
// It exists so this package never imports internal/spotify. A Spotify user's
// targets come from their affinity profile; a manual-picks user's come from
// artists they chose on Ticketmaster. Keeping the search input free of
// Spotify's types is what makes "the manual path cannot reach Spotify" a
// property the compiler enforces rather than one a reviewer has to notice
// (docs/public-path.md §5). Do not add a Spotify import here to save a
// conversion — `go list -deps ./internal/concerts` must not list it.
type Target struct {
	// ID is the viewer-facing artist identity carried onto every Concert
	// this target produces: a Spotify artist ID for profile targets, a
	// "tm:<attraction>" ID for picks. It also keys artist_resolutions and
	// the per-artist cache, so it must be stable for a given artist.
	ID   string
	Name string
	// Score gates the fallback escalation (SearchDeps.MinFallbackScore).
	Score  float64
	Genres []string
	// AttractionID, when set, is an already-known Ticketmaster attraction:
	// the artist_resolutions lookup and name resolution are skipped and the
	// ID is searched directly. Empty means "resolve by name", which is every
	// profile target.
	AttractionID string
}
