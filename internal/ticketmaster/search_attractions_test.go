package ticketmaster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The picker's search asks for size=200 Music attractions by keyword, and
// decodes what a person needs to choose between same-named entries: the
// primary genre (never "Undefined"), an image, and the upcoming count.
func TestSearchAttractions_RequestAndDecode(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/discovery/v2/attractions.json" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.Query()
		io.WriteString(w, `{"_embedded":{"attractions":[
		  {"id":"K8vLIVE","name":"Disclosure",
		   "images":[{"url":"https://img/wide.jpg","ratio":"16_9","width":2048},{"url":"https://img/small.jpg","ratio":"16_9","width":640},{"url":"https://img/sq.jpg","ratio":"1_1","width":100}],
		   "classifications":[{"primary":false,"genre":{"name":"Undefined"}},{"primary":true,"segment":{"name":"Music"},"genre":{"name":"Dance/Electronic"}}],
		   "upcomingEvents":{"_total":2,"ticketmaster":2}},
		  {"id":"K8vDEAD","name":"Disclosure","classifications":[{"genre":{"name":"Undefined"}}]},
		  {"id":"","name":"No ID"}
		]}}`)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	c := NewClient(&http.Client{Transport: rewriteTransport{base: base, next: http.DefaultTransport}}, "test-key")

	out, err := c.SearchAttractions(context.Background(), "  Disclosure ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("keyword") != "Disclosure" || got.Get("size") != "200" || got.Get("classificationName") != "Music" {
		t.Errorf("query = %v", got)
	}
	if len(out) != 2 {
		t.Fatalf("got %d candidates, want 2 (the ID-less one dropped): %+v", len(out), out)
	}
	live := out[0]
	if live.ID != "K8vLIVE" || live.Genre != "Dance/Electronic" || live.Upcoming != 2 || live.ImageURL != "https://img/small.jpg" {
		t.Errorf("live = %+v", live)
	}
	if out[1].Genre != "" || out[1].Upcoming != 0 {
		t.Errorf("dead = %+v, want no genre and no upcoming", out[1])
	}
}

// By-ID lookup: a 404 and a non-music attraction both mean "no such artist".
func TestGetAttraction_NotFoundAndNonMusic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/discovery/v2/attractions/K8vSEAT.json":
			io.WriteString(w, `{"id":"K8vSEAT","name":"Club Level Seating: Ball Arena","classifications":[{"segment":{"name":"Miscellaneous"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	c := NewClient(&http.Client{Transport: rewriteTransport{base: base, next: http.DefaultTransport}}, "test-key")

	for _, id := range []string{"K8vGONE", "K8vSEAT"} {
		if _, err := c.GetAttraction(context.Background(), id); err != ErrAttractionNotFound {
			t.Errorf("%s: err = %v, want ErrAttractionNotFound", id, err)
		}
	}
}
