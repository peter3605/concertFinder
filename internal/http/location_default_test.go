package http

import (
	"encoding/json"
	"testing"
)

// The frontend decides whether to ask the browser for coordinates purely from
// is_default, so the flag has to mean exactly one thing: "the user never chose
// this". Getting it backwards in either direction is silent — a fallback that
// omits the flag strands every new user at the operator's configured city, and
// a stored location that carries it re-prompts someone who already picked.
//
// These assert the wire format rather than the handler, because the contract
// that matters is the JSON: a Go bool that never reaches the client as
// `is_default` is not a signal.

func TestFallbackLocationIsMarkedDefault(t *testing.T) {
	b, err := json.Marshal(locationDTO{
		Latitude:    40.7128,
		Longitude:   -74.0060,
		RadiusMiles: 50,
		IsDefault:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["is_default"] != true {
		t.Fatalf("fallback location must serialize is_default=true, got %v in %s", got["is_default"], b)
	}
}

func TestStoredLocationOmitsIsDefault(t *testing.T) {
	b, err := json.Marshal(locationDTO{
		Latitude:    38.8951,
		Longitude:   -77.0364,
		RadiusMiles: 50,
	})
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["is_default"]; present {
		t.Fatalf("a stored location must not carry is_default at all (omitempty), got %s", b)
	}
}
