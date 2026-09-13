package http

import (
	"testing"

	"github.com/peterho/concertfinder/internal/concerts"
)

// The daily seed fetches a fixed radius around each city; this view serves a
// radius the caller chooses, defaulting to DiscoverDefaultRadius. If the seed
// fetches a smaller circle than the view serves, a visitor gets a list
// filtered against events that were never fetched — thinner than it should be,
// with nothing anywhere to say why.
//
// Nothing at compile time relates the two constants, so this is what relates
// them. They live in different packages because jobs cannot import this one.
func TestSeedRadiusCoversTheDiscoverDefault(t *testing.T) {
	if concerts.DiscoverSeedRadiusMiles < DiscoverDefaultRadius {
		t.Fatalf("seed radius %d is smaller than the view's default radius %d: the edge of every seeded city is empty",
			concerts.DiscoverSeedRadiusMiles, DiscoverDefaultRadius)
	}
}
