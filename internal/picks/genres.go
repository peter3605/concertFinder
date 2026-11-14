package picks

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/peterho/concertfinder/internal/ticketmaster"
)

// GenreCatalogTTL is how long the list of Music genres is reused. Ticketmaster
// has 24 of them and they do not change from one week to the next.
const GenreCatalogTTL = 24 * time.Hour

// ErrUnknownGenre is returned for a genre ID that is not a top-level Music
// genre: a sub-genre, a genre from another segment, or a made-up value.
var ErrUnknownGenre = errors.New("picks: not a top-level music genre")

// GenreLister is the Ticketmaster surface the genre picker needs.
// *ticketmaster.Client satisfies it.
type GenreLister interface {
	MusicGenres(ctx context.Context) ([]ticketmaster.Genre, error)
}

// GenreCatalog serves the pickable genres from one process-wide copy.
//
// It is also what a genre pick is validated against, for the same reason an
// artist pick takes its name from Ticketmaster (Service.Lookup): the genre ID
// becomes a genreId on every listing fetch and the name becomes a facet, so
// neither may come from a client. Offering only this list is what keeps
// sub-genres out -- measured too sparse to be a feed (docs/public-path.md
// §1.2).
type GenreCatalog struct {
	TM GenreLister

	mu     sync.Mutex
	genres []ticketmaster.Genre
	at     time.Time
	now    func() time.Time
}

// Genres returns the catalog, fetching it when the copy is missing or older
// than GenreCatalogTTL. allowFetch is asked before the one upstream call and
// is where the caller charges quota; a refusal returns ErrQuota.
func (g *GenreCatalog) Genres(ctx context.Context, allowFetch func() bool) ([]ticketmaster.Genre, error) {
	g.mu.Lock()
	if g.genres != nil && g.clock().Sub(g.at) < GenreCatalogTTL {
		out := g.genres
		g.mu.Unlock()
		return out, nil
	}
	g.mu.Unlock()

	if allowFetch != nil && !allowFetch() {
		return nil, ErrQuota
	}
	genres, err := g.TM.MusicGenres(ctx)
	if err != nil {
		return nil, err
	}
	if genres == nil {
		genres = []ticketmaster.Genre{}
	}
	g.mu.Lock()
	g.genres, g.at = genres, g.clock()
	g.mu.Unlock()
	return genres, nil
}

// Lookup returns the catalog entry for id, or ErrUnknownGenre.
func (g *GenreCatalog) Lookup(ctx context.Context, id string, allowFetch func() bool) (ticketmaster.Genre, error) {
	genres, err := g.Genres(ctx, allowFetch)
	if err != nil {
		return ticketmaster.Genre{}, err
	}
	for _, x := range genres {
		if x.ID == id {
			return x, nil
		}
	}
	return ticketmaster.Genre{}, ErrUnknownGenre
}

func (g *GenreCatalog) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}
