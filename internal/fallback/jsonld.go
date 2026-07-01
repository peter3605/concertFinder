package fallback

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/peterho/concertfinder/internal/concerts"
)

// ExtractMusicEvents walks HTML, pulls JSON-LD blocks, finds every
// schema.org MusicEvent (including nested inside @graph, ItemList,
// ListItem.item) and maps them to concerts.Concert. pageURL is used as a
// fallback ticket URL when the event has none.
func ExtractMusicEvents(htmlBytes []byte, pageURL, artistName string) []concerts.Concert {
	doc, err := html.Parse(strings.NewReader(string(htmlBytes)))
	if err != nil {
		return nil
	}
	var blocks []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			for _, a := range n.Attr {
				if a.Key == "type" && strings.EqualFold(a.Val, "application/ld+json") {
					if n.FirstChild != nil {
						blocks = append(blocks, n.FirstChild.Data)
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)

	var out []concerts.Concert
	for _, block := range blocks {
		out = append(out, parseJSONLDBlock(block, pageURL, artistName)...)
	}
	return out
}

func parseJSONLDBlock(block, pageURL, artistName string) []concerts.Concert {
	block = strings.TrimSpace(block)
	if block == "" {
		return nil
	}
	// JSON-LD blocks can be a single object or a top-level array.
	var out []concerts.Concert
	var raw any
	if err := json.Unmarshal([]byte(block), &raw); err != nil {
		return nil
	}
	findMusicEvents(raw, func(m map[string]any) {
		if c, ok := toConcert(m, pageURL, artistName); ok {
			out = append(out, c)
		}
	})
	return out
}

// findMusicEvents recurses through arbitrary JSON, invoking fn once for
// every object whose @type includes "MusicEvent". A MusicEvent is a leaf —
// we don't recurse into its children, so nested location objects don't
// misfire as further events.
func findMusicEvents(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		if typeMatches(x["@type"], "MusicEvent") {
			fn(x)
			return
		}
		for k, val := range x {
			if k == "@type" || k == "@context" {
				continue
			}
			findMusicEvents(val, fn)
		}
	case []any:
		for _, el := range x {
			findMusicEvents(el, fn)
		}
	}
}

func typeMatches(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return strings.EqualFold(x, want)
	case []any:
		for _, el := range x {
			if s, ok := el.(string); ok && strings.EqualFold(s, want) {
				return true
			}
		}
	}
	return false
}

func toConcert(m map[string]any, pageURL, artistName string) (concerts.Concert, bool) {
	start, ok := parseTime(m["startDate"])
	if !ok {
		return concerts.Concert{}, false
	}
	venueName, city, state, country := extractLocation(m["location"])
	if venueName == "" || city == "" {
		return concerts.Concert{}, false
	}
	ticketURL, _ := m["url"].(string)
	if ticketURL == "" {
		if offers := m["offers"]; offers != nil {
			ticketURL = firstString(offers, "url")
		}
	}
	if ticketURL == "" {
		ticketURL = pageURL
	}
	// Absolutize relative URLs against pageURL.
	if u, err := url.Parse(ticketURL); err == nil && !u.IsAbs() {
		if base, err := url.Parse(pageURL); err == nil {
			ticketURL = base.ResolveReference(u).String()
		}
	}
	c := concerts.Concert{
		Artist:  concerts.ArtistRef{Name: artistName},
		Date:    start,
		Venue:   venueName,
		City:    city,
		State:   state,
		Country: country,
		Links:   []concerts.TicketLink{{Source: concerts.SourceOfficial, URL: ticketURL}},
	}
	c.DedupKey = concerts.DedupKey(c.Artist.Name, c.Date, c.Venue, c.City)
	return c, true
}

func parseTime(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func extractLocation(v any) (venueName, city, state, country string) {
	switch x := v.(type) {
	case map[string]any:
		venueName, _ = x["name"].(string)
		if addr, ok := x["address"].(map[string]any); ok {
			city, _ = addr["addressLocality"].(string)
			state, _ = addr["addressRegion"].(string)
			country, _ = addr["addressCountry"].(string)
		} else if addr, ok := x["address"].(string); ok {
			city = addr
		}
	case []any:
		if len(x) > 0 {
			return extractLocation(x[0])
		}
	}
	return
}

func firstString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok {
			return s
		}
	case []any:
		for _, el := range x {
			if s := firstString(el, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// ProbeTourPaths is the ordered set of common per-artist paths tried when the
// homepage doesn't yield JSON-LD (design §5.4.2).
var ProbeTourPaths = []string{"", "/tour", "/shows", "/live", "/events", "/dates"}
