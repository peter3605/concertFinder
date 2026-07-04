// Package spa embeds the built React SPA and serves it from the Go binary.
// The static/ directory is populated at container build time by
// `npm run build` (see Dockerfile). Locally, only the placeholder
// index.html is present — Vite's dev server on :3000 serves the real SPA
// during development, so this handler only fires when running the built
// binary directly.
package spa

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:static
var embedded embed.FS

// Handler serves the embedded SPA. Requests for real files (JS, CSS, etc.)
// return the file; anything else returns index.html so client-side routing
// works.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		// Programmer error — static/ must exist at compile time.
		panic("spa: embed sub failed: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deny weird paths early.
		clean := path.Clean(r.URL.Path)
		if clean == "/" || clean == "." {
			serveIndex(sub, w)
			return
		}
		trimmed := strings.TrimPrefix(clean, "/")
		if f, err := sub.Open(trimmed); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// No matching file → SPA route, serve index.html so React Router (or
		// window.location changes) can take over.
		serveIndex(sub, w)
	})
}

func serveIndex(sub fs.FS, w http.ResponseWriter) {
	body, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "spa not built", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Prevent caching of index.html so users pick up new bundle hashes.
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(body)
}
