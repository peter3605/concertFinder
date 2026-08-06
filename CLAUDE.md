# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Status

Phases 1–3 are implemented: Go backend under `/cmd` + `/internal`, React SPA under `/web`, SQL migrations in `/migrations`, Terraform in `/infra`.

```
go build ./...      # backend
go test ./...       # backend tests
go vet ./...
cd web && npm run build
```

`docs/design.md` remains the authoritative source of truth for architecture decisions, API choices, schema, and phased scope — read it before implementing anything non-trivial. Where this file and the design doc disagree about what the code does *today*, this file wins; the design doc describes intent.

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
- **Ticketmaster artist resolution is two-stage:** resolve name → `attractionId` via `/discovery/v2/attractions.json`, then query events filtered by that attraction ID. Naive keyword search produces false positives (cover bands, tribute acts). Positive resolutions are cached in `artist_resolutions` indefinitely; **negative** ones expire after `concerts.NegativeResolutionTTL` (30d), because resolution needs an exact name match and an artist can sign to TM later — a permanent negative cache silently excludes them forever.
- **Per-user daily caps must exceed `spotify.MaxScoredArtists` (200).** A scan needs roughly one call per artist per source once `concert_cache` lapses, so a cap below that count means a user can *never* cover their own profile: every scan spends the allowance partway and reports itself incomplete. This shipped as TM=100 against 200 artists and presented as a concert list quietly holding half the shows it should. Defaults are now 250/250/100, and `main.go` warns at startup if a cap drops below the artist count. The counterweight is `DefaultCacheTTL` (12h, `CONCERT_CACHE_TTL_HOURS`): it must stay **above** `SNAPSHOT_STALE_AFTER_HOURS` so SWR refreshes are cache-served, and **below** the janitor's 7-day `concert_cache` prune. `internal/config` tests pin all three relationships.
- **Every outbound TM/BIT/Songkick call spends per-user quota, including attraction resolution.** Quota is taken out per scan as a `rate.Reservations` block on the context (`rate.Allow(ctx, source)`), not one DB round trip per call. A source that runs out returns `errRateCapped`, which **must not** be treated as "no results" — doing so escalates the artist into the far more expensive Phase 2 fallback chain, i.e. spending more because we were trying to spend less.
- **Endpoints removed by Spotify (Feb 2026) that are NOT available:** `/recommendations`, `/audio-features`, `/audio-analysis`, `/artists/{id}/related-artists`, `/artists/{id}/top-tracks`, batch `/tracks`. Do not write code that calls these. Affinity is constructed entirely from the user's own explicit signals.
- **`GET /playlists/{id}/items` (Feb 2026 change):** only works for playlists the user owns or collaborates on. Skip merely-followed playlists.
- **Preserve Bandsintown tracking parameters** verbatim in event URLs shown to users — required by their display terms.
- **DICE.fm is excluded** from any scraping/fallback work; their ToS prohibits automated access.
- **Display "Powered by Spotify"** attribution on any UI surface showing Spotify-derived data.
- **AWS portability:** no AWS SDK imports in `/internal`. Secrets come from process env regardless of source (Phase 3 loads from `.env` on the EC2 box; Secrets Manager would be a swap without code changes). Postgres usage avoids RDS-specific features. Email delivery uses SMTP against SES so the app is not coupled to AWS.

## Affinity Scoring (design §4.3)

Per-artist score combines six weighted signals — followed (1.0), top artists weighted by time range (0.9 × {short=1.0, medium=0.8, long=0.6}), saved albums (0.7), saved tracks (0.5), recently played (0.4), owned playlists (0.2). Top 200 artists are submitted to concert search. These weights are starting values to be tuned during Phase 1 dogfooding — treat them as adjustable, not load-bearing.

## SWR read pattern (Phase 3, replaces the old synchronous fan-out)

The "get my concerts" request is now stale-while-revalidate against a
snapshot in `user_concert_snapshots`:

1. Resolve user from session cookie (middleware also stashes the full user
   in ctx so `handleMe` skips a duplicate DB read).
2. Read the snapshot for `(user_id, location_key)`.
3. Apply filters (date, genre, venue, weekday, saved_only) over the snapshot.
   **A facet's count must equal what clicking it returns.** Every filter is
   matched with the same rule its facet is bucketed by:
   - Genre — exact tag, case-insensitive. Not a substring: a "rock · 12"
     pill that also matched "indie rock" and "post-rock" returned forty.
   - Venue — compared under `concerts.Normalize`, the dedup normalizer, so
     one room spelled "9:30 CLUB" by one feed and "9:30 Club" by another is
     a single facet with a single count. Facets display the commonest raw
     spelling; ties break alphabetically so labels don't flicker between
     requests. Note this is a picker, not a search — normalization maps
     punctuation to a separator, so "930 Club" does not match "9:30 Club".
4. If the snapshot is missing, older than `SNAPSHOT_STALE_AFTER_HOURS`
   (default 6h), **or has `complete = false`**, enqueue a `ScanConcerts`
   river job. Response includes `refreshing: true` and the frontend polls
   every 10s to pick up the new snapshot (bounded — see below).

