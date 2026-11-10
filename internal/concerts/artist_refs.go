package concerts

import (
	"encoding/json"
	"fmt"
)

// Artist identity is per viewer, not per show (CF-28, docs/public-path.md §7).
//
// A row in the `concerts` table is shared by every user whose scan produced
// its dedup_key, and DedupKey is built from the artist's *name*, the venue's
// day, the venue and the city -- nothing about whose profile matched it.
// UpsertConcerts is last-writer-wins, so anything viewer-specific written into
// that row is served to every other viewer of it. The artist's ID and genres
// are exactly that: for a Spotify user they are Spotify's, for a manual user
// they are Ticketmaster's, and each is wrong for the other. Silent both ways:
// a manual user reads Spotify-derived data, and a manual scan re-points a
// Spotify user's subscribe bell at an ID their subscriptions do not hold.
//
// So the shared row carries only what the event source said (EncodeSharedRow)
// and each viewer's identity travels separately as ArtistRefs, stored on their
// own snapshot and saves and laid back over the shared rows on read
// (ApplyArtistRefs).

// ArtistRefs is one viewer's artist identity for each dedup_key they hold.
type ArtistRefs map[string]ArtistRef

// sharedArtist is the only part of ArtistRef a shared row may hold. The name
// is not viewer-specific: it is what DedupKey is built from, so every writer
// of a given row already agrees on it up to Normalize.
type sharedArtist struct {
	Name string `json:"name"`
}

// sharedRow shadows Concert.Artist. encoding/json resolves a name clash in
// favour of the shallower field, so the embedded Artist -- ID, genres and all
// -- is not encoded. That makes "the shared row has no artist ID" a property
// of the type rather than of every caller remembering to clear it.
type sharedRow struct {
	Concert
	Artist sharedArtist `json:"artist"`
}

// EncodeSharedRow is the only encoder for a `concerts.data` body. Per-request
// tags (Saved, Subscribed, Reason) are omitempty and never set on a scan's
// output, so they do not reach the row either.
func EncodeSharedRow(c Concert) ([]byte, error) {
	return json.Marshal(sharedRow{Concert: c, Artist: sharedArtist{Name: c.Artist.Name}})
}

// RefsOf collects the viewer-specific artist identity of a scan's results,
// keyed by dedup_key. One dedup_key names one artist (the name is part of the
// key), so there is nothing to merge.
func RefsOf(cs []Concert) ArtistRefs {
	out := make(ArtistRefs, len(cs))
	for _, c := range cs {
		ref := c.Artist
		if len(ref.Genres) > 0 {
			ref.Genres = append([]string(nil), ref.Genres...)
		}
		out[c.DedupKey] = ref
	}
	return out
}

// ApplyArtistRefs lays one viewer's artist identity over shared rows, in
// place. It must run before anything reads Artist.ID or Artist.Genres:
// subscribe tagging, reasons, genre facets and the genre filter.
//
// A row with no ref for this viewer gets *no* ID and no genres -- never the
// row's own. Falling back to the row would reintroduce precisely the leak
// this exists to close, for whichever rows happen to lack a ref; an act with
// no ID is what a discover card already is, and both clients render it.
func ApplyArtistRefs(cs []Concert, refs ArtistRefs) {
	for i := range cs {
		ref, ok := refs[cs[i].DedupKey]
		if !ok {
			cs[i].Artist.ID = ""
			cs[i].Artist.Genres = nil
			continue
		}
		cs[i].Artist.ID = ref.ID
		cs[i].Artist.Genres = append([]string(nil), ref.Genres...)
		if len(cs[i].Artist.Genres) == 0 {
			cs[i].Artist.Genres = nil
		}
		if ref.Name != "" {
			cs[i].Artist.Name = ref.Name
		}
	}
}

// DecodeArtistRefs parses a snapshot's artist_refs column. Empty input is an
// empty set, not an error: a snapshot written before the column existed and
// not reached by its backfill simply has no refs.
func DecodeArtistRefs(raw []byte) (ArtistRefs, error) {
	refs := ArtistRefs{}
	if len(raw) == 0 {
		return refs, nil
	}
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fmt.Errorf("decode artist refs: %w", err)
	}
	return refs, nil
}

// ArtistRefsFromRaw parses per-key refs as the database hands them back
// (dedup_key -> one JSON object). A ref that will not decode is dropped, which
// ApplyArtistRefs then treats as "no identity" -- the safe direction.
func ArtistRefsFromRaw(raw map[string][]byte) ArtistRefs {
	refs := make(ArtistRefs, len(raw))
	for k, b := range raw {
		var r ArtistRef
		if len(b) == 0 || json.Unmarshal(b, &r) != nil {
			continue
		}
		refs[k] = r
	}
	return refs
}
