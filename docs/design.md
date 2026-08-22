# ConcertFinder

**Spotify-Driven Concert Discovery — Project Proposal & Design Document**

- Version: 1.1
- Status: Phases 1–3 implemented; resynced against the code 2026-08-12
- Geographic Scope: United States

> **Reading this document.** It is the authoritative record of *intent* —
> why the architecture is shaped the way it is, and what the constraints
> are. Where it describes behavior that has since changed in code, the
> section is annotated inline. `CLAUDE.md` is authoritative for what the
> code does *today*; this document is authoritative for why.

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [Architecture](#2-architecture)
3. [Authentication Flow](#3-authentication-flow)
4. [Spotify Integration & Affinity Scoring](#4-spotify-integration--affinity-scoring)
5. [Concert Search Layer](#5-concert-search-layer)
6. [Aggregation & Deduplication](#6-aggregation--deduplication)
7. [Data Storage](#7-data-storage)
8. [Concurrency & Rate Limiting](#8-concurrency--rate-limiting)
9. [Terms of Service Compliance](#9-terms-of-service-compliance)
10. [Phased Roadmap](#10-phased-roadmap)
11. [AWS Architecture (Phase 3 Reference)](#11-aws-architecture-phase-3-reference)
12. [Risks & Open Questions](#12-risks--open-questions)
- [Appendix A: Configuration Reference](#appendix-a-configuration-reference)
- [Appendix B: Initial External Account Setup Checklist](#appendix-b-initial-external-account-setup-checklist)
- [Appendix C: Glossary](#appendix-c-glossary)

---

## 1. Project Overview

ConcertFinder is a web application that derives a personalized concert recommendation feed from a user's Spotify listening data. The core problem it addresses is concert discovery fragmentation: ticketing for live music is spread across Ticketmaster, Live Nation, Bandsintown, individual artist websites, and a long tail of venue-specific platforms. A fan who wants to know which of their favorite artists are touring nearby today must check several places manually.

ConcertFinder consolidates this. It uses the Spotify Web API to construct a comprehensive artist-affinity profile from the user's recently played tracks, top artists and tracks, saved tracks and albums, followed artists, and owned playlists. It then searches multiple ticketing data sources in parallel for shows by those artists in the user's geographic area, deduplicates results, and presents them with direct purchase links.

The initial scope is the United States only. The initial deployment is single-user (local development) with a clear path to multi-user hosted deployment in later phases.

### 1.1 Goals

- Build a personalized concert feed that reflects the breadth of the user's Spotify activity, not just one signal.
- Search multiple ticketing sources per artist to maximize coverage, including small and indie venues where feasible.
- Present results with direct links to the original ticket source for purchase.
- Design for eventual multi-user hosted deployment without overbuilding in Phase 1.
- Respect all third-party API terms of service, including Spotify's prohibitions on long-term content caching and machine learning training.

### 1.2 Non-Goals

- Ticket purchase flow. ConcertFinder links out to the source for purchase; it does not handle transactions, accounts, or payments.
- Exhaustive coverage of every venue. Truly small DIY venues that publish only to social media or their own websites are outside the addressable surface for an API-driven approach.
- International coverage. Phase 1–3 are US-only. International support is a possible Phase 4+ direction but is not designed for in this document.
- Music recommendations or discovery beyond what Spotify already reflects. The system surfaces concerts for artists the user already engages with.
- Real-time ticket pricing or seat availability. Aggregator APIs do not consistently expose this, and ticket purchase happens on the source site.

---

## 2. Architecture

ConcertFinder is a three-tier application: a React single-page frontend, a Go backend API, and a PostgreSQL database. The backend mediates all third-party API access; the frontend never contacts Spotify or ticketing APIs directly.

### 2.1 Stack

| Layer | Technology | Rationale |
|---|---|---|
| Frontend | React + TypeScript + Vite | Lightweight SPA; backend is Go so Next.js features are unused. |
| Backend | Go 1.25 (chi router) | I/O concurrency for fan-out across ticket APIs; single static binary; strong typing. |
| Database | PostgreSQL 16 | Mature, portable across hosting providers; no provider-specific features used. |
| DB access | pgx + sqlc | Type-safe SQL generation without ORM weight. |
| Background jobs | river (Postgres-backed) | No Redis dependency until volume justifies it. |
| Local dev | Docker Compose | One command to bring up the full stack. |
| Logging | log/slog (stdlib) | Structured JSON logging, stdlib since Go 1.21. |
| Hosting (Phase 3) | AWS EC2 t4g.small + Neon Postgres | Single instance; see §10.3 and §11. RDS db.t4g.micro was the original database and became the largest line on the bill at ~$14/mo. ECS Fargate was the original compute sketch and is deferred (§11.3). |

### 2.2 Why a Backend (and not browser-only)

A backend-first design is mandatory rather than optional, for the following reasons:

- Third-party API keys for Ticketmaster and other paid or rate-limited services cannot live in browser code.
- Spotify refresh tokens are long-lived and must be stored encrypted at rest. Browser storage is unsuitable.
- Concert search is a fan-out across many APIs per request. Server-side coordination enables shared caching, rate-limit pooling, and progressive result streaming.
- Multi-user support in later phases requires server-side session management and per-user data isolation.

### 2.3 Project Layout

The Go backend follows a standard layered structure with feature-oriented internal packages:

```
/cmd
  /server         main.go for the API; river workers run in-process
/internal
  /spotify        Spotify client and types
  /affinity       affinity scoring over Spotify signals
  /ticketmaster   Ticketmaster client, types
  /fallback       Phase 2 chain: MusicBrainz, JSON-LD, Songkick, geocoding turnstiles
  /concerts       aggregation, dedup, event grouping, filters, search fan-out
  /geocoding      shared distance math and place normalization
  /auth           PKCE handshake, token storage, refresh middleware, rate limiting
  /db             pgx queries, migrations, caches
  /http           HTTP handlers, middleware
  /jobs           river job args, workers, and wall-clock schedules
  /rate           per-user daily API quota ledger
  /email          digest and instant-notify rendering, SMTP sender
  /config         env parsing and cross-setting invariants
/migrations       SQL migration files
/web              React + TS frontend
/infra            Terraform definitions (Phase 3)
```

Each external API has its own dedicated package with its own types. Shared "models" packages are deliberately avoided; external API schemas drift independently and should not couple to one another.

Two departures from the original sketch, both from Phase 3:

- **There is no `/cmd/worker`.** River workers are registered in the same
  process as the API server. A separate worker binary would double the
  deploy surface and the database connection count for a workload that idles
  most of the day; splitting it later is a `main.go` change, since the
  workers already take their dependencies as struct fields. On Neon that
  second process would also double the LISTEN/NOTIFY connections against a
  0.25 CU compute.
- **`/internal/bandsintown` was deleted** (see §5.3).

---

## 3. Authentication Flow

ConcertFinder uses the Spotify Authorization Code with PKCE flow. PKCE is selected over the plain Authorization Code flow for several reasons:

- It works identically for web, mobile, and desktop clients, supporting future client expansion without flow changes.
- It avoids any need to transmit a client secret.
- Spotify's PKCE flow returns refresh tokens, so there is no functional loss versus plain Authorization Code.
- Implicit Grant has been deprecated since November 27, 2025 and is no longer an option.

### 3.1 Configuration

The application must be registered at `https://developer.spotify.com/dashboard` before first use. The required configuration values are:

| Item | Value |
|---|---|
| Spotify Client ID | `[TODO: pending registration]` |
| Redirect URI (dev) | `https://127.0.0.1:3000/api/auth/callback` |
| Redirect URI (prod) | `https://[TODO: production domain]/callback` |
| Allowed scopes | See section 3.6 |

Note: `http://localhost` redirect URIs are explicitly prohibited by Spotify as of the November 2025 OAuth migration. Only `http://127.0.0.1` (with the literal IP) is accepted for local development.

### 3.2 Flow Diagram

```
Browser                Go API                  Spotify
   |                      |                        |
   |--/api/auth/login---->|                        |
   |                      |  generate verifier,    |
   |                      |  challenge, state      |
   |                      |  stash in session      |
   |<--302 redirect-------|                        |
   |---authorize?code_challenge=...--------------->|
   |                      |                        |
   |   (user consents)                             |
   |                      |                        |
   |<--302 /callback?code=...&state=...------------|
   |--GET /callback?code=...&state=...->|          |
   |                      | verify state           |
   |                      | POST /api/token        |
   |                      |  (code + verifier)---->|
   |                      |<--access + refresh-----|
   |                      | encrypt refresh token  |
   |                      | persist to DB          |
   |                      | issue session cookie   |
   |<--302 / + cookie-----|                        |
```

### 3.3 PKCE Mechanics

1. On `/api/auth/login`, the backend generates a 64-byte random code verifier, computes its SHA-256 challenge, and stores both the verifier and a random CSRF state value in a short-lived server-side session (cookie-keyed, 10 minute expiry).
2. The backend redirects the browser to `https://accounts.spotify.com/authorize` with the challenge, state, requested scopes, and redirect URI.
3. Spotify presents the consent screen to the user and, on approval, redirects back to the configured callback URL with an authorization code and the original state value.
4. The backend verifies the returned state matches the stored value, exchanges the code plus the original verifier at the Spotify token endpoint, and receives an access token (1 hour TTL) and refresh token.
5. The refresh token is encrypted using AES-GCM with a key from environment configuration, stored in the database, and the in-memory verifier is discarded.
6. A session cookie is issued to the browser. The browser never sees any Spotify token.

### 3.4 Token Refresh

Every outbound Spotify call is wrapped in middleware that ensures the access token is fresh. On expiry, the middleware exchanges the stored refresh token for a new access token. Spotify occasionally rotates the refresh token in the response; when this occurs, the new refresh token must be persisted, replacing the old one. Failure to handle this case results in eventual auth failure after rotation.

### 3.5 Session Management

Browser sessions use server-side state keyed by an opaque random session ID stored in an HttpOnly, Secure, SameSite=Lax cookie. The cookie name is `cf_session`. The session row in the database holds the user ID, creation time, and last-seen time. JWT-based sessions were considered and rejected: server-side sessions are simpler to revoke, easier to debug, and the marginal database read per request is negligible.

### 3.6 Required Scopes

Following the minimum-scope principle, ConcertFinder Phase 1 requests only the scopes required for affinity profile construction:

| Scope | Endpoints Enabled |
|---|---|
| `user-read-recently-played` | GET /me/player/recently-played |
| `user-top-read` | GET /me/top/artists, GET /me/top/tracks |
| `user-library-read` | GET /me/tracks, GET /me/albums |
| `user-follow-read` | GET /me/following?type=artist |
| `playlist-read-private` | GET /me/playlists, GET /playlists/{id}/items |

Notably *not* requested in Phase 1: `user-read-email` and `user-read-private`. These will be added in Phase 3 when email notifications are introduced, triggering a re-authorization prompt.

---

## 4. Spotify Integration & Affinity Scoring

### 4.1 API Surface

Affinity profile construction draws from six Spotify data sources. All are verified against the current OpenAPI specification and the February 2026 Web API migration changelog.

| Source | Endpoint | Pagination |
|---|---|---|
| Recently played | `GET /me/player/recently-played?limit=50` | Cursor (before); 50 max total |
| Top artists | `GET /me/top/artists?time_range={range}&limit=50` | Offset; pull all 3 time ranges |
| Top tracks | `GET /me/top/tracks?time_range={range}&limit=50` | Offset; pull all 3 time ranges |
| Saved tracks | `GET /me/tracks?limit=50` | Offset; full pagination |
| Saved albums | `GET /me/albums?limit=50` | Offset; full pagination |
| Followed artists | `GET /me/following?type=artist&limit=50` | Cursor (after) |
| Owned playlists | `GET /me/playlists` then `GET /playlists/{id}/items` | Offset; both |

A critical behavioral change introduced in February 2026: `GET /playlists/{id}/items` now only returns items for playlists the user owns or collaborates on. Items of merely-followed playlists are no longer accessible. The affinity pipeline must skip playlists where the user is not the owner or a collaborator.

Artist metadata (genres, canonical name, image URL) is retrieved via `GET /artists/{id}` on demand, one call per unique artist. The legacy batch endpoint `GET /artists` is deprecated and may be removed; it is not relied on.

### 4.2 Removed and Deprecated Endpoints

The following endpoints were considered and explicitly excluded due to deprecation or removal:

| Endpoint | Status | Implication |
|---|---|---|
| `/recommendations` | Removed (Nov 2024 / Feb 2026) | Cannot use Spotify's recommendation engine. |
| `/audio-features`, `/audio-analysis` | Removed | Cannot use Spotify audio features for affinity scoring. |
| `/artists/{id}/related-artists` | Removed | Cannot expand affinity via Spotify-computed similarity. |
| `/artists/{id}/top-tracks` | Removed | Not needed for this app. |
| `/tracks` (batch) | Removed | Track details must be fetched individually if ever needed. |

This is significant: ConcertFinder cannot rely on any Spotify-provided "users who like X also like Y" expansion. The affinity profile is constructed entirely from the user's own explicit signals.

### 4.3 Affinity Scoring

Each unique artist receives a score combining frequency and source weight. The weights below are starting values to be tuned during Phase 1 dogfooding:

```
score(artist) =
    1.0 * count_in_followed
  + 0.9 * weighted_top_artists       # short=1.0, medium=0.8, long=0.6
  + 0.7 * count_in_saved_albums
  + 0.5 * count_in_saved_tracks
  + 0.4 * count_in_recently_played
  + 0.2 * count_in_owned_playlists
```

Rationale for weighting:

- Followed artists are an explicit user action and carry the highest per-occurrence weight.
- Top artists across multiple time ranges represent Spotify's own computed affinity. Short-term is weighted highest as it reflects current taste.
- Saved albums (committing to a full work) is weighted above saved tracks (saving a single song).
- Recently played is weighted moderately. It is the most volatile signal and the rolling window is only 50 items.
- Playlist appearances are weighted lowest. Playlist composition is heterogeneous and includes one-off additions.

After scoring, artists are sorted descending and capped (Phase 1: top 200) before being submitted to the concert search layer. This bound is essential for staying within ticket-API rate limits.

### 4.4 ToS-Compliant Caching

Spotify's Developer Terms prohibit long-term caching of Spotify Content and prohibit using Spotify data to train machine learning models. ConcertFinder's approach:

- Listening data (saved tracks, top artists, etc.) is never persisted to the database. It is held in memory for the duration of profile construction and discarded.
- The derived affinity profile (artist IDs, derived scores, derived genres) is cached for 24 hours and recomputed on expiry.
- Artist display data (name, image) shown in the UI is passed through from the computation, not stored long-term.
- No model training of any kind on Spotify-derived data.
- All UI surfaces displaying Spotify-sourced data include the "Powered by Spotify" attribution per the Developer Terms.

---

## 5. Concert Search Layer

For each artist on the curated affinity list, ConcertFinder fans out to concert data sources in parallel. The fan-out is bounded by a semaphore to respect rate limits, with context deadlines so a slow source cannot hang the scan.

### 5.1 Sources

| Source | Endpoint | Coverage | Auth |
|---|---|---|---|
| Ticketmaster Discovery | `GET /discovery/v2/events.json` | Ticketmaster + Live Nation network; most large tours | API key (free, instant signup) |
| Phase 2 fallback chain | MusicBrainz → artist site JSON-LD | Long tail; whatever the artist publishes themselves | None (MusicBrainz is open) |
| Songkick (Phase 2, unused) | `GET api.songkick.com/api/3.0/...` | Variable indie coverage | API key — never obtained; tier is skipped without `SONGKICK_API_KEY` |

**Ticketmaster is the sole primary source.** The design originally paired it
with Bandsintown so that a miss by one was covered by the other; that
redundancy no longer exists (§5.3). The practical consequence is that the
Phase 2 fallback chain went from a long-tail supplement to the *only*
secondary source, which is why its budget and concurrency are tuned as
carefully as they are in §5.4.4.

### 5.2 Ticketmaster Resolution Pattern

Naively keyword-searching events by artist name produces false positives (cover bands, tribute acts, artists with shared names). The correct pattern is two-stage:

1. Resolve the artist name to a Ticketmaster `attractionId` via the `/discovery/v2/attractions.json` endpoint. The result is stable per-artist and worth caching indefinitely (with periodic refresh).
2. Query `/discovery/v2/events.json` filtered by the resolved attraction ID, plus `latlong`, `radius`, and `classificationName=Music`.

Free tier limits are 5 requests/sec and 5,000 requests/day. With a 200-artist list this allows roughly 25 full refresh cycles per day before throttling. Per-artist resolution is cached in the `artist_resolutions` table (see section 7).

**Resolution caching is asymmetric, and deliberately so.** A positive
resolution (name → `attractionId`) is kept forever; a negative one expires
after `concerts.NegativeResolutionTTL` (30 days). Resolution requires an exact
name match, and an unsigned artist can sign to Ticketmaster at any time — a
permanent negative would exclude them silently and forever, with no error and
no log line. The same asymmetry applies to every negative cache in the system;
see §7.3.

### 5.3 Bandsintown (removed)

*Historical. Bandsintown was a primary source in the original design and
through Phases 1–2. It was removed in Phase 3 (migration 0015) and no client
remains in the tree.*

Bandsintown's public API accepts an `app_id` query parameter (any chosen
string, used for attribution) and takes the artist name as a path parameter.
Its coverage is artist-driven — artists submit their own tour dates — which is
why it was expected to be stronger than Ticketmaster for smaller and DIY acts.

**What actually happened:** every request returned HTTP 403 with an AWS
`explicit deny in an identity-based policy` body, for the entire period the
integration was wired up. A partnership-program request went unanswered. The
integration therefore contributed zero results while costing one call per
artist per scan and a slot in the per-user quota ledger — a source that looked
live in the code and in the logs, and was not.

**If a future phase reconsiders it:** confirm the API answers a real request
before writing a client. The failure mode here was not that Bandsintown was
unavailable, it was that unavailability was indistinguishable from "this artist
has no shows," so the source was believed to be working for months.

Retiring a source does not retire its data: `concerts.data` blobs written
before removal still carry `"source":"bandsintown"` ticket links until the
janitor prunes those events, so the `Source` constant and its display label
outlive the client. See §6 for why the priority table must keep an entry for it.

### 5.4 Small-Artist Fallback (Phase 2)

When the primary source returns zero results for a high-affinity artist (above `PHASE2_MIN_SCORE`), ConcertFinder escalates to a layered fallback. This is Phase 2 work; Phase 1 omits it entirely.

**Escalation requires a real miss, not just an empty result.** If Ticketmaster
was skipped because the user's daily quota ran out, the artist is *not*
escalated. Treating a quota refusal as "no results" pushes the artist into the
far more expensive fallback chain — spending more precisely because we were
trying to spend less. This is why `rate.Allow` returns a distinguishable
`errRateCapped` rather than an empty slice.

#### 5.4.1 Tier A: Structured Fallbacks

1. Check the artist's Spotify `external_urls` for an official site URL.
2. Query Songkick API for the artist by name; if results, return them.

#### 5.4.2 Tier B: URL Resolution and JSON-LD Extraction

1. Resolve the artist's official homepage URL. The default resolver is **MusicBrainz** (`/ws/2/artist` search → `/ws/2/artist/{mbid}?inc=url-rels` → filter for `type == "official homepage"`) — free, no API key, ToS-clean, and community-maintained coverage that skews well toward long-tail artists. A Brave Search implementation is retained as a plug-in alternative behind `BRAVE_SEARCH_API_KEY`; if that env var is set the chain uses Brave instead. Resolutions are cached by normalized artist name in `mb_url_cache`, shared across every user and every restart — a hit costs a DB read instead of a slot in a 1 req/sec global turnstile. Positives are kept indefinitely; negatives expire after `db.NegativeMBURLTTL` (30 days), because MusicBrainz is a user-edited database that gains URL relationships continuously.
2. Fetch the artist's homepage and any common tour-page paths (`/tour`, `/shows`, `/live`). Look for `<script type="application/ld+json">` blocks containing schema.org `MusicEvent` entities. Many artist sites (Squarespace, Bandzoogle templates) publish these automatically for SEO.
3. If no structured data is found, surface a prefilled Google search link to the user as the terminal fallback. Do not build heuristic HTML parsers; the maintenance burden is unjustifiable.

**Why not use an LLM for URL resolution.** Direct LLM lookup (`"what's X's website?"`) hallucinates plausible-but-wrong URLs, has a training cutoff that misses new artists, and has the worst coverage on precisely the long-tail artists this tier exists for. LLM-driven scraping ("computer use") is still scraping — same ToS and WAF concerns as before. MusicBrainz's structured URL relationships give us verifiable data at $0.

#### 5.4.3 Scraping Etiquette

- User-Agent identifies the application and provides a contact URL.
- Respect robots.txt for every fetched host (`github.com/temoto/robotstxt` for Go).
- Per-domain rate limit: minimum 3 seconds between requests to the same host.
- Aggressive caching: 6–24 hour TTL on fetched pages.
- DICE.fm is explicitly excluded as their Terms of Service prohibit automated access.

#### 5.4.4 Budgeting the Chain (Phase 3)

The chain's two resolvers — MusicBrainz and Nominatim — each impose a hard
1 req/sec/IP limit, enforced client-side by a single process-wide turnstile
per resolver. Everything below follows from that one fact.

- **Cost scales with escalating artists, not with parallelism.** Adding
  goroutines buys nothing when every lookup queues behind the same turnstile.
  A cold 200-artist profile measured ~250s of MusicBrainz plus ~86s of
  Nominatim against a 300s `ScanBudget`; unbounded, the fallback consumes the
  entire scan and starves the Ticketmaster fan-out that produces most results.
- **The chain gets its own scan-wide deadline** (`PHASE2_FALLBACK_BUDGET_SECONDS`,
  default 120s), shared by every artist in the scan. A *per-artist* cap is not
  a cap: multiplied by 200 artists it bounds nothing.
- **Running out of budget is logged, not counted as scan incompleteness.**
  Flagging it would mark `complete = false` on nearly every cold scan, which
  the SWR handler reads as permanently stale and converts into a rescan loop
  (§6.0). The trade is real and worth naming: a partially-escalated scan is
  silently partial.
- **The budget is per scan; the turnstiles are per process.** N concurrent
  scans split one resolver's throughput while each still holds a full budget,
  so coverage thins as users are added — ~55 lookups solo becomes ~11 each at
  five-way concurrency, with no error and no log.
  `PHASE2_FALLBACK_CONCURRENCY` (default 1, matching the turnstiles) admits a
  bounded number of scans to the chain; the rest skip it and log *why*,
  distinctly from budget exhaustion. Skipping is acceptable because resolver
  results persist in `mb_url_cache` / `venue_geo_cache` and scans repeat
  nightly, so coverage accrues across runs.
- **Anything enforcing a rate limit must be cancellable.** The turnstile is a
  capacity-1 channel, not a `sync.Mutex`: `Lock()` is not context-aware, so
  200 artists queued behind a ~1.1s-per-holder mutex could not be released by
  the scan deadline at all. Observed before the fix: a 5-minute job that ran
  for 978 seconds.

#### 5.4.5 Measured Viability

Raising the budget from 60s to 120s eliminated the `artists_not_escalated=34`
exhaustion (a sixth of a 200-artist profile getting no secondary lookup) but
did not change the number of concerts found — so the budget was not the
binding constraint. `TestJSONLDViability` measures what is. Against 114
artists that reached the chain (one real profile, 2026-08-12):

| Stage | Count | Of asked |
|---|---:|---:|
| MusicBrainz asked | 114 | 100% |
| …no official homepage | 23 | 20.2% |
| …homepage resolved | 91 | 79.8% |
| resolved → site unreachable (dead domain, 404, 403) | 12 | 10.5% |
| resolved → reachable, **no JSON-LD at all** | 40 | 35.1% |
| resolved → JSON-LD present, no `MusicEvent` | 32 | 28.1% |
| resolved → **`MusicEvent` extracted** | **7** | **6.1%** |

Three things follow, and only the first was expected:

1. **The tier converts about 1 artist in 16.** Seven artists yielded 86
   `MusicEvent` entities before radius filtering. That is not nothing — those
   are shows no other source in the system would have found — but it sets the
   ceiling on what any amount of budget tuning can buy.
2. **Every one of the seven hits came from the homepage.** `/tour`, `/shows`,
   `/live`, `/events`, and `/dates` contributed zero. For the 84 artists that
   produced nothing, the chain was fetching six pages each, serialized behind
   the 3s per-host interval and billed to the scan-wide budget. This is now
   cut short: a homepage with no JSON-LD blocks at all ends the probe, since
   no site in that group produced an event on any path. Sites that publish
   *some* structured data still get the full walk, because they are the ones
   plausibly one template change away from publishing events.
3. **12 "resolved" homepages are dead.** MusicBrainz URL relationships are not
   garbage-collected, so it returns domains that no longer resolve
   (`macmillerofficial.com`), 404, or 403. These currently cost a full
   six-path probe each and are cached as a positive resolution *forever*
   (§7.3) — the URL resolved, so nothing marks it bad. Worth revisiting: a
   positive that has never once been fetchable is functionally a negative.

The tier's cost is dominated by artists it will never convert, which is why
the funnel is worth re-measuring rather than assumed. `TestJSONLDViability` is
opt-in (`CF_VIABILITY_DSN`) and reuses the production fetcher and extractor,
so its numbers are the chain's numbers, not a re-implementation's.

---

## 6. Aggregation & Deduplication

The same concert appears in multiple sources. A user-facing list with duplicates is unusable. Deduplication uses a normalized composite key:

```
dedup_key = sha256(
    normalize(artist_name)
  + iso_date(event_datetime)
  + normalize(venue_name)
  + normalize(city)
)

normalize(s) = lowercase(s)
             |> strip_punctuation
             |> remove_leading("the ", "a ", "an ")
             |> collapse_whitespace
```

Records sharing a dedup key are merged into a single canonical event with multiple ticket links. Source priority for the canonical record's presentation (e.g. headline link, image source):

1. Artist's own official site (when surfaced via the Phase 2 fallback)
2. Ticketmaster / Live Nation network link
3. Songkick or other aggregator links
4. Anything unrecognized, last

All discovered ticket links are presented to the user, sorted by the priority above. This serves two purposes: it gives the user choice of vendor (which can affect price and fees), and it provides graceful degradation if any one link is broken or sold out.

**Unknown sources sort last, explicitly.** `priorityOf` returns a large
sentinel for a source missing from the table rather than letting a bare map
lookup yield the zero value — which is numerically *better* than
Ticketmaster's 2 and would promote unrecognized links to the top of every
card. This is also why the retired Bandsintown constant keeps its table entry
(§5.3): stored links outlive their client, and deleting the entry would
silently reorder every card holding one.

### 6.0 Snapshot / SWR Pattern (Phase 3)

The synchronous streaming pattern below was superseded during Phase 3 by a
stale-while-revalidate design against `user_concert_snapshots`. GET
`/api/me/concerts` returns the last completed scan immediately and enqueues
a background refresh when the snapshot is stale. The fan-out itself now runs
inside the river `ScanConcerts` worker, with the same §8.1 concurrency
(semaphore=10, per-job budget=5min). See `CLAUDE.md > SWR read pattern` for
the current handler flow.

**A snapshot is stale when it is missing, older than
`SNAPSHOT_STALE_AFTER_HOURS` (default 6), or marked `complete = false`.** That
third condition is what keeps partial scans visible. `concerts.Search` returns
an `*IncompleteError` alongside its partial results when artists were skipped
or a source hit its quota; the worker persists what it found — a half-filled
page beats an empty one — but records the incompleteness. Stamping
`computed_at = now()` on a truncated scan and calling it success makes it
indistinguishable from a complete one, and it is then trusted for the full
staleness window.

**Quota exhaustion is a wait, not a retry.** A scan that ran out of daily
quota cannot do better until the rate ledger's UTC day rolls over, so the
worker records `retry_after` on the snapshot and returns success rather than
erroring. The SWR handler refuses to enqueue before that instant and reports
`refreshing: false`. Both halves are required: without them, `complete = false`
means "permanently stale," and the frontend's 10s poll re-enqueues an
impossible scan every 10 seconds for the rest of the day while river
separately retries it. Incompleteness caused by *skipped artists* still
retries normally — time alone does not fix that one.

**One scan per (user, location) at a time**, expressed as args-level
`ByArgs` + `ByState` uniqueness over every non-terminal state. Never as
`ByPeriod`: a scan takes ~60s, so a 30s window lapses mid-run and a second
scan starts underneath the first. Because a scan reserves the user's whole
daily quota block up front, that second scan gets zero permits, reports itself
rate-capped, and overwrites the first one's good snapshot with
`complete = false`.

**Four triggers, one job.** Login pre-warm, a stale read, the nightly fanout,
and `POST /api/me/concerts/refresh` all insert the same `ScanConcerts` args,
and river's uniqueness collapses them to one in flight. Manual refresh is
additionally throttled by `ManualRefreshMinInterval` (15 min) measured from
the snapshot's `computed_at` — uniqueness prevents two scans running at once
but says nothing about the gap between completed ones, and every scan spends
real quota. `retry_after` outranks that interval, so a manual click can never
bypass the quota guard.

**Filters are applied over the snapshot, and every facet count must equal what
clicking it returns.** Each filter matches by the same rule its facet buckets
by — genre is an exact case-insensitive tag match (substring matching turned a
"rock · 12" pill into forty results by also matching "indie rock" and
"post-rock"), and venue is compared under the dedup normalizer so one room
spelled two ways by two feeds is one facet with one count. Counts are of
*events*, not artist matches (§6.2).

### 6.1 Streaming Result Pattern (historical — Phase 2)

A naive implementation would block until all artist searches complete, then return one large response. For 200 artists this can take 30+ seconds, which is unacceptable UX. ConcertFinder uses a streaming pattern:

- Results stream into a Go channel as each artist's fan-out completes.
- A collector goroutine deduplicates incrementally and pushes results to an in-memory aggregated set.
- The HTTP handler enforces a 15-second context timeout (configurable). Any artist searches still in flight are canceled cleanly via `context.Done()`.
- The frontend can poll a follow-up endpoint to retrieve any results that completed after the initial response (Phase 2 enhancement).

### 6.2 Event Grouping — Multi-Artist Bills (Phase 3)

Deduplication collapses *sources*. Grouping collapses *artists*. They are
different problems, and §12.2 raised the second one as an open question; this
is the answer.

A festival where the user's profile matched six artists is six `Concert` rows
sharing a date, venue, and city — one night out rendered as most of a screen.
`/me/concerts` and `/me/saved-concerts` therefore return `events[]`, not
`concerts[]`, folded by

```
event_key = sha256(iso_date(dt) + normalize(venue) + normalize(city))
```

into one `Event` carrying an `Acts[]` list, with ticket links unioned and
deduped by URL.

- **Grouping happens at assembly time, never inside `DedupKey`.** `dedup_key`
  is the primary key of the `concerts` table and half of the primary key of
  `user_saved_concerts`, so folding the artist out of it would orphan every
  existing save and erase the per-artist rows the subscribe control and the
  genre facets are built on. Storage stays one row per (artist, date, venue,
  city).
- **Saves and subscriptions stay per artist.** Each `Act` carries its own
  `dedup_key`, `saved`, and `subscribed`; the card renders one star + bell per
  act. Subscribing patches that artist across every event in the list, since
  an artist can appear on several bills.
- **`event_key` is day-granular on purpose.** Acts at a festival have
  different set times, so keying on the full timestamp would split exactly the
  bills this exists to merge. The cost is that a venue string naming a
  multi-room complex merges genuinely separate shows on the same night. That
  is the accepted trade: the alternative loses every festival.
- **Counts are of events.** Facet counts collect distinct event keys per
  bucket, because the invariant in §6.0 is measured against the grouped list —
  counting concerts would promise twice the cards a click delivers.
- Grouping runs *after* filtering and after the saved/subscribed overlay, so
  an event survives a filter if any of its acts does.
- **The email renderers group too**, at render time only (§10.3). The net-new
  bookkeeping behind them stays keyed on `dedup_key`, one per (artist, show),
  so an act added later to a bill the user was already emailed about still
  mails on its own.

---

## 7. Data Storage

The schema is intentionally minimal in Phase 1 and explicitly excludes any persisted Spotify listening data, per Spotify's caching restrictions.

### 7.1 Schema (Phase 1)

```sql
-- Users
CREATE TABLE users (
  id                       UUID PRIMARY KEY,
  spotify_user_id          TEXT NOT NULL UNIQUE,
  display_name             TEXT,
  encrypted_refresh_token  BYTEA NOT NULL,
  refresh_token_nonce      BYTEA NOT NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Browser sessions
CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX ON sessions(expires_at);

-- User location settings (Phase 2 surfaces this in UI; Phase 1 hardcoded)
CREATE TABLE user_locations (
  user_id        UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  latitude       DOUBLE PRECISION NOT NULL,
  longitude      DOUBLE PRECISION NOT NULL,
  radius_miles   INTEGER NOT NULL DEFAULT 50,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Derived affinity profile (NOT raw Spotify data)
CREATE TABLE affinity_profiles (
  user_id      UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  artists      JSONB NOT NULL,
  computed_at  TIMESTAMPTZ NOT NULL
);

-- Artist name resolution across platforms (stable; not Spotify data)
-- The bandsintown_name column was dropped in migration 0015 (§5.3) and
-- official_url in 0011 — homepage resolution lives in mb_url_cache, keyed by
-- normalized name so it is shared across users rather than per Spotify ID.
CREATE TABLE artist_resolutions (
  spotify_artist_id           TEXT PRIMARY KEY,
  ticketmaster_attraction_id  TEXT,
  resolved_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Short-TTL concert search cache (ticket data, not Spotify data)
CREATE TABLE concert_cache (
  cache_key   TEXT PRIMARY KEY,
  results     JSONB NOT NULL,
  fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON concert_cache(fetched_at);
```

### 7.1a Tables Added in Phases 2–3

`/migrations` is authoritative; this is the map of what each table is for.

| Table | Added for | Purpose |
|---|---|---|
| `oauth_handshakes` | Phase 1 | Short-lived PKCE verifier + CSRF state, keyed by handshake cookie. |
| `concerts` | Phase 3 (0012) | Canonical event rows keyed by `dedup_key`. Snapshots reference these rather than embedding blobs, so one show is stored once across all users. |
| `user_concert_snapshots` | Phase 3 | Per (user, location_key) scan result: ordered `dedup_keys[]`, `computed_at`, `complete` (0013), `retry_after` (0014). |
| `user_saved_concerts` | Phase 3 | Per-user stars. Primary key includes `dedup_key`, which is why §6.2 forbids folding the artist out of it. |
| `user_subscribed_artists` | Phase 3 | Per-artist bell; drives instant notifications. |
| `user_digest_sent` | Phase 3 | Exact net-new bookkeeping for email — one row per (user, `dedup_key`) already mailed. |
| `rate_ledger` | Phase 3 | Per (user, source, UTC day) API call counter (§8.3). |
| `mb_url_cache` | Phase 2 | MusicBrainz homepage resolutions, shared globally by normalized artist name. |
| `venue_geo_cache` | Phase 2 | Nominatim geocodes by normalized place key. |

### 7.2 Encryption of Refresh Tokens

Refresh tokens are encrypted at rest using AES-256-GCM. The key is loaded from the environment variable `ENCRYPTION_KEY` as a 32-byte hex-encoded value. A unique nonce is generated per token and stored alongside the ciphertext. Phase 3 keeps reading it from the process environment (injected from `/opt/concertfinder/.env`); Secrets Manager is deferred per §11.3, and moving to it is a config change rather than a code change.

### 7.3 Cache TTLs

| Table / Cache | TTL | Rationale |
|---|---|---|
| `affinity_profiles` | 24 hours | Balance freshness against Spotify API load. Also the ToS ceiling (§4.4). |
| `artist_resolutions` | Positive: indefinite. Negative: 30d (`concerts.NegativeResolutionTTL`) | Stable mappings; see §5.2 for why the negative must expire. |
| `mb_url_cache` | Positive: indefinite. Negative: 30d (`db.NegativeMBURLTTL`) | MusicBrainz gains URL relationships continuously. |
| `venue_geo_cache` | Positive: indefinite. Negative: 30d (`db.negativeGeoTTL`) | City coordinates don't drift; Nominatim misses are often transient. |
| `concert_cache` | 12 hours (`CONCERT_CACHE_TTL_HOURS`) | See the ordering constraint below. |
| `user_concert_snapshots` | 6 hours (`SNAPSHOT_STALE_AFTER_HOURS`) | Staleness threshold for the SWR read, not a delete. |
| `sessions` | 30 days last_seen, 90 days created | Reasonable browser persistence. |

**Every negative cache expires; positives don't.** Three lookups record "we
asked and there was nothing," and each has been a silent permanent exclusion at
some point. The reasoning is identical in all three cases — the underlying
answer can change without anyone telling us — and so is the failure mode: no
error, no log, the artist or venue simply never appears again. Positives are
kept forever on purpose, since re-fetching them spends a 1 req/sec turnstile
for data that does not change.

**A TTL enforced only in SQL is not enforced.** `MusicBrainzClient` consults a
5000-entry in-process LRU *before* the DB, and at 200 artists per scan nothing
ever evicts a negative — so the hot entry carries its own `resolved_at`, and
`GetMBURL` returns the row's timestamp rather than letting a promoted negative
restart its clock.

**`concert_cache`'s TTL is bounded on both sides.** It must stay *above*
`SNAPSHOT_STALE_AFTER_HOURS`, so an SWR-triggered refresh is served from cache
rather than re-billing the upstream, and *below* the janitor's 7-day prune of
the same table. It is also the main lever on what a user costs: a scan covers
200 artists, so every expiry is worth roughly one call per artist per source.
At the original 4 hours the cache lapsed several times a day and scans ran out
of quota partway through, returning a fraction of a user's shows.
`internal/config` has tests pinning all three relationships.

---

## 8. Concurrency & Rate Limiting

### 8.1 Fan-Out Pattern

*Phase 3 moved this out of the HTTP handler and into `ScanConcertsWorker`. The
concurrency shape is unchanged; what changed is who waits for it — the read
path now serves a snapshot (§6.0) and the fan-out runs in the background.*

The flow inside one scan job:

1. Load (or compute) the user's affinity profile. Cached for 24 hours.
2. Take the top-N artists from the profile (N=200).
3. Reserve the user's per-source daily quota for the whole scan up front, as a
   `rate.Reservations` block on the context, rather than one DB round trip per
   call (§8.3).
4. Spawn one goroutine per artist, governed by a buffered semaphore (capacity
   10). Each queries Ticketmaster, escalating to the Phase 2 chain only on a
   genuine miss (§5.4).
5. Deduplicate and accumulate; normalize merged results into the shared
   `concerts` table and write the ordered `dedup_keys` to the snapshot row.
6. Stop at `ScanBudget` (5 minutes). Partial results are persisted with
   `complete = false` rather than discarded (§6.0).

### 8.2 Rate Limit Handling

Every external API call is wrapped in a retry helper with the following policy:

- On HTTP 429: read the `Retry-After` header. If present and numeric, sleep that many seconds (**clamped** to 30s) and retry. If absent, use exponential backoff with jitter: `min(2^attempt * 100ms + random(0-100ms), 30s)`.
- On 5xx: exponential backoff with jitter, capped at 3 retries. Do *not* retry 4xx other than 429.
- All retries respect the parent `context.Context` deadline — now the scan
  job's `ScanBudget` rather than an HTTP handler's timeout.

"Clamped" is load-bearing: a `Retry-After` longer than 30s is *shortened*
toward 30s, never replaced by the sub-second backoff path. Falling back to the
short backoff when the header looks unreasonable turns a soft rate limit into
a ban.

### 8.3 Per-User Rate Accounting (Phase 3)

In a multi-user deployment, a single heavy user must not exhaust Ticketmaster's 5,000-request daily quota for everyone. Per-user accounting is added in Phase 3:

- Track request counts per user per upstream service in the `rate_ledger`
  table, bucketed by UTC day.
- Quota is taken out per scan as a reservation block on the context, not one
  DB round trip per call; unspent permits are handed back at the end.
- Caps: `RATE_CAP_TM_PER_USER_DAILY` (250), `RATE_CAP_SONGKICK_PER_USER_DAILY`
  (100). 0 disables the cap for that source.
- On user cap exceeded: degrade gracefully (serve cached results, surface a
  "refresh limited" message), do not fail entirely.

**The cap must exceed the artist count, not just fit under the shared
ceiling.** A scan needs roughly one call per artist per source once
`concert_cache` lapses, so a cap below `MaxScoredArtists` (200) means a user
can *never* cover their own profile: every scan spends the allowance partway
through and reports itself incomplete. This shipped as TM=100 against 200
artists, and presented as a concert list quietly holding half the shows it
should. `main.go` warns at startup when a cap drops below the artist count.

**Every outbound call spends quota, including attraction resolution** — not
just the events query. A source that runs out returns `errRateCapped`, which
must not be read as "no results" (§5.4).

**Sizing trade-off.** Ticketmaster's account-wide budget is 5,000/day, so 250
per user supports roughly 20 concurrently active users rather than 50. The
ledger enforces per-user limits only; it does not model the account total.
Revisit before onboarding a crowd.

---

## 9. Terms of Service Compliance

Every third-party data source has terms that constrain how its data may be used. ConcertFinder's compliance posture is summarized below; the full terms must be reviewed before each phase's deployment.

### 9.1 Spotify Developer Terms

- No long-term caching of Spotify Content. Listening data held in memory only; affinity derivations cached 24 hours maximum.
- No use of Spotify data to train machine learning models, including embedding generation or similarity learning.
- Spotify attribution displayed on all UI surfaces showing Spotify-derived data.
- Phase 1 operates under Spotify Development Mode. Phase 3 requires application to Extended Quota Mode to lift the authorized-user cap. As of February 2026, Development Mode also requires the developer to hold a Spotify Premium subscription and limits one Client ID per developer. **Action: apply for Extended Quota Mode as soon as Phase 2 is demoable.**

### 9.2 Ticketmaster Discovery API Terms

- Must display Ticketmaster attribution on results derived from their API.
- Must link the user back to the Ticketmaster purchase page (we do this by default).
- Short-term result caching is permitted; permanent caching is not.
- Rate limits enforced: 5 req/sec, 5,000 req/day (free tier).

### 9.3 Bandsintown Public API Terms

*No longer applicable — the integration was removed (§5.3). Retained because
stored ticket links from the period still carry Bandsintown URLs, and those
links keep their tracking and attribution parameters when displayed.*

- Preserve all tracking and attribution parameters in event URLs when presenting to users.
- Display the artist's upcoming events in a manner consistent with their display requirements.
- High-volume production use requires applying to their partnership program. The public API is sufficient for personal and demo-scale use.

### 9.4 MusicBrainz and Nominatim

Both are free community services whose terms are enforced by etiquette rather
than by an API key, which makes them easy to violate accidentally:

- Requests must carry a `User-Agent` identifying the application and a contact
  URL. Anonymous requests are rejected outright.
- 1 request/second/IP, hard. Enforced client-side by a process-wide turnstile
  per service (§5.4.4), not per scan and not per goroutine.
- Results are cached aggressively and globally (`mb_url_cache`,
  `venue_geo_cache`) so repeat scans and additional users cost nothing.
- MusicBrainz returns 503 when busy even inside the documented limit; the
  client retries with backoff rather than treating it as a negative answer.

### 9.5 Privacy

User data stored by ConcertFinder is limited to: Spotify user ID, display
name, encrypted refresh token, location settings, derived affinity profile
(24h TTL), saved/subscribed concert keys, email delivery bookkeeping, and
session metadata.

**Email address is collected as of Phase 3**, via the `user-read-email` scope,
solely to deliver the digest and instant notifications the user opted into. It
is stored in plaintext (it is a delivery address, not a credential) and is
removed with the rest of the user's rows on account deletion. The application
collects no payment information, no browsing data outside the application, and
nothing else not directly used for the concert feed. `/privacy` and `/terms`
pages ship with the Phase 3 deployment.

---

## 10. Phased Roadmap

### 10.1 Phase 1: Single-User Local MVP — *complete*

Target: working end-to-end demo on the developer's machine. Estimated effort: 2–3 focused weekends.

**In Scope**

- Spotify PKCE authentication; refresh token encrypted in Postgres.
- Full affinity profile from all six Spotify signal sources.
- Concert search: Ticketmaster (via attraction resolution) + Bandsintown (the latter since removed — §5.3).
- Concurrent fan-out with semaphore + context timeout.
- Deduplication by normalized (artist, date, venue, city).
- Single grouped-by-month concert list view in the frontend.
- Hardcoded location (developer's city).
- Docker Compose: db + api + web services.

**Out of Scope**

- Multi-user support beyond the developer.
- Small-artist fallback chain (Phase 2).
- Location picker UI.
- Background sync; profile computed on demand.
- Filters (genre, price, distance).
- AWS deployment.

### 10.2 Phase 2: Full Coverage Locally — *complete*

Target: feature-complete on the local machine, ready to consider hosting.

**In Scope**

- Small-artist fallback: Songkick + Tier B JSON-LD extraction from artist sites. Shipped with **MusicBrainz** as the default resolver rather than Brave Search (§5.4.2); Songkick is wired but inert without an API key, which was never obtained.
- Location picker UI with geocoding.
- Filters: genre, distance, date range, weekday/weekend.
- Background daily affinity refresh via river.
- Result streaming: frontend polls for late-arriving results after initial response. Superseded by SWR polling against the snapshot (§6.0).
- Begin Spotify Extended Quota Mode application.

### 10.3 Phase 3: Hosted Multi-User on AWS — *application complete; deployment not*

Target: shareable public URL; small group of users beyond the developer.

**Deployment approach.** Rather than the ECS Fargate architecture originally sketched in earlier drafts of §11, Phase 3 uses a single EC2 t4g.small with Postgres hosted on Neon. Caddy on the instance handles TLS termination via Let's Encrypt. The React SPA is embedded into the Go binary at build time (`go:embed`), so there is no separate S3/CloudFront asset pipeline. GitHub Actions deploys over SSM, running `build` and `up -d --wait` as separate commands followed by an end-to-end smoke test (see §11). This trades zero-downtime deploys and auto-scaling for ~$5/mo steady-state vs. ~$30–50/mo on Fargate — acceptable for the intended scale of a small user group.

The database was RDS db.t4g.micro through the first Terraform drafts. At ~$14/mo it was the largest line on the bill, and the two ways to remove it are not equivalent: running Postgres as a container on the app instance saves the same amount but puts the database in the same 2 GiB as a box that builds its own Docker image on every deploy — a bad build could then take the database with it. Neon moves the database off that box entirely for the same saving, which is the actual argument; the cost is a shorter backup history (covered by nightly `pg_dump` to S3), traffic crossing the public internet under TLS rather than staying in a security group, and single-digit-millisecond round trips instead of sub-millisecond ones. The last of those is absorbed by the SWR read path and the bounded scan fan-out. Note that the app **never scales to zero** despite that being Neon's headline feature — River polls every second — so the free plan is a compute-hour budget, not a storage one; see `docs/aws-deploy.md`. See §11 for the current reference architecture, §11.3 for the deferred scale-up options, and `docs/aws-deploy.md` for the operator runbook.

**In Scope**

- AWS deployment: EC2 t4g.small + Neon PostgreSQL, Caddy TLS, Elastic IP. DNS lives at the registrar (Cloudflare), not Route 53. **Not yet done** — Terraform exists in `/infra` but has never been applied, and the GitHub Actions deploy has never succeeded because `AWS_DEPLOY_ROLE_ARN` and `EC2_INSTANCE_ID` are unset. Everything else in this phase runs locally.
- Per-user rate-limit accounting against shared API quotas (§8.3).
- Email notifications for newly detected shows (introduces `user-read-email` scope, a re-auth flow, and SES via SMTP). Two channels: a daily digest and instant notifications for subscribed artists.
- Privacy policy and terms of service pages.
- SWR snapshot read path with background scans (§6.0), replacing the synchronous fan-out.
- Manual refresh endpoint, throttled independently of river's job uniqueness.
- Event grouping for multi-artist bills (§6.2), in both the web list and the email renderers.
- Account deletion.
- Nightly `pg_dump` to a write-only S3 bucket (`scripts/backup-db.sh`), replacing the RDS backup retention given up in the move to Neon.
- Observability: minimum-viable CloudWatch alarms on EC2 status check + estimated billing. There is deliberately no database alarm — Neon publishes no CloudWatch metrics, so its storage and compute-hour headroom can only be alerted on from the Neon console. Application logs stay in Docker (`docker compose logs`).
- Terraform definitions checked into `/infra` covering EC2, security groups, IAM (EC2 role + GitHub OIDC deploy role), Elastic IP, the backup bucket, and the two CloudWatch alarms above. The database is deliberately out of scope — it is one Neon console object, and keeping it out of state also keeps the plaintext database password out of `terraform.tfstate`. DNS is out of scope for the same shape of reason — the registrar is authoritative, so Terraform emits the records to publish (`dns_records`) rather than managing a zone.

**Deferred until scale demands it** (see §11.3)

- ECS Fargate, CloudFront/S3 frontend split, Secrets Manager, ALB, blue/green deploys, CloudWatch dashboards + log shipping.

#### 10.3.1 Scheduled Work

Four daily jobs — affinity refresh, scan fanout, digest fanout, janitor — run
from wall-clock UTC schedules (`DAILY_*_HOUR_UTC`, defaults 06/07/09/10), not
from `river.PeriodicInterval`.

**Why not intervals.** River's periodic scheduler holds in-memory state only
and re-anchors every job to process start, so a 24-hour interval means "24
hours after this process booted." The time of day drifts with each deploy, and
with `RunOnStart: false` a process restarting more often than the interval
fires the job *never*. Deploys restart the server on every push to `main`, so
a two-deploy day was a day with no scan, no digest, and no janitor run — and
nothing logged that. `jobs.DailyAt(hour, min)` computes the next real UTC
occurrence instead, which makes restarts harmless and `RunOnStart: false`
correct rather than dangerous.

**The digest must trail the scan** by at least `config.MinScanDigestGapHours`
(2h). The digest emails whatever snapshot exists, so scheduling it alongside
the scan makes every digest describe the *previous* day's results — silently,
because a stale snapshot is still a valid one. Two hours clears the fanout's
60-minute spread plus `ScanBudget` and retries. The gap is computed modulo 24
so a 23:00 scan with a 01:00 digest reads as 2, not −22; `main.go` warns at
startup when it is too small.

**The digest is deliberately not chained off `ScanConcertsWorker`** the way
instant-notify is. A scan fires on login, on stale reads, and on manual
refresh, so chaining would email on all of them. Suppressing that needs a
trigger field on `ScanConcertsArgs` — which changes what `ByArgs` uniqueness
hashes and would stop two scans deduplicating against each other (§6.0). The
scheduled offset avoids the trap entirely.

### 10.4 Phase 4: Polish and Scale

Target: production-quality public app. Speculative.

- Additional sources. SeatGeek is the leading candidate; Ticketmaster being the sole primary (§5.3) makes a second one more valuable than it looked in Phase 1. Bandsintown would require their partnership program to actually respond.
- User-favorited venues, calendar integration.
- Mobile-friendly responsive design improvements.
- Possible international expansion (significant data-source rework).

---

## 11. AWS Architecture (Phase 3 Reference)

Phase 3 deploys on AWS, with the database on Neon rather than an AWS service. The application remains portable: no AWS SDK imports in `/internal`, all AWS-specific configuration is environment-driven, and Postgres usage avoids provider-specific features — which is what let the database move off RDS without touching a line of Go. The architecture below is the free-tier-anchored target; §11.3 lists what was deliberately deferred.

| Component | AWS Service | Notes |
|---|---|---|
| Application host | EC2 t4g.small (ARM64, Amazon Linux 2023) | Runs `docker compose`: Go API container + Caddy container. Single instance; no ALB. |
| Database | RDS PostgreSQL 16 (db.t4g.micro) | Private subnet; reachable only from the EC2 security group on port 5432. |
| Frontend assets | Embedded in the Go binary (`go:embed`) | Served by the Go API at `/`. Backend endpoints live under `/api/*`. |
| Secrets | `.env` file on the EC2 instance, mode 600 | Migrate to Secrets Manager if compromise risk changes. |
| TLS certificates | Caddy + Let's Encrypt | Auto-provisioned on first HTTPS request. |
| Response security headers | Caddy, site level | HSTS (`includeSubDomains`, no `preload`), `nosniff`, `Referrer-Policy`, `frame-ancestors 'none'` + `X-Frame-Options`. Site level so they cover Caddy's own error responses, not just proxied ones. Deliberately not a full CSP — restricting script/style sources needs testing against the Vite bundle. |
| DNS | Cloudflare (registrar is authoritative) | Apex A record → Elastic IP. No Route 53 zone: the registrar already answers for the domain, so records go live in seconds with nothing to delegate. All records stay unproxied — a proxied CNAME breaks SES DKIM, and proxying the apex collapses every client into a Cloudflare edge IP, defeating the `/api/auth` rate limiter. |
| Static public IP | Elastic IP | Free while attached to a running instance. |
| Deploy | GitHub Actions → SSM `RunShellScript` | OIDC federation; no long-lived AWS keys in GitHub. `build` and `up -d` are separate commands — `up -d --build` tears running containers down as part of the same command, so a failed or OOM-killed build takes the site with it. |
| Deploy verification | Container healthcheck + end-to-end smoke test | `up -d --wait` blocks on the api container's healthcheck, which runs the server binary as `/server -healthcheck` (the image is distroless — no shell, no curl — so the binary is the only available probe). `scripts/verify-deploy.sh` then fetches `/api/healthz` *through Caddy* on 443, covering the half `--wait` cannot see. Without both, `docker compose up -d` exits 0 the moment a container is started, so a container that exits on bad config and crash-loops under `restart: unless-stopped` is indistinguishable from a successful deploy. |
| Health endpoint | `GET /api/healthz` | Pings Postgres. Every route on this server needs the database, so a check that skips it reports green while everything 500s. Reporting unhealthy does not restart the container — Docker restart policies act on exit, not health — so an RDS blip surfaces in `docker compose ps` instead of becoming a restart loop. |
| Logs | Docker logs on the box | `docker compose logs -f` over SSM when needed. |
| Alerting | Three CloudWatch alarms | EC2 status check failure, RDS free storage low, estimated monthly billing over threshold. None is wired to an SNS topic — alarm state is visible in the console only, so nothing pages you. |
| Scheduled jobs | River workers folded into the API binary | No EventBridge dependency. |
| Email (Phase 3) | Amazon SES via SMTP | SMTP-only integration preserves portability across providers. |
| IaC | Terraform in `/infra` | Reproduces the setup end-to-end; `docs/aws-deploy.md` is the manual fallback. |

### 11.1 Estimated Phase 3 Cost (Low Scale)

At single-digit users:

AWS replaced the 12-month free tier on 2025-07-15; accounts created since get
credits on a 6-month plan that closes the account when it lapses, not free
service-hours. `docs/aws-deploy.md` carries the authoritative breakdown and the
reasoning. Summary, us-east-1, excluding credits:

| Item | Now | From 2027-01-01 |
|---|---|---|
| EC2 t4g.small | $0 — standalone trial, ends 2026-12-31 | ~$12.26/mo |
| RDS db.t4g.micro + 20 GiB | ~$14/mo | ~$14/mo |
| Public IPv4 (Elastic IP) | ~$3.65/mo | ~$3.65/mo |
| EBS 20 GiB gp3 | ~$1.60/mo | ~$1.60/mo |
| DNS (Cloudflare) | $0 | $0 |
| SES, SSM, CloudWatch, 100 GB egress | ~$0 at this scale | ~$0 |
| **Total** | **~$20/mo** | **~$32/mo** |

Plus ~$10–15/yr for the domain. The largest single lever is RDS: running
Postgres as a container on the instance removes ~$14/mo, trading away managed
backups. The t4g.small trial ending 2026-12-31 is the one dated cliff.

### 11.2 Portability Constraints

- No AWS SDK imports in `/internal` application code. AWS-specific behavior lives only in infrastructure configuration.
- Database access is plain Postgres protocol. Same connection string works against RDS, Supabase, Neon, or a self-hosted VPS Postgres.
- Secret loading reads from process environment. In prod, `docker compose` injects from `/opt/concertfinder/.env`; locally it injects from `.env`. Migrating to Secrets Manager later is a config change, not a code change.
- The frontend bundle is embedded into the Go binary via `go:embed` in production. In dev, Vite serves the bundle with an API proxy to Go. Splitting to CloudFront + S3 later is a build-time change; no application code change.
- Email delivery uses SMTP against SES's SMTP endpoint. Any SMTP provider (Postmark, Resend, Mailgun, Sendgrid) works with only environment variable changes.

### 11.3 Deferred Scaling Options

The single-instance architecture is a deliberate free-tier / low-ops choice, not a permanent commitment. Trigger points for moving up:

| Move | Trigger |
|---|---|
| Add ALB in front of EC2 | Zero-downtime deploys become required, or a second AZ is needed for availability. |
| Split to ECS Fargate | Regular OOM or CPU saturation on t4g.small; auto-scaling matters. |
| CloudFront + S3 for the SPA | Global user base; asset egress becomes a measurable cost or latency factor. |
| AWS Secrets Manager | Multi-instance secret rotation, or compliance requires no plaintext credentials on host. |
| CloudWatch dashboards + Logs shipping | Debugging via `docker compose logs` no longer scales, or an on-call rotation exists. |

---

## 12. Risks & Open Questions

### 12.1 Known Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Spotify Web API changes invalidate an endpoint we depend on | Medium | Verified all endpoints against current spec; monitor changelog; concentrate API contact in `/internal/spotify` package for blast-radius containment. |
| Ticketmaster rate limit insufficient at scale | Medium | Per-user accounting in Phase 3 (§8.3); apply for higher tier; aggressive `concert_cache` TTL. At 250/user against a 5,000/day account budget this binds at ~20 active users. |
| ~~Bandsintown restricts public API for high volume~~ | **Realized** | The public API 403'd every request and the partnership request went unanswered. Source removed (§5.3). Ticketmaster is now the only primary, so a Ticketmaster outage is a total outage. |
| A dead source is indistinguishable from an empty one | High | The Bandsintown failure ran for months because "403" and "this artist has no shows" both rendered as an empty list. Sources must surface transport failure distinctly from a negative result — the same rule that keeps `errRateCapped` out of the empty-result path (§5.4). |
| Spotify Extended Quota Mode application is rejected or delayed | Low | Phase 1 and 2 do not require it. Begin application early in Phase 2. |
| Small-artist coverage remains poor despite fallbacks | High | Explicit non-goal of exhaustive coverage; "search Google" link is acceptable terminal fallback. |
| Privacy / GDPR concerns at multi-user scale | Low (US-only) | Phase 3 requires privacy policy; minimal PII collected. |

### 12.2 Open Questions

**Answered**

- *How should multi-artist shows (festivals, opening acts) be presented?*
  Grouped by `event_key` at assembly time, one card per event with a per-act
  star and bell. Storage stays one row per artist. See §6.2.
- *Email notification cadence: daily digest vs. immediate?* Both, independently
  opted into. The daily digest covers everything new in the feed; instant
  notification fires only for explicitly subscribed artists.
- *Top-N cutoff.* 200 held up, but it is not a free parameter — it sets the
  floor for the per-user rate caps (§8.3) and dominates fallback cost (§5.4.4).

**Still open**

- Should concerts the user has already viewed be deprioritized in subsequent
  loads? Still unimplemented; the snapshot has no read-state.
- Is the Phase 2 fallback chain worth its complexity? Now measured (§5.4.5):
  it converts ~6% of the artists that reach it, and its cost is dominated by
  the 94% it won't. Partly answered — the wasted tour-path probing is fixed —
  but the judgment call stands, and it should be re-measured against a second
  profile before anyone invests further in the tier.
- Should a resolved homepage that has never once been fetchable decay back to
  a negative? Twelve of 91 are dead domains cached as permanent positives
  (§5.4.5).
- Songkick was designed in as a Phase 2 tier but never activated. Either get a
  key and measure it, or remove the tier.
- Ticketmaster is now a single point of failure for the entire primary feed.
  No second primary source has been identified; SeatGeek is the obvious
  candidate (§10.4).

---

## Appendix A: Configuration Reference

Environment variables read by the Go API. `.env.example` is the authoritative,
commented copy; this table is the map. In production the file lives at
`/opt/concertfinder/.env` on the instance and `docker compose` injects it —
Secrets Manager is deferred (§11.3), so the loading path is the same in both
environments.

**Core**

| Variable | Purpose |
|---|---|
| `SPOTIFY_CLIENT_ID` | OAuth client ID |
| `SPOTIFY_REDIRECT_URI` | OAuth callback URL (`https://127.0.0.1:3000/api/auth/callback` for dev) |
| `TICKETMASTER_API_KEY` | Ticketmaster Discovery API key |
| `DATABASE_URL` | Postgres connection string |
| `ENCRYPTION_KEY` | 32-byte hex-encoded AES-GCM key for refresh token encryption |
| `SESSION_COOKIE_DOMAIN` | Cookie domain (`127.0.0.1` for dev) |
| `LISTEN_ADDR` | Bind address for the API server |
| `SIGNING_KEY` | Optional 32-byte hex HMAC key for CSRF and unsubscribe tokens. Derived from `ENCRYPTION_KEY` when unset; set it explicitly only to rotate signing without invalidating stored refresh-token ciphertexts. |
| `SITE_DOMAIN` | Bare apex the deployment serves. Consumed by **Caddy**, not by the Go binary — `docker-compose.prod.yml` hands the caddy container the same `.env`, and the Caddyfile's site block is `{$SITE_DOMAIN} {`. Unset in production it expands to nothing, that line collapses into a global options block, and Caddy exits with `unrecognized global option: encode`. Validated at startup anyway (see below), because the api container is the only thing in the system that can see the real file. |
| `USER_LATITUDE` / `USER_LONGITUDE` / `USER_RADIUS_MILES` | Fallback location for users who never set one; the Phase 1 hardcoded location |

**Phase 2 — fallback chain (§5.4)**

| Variable | Default | Purpose |
|---|---|---|
| `PHASE2_FALLBACKS_ENABLED` | `false` | Master switch for the chain |
| `PHASE2_MIN_SCORE` | `2.0` | Affinity floor for escalating an artist |
| `PHASE2_FALLBACK_BUDGET_SECONDS` | `120` | Scan-wide wall-clock for the whole chain; negative disables it |
| `PHASE2_FALLBACK_CONCURRENCY` | `1` | Scans admitted to the chain at once, process-wide |
| `BRAVE_SEARCH_API_KEY` | unset | Overrides MusicBrainz as the URL resolver when set |
| `SONGKICK_API_KEY` | unset | Tier is skipped entirely without it |

**Phase 3 — snapshots, quota, scheduling, email**

| Variable | Default | Purpose |
|---|---|---|
| `SNAPSHOT_STALE_AFTER_HOURS` | `6` | Staleness threshold for the SWR read (§6.0) |
| `CONCERT_CACHE_TTL_HOURS` | `12` | Upstream response cache; bounded on both sides (§7.3) |
| `RATE_CAP_TM_PER_USER_DAILY` | `250` | Must exceed the 200-artist scan (§8.3) |
| `RATE_CAP_SONGKICK_PER_USER_DAILY` | `100` | 0 disables the cap |
| `DAILY_AFFINITY_HOUR_UTC` | `6` | Wall-clock schedule, not an interval (§10.3.1) |
| `DAILY_SCAN_HOUR_UTC` | `7` | |
| `DAILY_DIGEST_HOUR_UTC` | `9` | Must trail the scan by ≥ 2h |
| `DAILY_JANITOR_HOUR_UTC` | `10` | |
| `EMAIL_DELIVERY_MODE` | `log` | `log` writes to slog; `smtp` sends |
| `SMTP_HOST` / `PORT` / `USERNAME` / `PASSWORD` / `FROM` | — | SES SMTP credentials |
| `SITE_BASE_URL` | `https://127.0.0.1:3000` | Base for unsubscribe links in email |
| `CONTACT_EMAIL` | — | Operator contact shown on `/privacy` and `/terms` |

Configuration problems are handled in three tiers, by how bad the failure is:

1. **Fall back to the default.** Out-of-range or unparseable *tuning* values —
   hours, caps, TTLs — use the defaults above rather than failing startup. An
   hour of `25` would otherwise be normalized by `time.Date` into the next day,
   silently moving a job.
2. **Warn and run.** Relationships between settings that are suspicious but
   survivable: the cache/staleness/prune ordering (§7.3), the scan→digest gap
   (§10.3.1), a per-user rate cap below the 200-artist scan (§8.3). These are
   logged loudly at startup; the process serves.
3. **Refuse to start.** `Config.Validate` returns errors and `main` exits
   non-zero. This tier exists because every one of its members fails *silently*
   at runtime otherwise — the site serves, health checks are green, and the
   broken thing simply never happens:

   | Rejected | What it looked like before |
   |---|---|
   | Missing/malformed `ENCRYPTION_KEY` | One warning, then every auth and `/me` route skipped wiring. The SPA loaded and login 404'd. |
   | `SPOTIFY_REDIRECT_URI` not ending in `/api/auth/callback` | Lands on the SPA catch-all, so OAuth "succeeds" onto a logged-out page. |
   | Loopback `SITE_BASE_URL` behind a real `SESSION_COOKIE_DOMAIN` | Unsubscribe links in real mail pointing at the recipient's own machine; `127.0.0.1` in the MusicBrainz/Nominatim User-Agent. |
   | Missing `SITE_DOMAIN`, or one disagreeing with `SITE_BASE_URL` | Caddy crash loop, or a certificate for a name none of the emailed links use. |
   | `EMAIL_DELIVERY_MODE=smtp` with no `SMTP_HOST`/`SMTP_FROM` | Digest jobs run and deliver nothing. |
   | Missing `SPOTIFY_CLIENT_ID`, `TICKETMASTER_API_KEY`, `SESSION_COOKIE_DOMAIN` | Empty results or a broken login, attributed to anything but config. |

   Every problem is reported in one pass, so a bad `.env` costs one round trip
   to fix rather than one restart per variable.

---

## Appendix B: Initial External Account Setup Checklist

1. Register a Spotify application at `developer.spotify.com/dashboard`. Configure `https://127.0.0.1:3000/api/auth/callback` as a redirect URI. Note the Client ID.
2. Confirm developer account holds a Spotify Premium subscription (required for Development Mode as of Feb 2026).
3. Sign up for Ticketmaster Discovery API at `developer.ticketmaster.com`. Confirm API key.
4. Install `mkcert` and generate a local CA for `https://127.0.0.1`.
5. Install Go 1.25+, Node 24+ (active LTS; Node 20 went end-of-life 2026-04-30), Docker Desktop.
6. Clone the repository, copy `.env.example` to `.env` and fill it in, then bring up the stack (`docs/local-dev.md`).

Nothing else requires an account. MusicBrainz and Nominatim are open (they
require a descriptive `User-Agent`, not a key); Songkick and Brave Search are
optional and inert when unset.

---

## Appendix C: Glossary

| Term | Definition |
|---|---|
| PKCE | Proof Key for Code Exchange. OAuth 2.0 extension that secures the authorization code flow without requiring a client secret. |
| Affinity profile | The derived list of artists the user is inferred to like, with associated scores. Computed from Spotify signal sources. |
| Fan-out | Concurrent dispatch of many parallel requests, typically to multiple external services, with results collected when complete or timeout. |
| Attraction (Ticketmaster) | Ticketmaster's name for an artist or performer entity. Events are linked to attractions. |
| JSON-LD | JavaScript Object Notation for Linked Data. A standard for embedding structured data in web pages, often used for schema.org markup. |
| Extended Quota Mode | Spotify Web API mode that lifts the Development Mode user cap, requiring application and approval. |
| `dedup_key` | Hash of (artist, date, venue, city). Identifies one artist's appearance at one show; the primary key of the `concerts` table. Collapses the *same show reported by different sources*. |
| `event_key` | Hash of (date, venue, city) — artist omitted. Identifies one show; collapses *different artists on the same bill*. Presentation-only; never stored as a key. |
| Snapshot | A completed scan's result for one (user, location), stored as an ordered list of `dedup_key`s plus its freshness and completeness state. |
| SWR | Stale-while-revalidate. Serve the existing snapshot immediately, enqueue a refresh in the background if it is stale. |
| Turnstile | The capacity-1 channel enforcing a minimum gap between requests to a rate-limited service. Chosen over a mutex because it can be abandoned when the context is cancelled. |
| Reservation | A block of per-user API quota taken out at the start of a scan and partially returned at the end, instead of one ledger write per call. |