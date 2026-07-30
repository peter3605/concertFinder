package http

import (
	"net/http"
	"time"
)

// SiteInfoHandler serves GET /api/site-info: small public bootstrap payload
// consumed by the frontend to fill in operator-specific values (contact
// email, effective date) on static content pages. Unauthenticated.
type SiteInfoHandler struct {
	ContactEmail string
	// EffectiveDate is the "as of" date for privacy/terms text. Injected at
	// build/run time rather than hardcoded in the React source so the pages
	// stay honest as content evolves.
	EffectiveDate string
}

func (h *SiteInfoHandler) Get(w http.ResponseWriter, _ *http.Request) {
	effective := h.EffectiveDate
	if effective == "" {
		effective = time.Now().UTC().Format("2006-01-02")
	}
	writeJSON(w, map[string]string{
		"contact_email":  h.ContactEmail,
		"effective_date": effective,
	})
}