The actual TM/BIT/fallback fan-out happens inside `ScanConcertsWorker`
(design §6.1 concurrency lives there, not in the HTTP handler):

- Top-N artists (N=200) with a per-artist goroutine bounded by a **buffered
  semaphore of capacity 10**.
- Each artist fans out to TM and BIT in parallel (independent goroutines —
  BIT failure never cancels TM).
- `ScanBudget = 5 * time.Minute` per job. Fallback resolver + venue geocoder
  are rate-limited (MB and Nominatim are both 1 req/sec/IP).
- **The fallback chain gets its own scan-wide deadline**
  (`SearchDeps.FallbackBudget`, default 60s, env
  `PHASE2_FALLBACK_BUDGET_SECONDS`). Its lookups are globally serialized at
  1 req/sec, so cost scales with the number of *escalating artists* and
  parallelism buys nothing: a cold 200-artist profile measured ~250s of
  MusicBrainz + ~86s of Nominatim against a 300s `ScanBudget`. The deadline
  is shared by every artist (a per-artist cap × 200 artists is not a cap).
  Running out is logged, **not** treated as incompleteness — flagging it
  would set `complete = false` on nearly every cold scan, which the SWR
  handler reads as permanently stale and turns into a rescan loop.
- **The fallback budget is per-scan, but its resolvers are process-wide.**
  Each of MusicBrainz and Nominatim has one 1 req/sec turnstile shared by
  every scan, so N concurrent scans split that throughput while each still
  holds a full budget: ~55 lookups solo becomes ~11 each at five-way
  concurrency, with no error and no log — small-artist coverage just thins
  as users are added. `SearchDeps.FallbackGate` (`PHASE2_FALLBACK_CONCURRENCY`,
  default 1) admits a bounded number of scans to the fallback; the rest skip
  it and log *why*, distinctly from budget exhaustion. **The gate must be
  one instance for the whole process** — build it outside the `searchDeps`
  closure, since a per-scan gate is a no-op. Skipping is acceptable because
  resolver results persist in `mb_url_cache` / `venue_geo_cache` and scans
  repeat nightly, so coverage accrues across runs.
- Anything enforcing a rate limit must be cancellable. `fallback.rateLimiter`
  uses a capacity-1 channel, not a `sync.Mutex`: `Lock()` is not ctx-aware,
  so 200 artists queueing behind a ~1.1s-per-holder mutex could not be
  released by the scan deadline at all (observed: a 5-minute job running
  978s).
- Merged results are normalized into the shared `concerts` table; the
  snapshot row holds the ordered `dedup_keys` array (migration 0012).

**Partial scans must stay visible.** `concerts.Search` returns an
`*IncompleteError` alongside its partial results when artists were skipped
(budget expired) or a source hit its quota. The worker still persists what
it found — a half-filled page beats an empty one — but writes
`complete = false`, which the SWR handler treats as stale regardless of age,
and returns the error so river retries (`ScanMaxAttempts = 3`). Never
convert that error into a silent success: a truncated scan stamped
`computed_at = now()` looks identical to a complete one and gets trusted for
the full staleness window.

**One scan per (user, location) at a time.** `ScanConcertsArgs.InsertOpts`
declares `ByArgs` + `ByState` uniqueness over every non-terminal state.
Never express this as `ByPeriod`: a scan takes ~60s, so a 30s window lapsed
mid-run and a second scan started underneath the first. Because a scan
reserves the user's entire daily quota block up front, the second got zero
permits, reported `rate_capped`, and overwrote the first one's good snapshot
with `complete = false`. Terminal states stay *out* of `ByState` so a
finished scan doesn't block the next legitimate refresh. Call sites must
pass `nil` insert opts — a non-empty `UniqueOpts` at the call site silently
overrides the args-level policy.

**Quota exhaustion is a wait, not a retry.** A capped scan cannot do better
until the rate ledger's UTC day rolls over, so the worker records
`retry_after` (migration 0014) and returns nil rather than erroring. The SWR
handler refuses to enqueue before that instant and reports
`refreshing: false`. Without both halves, `complete = false` means
"permanently stale", and the 10s poll loop re-enqueues an impossible scan
every 10s for the rest of the day while river separately retries it.
Incompleteness with *skipped artists* still retries normally — that one time
can't fix.

Retry policy: HTTP 429 honors `Retry-After` **clamped to 30s** — clamping
shortens an over-long wait toward 30s, it never falls back to the
sub-second backoff (that turns a soft limit into a ban). Exponential
backoff with jitter only when there is no usable `Retry-After`; 5xx exp
backoff capped at 3 retries; never retry other 4xx.

Frontend polling is bounded: `MAX_REFRESH_POLLS` caps a refresh at ~10
minutes of 10s polls, and transient fetch errors retry with backoff instead
of killing the loop or replacing already-loaded data with an error screen.

Snapshots are pre-warmed on login (via `OnLoginSuccess` hook) and refreshed
nightly by `FanoutScanConcerts` (jobs spread across 60min to avoid a
thundering herd).

## Deduplication (design §6)

