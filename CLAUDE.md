# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Status

Phases 1–3 are implemented **and deployed**, serving at `https://concertfinder.app`: Go backend under `/cmd` + `/internal`, React SPA under `/web`, SQL migrations in `/migrations`, Terraform in `/infra` (applied), native iOS client under `/ios`.

```
go build ./...      # backend
go test ./...       # backend tests
go vet ./...
cd web && npm run build
```

The iOS app is a **second client of the same API** — same binary, same handlers, same database, no mobile BFF (`docs/ios-app-plan.md` §1.1). Its `.xcodeproj` is generated, not committed:

```
cd ios && xcodegen generate
xcodebuild test -project ConcertFinder.xcodeproj -scheme ConcertFinder \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

Two consequences of having two clients, both of which are cheap now and expensive later: `/api/me/*` responses are **additive-only** — a field rename is a breaking change for builds already on someone's phone — and `/api/site-info` carries `min_ios_build` as the escape hatch when that rule has to be broken.

**`docs/ios-app-plan.md` §0 is the current status of the iOS work and what is left.** Read it before acting on anything else in that document: the body below §0 is the plan as originally written, its tense is aspirational, and most of it is already built. What remains is gated on Spotify and Apple accounts, not on code. One trap it records: do not `terraform apply` until the APNs key is in hand, or `APNS_P8_KEY`'s `REPLACE_ME` placeholder fails the next deploy (`docs/aws-deploy.md` §7a).

`docs/design.md` remains the authoritative source of truth for architecture decisions, API choices, schema, and phased scope — read it before implementing anything non-trivial. Where this file and the design doc disagree about what the code does *today*, this file wins; the design doc describes intent.

## What ConcertFinder Is

A web app — and, since `/ios`, a native iOS app against the same API — that builds a personalized concert feed from a user's Spotify listening data by fanning out across ticketing APIs for shows by artists the user already engages with. Ticketmaster is the only primary source; the Phase 2 fallback chain (MusicBrainz → official artist site → JSON-LD) is the only secondary. US-only. Phase 1 is single-user local; multi-user AWS deployment is Phase 3.

## Planned Architecture

Three-tier: React+TS+Vite SPA → Go 1.25 API (chi router) → PostgreSQL 16. The Go backend mediates **all** third-party API access; the frontend never contacts Spotify or ticketing APIs directly. Reasons this is non-negotiable: API keys can't live in browser code, Spotify refresh tokens must be encrypted at rest, and concert search is a server-side fan-out with shared caching and rate-limit pooling.

Planned Go layout (see design §2.3):

```
/cmd/{server,worker}    entrypoints
/internal/spotify       Spotify client + affinity scoring
/internal/ticketmaster  TM client
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
- **Spotify redirect URI is `https://127.0.0.1:3000/api/auth/callback`** for local dev (`https://<domain>/api/auth/callback` in prod) — `http://localhost` is rejected by Spotify as of Nov 2025. The path is not `/callback`: the handler is mounted under `/api/auth`, and a dashboard entry pointing at `/callback` hits the SPA catch-all instead, so login silently completes into a logged-out app.
- **PKCE flow only.** Implicit Grant is deprecated. Authorization Code without PKCE is not used.
- **Ticketmaster artist resolution is two-stage:** resolve name → `attractionId` via `/discovery/v2/attractions.json`, then query events filtered by that attraction ID. Naive keyword search produces false positives (cover bands, tribute acts). Positive resolutions are cached in `artist_resolutions` indefinitely; **negative** ones expire after `concerts.NegativeResolutionTTL` (30d), because resolution needs an exact name match and an artist can sign to TM later — a permanent negative cache silently excludes them forever.
- **Per-user daily caps must exceed `spotify.MaxScoredArtists` (200).** A scan needs roughly one call per artist per source once `concert_cache` lapses, so a cap below that count means a user can *never* cover their own profile: every scan spends the allowance partway and reports itself incomplete. This shipped as TM=100 against 200 artists and presented as a concert list quietly holding half the shows it should. Defaults are now TM=250 / Songkick=100, and `main.go` warns at startup if a cap drops below the artist count. The counterweight is `DefaultCacheTTL` (12h, `CONCERT_CACHE_TTL_HOURS`): it must stay **above** `SNAPSHOT_STALE_AFTER_HOURS` so SWR refreshes are cache-served, and **below** the janitor's 7-day `concert_cache` prune. `internal/config` tests pin all three relationships.
- **Per-user caps multiply; the account ceiling is what the upstream enforces.**
  Ticketmaster's 5000/day is per API key, not per user of ours, so ten users at
  `RATE_CAP_TM_PER_USER_DAILY=500` is the entire allowance and the eleventh
  user's scan gets upstream 403s — which arrive looking exactly like artists
  with no shows. `rate_ledger_account` (migration 0017) is a second counter
  keyed `(source, day)` with no `user_id` and no FK, charged in `Reserve` after
  the per-user block and for no more than that block already allowed. An
  overdraw is refunded to **both** ledgers: returning it only to the account
  would leave a user charged for calls they were never granted, draining their
  personal cap on days someone else was busy. `Release` likewise refunds both,
  account first — leaving the shared counter high starves everyone, leaving one
  user's high costs only them. `CheckAndIncrement` checks it too, or it would
  be an unbounded path to the upstream sitting beside the bounded one.
  `RATE_CAP_TM_ACCOUNT_DAILY` / `RATE_CAP_SONGKICK_ACCOUNT_DAILY` default to
  5000; unset (0) disables the ceiling and restores the old behaviour exactly.
  Exhaustion reuses the existing `retry_after` path, which is correct because
  the remedy is the same: wait for the UTC day to roll over.
- **Every outbound TM/Songkick call spends per-user quota, including attraction resolution.** Quota is taken out per scan as a `rate.Reservations` block on the context (`rate.Allow(ctx, source)`), not one DB round trip per call. A source that runs out returns `errRateCapped`, which **must not** be treated as "no results" — doing so escalates the artist into the far more expensive Phase 2 fallback chain, i.e. spending more because we were trying to spend less. **A call site charges as many permits as it makes requests**: use `rate.AllowN` where one logical lookup is several requests. Songkick's `SearchArtistEvents` is two (resolve the artist ID, then its calendar) with no cache in between, so charging one permit made `RATE_CAP_SONGKICK_PER_USER_DAILY` mean twice its stated number. `TakeN` is all-or-nothing and hands back an over-draw on refusal, so a refused 2-permit take doesn't strand the last permit.
- **Endpoints removed by Spotify (Feb 2026) that are NOT available:** `/recommendations`, `/audio-features`, `/audio-analysis`, `/artists/{id}/related-artists`, `/artists/{id}/top-tracks`, batch `/tracks`. Do not write code that calls these. Affinity is constructed entirely from the user's own explicit signals.
- **`GET /playlists/{id}/items` (Feb 2026 change):** only works for playlists the user owns or collaborates on. Skip merely-followed playlists.
- **Bandsintown has been removed** (migration 0015). Its public API returned an AWS `explicit deny in an identity-based policy` 403 on every request for the whole time it was wired up, and the partnership request went unanswered. Ticketmaster is the sole primary source. Do not re-add a BIT client without first confirming the API actually answers; the previous integration silently contributed nothing while costing a per-artist call and a quota slot.
- **Every negative cache expires; positives don't.** Three lookups cache
  "we asked and there was nothing", and all three have been a silent
  permanent exclusion at some point: `concerts.NegativeResolutionTTL` (30d,
  Ticketmaster attractions), `db.NegativeMBURLTTL` (30d, MusicBrainz
  homepages), `db.negativeGeoTTL` (30d, Nominatim places). The reasoning is
  the same each time — an artist signs to TM later, MusicBrainz is a
  user-edited wiki that gains URL relationships continuously, Nominatim
  misses are often transient — and the failure mode is the same: no error,
  no log, the artist or venue simply never appears again. Positive results
  are kept forever on purpose; re-fetching them spends a 1 req/sec turnstile
  for data that doesn't change. **A TTL enforced only in SQL is not
  enforced**: `MusicBrainzClient` consults a 5000-entry in-process LRU
  *before* the DB, and at 200 artists a scan nothing ever evicts a negative,
  so `mbCacheEntry` carries `resolvedAt` and `GetMBURL` returns the row's
  timestamp rather than letting a promoted negative restart its clock. The
  janitor drops expired negatives from both tables (`PruneExpiredNegativeMBURLs`,
  `PruneExpiredNegativeGeo`); readers already ignore them, so this is about
  rows, not correctness — one accumulates per artist and per city we ever
  failed to resolve. Positives are never pruned.
- **Third-party User-Agents are a contract, not a formality.** MusicBrainz
  403s anonymous traffic and Nominatim's stated remedy for a UA it can't act
  on is a block — a failure that arrives as silence, not an error we raise.
  `main.go` builds one string from `SITE_BASE_URL` + `CONTACT_EMAIL` and hands
  it to both clients. The package-level defaults are last-resort fallbacks;
  the previous one named a GitHub repo that does not exist, which satisfied
  the letter of both policies and none of their purpose.
- **Retiring a source does not retire its stored links.** `concerts.data` blobs keep `"source":"bandsintown"` links until the janitor prunes the events, so `Source` constants and `SOURCE_LABELS` entries outlive their clients. `concerts.priorityOf` sorts a source missing from `sourcePriority` *last* — a bare map lookup returns 0, which is a higher priority than Ticketmaster's 2, so deleting the entry would promote dead links to the top of every card.
- **The already-sent ledger is keyed by channel, and every read and write must say which.** `user_digest_sent` is `(user_id, dedup_key, channel)` since migration 0016. Before that it had no channel, and the daily digest and instant-notify shared it *deliberately* — one email per show, whichever path found it first. Push could not join that unchanged: writing those rows suppresses the email, reading them means a user opted into both channels gets exactly one, decided by which worker ran first. **Neither failure raises an error or logs anything.** `db.FilterUnsentDedupKeys` / `RecordDigestSent` / `CountDigestSent` therefore all take a `db.Channel`, and the argument is mandatory precisely so each call site states its intent. Email digest and instant-notify both pass `ChannelEmail` — they are two triggers for one channel and must keep suppressing each other. `ScanConcertsWorker` computes its candidate set **once** and filters per channel; filtering once and fanning out reintroduces the bug exactly.
- **DICE.fm is excluded** from any scraping/fallback work; their ToS prohibits automated access.
- **Display "Powered by Spotify"** attribution on any UI surface showing Spotify-derived data.
- **AWS portability:** no AWS SDK imports in `/internal`. Secrets come from process env regardless of source (Phase 3 loads from `.env` on the EC2 box; Secrets Manager would be a swap without code changes). Postgres usage avoids provider-specific features — which is what made the move off RDS to **Neon** a Terraform-and-docs change with zero code touched. Email delivery uses SMTP against SES so the app is not coupled to AWS.
- **Postgres is Neon, not RDS, and not managed by Terraform.** Two things about the connection string are load-bearing. It must be the **direct** endpoint: River picks jobs up via LISTEN/NOTIFY, and Neon's pooled endpoint is PgBouncer in transaction mode, which does not support it and does not report that — job pickup silently degrades to the 1s `FetchPollInterval` fallback and leader resignations take ~5s. And it must keep `?sslmode=require`, which is what replaces the `rds.force_ssl=1` parameter group now that database traffic crosses the public internet instead of sitting in a security group. **The app never scales to zero** — River polls every second forever — so Neon's free plan is a compute-hour budget (~183 of ~192 CU-hours at a pinned 0.25 CU), not a storage question. Pin min *and* max compute; one autoscale spike during the nightly fanout exhausts the month, and exhaustion means a suspended compute and 500s, not a warning.

## Affinity Scoring (design §4.3)

Per-artist score combines six weighted signals — followed (1.0), top artists weighted by time range (0.9 × {short=1.0, medium=0.8, long=0.6}), saved albums (0.7), saved tracks (0.5), recently played (0.4), owned playlists (0.2). Top 200 artists are submitted to concert search. These weights are starting values to be tuned during Phase 1 dogfooding — treat them as adjustable, not load-bearing.

**Hydration is bounded and failure-tolerant, and both halves matter.** Every
paginated source has a page cap (`internal/spotify/sources.go`) because they
otherwise walk a library of any size, 50 rows at a time, sequentially, inside
`affinity.ComputeTimeout` (60s) — 10k saved tracks is 200 round trips from one
source. Past the timeout the profile never computed, and since
`ScanConcertsWorker` computes affinity *before* it searches, that user's feed
stayed empty forever while the SWR poll re-enqueued a scan that could not
succeed. The size of someone's library silently decided whether the product
worked for them. Caps keep the newest rows, which is what the scoring wants
anyway.

`HydrateSources` returns `(Sources, []error)`, not `(Sources, error)`: a
failing source degrades the profile, it does not destroy it. It deliberately
does **not** use `errgroup.WithContext` — a shared cancelling context is
exactly the "first error kills the other five" behavior being avoided. Only a
total wipeout (no signal at all) is fatal, because persisting an empty profile
would cache it for the full 24h TTL. `RefreshAffinityArgs` declares
`InsertOpts` for the same class of reason: river's defaults are 25 attempts and
no uniqueness, so a revoked Spotify grant meant two dozen full six-endpoint
fan-outs. `RefreshAffinityWorker.Timeout` exists because river's default job
timeout is 60s — the *same number* as `ComputeTimeout`, so the two raced.

## SWR read pattern (Phase 3, replaces the old synchronous fan-out)

The "get my concerts" request is now stale-while-revalidate against a
snapshot in `user_concert_snapshots`:

1. Resolve user from session cookie (middleware also stashes the full user
   in ctx so `handleMe` skips a duplicate DB read).
2. Read the snapshot for `(user_id, location_key)`.
3. Apply filters (date, genre, venue, weekday) over the snapshot. There is no
   `saved_only` or `radius` parameter — both were parsed server-side long
   after the last client stopped sending them (saves moved to their own
   endpoint; the radius the user picks is applied upstream at fetch time).
   **Shows that have already happened are dropped first**, at a floor of the
   start of the current UTC day. Nothing else enforces that: snapshots are
   rebuilt every few hours and the janitor keeps past events another 7 days,
   so last night's show used to head a list titled "Upcoming concerts". The
   floor is a day boundary rather than `now()` so a matinee doesn't disappear
   from its own listing mid-afternoon, and it applies to `/me/saved-concerts`
   too — that query sorts by `event_date` ascending, so past saves landed at
   the very top.
   **A facet's count must equal what clicking it returns.** Facets are
   computed after the past-show floor and before the user's own filters —
   counting the raw snapshot would promise cards no view can produce. Every
   filter is matched with the same rule its facet is bucketed by:
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

The actual Ticketmaster + fallback fan-out happens inside `ScanConcertsWorker`
(design §6.1 concurrency lives there, not in the HTTP handler):

- Top-N artists (N=200) with a per-artist goroutine bounded by a **buffered
  semaphore of capacity 10**.
- Each artist queries Ticketmaster, escalating to the fallback chain only
  when TM returns nothing and the artist was not skipped for quota.
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

## When scans run

Four triggers, all funnelling into the same `ScanConcerts` job, which river's
args-level uniqueness collapses to one in flight per (user, location):

1. **Login** — `OnLoginSuccess` pre-warms a snapshot.
2. **A stale read** — the SWR handler, per the rules above.
3. **The nightly fanout** — `FanoutScanConcerts`, one job per user with a
   session in the last 14 days, spread across 60min to avoid a thundering herd.
4. **`POST /me/concerts/refresh`** — an explicit user request.

**Daily jobs use wall-clock schedules, never `river.PeriodicInterval`.**
River's periodic scheduler keeps in-memory state only and re-anchors every job
to process start, so a 24h interval means "24h after this process booted": the
time of day drifts with each deploy, and — with `RunOnStart: false` — a process
restarting more often than the interval fires the job *never*. Since deploys
restart the server on every push to `main`, a two-deploy day was a day with no
scan, no digest, and no janitor run. `jobs.DailyAt(hour, min)` computes the
next real UTC occurrence instead, which makes restarts harmless and keeps
`RunOnStart: false` correct. Hours come from `DAILY_*_HOUR_UTC`.

**The digest must trail the scan by `config.MinScanDigestGapHours` (2h).** It
emails whatever snapshot exists, so scheduling it alongside the scan makes
every digest describe the *previous* day's results — silently, since a stale
snapshot is still a valid one. Two hours clears the 60min spread plus
`ScanBudget` and retries. `main.go` warns at startup when the gap is too small;
`ScanDigestGapHours()` is modular so a 23:00 scan with a 01:00 digest reads as
2, not −22.

Deliberately *not* chained off `ScanConcertsWorker` the way instant-notify is:
a scan fires on login, stale reads, and manual refresh, so chaining would email
on all of them, and suppressing that needs a trigger field on
`ScanConcertsArgs` — which changes what `ByArgs` uniqueness hashes and would
stop two scans deduplicating against each other.

**Manual refresh is throttled by `ManualRefreshMinInterval` (15min) measured
from the snapshot's `computed_at`.** River's uniqueness stops two scans running
at once but says nothing about the gap between completed ones, and a scan
spends real quota. The refusal returns 429 with the instant it lifts plus a
reason, so the UI distinguishes "you just refreshed" from "today's allowance is
gone". `retry_after` outranks the interval — a manual click must never bypass
the quota guard, since that scan is guaranteed to come back capped.

## Deduplication (design §6)

```
dedup_key = sha256(normalize(artist) + iso_date(dt) + normalize(venue) + normalize(city))
normalize = lowercase → strip_punctuation → strip leading "the "/"a "/"an " → collapse whitespace
```

Records sharing a key merge into one canonical event with multiple ticket links sorted by source priority: artist's official site → Ticketmaster/Live Nation → Songkick/other → anything unrecognized (see `priorityOf`).

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

- **The email renderers group too, but only at render time.** `RenderDigest`
  and `RenderInstantNotify` call `GroupEvents` on the way in and count events
  in the subject; a festival that matched six artists mails as one entry, not
  six rows under "6 new shows". What must **not** move is the net-new
  bookkeeping around them — `db.FilterUnsentDedupKeys` / `db.RecordDigestSent`
  stay keyed on `dedup_key`, one per (artist, show), so an act added to a bill
  the user was already emailed about still mails on its own. Grouping the
  sent-set instead would mark the whole bill delivered on first sight.
  Instant-notify is safe to fold despite being per-subscription because its
  input is already narrowed to subscribed artists, so a merged entry can only
  name artists the user asked about — it never leaks the rest of the lineup.

## Unsubscribe and email headers

**One unsubscribe link, one meaning: no more email.** `db.OptOutAllEmail`
clears `digest_opt_in` *and* `instant_notify_opt_in`. It used to clear only the
digest — but instant-notify mail carries the same link under the words "Stop
these notifications", so a user who clicked it kept receiving instant mail with
no indication of why or what else to press.

**The opt-out is on POST; GET only renders a confirmation page.** Mail security
gateways (Outlook Safe Links, scanning appliances) fetch every URL in an
inbound message, and a state-changing GET honors that fetch — unsubscribing
people who never clicked. POST is also what RFC 8058 one-click sends, so the
same handler serves both. The token is read from the query string *or* the form
body because the confirmation page posts a field while one-click repeats the
URL.

`buildMIME` emits `Date`, a unique `Message-ID` on the From domain,
RFC 2047-encoded `Subject`, and `List-Unsubscribe` +
`List-Unsubscribe-Post: List-Unsubscribe=One-Click`. None of this is
cosmetic — missing Date/Message-ID is a standing SpamAssassin penalty, the
digest subject contains a literal em dash that is malformed sent raw, and
Gmail/Yahoo's bulk-sender rules expect one-click unsubscribe. Every
interpolated header value goes through `sanitizeHeader`: display names and
addresses come from Spotify, and a CRLF in one would let it inject headers.

## Deployment

**Run `./scripts/check-deploy-config.sh` after touching `Caddyfile` or either
compose file.** CI runs it too (the `deploy-config` job, which `deploy` depends
on). These are the only files in the repo that never execute locally —
`docker-compose.yml` has no Caddy service and `go run` reads neither — and
three defects have shipped in them, each presenting identically: Caddy exits,
`restart: unless-stopped` makes it a crash loop, and the api container beside
it looks healthy throughout.

Two of those are worth naming, because both are invisible to the obvious check:

- `header_up` is a **`reverse_proxy` subdirective**, never a site-level one.
  At site level it is a config-adapt error.
- The caddy service needs `env_file`. Without it `SITE_DOMAIN` is empty inside
  the container, `{$SITE_DOMAIN} {` collapses into a *global options block*,
  and Caddy dies with `unrecognized global option: encode`. Running
  `caddy validate` with the variable exported in your shell passes happily —
  the script asserts against the compose-resolved environment instead, which
  is what actually reaches the container.

`scripts/verify-deploy.sh` is in the same category — it only ever executes on
the instance, mid-deploy — so the preflight `bash -n`s it and asserts its
executable bit. It is the step that decides whether a deploy succeeded, so a
typo in it would fail an otherwise fine deploy right after the new containers
were already up.

**The preflight cannot check the deployment's own `.env`.** It writes a
synthetic one (`SITE_DOMAIN=example.com`) precisely so it never reads a
developer's real secrets, which means it proves the *wiring* and can say
nothing about whether the production file sets anything. `SITE_DOMAIN` was
missing from `.env.example` entirely for that reason — documented in
`infra/README.md` and `docs/aws-deploy.md`, absent from the template an
operator actually copies. Two places now cover it: it is in `.env.example`, and
`config.Validate` rejects a missing `SITE_DOMAIN`, or one whose host disagrees
with `SITE_BASE_URL`, whenever `SESSION_COOKIE_DOMAIN` is non-loopback. The Go
binary never *uses* `SITE_DOMAIN` — it validates it because the api container
is handed the same `.env` and is therefore the only thing in the system that
can see the real file. A mismatch is equally silent: Caddy provisions a
certificate for one name while every emailed link and the
MusicBrainz/Nominatim User-Agent point at another.

The proxy **overwrites** `True-Client-IP`, `X-Real-IP`, and `X-Forwarded-For`
rather than passing them through. `chi/middleware.RealIP` reads them in that
order and Caddy only appends to XFF, so without this a client could set
`True-Client-IP` freely and land in whichever bucket it liked in the
`/api/auth` rate limiter.

Caddy sets HSTS, `X-Content-Type-Options`, `Referrer-Policy`,
`Content-Security-Policy: frame-ancestors 'none'` and `X-Frame-Options` at
**site level**, so they cover Caddy's own error responses (a 502 when the api
is down) and not just what the api returns. `header` is a site-level directive;
only `header_up` is reverse_proxy-only. The CSP is deliberately *only*
`frame-ancestors` — restricting script/style sources needs testing against the
Vite bundle, and a wrong one breaks the SPA on first load.

**A deploy must prove the site serves, not that containers started.**
`/api/healthz` pings Postgres — every route needs the database, so a check that
skips it reports green while everything 500s — but for a while nothing ever
called it. That matters because `docker compose up -d` exits 0 the moment a
container is *started*: with `config.Validate`'s hard exit on a bad `.env` and
`restart: unless-stopped`, one wrong variable produced a crash-looping api
container, an SSM `Success`, and a green workflow, with the site down
throughout. Fail-fast validation made this *more* likely, not less — it turned
silent misconfiguration into a hard exit without giving the exit anywhere to
surface. Two mechanisms now close it, and they cover different halves:

- `up -d --wait` blocks on the api container's `healthcheck`, which runs
  **the server binary in `-healthcheck` mode** (`runHealthcheck` in
  `cmd/server/main.go`). The image is distroless — no shell, no curl, no wget —
  so the binary is the only thing available to probe with; use `CMD`, never
  `CMD-SHELL`. It reads `LISTEN_ADDR` straight from the env rather than through
  `config.Load`, so the probe can't fail for reasons unrelated to whether the
  server answers. Reporting unhealthy does **not** restart the container
  (Docker restart policies act on exit, not health), so a database blip shows up
  in `docker compose ps` instead of becoming a restart loop. That matters more
  with Neon than it did with RDS: the database is now across the public internet
  rather than inside the VPC, so transient connection failures are likelier.
- `scripts/verify-deploy.sh` then fetches `/api/healthz` **through Caddy** over
  443. This is the half `--wait` cannot see: the healthcheck runs inside the api
  container and proves only that the Go process answers itself, while every
  deploy defect this repo has shipped was a Caddy crash loop next to a healthy
  api container. It dumps `ps` and container logs on failure, so it is also the
  step that explains a bad deploy. A `--wait` failure is swallowed in the
  workflow on purpose so this still runs and produces that output.

Deploys build the image **on the instance**, but `build` and `up -d` are
separate SSM steps: `up -d --build` tears down running containers as part of
the same command, so a failed or OOM-killed build took the site with it. Keep
them separate in the runbook's manual and rollback commands too — a rollback is
by definition a moment when the site is already unhappy. CI runs gofmt, vet,
`go build`, `go test`, and `npm run build` — `go test` alone compiles only
packages that have tests, and several here have none. Building in Actions and
pushing to ECR would remove the on-instance build entirely; that needs an ECR
repo plus instance-profile pull permissions in `/infra`.

## Required Environment Variables (Appendix A)

Core: `SPOTIFY_CLIENT_ID`, `SPOTIFY_REDIRECT_URI`, `TICKETMASTER_API_KEY`, `DATABASE_URL`, `ENCRYPTION_KEY` (32-byte hex), `SESSION_COOKIE_DOMAIN`, `LISTEN_ADDR`, `SIGNING_KEY` (optional 32-byte hex; derived from `ENCRYPTION_KEY` when unset — set it only if you want to rotate signing without touching stored refresh-token ciphertexts).

Phase 2 fallback: `PHASE2_FALLBACKS_ENABLED`, `PHASE2_MIN_SCORE`, `PHASE2_FALLBACK_BUDGET_SECONDS`, `PHASE2_FALLBACK_CONCURRENCY`, `BRAVE_SEARCH_API_KEY` (optional — MB is the default resolver), `SONGKICK_API_KEY`.

Phase 3: `SNAPSHOT_STALE_AFTER_HOURS`, `CONCERT_CACHE_TTL_HOURS`, `RATE_CAP_TM_PER_USER_DAILY`, `RATE_CAP_SONGKICK_PER_USER_DAILY`, `RATE_CAP_TM_ACCOUNT_DAILY`, `RATE_CAP_SONGKICK_ACCOUNT_DAILY`, `EMAIL_DELIVERY_MODE` (`log`/`smtp`), `SMTP_HOST`/`PORT`/`USERNAME`/`PASSWORD`/`FROM`, `SITE_BASE_URL`, `CONTACT_EMAIL`, `SITE_DOMAIN`.

iOS (all optional; unset means the web app behaves exactly as before): `MOBILE_CALLBACK_URL`, `IOS_APP_ID`, `MIN_IOS_BUILD`, `APNS_KEY_ID`, `APNS_TEAM_ID`, `APNS_BUNDLE_ID`, `APNS_P8_KEY`, `APNS_ENVIRONMENT`. Two things `config.Validate` refuses to start on, because both are silent otherwise: a **partial** APNs set — it wires up successfully and then drops every notification, indistinguishable from nobody having opted in — and a `MOBILE_CALLBACK_URL` whose host disagrees with `SITE_BASE_URL`, since iOS fetches `apple-app-site-association` from the link's own domain and a mismatch ends the login in Safari with the app still waiting. Empty `IOS_APP_ID` makes that route 404 **on purpose**: serving an association naming an empty app is worse, because iOS caches it.

`SITE_DOMAIN` is the odd one out: it is consumed by **Caddy**, not by the Go
binary, via `docker-compose.prod.yml` handing the caddy container the same
`.env`. `config.Load` reads it and `config.Validate` checks it anyway — see the
Deployment section for why that is the only place it can be caught.

All of these are read from the **process environment** — the binary has no
dotenv dependency and never opens `.env` itself. `docker compose` is what loads
the file, locally from `.env` and in prod from `/opt/concertfinder/.env` on the
EC2 instance. Running `go run ./cmd/server` directly therefore needs the file
sourced first (`set -a && . ./.env && set +a`), or the server comes up with an
empty config and fails on `DATABASE_URL`.

## Phase Discipline

When proposing or implementing work, check which phase it belongs to before expanding scope:

- **Phase 1 (MVP):** PKCE auth, full affinity from all 6 signals, TM only (BIT was in scope at the time and has since been removed), semaphore fan-out, dedup, month-grouped list view, **hardcoded location**, Docker Compose. No multi-user, no fallbacks, no filters, no background sync.
- **Phase 2:** Small-artist fallback (Songkick + JSON-LD extraction via Brave Search), location picker, filters, river background jobs, late-result polling. Begin Extended Quota Mode application.
- **Phase 3:** AWS single-instance deployment (EC2 t4g.small + Neon Postgres, Caddy TLS, SPA embedded in Go binary, `.env` on the instance, GitHub Actions → SSM `docker compose up -d`), per-user rate accounting, email notifications (re-auth for `user-read-email`) via SES SMTP, privacy policy + ToS pages, Terraform in `/infra`. The database was RDS db.t4g.micro until it became the largest line on the bill (~$14/mo); Neon's free plan replaced it, with nightly `pg_dump` to S3 (`scripts/backup-db.sh`) standing in for the 7-day RDS retention that was given up. Full ECS Fargate + CloudFront/S3 + Secrets Manager is deferred (see design §11.3 for triggers).

If a request would pull Phase 2/3 work into Phase 1, flag it rather than silently expanding.
