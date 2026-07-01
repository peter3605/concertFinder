package fallback

import (
	"testing"
	"time"
)

const tourPageHTML = `
<!DOCTYPE html>
<html><head>
<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "ItemList",
  "itemListElement": [
    {
      "@type": "ListItem",
      "item": {
        "@context": "https://schema.org",
        "@type": "MusicEvent",
        "name": "Test Artist at Bowery Ballroom",
        "startDate": "2026-05-15T20:00:00-04:00",
        "url": "/tour/nyc",
        "location": {
          "@type": "MusicVenue",
          "name": "Bowery Ballroom",
          "address": {
            "@type": "PostalAddress",
            "addressLocality": "New York",
            "addressRegion": "NY",
            "addressCountry": "US"
          }
        }
      }
    },
    {
      "@type": "ListItem",
      "item": {
        "@type": "MusicEvent",
        "name": "Test Artist at 9:30 Club",
        "startDate": "2026-05-20",
        "location": {
          "@type": "MusicVenue",
          "name": "9:30 Club",
          "address": {"@type":"PostalAddress","addressLocality":"Washington","addressRegion":"DC","addressCountry":"US"}
        }
      }
    }
  ]
}
</script>
<script type="application/ld+json">
{"@type": "Product", "name": "not an event"}
</script>
</head><body></body></html>`

func TestExtractMusicEvents_ItemListNested(t *testing.T) {
	got := ExtractMusicEvents([]byte(tourPageHTML), "https://testartist.example/tour", "Test Artist")
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	e1 := got[0]
	if e1.Venue != "Bowery Ballroom" || e1.City != "New York" || e1.State != "NY" {
		t.Errorf("event 1 wrong: %+v", e1)
	}
	if !e1.Date.Equal(time.Date(2026, 5, 15, 20, 0, 0, 0, time.FixedZone("", -4*3600))) {
		t.Errorf("event 1 date wrong: %v", e1.Date)
	}
	// relative /tour/nyc must be absolutized against pageURL
	if e1.Links[0].URL != "https://testartist.example/tour/nyc" {
		t.Errorf("relative URL not resolved: %s", e1.Links[0].URL)
	}
	// event 2 has no url — should fall back to pageURL
	if got[1].Links[0].URL != "https://testartist.example/tour" {
		t.Errorf("fallback URL missing: %s", got[1].Links[0].URL)
	}
}

const graphHTML = `<html><head>
<script type="application/ld+json">
{"@context":"https://schema.org","@graph":[
  {"@type":["Event","MusicEvent"],"name":"X","startDate":"2026-06-01T20:00:00",
   "location":{"@type":"MusicVenue","name":"V","address":{"addressLocality":"LA","addressRegion":"CA"}}}
]}
</script></head></html>`

func TestExtractMusicEvents_GraphAndArrayType(t *testing.T) {
	got := ExtractMusicEvents([]byte(graphHTML), "https://x.example", "A")
	if len(got) != 1 || got[0].City != "LA" {
		t.Fatalf("wrong: %+v", got)
	}
}

func TestExtractMusicEvents_NoEventsReturnsEmpty(t *testing.T) {
	html := `<html><head><script type="application/ld+json">{"@type":"Organization","name":"nope"}</script></head></html>`
	got := ExtractMusicEvents([]byte(html), "https://x", "A")
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}