```
dedup_key = sha256(normalize(artist) + iso_date(dt) + normalize(venue) + normalize(city))
normalize = lowercase → strip_punctuation → strip leading "the "/"a "/"an " → collapse whitespace
```

Records sharing a key merge into one canonical event with multiple ticket links sorted by source priority: artist's official site → Ticketmaster/Live Nation → Bandsintown (tracking params preserved) → Songkick/other.

## Event grouping (multi-artist bills)

Dedup collapses *sources*; grouping collapses *artists*. A festival where
the user's profile matched six artists is six `Concert` rows with the same
date, venue, and city — one night out rendered as most of a screen. The
`/me/concerts` and `/me/saved-concerts` responses therefore return
`events[]`, not `concerts[]`: `concerts.GroupEvents` folds rows sharing

```
event_key = sha256(iso_date(dt) + normalize(venue) + normalize(city))
```

into one `Event` carrying an `Acts[]` list, with ticket links unioned and
deduped by URL.

- **Grouping happens at assembly time, never in `DedupKey`.** `dedup_key`
  is the primary key of the `concerts` table and half of
  `user_saved_concerts`' primary key, so folding the artist out of it would
  orphan every existing save and erase the per-artist rows the subscribe
  control and genre facets are built on. Storage stays one row per
  (artist, date, venue, city).
- **Saves and subscriptions stay per artist.** Each `Act` carries its own
  `dedup_key`, `saved`, and `subscribed`, and the card renders one
  star + bell pair per act. Subscribing patches that artist across *every*
  event in the list, since an artist can appear on several bills.
- **`event_key` is day-granular on purpose.** Acts at one festival have
  different set times, so keying on the full timestamp would split exactly
  the bills this exists to merge. The cost is that a venue string naming a
  multi-room complex merges genuinely separate shows on the same night;
  that is the accepted trade, because the alternative loses every festival.
- **Facets and `count` are counts of events, not artist matches.** The
  facet invariant ("a facet's count must equal what clicking it returns")
  is now measured against the grouped list — `computeFacets` collects
  distinct event keys per bucket, and `internal/http/facets_test.go`
  asserts `len(GroupEvents(Apply(...)))`. Counting concerts here would
  promise twice the cards a click delivers, the same class of bug as the
  old substring genre match.
- Grouping runs *after* filtering and after the saved/subscribed overlay,
  so an event survives a filter if any of its acts does.

Not yet grouped: the email digest and instant-notify renderers
(`internal/email/digest.go`) still emit one line per concert, so a festival
mails as six rows and the subject counts six.

## Required Environment Variables (Appendix A)

Core: `SPOTIFY_CLIENT_ID`, `SPOTIFY_REDIRECT_URI`, `TICKETMASTER_API_KEY`, `BANDSINTOWN_APP_ID`, `DATABASE_URL`, `ENCRYPTION_KEY` (32-byte hex), `SESSION_COOKIE_DOMAIN`, `LISTEN_ADDR`.

Phase 2 fallback: `PHASE2_FALLBACKS_ENABLED`, `PHASE2_MIN_SCORE`, `PHASE2_FALLBACK_BUDGET_SECONDS`, `PHASE2_FALLBACK_CONCURRENCY`, `BRAVE_SEARCH_API_KEY` (optional — MB is the default resolver), `SONGKICK_API_KEY`.

Phase 3: `SNAPSHOT_STALE_AFTER_HOURS`, `CONCERT_CACHE_TTL_HOURS`, `RATE_CAP_TM_PER_USER_DAILY`, `RATE_CAP_BIT_PER_USER_DAILY`, `RATE_CAP_SONGKICK_PER_USER_DAILY`, `EMAIL_DELIVERY_MODE` (`log`/`smtp`), `SMTP_HOST`/`PORT`/`USERNAME`/`PASSWORD`/`FROM`, `SITE_BASE_URL`, `CONTACT_EMAIL`.

Loaded from `.env` in all environments; in prod the file lives at `/opt/concertfinder/.env` on the EC2 instance.

## Phase Discipline

When proposing or implementing work, check which phase it belongs to before expanding scope:

- **Phase 1 (MVP):** PKCE auth, full affinity from all 6 signals, TM + BIT only, semaphore fan-out, dedup, month-grouped list view, **hardcoded location**, Docker Compose. No multi-user, no fallbacks, no filters, no background sync.
- **Phase 2:** Small-artist fallback (Songkick + JSON-LD extraction via Brave Search), location picker, filters, river background jobs, late-result polling. Begin Extended Quota Mode application.
- **Phase 3:** AWS single-instance deployment (EC2 t4g.small + RDS db.t4g.micro, Caddy TLS, SPA embedded in Go binary, `.env` on the instance, GitHub Actions → SSM `docker compose up -d`), per-user rate accounting, email notifications (re-auth for `user-read-email`) via SES SMTP, privacy policy + ToS pages, Terraform in `/infra`. Full ECS Fargate + CloudFront/S3 + Secrets Manager is deferred (see design §11.3 for triggers).

If a request would pull Phase 2/3 work into Phase 1, flag it rather than silently expanding.
