package http

import (
	"net/http"
)

// AASAHandler serves /.well-known/apple-app-site-association, the file iOS
// fetches to verify this domain's claim over its universal links.
//
// Served from Go rather than a Caddy handle block. The Caddyfile is already
// covered by ./scripts/check-deploy-config.sh, and putting the app identifier
// there would split the app's identity across two files that are validated by
// different tooling.
//
// Apple's requirements, all of which are silent failures if broken: served
// over valid TLS, no redirects, Content-Type application/json, and no query
// string. There is deliberately no .json extension on the path.
type AASAHandler struct {
	// AppID is "<TeamID>.<BundleID>".
	AppID string
}

func (h *AASAHandler) Get(w http.ResponseWriter, r *http.Request) {
	// An unconfigured deployment must 404 rather than serve an association
	// naming an empty app. iOS caches this file, and a bad one teaches the
	// device the domain does not belong to the app.
	if h.AppID == "" {
		http.NotFound(w, r)
		return
	}
	// Only the OAuth return path is claimed. Claiming "*" would route every
	// link on the domain into the app, including the privacy and terms pages
	// that App Review opens in a browser.
	writeJSON(w, map[string]any{
		"applinks": map[string]any{
			"details": []map[string]any{
				{
					"appIDs": []string{h.AppID},
					"components": []map[string]any{
						{"/": "/app/auth/callback", "comment": "OAuth return"},
					},
				},
			},
		},
	})
}
