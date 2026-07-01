# ConcertFinder

**Spotify-Driven Concert Discovery — Project Proposal & Design Document**

- Version: 1.0
- Status: Draft
- Geographic Scope: United States

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
| Backend | Go 1.22+ (chi router) | I/O concurrency for fan-out across ticket APIs; single static binary; strong typing. |
| Database | PostgreSQL 16 | Mature, portable across hosting providers; no provider-specific features used. |
| DB access | pgx + sqlc | Type-safe SQL generation without ORM weight. |
| Background jobs | river (Postgres-backed) | No Redis dependency until volume justifies it. |
| Local dev | Docker Compose | One command to bring up the full stack. |
| Logging | log/slog (stdlib) | Structured JSON logging, native to Go 1.22. |
| Hosting (Phase 3) | AWS (ECS Fargate + RDS) | Builds on existing operator experience. |

### 2.2 Why a Backend (and not browser-only)

A backend-first design is mandatory rather than optional, for the following reasons:

- Third-party API keys for Ticketmaster, Bandsintown, and other paid or rate-limited services cannot live in browser code.
- Spotify refresh tokens are long-lived and must be stored encrypted at rest. Browser storage is unsuitable.
- Concert search is a fan-out across many APIs per request. Server-side coordination enables shared caching, rate-limit pooling, and progressive result streaming.
- Multi-user support in later phases requires server-side session management and per-user data isolation.

### 2.3 Project Layout

The Go backend follows a standard layered structure with feature-oriented internal packages:

```
/cmd
  /server         main.go for the API
  /worker         main.go for background jobs (Phase 2+)
/internal
  /spotify        Spotify client, types, affinity scoring
  /ticketmaster   Ticketmaster client, types
  /bandsintown    Bandsintown client, types
  /concerts       aggregation, dedup, scoring
  /auth           PKCE handshake, token storage, refresh middleware
  /db             sqlc-generated code
  /http           HTTP handlers, middleware
/migrations       SQL migration files
/web              React + TS frontend (or separate repo)
/infra            Terraform definitions (Phase 3)
```

Each external API has its own dedicated package with its own types. Shared "models" packages are deliberately avoided; external API schemas drift independently and should not couple to one another.

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
| Redirect URI (dev) | `https://127.0.0.1:3000/callback` |
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

For each artist on the curated affinity list, ConcertFinder fans out to multiple concert data sources in parallel. The fan-out is bounded by a semaphore to respect rate limits, with per-request context timeouts to ensure the end-user request does not hang on a slow source.

### 5.1 Sources

| Source | Endpoint | Coverage | Auth |
|---|---|---|---|
| Ticketmaster Discovery | `GET /discovery/v2/events.json` | Ticketmaster + Live Nation network; most large tours | API key (free, instant signup) |
| Bandsintown | `GET rest.bandsintown.com/artists/{name}/events` | Broad indie coverage; smaller venues | `app_id` parameter (no auth, partnership for high volume) |
| Songkick (Phase 2) | `GET api.songkick.com/api/3.0/...` | Variable indie coverage | API key |

### 5.2 Ticketmaster Resolution Pattern

Naively keyword-searching events by artist name produces false positives (cover bands, tribute acts, artists with shared names). The correct pattern is two-stage:

1. Resolve the artist name to a Ticketmaster `attractionId` via the `/discovery/v2/attractions.json` endpoint. The result is stable per-artist and worth caching indefinitely (with periodic refresh).
2. Query `/discovery/v2/events.json` filtered by the resolved attraction ID, plus `latlong`, `radius`, and `classificationName=Music`.

Free tier limits are 5 requests/sec and 5,000 requests/day. With a 200-artist list this allows roughly 25 full refresh cycles per day before throttling. Per-artist resolution is cached in the `artist_resolutions` table (see section 7).

### 5.3 Bandsintown

Bandsintown's public API accepts an `app_id` query parameter (any chosen string, used for attribution). The endpoint accepts artist name as a path parameter. URL returned in event records contains tracking parameters that must be preserved when displaying the link, per Bandsintown's display requirements.

Bandsintown's coverage is artist-driven (artists submit their own tour dates), which is why it is stronger for smaller and DIY acts than Ticketmaster. The public API is sufficient for personal and small-scale use; high-volume production use requires applying to their partnership program. This is a Phase 3 concern.

