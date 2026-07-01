# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Status

This repo currently contains **only a design document** (`docs/design.md`). No source code, build system, dependencies, tests, or git history exist yet. There are no commands to run because there is nothing to build. The first implementation work will scaffold the Go backend and React frontend described below.

When asked to implement something, read `docs/design.md` first — it is the authoritative source of truth for architecture decisions, API choices, schema, and phased scope.

## What ConcertFinder Is

A web app that builds a personalized concert feed from a user's Spotify listening data by fanning out across ticketing APIs (Ticketmaster, Bandsintown, later Songkick) for shows by artists the user already engages with. US-only. Phase 1 is single-user local; multi-user AWS deployment is Phase 3.

## Planned Architecture

Three-tier: React+TS+Vite SPA → Go 1.22+ API (chi router) → PostgreSQL 16. The Go backend mediates **all** third-party API access; the frontend never contacts Spotify or ticketing APIs directly. Reasons this is non-negotiable: API keys can't live in browser code, Spotify refresh tokens must be encrypted at rest, and concert search is a server-side fan-out with shared caching and rate-limit pooling.

Planned Go layout (see design §2.3):

```
/cmd/{server,worker}    entrypoints
/internal/spotify       Spotify client + affinity scoring
/internal/ticketmaster  TM client
/internal/bandsintown   BIT client
/internal/concerts      aggregation, dedup, scoring
/internal/auth          PKCE, token storage, refresh middleware
/internal/db            sqlc-generated code
/internal/http          handlers, middleware
/migrations             SQL migrations
/web                    React+TS frontend
/infra                  Terraform (Phase 3)
```

Each external API has its own package with its own types — **do not introduce a shared "models" package**. External schemas drift independently.

## Architectural Constraints That Are Easy to Violate

These come from the design doc and from third-party ToS; getting them wrong has real consequences:

- **No long-term caching of Spotify Content.** Raw listening data (saved tracks, top artists, recently played, etc.) is held in memory only and discarded after profile construction. Only the *derived* affinity profile (artist IDs + scores) is persisted, with a 24-hour TTL. Do not add tables that store raw Spotify response data.
- **No ML training on Spotify data.** This includes embeddings and similarity learning.
- **Refresh tokens are AES-256-GCM encrypted at rest** with a per-token nonce. Key comes from `ENCRYPTION_KEY` env var (Phase 1) or AWS Secrets Manager (Phase 3). Never log or return tokens.
- **Spotify redirect URI must be `https://127.0.0.1:3000/callback`** for local dev — `http://localhost` is rejected by Spotify as of Nov 2025.
- **PKCE flow only.** Implicit Grant is deprecated. Authorization Code without PKCE is not used.
- **Ticketmaster artist resolution is two-stage:** resolve name → `attractionId` via `/discovery/v2/attractions.json`, then query events filtered by that attraction ID. Naive keyword search produces false positives (cover bands, tribute acts). Cache resolutions in `artist_resolutions` indefinitely.
- **Endpoints removed by Spotify (Feb 2026) that are NOT available:** `/recommendations`, `/audio-features`, `/audio-analysis`, `/artists/{id}/related-artists`, `/artists/{id}/top-tracks`, batch `/tracks`. Do not write code that calls these. Affinity is constructed entirely from the user's own explicit signals.
- **`GET /playlists/{id}/items` (Feb 2026 change):** only works for playlists the user owns or collaborates on. Skip merely-followed playlists.
- **Preserve Bandsintown tracking parameters** verbatim in event URLs shown to users — required by their display terms.
- **DICE.fm is excluded** from any scraping/fallback work; their ToS prohibits automated access.
- **Display "Powered by Spotify"** attribution on any UI surface showing Spotify-derived data.
- **AWS portability:** no AWS SDK imports in `/internal`. Secrets come from process env regardless of source (Secrets Manager → ECS env → app). Postgres usage avoids RDS-specific features.

## Affinity Scoring (design §4.3)

Per-artist score combines six weighted signals — followed (1.0), top artists weighted by time range (0.9 × {short=1.0, medium=0.8, long=0.6}), saved albums (0.7), saved tracks (0.5), recently played (0.4), owned playlists (0.2). Top 200 artists are submitted to concert search. These weights are starting values to be tuned during Phase 1 dogfooding — treat them as adjustable, not load-bearing.

## Concurrency Pattern (design §6.1, §8.1)

The "get my concerts" request:

1. Resolve user from session cookie.
2. Load/compute affinity profile (24h cache).
3. Take top-N artists (N=200 Phase 1).
4. Goroutine per artist, gated by **buffered semaphore capacity 10**. Each fans out internally to TM + BIT in parallel.
5. Results stream to a channel; collector goroutine dedupes incrementally.
6. **15-second context deadline** on the HTTP handler; in-flight goroutines cancel via `context.Done()`. All retries share this budget.

Retry policy: HTTP 429 honors `Retry-After` (capped 30s), else exponential backoff with jitter; 5xx exp backoff capped at 3 retries; never retry other 4xx.

## Deduplication (design §6)

```
dedup_key = sha256(normalize(artist) + iso_date(dt) + normalize(venue) + normalize(city))
normalize = lowercase → strip_punctuation → strip leading "the "/"a "/"an " → collapse whitespace
```

Records sharing a key merge into one canonical event with multiple ticket links sorted by source priority: artist's official site → Ticketmaster/Live Nation → Bandsintown (tracking params preserved) → Songkick/other.

## Required Environment Variables (Appendix A)

`SPOTIFY_CLIENT_ID`, `SPOTIFY_REDIRECT_URI`, `TICKETMASTER_API_KEY`, `BANDSINTOWN_APP_ID`, `DATABASE_URL`, `ENCRYPTION_KEY` (32-byte hex), `SESSION_COOKIE_DOMAIN`, `LISTEN_ADDR`. Loaded from `.env` locally; injected from Secrets Manager in Phase 3.

## Phase Discipline

When proposing or implementing work, check which phase it belongs to before expanding scope:

- **Phase 1 (MVP):** PKCE auth, full affinity from all 6 signals, TM + BIT only, semaphore fan-out, dedup, month-grouped list view, **hardcoded location**, Docker Compose. No multi-user, no fallbacks, no filters, no background sync.
- **Phase 2:** Small-artist fallback (Songkick + JSON-LD extraction via Brave Search), location picker, filters, river background jobs, late-result polling. Begin Extended Quota Mode application.
- **Phase 3:** AWS (ECS Fargate + RDS + CloudFront/S3 + Secrets Manager), per-user rate accounting, email notifications (re-auth for `user-read-email`), privacy policy, Terraform in `/infra`.

If a request would pull Phase 2/3 work into Phase 1, flag it rather than silently expanding.
