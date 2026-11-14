package ticketmaster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type attractionsResp struct {
	Embedded struct {
		Attractions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"attractions"`
	} `json:"_embedded"`
}

// ResolveAttraction looks up an attractionId for an artist name.
// Returns ("", nil) if no case-insensitive-exact-match attraction is found —
// callers should treat that as a negative resolution and cache it (design §5.2).
func (c *Client) ResolveAttraction(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("keyword", name)
	q.Set("classificationName", "Music")
	q.Set("size", "10")
	q.Set("apikey", c.APIKey)
	u := APIBase + "/attractions.json?" + q.Encode()

	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return "", fmt.Errorf("resolve attraction %q: %w", name, err)
	}
	var out attractionsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode attractions: %w", err)
	}
	target := strings.ToLower(strings.TrimSpace(name))
	for _, a := range out.Embedded.Attractions {
		if strings.ToLower(strings.TrimSpace(a.Name)) == target {
			return a.ID, nil
		}
	}
	return "", nil
}

// AttractionSearchSize is how many attractions one picker search asks for.
// Not ResolveAttraction's 10: common-word names ("Ghost", 309 results; "Low",
// 127) have their real entry outside the first page and inside the first 200,
// and Ticketmaster's ordering does not put an exact match first
// (docs/public-path.md §1.1). One call at 200 costs the same permit as one at
// 10, so the ranking is done on our side over the larger page.
const AttractionSearchSize = 200

// AttractionCandidate is one candidate from a picker search: enough for a
// person to tell two same-named entries apart (genre, upcoming count), and
// everything a stored pick holds.
type AttractionCandidate struct {
	ID       string
	Name     string
	Genre    string
	ImageURL string
	// Upcoming is Ticketmaster's count of upcoming events for the attraction,
	// across all sources it reports.
	Upcoming int
}

// attractionJSON is one attraction object as the Discovery API sends it, the
// same shape in a search page and from the by-ID endpoint.
type attractionJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Images []struct {
		URL   string `json:"url"`
		Ratio string `json:"ratio"`
		Width int    `json:"width"`
	} `json:"images"`
	Classifications []struct {
		Primary bool `json:"primary"`
		Segment struct {
			Name string `json:"name"`
		} `json:"segment"`
		Genre struct {
			Name string `json:"name"`
		} `json:"genre"`
	} `json:"classifications"`
	UpcomingEvents struct {
		Total int `json:"_total"`
	} `json:"upcomingEvents"`
}

type attractionSearchResp struct {
	Embedded struct {
		Attractions []attractionJSON `json:"attractions"`
	} `json:"_embedded"`
}

// SearchAttractions runs one keyword search over Music attractions and
// returns them in Ticketmaster's order. Ranking is the caller's job.
//
// Keyword search has no prefix match and no typo tolerance (measured,
// docs/public-path.md §1.1): "phoe" and "Kruangbin" both return nothing. An
// empty result is therefore an ordinary answer, not an error.
func (c *Client) SearchAttractions(ctx context.Context, query string) ([]AttractionCandidate, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("keyword", query)
	q.Set("classificationName", "Music")
	q.Set("size", fmt.Sprint(AttractionSearchSize))
	q.Set("apikey", c.APIKey)
	u := APIBase + "/attractions.json?" + q.Encode()

	body, _, err := c.doGETRetry(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("search attractions: %w", err)
	}
	var resp attractionSearchResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode attractions: %w", err)
	}
	out := make([]AttractionCandidate, 0, len(resp.Embedded.Attractions))
	for _, a := range resp.Embedded.Attractions {
		if at, ok := a.candidate(); ok {
			out = append(out, at)
		}
	}
	return out, nil
}

// ErrAttractionNotFound is GetAttraction's answer for an ID Ticketmaster does
// not know, or one that is not a music act.
var ErrAttractionNotFound = errors.New("ticketmaster: attraction not found")

// GetAttraction fetches one attraction by ID. It is how a pick learns its
// name and genre when the server has no recent search result holding them:
// the name becomes the artist on every concert the pick finds, and so part
// of the shared dedup_key, so it must be Ticketmaster's and never a client's.
func (c *Client) GetAttraction(ctx context.Context, id string) (AttractionCandidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AttractionCandidate{}, ErrAttractionNotFound
	}
	q := url.Values{}
	q.Set("apikey", c.APIKey)
	u := APIBase + "/attractions/" + url.PathEscape(id) + ".json?" + q.Encode()

	body, status, err := c.doGETRetry(ctx, u)
	if status == http.StatusNotFound {
		return AttractionCandidate{}, ErrAttractionNotFound
	}
	if err != nil {
		return AttractionCandidate{}, fmt.Errorf("get attraction: %w", err)
	}
	var a attractionJSON
	if err := json.Unmarshal(body, &a); err != nil {
		return AttractionCandidate{}, fmt.Errorf("decode attraction: %w", err)
	}
	if !a.isMusic() {
		return AttractionCandidate{}, ErrAttractionNotFound
	}
	at, ok := a.candidate()
	if !ok {
		return AttractionCandidate{}, ErrAttractionNotFound
	}
	return at, nil
}

// isMusic reports whether any classification puts the attraction in the
// Music segment. The search filters on classificationName=Music; the by-ID
// endpoint cannot, so this is the same rule applied after the fact.
func (a attractionJSON) isMusic() bool {
	for _, cl := range a.Classifications {
		if strings.EqualFold(cl.Segment.Name, "Music") {
			return true
		}
	}
	return false
}

func (a attractionJSON) candidate() (AttractionCandidate, bool) {
	if a.ID == "" || strings.TrimSpace(a.Name) == "" {
		return AttractionCandidate{}, false
	}
	at := AttractionCandidate{ID: a.ID, Name: a.Name, Upcoming: a.UpcomingEvents.Total}
	for _, cl := range a.Classifications {
		g := cl.Genre.Name
		// "Undefined" is a real value Ticketmaster sends, and a genre facet
		// reading "Undefined" is worse than none.
		if g == "" || strings.EqualFold(g, "Undefined") {
			continue
		}
		if at.Genre == "" || cl.Primary {
			at.Genre = g
		}
	}
	// Prefer the narrowest 16:9 image, which suits a list row; any image
	// beats none.
	best := -1
	for i, img := range a.Images {
		if img.URL == "" {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		cur := a.Images[best]
		if img.Ratio == "16_9" && (cur.Ratio != "16_9" || img.Width < cur.Width) {
			best = i
		}
	}
	if best >= 0 {
		at.ImageURL = a.Images[best].URL
	}
	return at, true
}
