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
//
// Caching:
//   - Files under /assets/* have content-hashed filenames (Vite output),
//     safe to cache aggressively — one year, immutable.
//   - Everything else (index.html, robots.txt, static images) uses no-cache
//     so users pick up new bundle hashes on the next visit.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		// Programmer error — static/ must exist at compile time.
		panic("spa: embed sub failed: " + err.Error())
	}
	return handlerFor(sub)
}

// handlerFor is Handler's body against an arbitrary filesystem, so the routing
// rules can be tested without a built SPA in static/ — the placeholder there
// holds one file, and the rule that matters most is what happens to the files
// that are absent.
func handlerFor(sub fs.FS) http.Handler {
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
			// Hashed bundle files (Vite emits e.g. /assets/index-Xyz.js) are
			// immutable — anything under /assets/ gets a 1-year cache. Non-
			// hashed static assets served from other paths fall through to
			// the default no-cache behavior.
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// A path that names a file we do not have is a 404, not a route.
		// Answering /robots.txt and /favicon.ico with index.html and a 200
		// tells a crawler the file exists and hands it HTML — and any missing
		// bundle chunk reads to the browser as a syntax error in a script
		// rather than a 404, which is the harder thing to diagnose.
		if looksLikeFile(clean) {
			http.NotFound(w, r)
			return
		}
		// No matching file → SPA route, serve index.html so React Router (or
		// window.location changes) can take over. The router owns what an
		// unknown route renders; this handler cannot tell one from a real one
		// without duplicating the route table.
		serveIndex(sub, w)
	})
}

// looksLikeFile reports whether a path is asking for a document rather than an
// app route. Every route in App.tsx is extension-free, so the extension is the
// whole test — it covers /robots.txt, /favicon.ico and /sitemap.xml without
// naming them. Keep it that way: a route with a dot in it would start 404ing
// silently.
func looksLikeFile(clean string) bool {
	return path.Ext(clean) != ""
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