### 5.4 Small-Artist Fallback (Phase 2)

When both primary sources return zero results for a high-affinity artist (above a configurable score threshold), ConcertFinder escalates to a layered fallback. This is Phase 2 work; Phase 1 omits it entirely.

#### 5.4.1 Tier A: Structured Fallbacks

1. Check the artist's Spotify `external_urls` for an official site URL.
2. Query Songkick API for the artist by name; if results, return them.

#### 5.4.2 Tier B: Search and JSON-LD Extraction

1. Use a search API (Brave Search API in 2026 is the recommended choice) to resolve unknown artists to their official site URL. Cache the resolution per Spotify artist ID indefinitely.
2. Fetch the artist's homepage and any common tour-page paths (`/tour`, `/shows`, `/live`). Look for `<script type="application/ld+json">` blocks containing schema.org `MusicEvent` entities. Many artist sites (Squarespace, Bandzoogle templates) publish these automatically for SEO.
3. If no structured data is found, surface a prefilled Google search link to the user as the terminal fallback. Do not build heuristic HTML parsers; the maintenance burden is unjustifiable.

#### 5.4.3 Scraping Etiquette

- User-Agent identifies the application and provides a contact URL.
- Respect robots.txt for every fetched host (`github.com/temoto/robotstxt` for Go).
- Per-domain rate limit: minimum 3 seconds between requests to the same host.
- Aggressive caching: 6–24 hour TTL on fetched pages.
- DICE.fm is explicitly excluded as their Terms of Service prohibit automated access.

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
3. Bandsintown link (with tracking parameters preserved)
4. Songkick or other aggregator links last

All discovered ticket links are presented to the user, sorted by the priority above. This serves two purposes: it gives the user choice of vendor (which can affect price and fees), and it provides graceful degradation if any one link is broken or sold out.

### 6.1 Streaming Result Pattern

A naive implementation would block until all artist searches complete, then return one large response. For 200 artists this can take 30+ seconds, which is unacceptable UX. ConcertFinder uses a streaming pattern:

