package db

import (
	"context"
	"testing"
	"time"
)

// ScanCachedConcerts is the whole data path of the signed-out discover view,
// and that view swallows its own failures on purpose: an unreadable cache
// answers 200 with an empty list, because the caller is the first screen a
// stranger sees. Which means a SQL error here is an empty section forever
// with a green build and nothing in a log — exactly the shape this suite
// exists to catch.
func TestScanCachedConcerts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, c := range []struct {
		key  string
		blob string
	}{
		{"tm:artist-one:38.8951,-77.0364,50", `[{"ID":"e1"}]`},
		{"tm:artist-two:38.8951,-77.0364,50", `[{"ID":"e2"}]`},
		{"page:https://example.com/tour", `"<html>"`},
	} {
		if err := SaveCachedConcerts(ctx, pool, c.key, []byte(c.blob)); err != nil {
			t.Fatalf("save %s: %v", c.key, err)
		}
	}

	got, err := ScanCachedConcerts(ctx, pool, "tm:", time.Hour, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want the two tm: rows and not the page: one, got %d: %s", len(got), got)
	}

	// The prefix is the only thing separating cached Ticketmaster payloads
	// from cached HTML, which is a JSON string and would decode to nothing.
	pages, err := ScanCachedConcerts(ctx, pool, "page:", time.Hour, 10)
	if err != nil {
		t.Fatalf("scan pages: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("want the one page: row, got %d", len(pages))
	}

	if got, err := ScanCachedConcerts(ctx, pool, "tm:", time.Hour, 1); err != nil || len(got) != 1 {
		t.Fatalf("limit ignored: got %d rows, err %v", len(got), err)
	}
	// Rows written before the window are invisible: a scan that has not run
	// in a week leaves nothing behind for this view to serve.
	if got, err := ScanCachedConcerts(ctx, pool, "tm:", time.Nanosecond, 10); err != nil || len(got) != 0 {
		t.Fatalf("age window ignored: got %d rows, err %v", len(got), err)
	}
}
