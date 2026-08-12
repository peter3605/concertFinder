package http

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONStatus is writeJSON for a non-200 status. The order matters:
// WriteHeader flushes the header map, so setting Content-Type afterwards is
// silently ignored and the client sniffs the body as text/plain.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