- Results stream into a Go channel as each artist's fan-out completes.
- A collector goroutine deduplicates incrementally and pushes results to an in-memory aggregated set.
- The HTTP handler enforces a 15-second context timeout (configurable). Any artist searches still in flight are canceled cleanly via `context.Done()`.
- The frontend can poll a follow-up endpoint to retrieve any results that completed after the initial response (Phase 2 enhancement).

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
CREATE TABLE artist_resolutions (
  spotify_artist_id           TEXT PRIMARY KEY,
  ticketmaster_attraction_id  TEXT,
  bandsintown_name            TEXT,
  official_url                TEXT,
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

### 7.2 Encryption of Refresh Tokens

Refresh tokens are encrypted at rest using AES-256-GCM. The key is loaded from the environment variable `ENCRYPTION_KEY` as a 32-byte hex-encoded value. A unique nonce is generated per token and stored alongside the ciphertext. In Phase 3 the key is sourced from AWS Secrets Manager rather than environment configuration.

### 7.3 Cache TTLs

| Table / Cache | TTL | Rationale |
|---|---|---|
| `affinity_profiles` | 24 hours | Balance freshness against Spotify API load. |
| `artist_resolutions` | Indefinite, refresh quarterly | Stable cross-platform mappings. |
| `concert_cache` | 4 hours | Stay within ticket API rate limits; concerts rarely change hour-to-hour. |
| `sessions` | 30 days last_seen, 90 days created | Reasonable browser persistence. |

---

## 8. Concurrency & Rate Limiting

### 8.1 Fan-Out Pattern

The end-to-end request flow for a "get my concerts" call:

1. Resolve user from session cookie.
2. Load (or compute) the user's affinity profile. Cached for 24 hours.
3. Take the top-N artists from the profile (N=200 in Phase 1).
4. Spawn one goroutine per artist, governed by a buffered semaphore (capacity 10). Each goroutine fans out internally to Ticketmaster + Bandsintown in parallel.
5. Send results to a collector channel. A separate goroutine deduplicates and accumulates.
6. Return results when all goroutines complete or the 15-second context timeout fires, whichever comes first.

### 8.2 Rate Limit Handling

Every external API call is wrapped in a retry helper with the following policy:

- On HTTP 429: read the `Retry-After` header. If present and numeric, sleep that many seconds (capped at 30s) and retry. If absent, use exponential backoff with jitter: `min(2^attempt * 100ms + random(0-100ms), 30s)`.
- On 5xx: exponential backoff with jitter, capped at 3 retries. Do *not* retry 4xx other than 429.
- All retries respect the parent `context.Context` deadline. The HTTP handler's 15-second budget is shared with all retries.

### 8.3 Per-User Rate Accounting (Phase 3)

In a multi-user deployment, a single heavy user must not exhaust Ticketmaster's 5,000-request daily quota for everyone. Per-user accounting is added in Phase 3:

- Track request counts per user per upstream service in a Postgres counter table (or Redis if/when added).
- Soft cap: 100 Ticketmaster requests per user per day (configurable).
- On user cap exceeded: degrade gracefully (serve cached results, surface a "refresh limited" message), do not fail entirely.

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

- Preserve all tracking and attribution parameters in event URLs when presenting to users.
- Display the artist's upcoming events in a manner consistent with their display requirements.
- High-volume production use requires applying to their partnership program. The public API is sufficient for personal and demo-scale use.

### 9.4 Privacy

User data stored by ConcertFinder is limited to: Spotify user ID, display name, encrypted refresh token, location settings, derived affinity profile (24h TTL), and session metadata. The application does not collect email, payment information, browsing data outside the application, or any data not directly used for the concert recommendation feature. A privacy policy document is required before any public deployment (Phase 3).

---

## 10. Phased Roadmap

### 10.1 Phase 1: Single-User Local MVP

Target: working end-to-end demo on the developer's machine. Estimated effort: 2–3 focused weekends.

**In Scope**

- Spotify PKCE authentication; refresh token encrypted in Postgres.
- Full affinity profile from all six Spotify signal sources.
- Concert search: Ticketmaster (via attraction resolution) + Bandsintown.
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

### 10.2 Phase 2: Full Coverage Locally

Target: feature-complete on the local machine, ready to consider hosting.

**In Scope**

- Small-artist fallback: Songkick + Tier B JSON-LD extraction from artist sites via Brave Search resolution.
- Location picker UI with geocoding.
- Filters: genre, distance, date range, weekday/weekend.
- Background daily affinity refresh via river.
- Result streaming: frontend polls for late-arriving results after initial response.
- Begin Spotify Extended Quota Mode application.

### 10.3 Phase 3: Hosted Multi-User on AWS

Target: shareable public URL; small group of users beyond the developer.

**In Scope**

- AWS deployment: ECS Fargate, RDS Postgres, CloudFront + S3 frontend, Secrets Manager, ACM, Route 53.
- Per-user rate-limit accounting against shared API quotas.
- Email notifications for newly detected shows (introduces `user-read-email` scope and a re-auth flow).
- Privacy policy and terms of service pages.
- Observability: CloudWatch metrics, structured logging dashboards, basic alerting.
- Terraform definitions checked into `/infra`.

### 10.4 Phase 4: Polish and Scale

Target: production-quality public app. Speculative.

- Additional sources (SeatGeek, possibly Bandsintown partnership API).
- User-favorited venues, calendar integration.
- Mobile-friendly responsive design improvements.
- Possible international expansion (significant data-source rework).

---

## 11. AWS Architecture (Phase 3 Reference)

Phase 3 deploys on AWS. The application is written to remain portable: all AWS-specific configuration is environment-driven, business logic contains no AWS SDK imports, and Postgres usage avoids RDS-specific features. The architecture below is the reference target, not a coupling.

| Component | AWS Service | Notes |
|---|---|---|
| Go API | ECS Fargate | Container runtime; alternative: App Runner. |
| Database | RDS PostgreSQL (db.t4g.micro to start) | Approx. $15/mo at low scale. |
| Frontend assets | CloudFront + S3 | Built React SPA served as static assets. |
| Secrets | AWS Secrets Manager | Spotify client secret, API keys, encryption key. |
| TLS certificates | ACM | Free; auto-renewing. |
| DNS | Route 53 | |
| Logs | CloudWatch Logs | slog JSON output ingested directly. |
| Scheduled jobs | EventBridge Scheduler | Daily affinity refresh trigger. |
| Queue (if needed) | SQS | Only if river-on-Postgres outgrows itself. |
| IaC | Terraform | In the `/infra` directory; check in alongside app code. |

### 11.1 Estimated Phase 3 Cost (Low Scale)

At single-digit users with normal usage patterns, expected monthly cost is approximately $30–50 USD. The Fargate task is the largest line item. A leaner alternative is a single small EC2 instance running Docker, which can bring monthly cost to ~$10 at the price of more operations overhead.

### 11.2 Portability Constraints

- No AWS SDK imports in `/internal` application code. AWS-specific behavior lives only in infrastructure configuration.
- Database access is plain Postgres protocol. Same connection string works against RDS, Supabase, Neon, or a self-hosted VPS Postgres.
- Secret loading reads from process environment. ECS injects from Secrets Manager; Docker Compose injects from `.env`. The application does not know the difference.
- Object storage (for the frontend bundle) is fronted by CloudFront in production. In dev the Vite dev server serves the bundle directly.

---

## 12. Risks & Open Questions

### 12.1 Known Risks

| Risk | Likelihood | Mitigation |
|---|---|---|
| Spotify Web API changes invalidate an endpoint we depend on | Medium | Verified all endpoints against current spec; monitor changelog; concentrate API contact in `/internal/spotify` package for blast-radius containment. |
| Ticketmaster rate limit insufficient at scale | Medium | Per-user accounting in Phase 3; apply for higher tier; aggressive `concert_cache` TTL. |
| Bandsintown restricts public API for high volume | Medium | Application for partnership tier is part of Phase 3 prep. |
| Spotify Extended Quota Mode application is rejected or delayed | Low | Phase 1 and 2 do not require it. Begin application early in Phase 2. |
| Small-artist coverage remains poor despite fallbacks | High | Explicit non-goal of exhaustive coverage; "search Google" link is acceptable terminal fallback. |
| Privacy / GDPR concerns at multi-user scale | Low (US-only) | Phase 3 requires privacy policy; minimal PII collected. |

### 12.2 Open Questions

- What is the right top-N cutoff for the artist list submitted to ticket search? 200 is a starting guess; will tune in Phase 1.
- Should concerts the user has already viewed be deprioritized in subsequent loads? Probably yes in Phase 3; out of scope for Phase 1.
- How should multi-artist shows (festivals, opening acts) be presented? Currently each artist match yields a row; merging by event ID across artists is a Phase 2 question.
- Email notification cadence in Phase 3: daily digest vs. immediate? Probably daily; user-configurable.

---

## Appendix A: Configuration Reference

Environment variables required by the Go API. In local development these are loaded from `.env` (gitignored). In Phase 3 they are injected from AWS Secrets Manager via the ECS task definition.

| Variable | Purpose |
|---|---|
| `SPOTIFY_CLIENT_ID` | OAuth client ID. `[TODO: register and populate]` |
| `SPOTIFY_REDIRECT_URI` | OAuth callback URL (`https://127.0.0.1:3000/callback` for dev) |
| `TICKETMASTER_API_KEY` | Ticketmaster Discovery API key |
| `BANDSINTOWN_APP_ID` | Bandsintown attribution identifier |
| `DATABASE_URL` | Postgres connection string |
| `ENCRYPTION_KEY` | 32-byte hex-encoded AES-GCM key for refresh token encryption |
| `SESSION_COOKIE_DOMAIN` | Cookie domain (`127.0.0.1` for dev) |
| `LISTEN_ADDR` | Bind address for the API server |

---

## Appendix B: Initial External Account Setup Checklist

1. Register a Spotify application at `developer.spotify.com/dashboard`. Configure `https://127.0.0.1:3000/callback` as a redirect URI. Note the Client ID.
2. Confirm developer account holds a Spotify Premium subscription (required for Development Mode as of Feb 2026).
3. Sign up for Ticketmaster Discovery API at `developer.ticketmaster.com`. Confirm API key.
4. Choose a Bandsintown `app_id` string (any identifier you want associated with your traffic).
5. Install `mkcert` and generate a local CA for `https://127.0.0.1`.
6. Install Go 1.22+, Node 20+, Docker Desktop.
7. Clone the repository and run `docker compose up` to bring up the stack.

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