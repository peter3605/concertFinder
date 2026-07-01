// Package fallback implements the Phase 2 small-artist coverage escalation
// chain described in docs/design.md §5.4. It is only invoked when both
// Ticketmaster and Bandsintown return zero events for a high-affinity artist,
// and it is gated behind PHASE2_FALLBACKS_ENABLED so Phase 1 deployments do
// not scrape by default.
//
// Tier A checks known structured sources (Spotify external_urls cached in
// artist_resolutions, Songkick). Tier B resolves an official URL via Brave
// Search, fetches the homepage plus common tour pages, and extracts
// schema.org MusicEvent entities from JSON-LD blocks.
//
// Scraping etiquette (§5.4.3):
//   - User-Agent identifies the app and provides a contact URL.
//   - robots.txt is fetched (and cached) per host; disallowed paths are skipped.
//   - Per-host rate limit: minimum 3s between requests to the same host.
//   - Fetched pages are cached in the concert_cache table with a "page:" key
//     prefix and a 12h TTL (§5.4.3: 6–24h).
//   - DICE.fm is on a permanent blocklist per their Terms.
package fallback
