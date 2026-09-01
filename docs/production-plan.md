# ConcertFinder — Production Readiness Plan

Derived from the full-stack review of 2026-09-01, written against `63ed196`.

**This file is the working state of the implementation run.** Tick boxes as you
finish, record any judgement call under *Decisions taken*, and append one line
per session under *Progress log*. A session that starts with a cleared context
should be able to read this file and know exactly where to pick up.

Read `CLAUDE.md` before touching serving, jobs, or quota code. It documents
constraints that are load-bearing and easy to undo; several tasks below exist
inside those constraints rather than around them.

---

## Rules of engagement

1. **One branch, one PR.** `production-hardening`. One commit per phase, so the
   PR is reviewable in slices even though it is large.
2. **Do not ask questions.** Every task below states what to do. If a task turns
   out to need a product decision this file does not answer, skip it, add it to
   *Deferred* with one sentence of why, and move on. Never stall.
3. **Additive-only on `/api/me/*`.** A field rename is a breaking change for
   builds already on a phone. New fields are fine; renames and removals are not.
4. **Never deploy.** No `terraform apply`, no `docker compose up` against prod,
   no pushing to `main`. Terraform changes are written and validated, never
   applied — `APNS_P8_KEY`'s ordering trap (`docs/aws-deploy.md` §7a) still
   applies.
5. **No new dependencies** in `/internal` or `ios/` without recording why under
   *Decisions taken*. The web app already has everything it needs.
6. **Every behaviour change gets a test** where the package already has tests.
   Where it does not (`ticketmaster`, `geocoding`, `affinity`), P2-9 adds the
   first ones.

## Verification

Run the lanes you touched, from the repo root. These are the gate for every
commit:

```
go build ./... && go vet ./... && go test ./...
npm --prefix web run lint && npm --prefix web run build
./scripts/check-deploy-config.sh
cd infra && terraform fmt -check && terraform validate     # never apply
cd ios && xcodegen generate && xcodebuild test \
  -project ConcertFinder.xcodeproj -scheme ConcertFinder \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

**Run builds serially, never in parallel.** This laptop is not powerful and
other sessions may be running. The Swift build is the expensive one — run it
once at the end of an iOS phase, not after each task.

## Checkpoint protocol

At the end of every phase:

1. Run the verification lanes for what you touched. Fix what you broke.
2. Tick the phase's boxes in this file, append to *Progress log*, and record any
   *Decisions taken* or *Deferred* entries.
3. `git add -A && git commit` with the phase name; push the branch.
4. Print exactly: `CHECKPOINT: phase N complete. Clear context and resume with:
   "Continue docs/production-plan.md from phase N+1."` — then **stop**.

The user clears the context and pastes that line back. This file is the handoff;
nothing important should live only in the conversation.

## Subagents

Use at most **two at a time**, and only where a phase splits cleanly across
languages that do not share files. Subagents edit code; they do **not** run
builds, tests, or git commands — the main session does all of that after their
work lands, serially. Give each subagent the phase's task IDs, the file paths,
and the instruction to read `CLAUDE.md` first.

- Phase 0 — serial, no subagents. Small and interdependent.
- Phase 1 — two agents: iOS lane (P1-1…P1-7) and web lane (P1-8…P1-11). P1-12
  is split across both; do it yourself afterwards.
- Phase 2 — two agents: Go lane (P2-1…P2-7, P2-9, P2-11, P2-12) and infra lane
  (P2-8, P2-10). The Go lane is large; consider running it as two sequential
  agents rather than one.
- Phase 3 — two agents: iOS lane and web lane, after you have done the shared
  backend work (P3-2's endpoint, P3-3's response field) yourself first.

---

## Phase 0 — Stop the bleeding

Serial. Nothing here is a feature; all of it is the difference between a bad day
and a bad week.

### P0-1 — Redact credentials from URL errors
- [x] Done

**Files:** `internal/ticketmaster/client.go`, `events.go`, `attractions.go`,
`internal/fallback/songkick.go`

The API key is in the query string, so on any transport error `http.Client.Do`
returns a `*url.Error` whose `Error()` string is the full URL — which is wrapped
and logged verbatim at `internal/concerts/search.go:399,429` and
`internal/fallback/fallback.go:65`. With a 10s client timeout and a 200-artist
fanout, timeouts are routine.

**Do:** add a small `redactURLError(err error) error` helper (one per package,
or a shared unexported one in each — do not create a new shared package for it).
It unwraps `*url.Error`, replaces the `URL` field with the path only, and
rewraps. Apply it at every `return` in `doGETRetry` and Songkick's equivalent.
Return errors as `fmt.Errorf("tm %s: %w", u.Path, redactURLError(err))`.

**Done when:** a unit test asserts that an error from a request to a URL
containing `apikey=SECRET` does not contain `SECRET` in its `Error()` string.

### P0-2 — Cap container logs
- [x] Done

**Files:** `docker-compose.prod.yml`, `.github/workflows/deploy.yml`

**Do:** add to both the `api` and `caddy` services:

```
logging:
  driver: json-file
  options:
    max-size: "10m"
    max-file: "3"
```

Add `docker builder prune -f --filter until=168h` to the deploy's cleanup step,
beside the existing `docker image prune -f` — image prune does not touch
buildkit cache, and both share the 20 GiB root volume.

**Done when:** `./scripts/check-deploy-config.sh` passes.

### P0-3 — Make the alarms reach a human
- [x] Done

**Files:** `infra/cloudwatch.tf`, `infra/variables.tf`, `infra/terraform.tfvars`

Both existing alarms have no `alarm_actions`, so their state is console-only.

**Do:** add an `aws_sns_topic` plus an `aws_sns_topic_subscription` of protocol
`email` to a new `var.alert_email` (declare it; set it in `terraform.tfvars`
using the existing `CONTACT_EMAIL` value). Wire the topic ARN into
`alarm_actions` and `ok_actions` on both existing alarms. Add a third alarm on
`StatusCheckFailed_System` whose action is
`arn:aws:automate:${var.region}:ec2:recover`.

**Done when:** `terraform fmt -check && terraform validate` pass. Do not apply.

### P0-4 — Make the backup self-installing and self-reporting
- [x] Done

**Files:** `infra/ec2.tf` (user_data), `scripts/backup-db.sh`,
`docs/aws-deploy.md`

The systemd timer that runs the backup exists only as copy-paste in the runbook,
so a rebuilt instance silently has no backups.

**Do:** move the `concertfinder-backup.service` and `.timer` unit files into
user_data as heredocs, followed by `systemctl enable --now
concertfinder-backup.timer`. Add an optional dead-man's-switch: if
`BACKUP_HEARTBEAT_URL` is set in the environment, `curl -fsS --max-time 10` it
after a successful upload (failure to ping must not fail the backup). Update
§7 of the runbook to say the timer is now installed by user_data and the manual
steps are a fallback.

**Done when:** `terraform validate` passes and `bash -n scripts/backup-db.sh` is
clean. Note in the runbook that existing instances need the units installed once
by hand, since user_data does not re-run.

### P0-5 — Make the deploy deterministic
- [x] Done

**Files:** `.github/workflows/deploy.yml`

**Do:** three changes.
1. Add `concurrency: {group: deploy-prod, cancel-in-progress: false}` at the
   workflow level.
2. Replace `git reset --hard origin/main` with `git fetch origin && git reset
   --hard ${{ github.sha }}` so the deployed commit is the one CI tested.
3. Tag the built image with the commit SHA and keep the last three, so a
   rollback is a retag rather than a rebuild on a 2 GiB box. Change the final
   `docker image prune -f` to prune only untagged images older than a week.

**Done when:** `./scripts/check-deploy-config.sh` passes and the workflow file
parses (`yq . .github/workflows/deploy.yml > /dev/null` or equivalent).

---

## Phase 1 — Close the dead ends

Every item is something a user hits and cannot recover from. Two subagents: iOS
lane and web lane.

### P1-1 — iOS: the 401 handler is deallocated the moment it is installed
- [x] Done

**Files:** `ios/ConcertFinder/Core/Networking/APIClient.swift:25`,
`ios/ConcertFinder/App/ConcertFinderApp.swift:77`

`invalidationHandler` is a `private weak var`, and the `SessionInvalidationBridge`
passed to it is constructed inline in `start()` with nothing else retaining it.
It dies before the first request, so on a 401 the token is cleared but
`auth.state` stays `.signedIn` forever.

**Do:** store the bridge strongly on `AppContainer` (`private let
invalidationBridge: SessionInvalidationBridge`), and break the resulting cycle
with `[weak auth]` inside the closure. Leave `APIClient`'s property weak or make
it strong — either is fine once something else owns the bridge; say which you
chose under *Decisions taken*.

**Done when:** a unit test drives `APIClient` against a stub returning 401 and
asserts the handler fires.

### P1-2 — iOS: sign-out leaves the previous account's data on screen
- [x] Done

**Files:** `ios/ConcertFinder/Core/Auth/AuthController.swift:185`, `FeedModel`,
`SavedModel`, `ArtistsModel`, `LocationModel`, `ios/ConcertFinder/App/ConcertFinderApp.swift`

**Do:** add `func reset()` to each of the four models, clearing their collections,
error, filters (including the `cf.filters` UserDefaults blob), and any polling
state. Call all four from both `signOut()` and `handleSessionExpiry()`. While
here, assign `auth.onSignOut` in `AppContainer.start()` so expiry also
deregisters the APNs device — today only the Settings button does.

**Done when:** a test signs in, populates a model, signs out, and asserts the
model is empty.

### P1-3 — iOS: four models set `error` and one screen renders it
- [x] Done

**Files:** `FeedView.swift`, `SavedView.swift`, `ArtistsView.swift`

A 500 or decode failure on a cold feed leaves `events` empty, so the user is told
"Nothing coming up near you yet" when the server is down.

**Do:** render `model.error` as an `InfoBanner` above the list on all three
screens (the banner component already exists — add an `.error(String)` case if
needed). Split the empty state: if `error != nil` show "We couldn't load your
concerts" with a Try again button that calls `load()`; only show the quiet-city
copy when the load succeeded and returned nothing. Also fix the "pull to refresh"
advice on `ContentUnavailableView`, which is not scrollable — make it a button.

### P1-4 — iOS: first-run cards are dead
- [x] Done

**Files:** `ios/ConcertFinder/Features/Feed/FeedView.swift:78,81`

`.navigationDestination` is attached to `list`, but the `isFirstRun` branch
renders `FirstRunView`, whose `NavigationLink(value:)` has no matching
destination — taps do nothing and log a purple runtime warning.

**Do:** move both `navigationDestination` modifiers off `list` and onto the
enclosing `Group` (around line 25) so every branch is covered.

### P1-5 — iOS: push deep links never open the event
- [x] Done

**Files:** `ios/ConcertFinder/App/RootView.swift:77`, `FeedModel.swift`

`pendingEventKey` is written in three places and read once, at `RootView.swift:77`,
which only sets `selection = .feed`. It is never resolved to an `Event`, never
pushed, and never cleared — so a second notification for the same event is a
silent no-op because `onChange` sees an unchanged value.

**Do:** hold a `NavigationPath` on `FeedModel`. On `pendingEventKey` change:
switch to the feed tab, look the key up in `model.events`, fetching the feed
first if it is absent, append the event to the path, then set
`container.pendingEventKey = nil`. If the event cannot be found after a fetch,
land on the feed and show an `InfoBanner` saying the show is no longer listed.

### P1-6 — iOS: setting a location does not update the feed
- [x] Done

**Files:** `ios/ConcertFinder/Features/Location/LocationModel.swift`,
`FeedModel.swift`, `FeedView.swift`

`LocationModel.saveCity()` / `useCurrentLocation()` update only themselves.
`FeedModel.isUsingFallbackLocation` refreshes only inside `start()`, which does
not re-run when the pushed `LocationView` is popped. The user completes the exact
action the banner asked for and finds the same wrong-city results and the same
banner.

**Do:** give `LocationModel` an `onLocationChanged: (() -> Void)?` assigned in
`AppContainer` to call `feed.refreshLocationState()` and `feed.load()`. Fire it
after a successful save from either path.

### P1-7 — iOS: pull-to-refresh spends quota and swallows the throttle
- [x] Done

**Files:** `FeedView.swift:29`, `FeedModel.swift:165`

Pull-to-refresh calls `POST /me/concerts/refresh`, which is throttled to 15
minutes and spends per-user Ticketmaster quota. On 429 the reason is stored,
never shown, and `load()` is not called — so the list does not even re-read.

**Do:** make `.refreshable` call `load()` (a cheap snapshot re-read). Move the
rescan to an explicit toolbar button that disables itself until `retryAfter` and
shows the reason in a banner when refused.

### P1-8 — Web: an expired session is an unrecoverable dead end
- [x] Done

**Files:** `web/src/lib/api.ts`, `web/src/lib/auth.tsx:33`,
`web/src/hooks/use-concerts.ts:24`, all callers

`fetchMe` runs once on mount and nothing re-evaluates auth. A 401 renders
"Error: HTTP 401" while the header still shows the user's name.

**Do:** add a `signOut()` to `AuthProvider` that sets `{kind:'anon'}` (no network
call — the session is already gone). Export a module-level hook the provider
registers on mount so non-React code can reach it. Route **every** fetch in the
app through a single `apiFetch` wrapper in `api.ts` that calls it on 401 before
returning. `RequireAuth` already redirects on `anon`.

**Done when:** no `fetch(` calls remain outside `api.ts`.

### P1-9 — Web: a filter matching nothing hides the filter bar and lies
- [x] Done

**Files:** `web/src/pages/saved.tsx:31,50`

`FilterBar` renders only when `events.length > 0`, so a genre with no saved shows
unmounts the bar while the list prints "Nothing saved yet." — the user is told
their saves are gone with no way to undo the filter short of a reload.

**Do:** gate the bar on `state.kind === 'loaded'`, matching `concerts.tsx`. Pick
the empty copy by whether any filter is active: "No saved shows match these
filters" versus "Nothing saved yet."

### P1-10 — Web: a cold start over ten minutes renders a blank page
- [x] Done

**Files:** `web/src/components/concerts-list.tsx:23`,
`web/src/hooks/use-concerts.ts:96`

`isFirstTime` requires `refreshing` and `isEmpty` requires `computed_at`. Once
`MAX_REFRESH_POLLS` forces `refreshing:false` on a scan that has not yet produced
a snapshot, neither holds and the user sees the literal string "0 shows".

**Do:** `const isFirstTime = data.count === 0 && !data.computed_at;` regardless of
`refreshing`. When `isFirstTime && !data.refreshing`, switch the copy to "Still
building your feed — this is taking longer than usual. Reload to keep waiting."

### P1-11 — Web: optimistic mutations have no `catch`
- [x] Done

**Files:** `web/src/hooks/use-concerts.ts:190,216`,
`web/src/pages/subscribe.tsx:84,100`

All four `await mutatingFetch(...)` outside a try. A dropped connection rejects,
nothing rolls back, no `actionError` is set, and the star stays lit until a later
poll silently un-lights it.

**Do:** wrap each in try/catch, roll back the optimistic state, and set
`actionError` — the rollback path and the `ActionError` component both already
exist. Add an in-flight guard keyed by `dedup_key` / `artistID` so a double-click
cannot land POST and DELETE out of order.

### P1-12 — Guard both unchecked first-act indexings
- [x] Done

**Files:** `internal/jobs/push.go:204,228`,
`web/src/components/event-card.tsx:39`

Both index `Acts[0]` unguarded — in the worker it panics the goroutine, on the
web it takes the whole SPA to the error boundary. Note `push.go:213` already
checks the length, so the inconsistency is visible in the same function.

**Do:** guard both. An event with no acts should be skipped by the worker (with a
`slog.Warn`) and not rendered by the card.

---

## Phase 2 — Survive more than one user

These failures only appear with concurrency, which means you will not see them in
manual testing and they will all arrive at once.

### P2-1 — A scan persists using the context that is expiring
- [x] Done

**Files:** `internal/jobs/workers.go:109,198,209,236`

`Work` wraps ctx in the 5-minute `ScanBudget`, and `UpsertConcerts` /
`UpsertConcertSnapshot` then run on that same context. A scan that uses its full
budget — the expensive cold scan you most want to keep — fails both writes and
River retries the whole fanout, spending quota again for nothing. The `Release`
path at :127 already does this correctly.

**Do:** `persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx),
15*time.Second)` and use it for both writes and for `enqueueNotifications`.

**Done when:** a test with an already-expired scan context asserts the snapshot
is still written.

### P2-2 — Latitude and longitude are unvalidated
- [x] Done

**Files:** `internal/http/location.go:116`, `internal/http/concerts.go:171`

`radius_miles` is bounded 1–500; lat/lng are not bounded and are not checked for
NaN or Inf. Because River uniqueness is per `(user, location)` and
`GET /me/concerts` enqueues a scan whenever no snapshot exists for that key,
cycling locations fills the five-worker queue with five-minute jobs and starves
everyone else's scans, digests and pushes.

**Do:** reject non-finite values and anything outside ±90 / ±180 with a 400.
Then bound the churn: count distinct `location_key` values per user per UTC day
(a small table or a reuse of the rate ledger) and refuse past a generous ceiling
— 10 is plenty for a real person — with a 429 and a clear message.

### P2-3 — The connection pool is four connections, shared with River
- [x] Done

**Files:** `internal/db/pool.go:12`, `.env.example`, `docs/local-dev.md`

pgx defaults to `max(4, NumCPU)`, i.e. 4 on a t4g.small, for a pool that serves
River's LISTEN/NOTIFY notifier (which holds one indefinitely), its elector,
producer and completer, five workers, and every HTTP request.

**Do:** parse the URL with `pgxpool.ParseConfig` and set `MaxConns` (default 20,
overridable by `DB_MAX_CONNS`) and `MinConns` 2 in code, so it holds regardless
of what the connection string says. Log `pool.Stat()` every 60s at INFO. Document
the variable in `.env.example`.

### P2-4 — A transient APNs failure loses the notification permanently
- [x] Done

**Files:** `internal/jobs/push.go:110,136-142`

`RecordDigestSent` runs before the send loop, the loop logs transient errors, and
`Work` returns `nil` — so the dedup keys are burned and the retry sends nothing.
The at-most-once trade-off is right for a crash; it is not right for an error you
can see.

**Do:** track a `transient bool` across the loop and return a wrapped error when
set, so River retries. Keep `RecordDigestSent` where it is — the retry then
re-sends nothing, which is the wrong outcome — so instead move it to **after**
the send round, recording only the keys whose sends succeeded or were refused for
a dead token. Read `CLAUDE.md`'s already-sent-ledger section before changing this;
the channel argument stays mandatory.

**Done when:** a test asserts that a transient send failure returns an error and
leaves the keys unrecorded, and that a dead-token failure does not.

### P2-5 — The account-wide quota ledger fails open
- [x] Done

**Files:** `internal/rate/ledger.go:307,331-336`

Per-user fail-open is defensible; the account cap is the one Ticketmaster
actually enforces, so a Postgres blip during a fanout lets concurrent scans blow
past 5000/day — and the resulting upstream 403s are read as "this artist has no
shows".

**Do:** on an account-ledger write error, grant a conservative bounded block
(`min(granted, 50)`) instead of an unlimited one, and log at ERROR.

### P2-6 — Rate limiting exists only on `/api/auth`
- [x] Done

**Files:** `cmd/server/main.go:522`, `internal/http/subscribed_artists.go:98`,
`internal/http/{saved_concerts,account,email_prefs,location}.go`,
`internal/http/unsubscribe.go`

**Do:** three things.
1. Mount `auth.IPRateLimit` on the whole `/api` subtree at a looser bound than
   `/api/auth`'s, and a tighter per-user bucket on `/api/me/artists/search`,
   which proxies straight to Spotify's `/v1/search` — one user can get the app's
   client ID 429'd. Bound the length of `q`.
2. Rate-limit `/api/healthz` at the Caddy layer (it pings Postgres against a
   pinned 0.25 CU compute) and `/api/unsubscribe`.
3. Wrap the five handlers that decode `r.Body` directly in
   `http.MaxBytesReader` — `devices.go` and `auth/mobile.go` already do this
   correctly, follow those. Add length bounds on `dedup_key`, `artistID` and
   `display_name`, and a ceiling on saves and subscriptions per user.

### P2-7 — Session IDs are stored unhashed with a 90-day TTL
- [x] Done

**Files:** `internal/db/sessions.go:28,44`, `internal/auth/session.go`, new
migration

The stored value is the live credential, and it is also the iOS bearer token — so
every nightly `pg_dump` in S3 contains working credentials for every user.

**Do:** store `sha256(session_id)` and look up by hash. New migration adds a
`token_hash` column, backfills nothing (existing sessions are invalidated —
that is acceptable and should be noted in the PR description), and drops the old
column in a **later** migration, not this one (expand/contract, see P2-8's note
on rollback safety).

### P2-8 — Infrastructure safety valves
- [x] Done

**Files:** `docker-compose.prod.yml`, `infra/ec2.tf:64`, `Dockerfile`

1. `stop_grace_period: 45s` on `api`. Docker SIGKILLs at 10s against a 30s
   shutdown budget; a deploy during a scan kills the job and River's rescuer
   defaults to about an hour, during which `ByState` uniqueness blocks that
   user's retry.
2. `subnet_id = data.aws_subnets.default.ids[0]` reads index 0 of an *unordered*
   set — if it shifts, Terraform plans a replacement of the instance, destroying
   `/opt/concertfinder`, the `.env` and the volume. Pin the subnet by literal ID
   in `terraform.tfvars` and add `lifecycle { prevent_destroy = true }`.
3. Add explicit `metadata_options { http_tokens = "required",
   http_put_response_hop_limit = 1 }` — IMDSv2-required is an AMI default, not an
   assertion, and that role reads every secret under `/concertfinder/*`.
4. Pin base images by digest in the `Dockerfile` and `docker-compose.prod.yml`
   (`node:24-alpine`, `golang:1.25-alpine`, `distroless/static-debian12`,
   `caddy:2-alpine`). Record the digests under *Decisions taken*.

### P2-9 — First tests for the only primary source
- [x] Done

**Files:** new `internal/ticketmaster/*_test.go`

`ticketmaster`, `geocoding`, `affinity` and `cmd/server` have no tests at all,
while `concerts`, `rate`, `fallback` and `auth` are well covered.

**Do:** table tests against captured fixture JSON for date/time parsing across
timezones, festival detection, lineup extraction, the two-stage attraction
resolution, and the 429 / `Retry-After` policy. Fixture files, not inline
literals. Redaction from P0-1 gets a test here too if it does not already have
one.

### P2-10 — `.env` values are written unquoted and then sourced
- [x] Done

**Files:** `scripts/render-env.sh:73`, `scripts/backup-db.sh:40-42`

`render-env.sh` writes `printf '%s=%s\n'`; `backup-db.sh` does `set -a; .
"$ENV_FILE"`. A password or key containing a space, `$`, backtick or `;` breaks
— or executes. Compose's parser does not behave this way, so the app stays
healthy and only the unmonitored backup dies.

**Do:** single-quote the value in `render-env.sh` (escaping any embedded single
quote), which Compose strips, so both consumers are safe. Add a test case to
`check-deploy-config.sh` or a small shell test.

### P2-11 — Outbound-fetch hygiene in the fallback chain
- [x] Done

**Files:** `internal/fallback/http.go:24,46,60-100,145`

Three problems in one file. The hardcoded `UserAgent` still names a GitHub repo
that does not exist — `main.go` only rebuilds the UA for the MusicBrainz and
Nominatim clients, and `CLAUDE.md` is explicit that this is a contract, not a
formality. The artist-site fetcher will follow any URL a MusicBrainz "official
homepage" relationship names — MusicBrainz is a user-editable wiki — with no
scheme allowlist and no private-IP check, and caches the body. And two maps
(`last`, `robotsCache.data`) are unbounded and never expire in a process that
never restarts.

**Do:** thread the real UA through from `main.go`. Reject non-`https` schemes and
check the resolved IP against loopback, private and link-local ranges in a
`DialContext` hook (which also covers redirects). Bound both maps with a simple
LRU and a TTL.

### P2-12 — Two data-layer gaps
- [x] Done

**Files:** new migration, `internal/db/janitor.go:243`,
`internal/jobs/workers.go:635`

1. `PruneStaleSnapshots` filters on `s.updated_at` but the only index is on
   `computed_at`. Add the index. Note the migrator runs each migration in a
   transaction, so `CREATE INDEX CONCURRENTLY` is not available; this table is
   small enough that a plain index is fine — say so in the migration comment.
2. The janitor prunes expired `mb_url_cache` and `venue_geo_cache` negatives but
   has no step for `artist_resolutions` rows with a NULL attraction ID past
   `NegativeResolutionTTL` — one row accumulates per artist ever asked about,
   against a 0.5 GB storage cap. Add the prune step, following the existing
   per-step failure isolation.

---

## Phase 3 — Fix the first five minutes

The product work. Everything here aims at one thing: a stranger's first session
ending with them understanding what the app did for them.

Do P3-2's endpoint and P3-3's response field yourself first — both clients depend
on them — then split the client work across two subagents.

### P3-1 — Ask for location before the first scan
- [x] Done

**Files:** `internal/http/auth.go` (`OnLoginSuccess`), `FirstRunView.swift`,
`web/src/pages/concerts.tsx:80`, `web/src/components/location-bar.tsx`

Sign-in enqueues a pre-warm scan against the *default* location; the user then
sets their real city, which produces a new `location_key` with no snapshot and
enqueues a second full scan. The first — up to five minutes and a chunk of a
250-call allowance — is discarded, and the user waited through it.

**Do:** stop enqueuing the pre-warm scan when the user has no location of their
own (`is_default: true`). On iOS, make "where are you?" the first step of the
first-run flow, and start the scan when it is answered. On web, show a
pre-prompt card explaining why before triggering the browser geolocation dialog
— a denial there is permanent, so it should never be a surprise.

### P3-2 — A signed-out "shows near you" view
- [x] Done

**Files:** new `internal/http/discover.go`, `cmd/server/main.go`,
`web/src/pages/login.tsx`, new `web/src/pages/discover.tsx`, `FirstRunView.swift`

Nothing is visible before you connect Spotify, which costs four things at once:
App Review needs a demo account to see anything, the cold-start wait has nothing
to fill it, the web app has no marketing surface, and a visitor not on the
Spotify allowlist bounces off a 403 having never seen the product.

**Do:** add `GET /api/discover?lat=&lng=&radius=` — unauthenticated,
IP-rate-limited, **served entirely from `concert_cache`**. It must never trigger
an upstream fetch and never touch the rate ledger; an unauthenticated endpoint
that spends account quota is a quota drain with a URL. Cap the result at 50 rows,
order by date. Return the existing `Event` shape so both clients reuse their
models.

Render it on the web login page below the sign-in button, and behind the iOS
first-run screen as the backdrop, both labelled clearly as "popular shows near
you" rather than anything implying personalisation. If the cache is empty for
that area, render nothing — no empty state, no error.

### P3-3 — Show why each artist is in the feed
- [x] Done

**Files:** `internal/http/concerts.go`, `internal/affinity`, `Models.swift`,
`web/src/lib/types.ts`, `EventCard`/`event-card.tsx`

`GET /api/me/affinity` is the one endpoint no client calls, so the product's
whole differentiator — *we read your listening and scored these artists* — never
appears in either interface. A user cannot tell the feed apart from a generic
local-listings site.

**Do:** add an **additive** `reason` string to each `Act` in the concerts
response, derived from the affinity profile's strongest contributing signal:
"You follow them", "#7 in your top artists", "You've saved 3 of their albums",
"Recently played". One short line, rendered under the artist name on the card on
both clients. Do not persist anything new — the profile is already in the
snapshot path, and `CLAUDE.md`'s no-raw-Spotify-persistence rule still holds.

### P3-4 — Prime push at the moment it is earned
- [x] Done

**Files:** `FirstRunView.swift:164`, `ArtistsView.swift:146`,
`PushRegistrar.swift`, `FeedModel.swift`

Both screens promise notifications. Push is opt-in, defaults off, and is only
reachable from Settings — nothing in either flow primes it.

**Do:** after the first scan completes successfully and the feed has results,
show a single soft prompt explaining what will be notified, and request
authorization only if accepted. Show it once ever (`FirstRunTracker`). Until it
ships, the copy on both screens must not promise a notification the app cannot
send.

### P3-5 — Introduce save versus subscribe
- [x] Done

**Files:** `EventCard.swift`, `web/src/components/event-card.tsx:198,213`,
`FeedView.swift` empty state

The affordances are individually readable and correctly per-act, but nothing
introduces the pair, and subscribe — the retention action — is buried on a
separate tab. The web icons are also backwards: `StarOff` / `BellOff` for the
*unset* state, and a struck-through bell conventionally means "muted".

**Do:** use outline versus filled for unset/set on web, not the `Off` variants.
Add one dismissible hint above the first card naming both actions in a sentence.
In the empty-feed state, surface "Get alerts when an artist announces a show" as
the primary action, since it is the most useful thing a user can do there.

### P3-6 — Web: keep the filter bar mounted across loads
- [x] Done

**Files:** `web/src/hooks/use-concerts.ts:72`, `web/src/pages/concerts.tsx:138`

Every filter change sets `{kind:'loading'}`, which unmounts the filter bar and
the list, drops keyboard focus to `<body>`, and scrolls to top. Setting a date
range costs a full round trip between the two inputs. The generation counter that
discards stale responses is already correct, so this is purely a state-shape
choice.

**Do:** keep the previous `data` and add a `stale: boolean`. Render the bar from
last-known facets throughout, with `aria-busy` on the list while stale.

### P3-7 — Web: the missing public surface
- [x] Done

**Files:** `web/index.html`, new `web/public/`, `web/src/App.tsx:69`,
`internal/http/spa/spa.go:56`, `web/vite.config.ts`

`index.html` is twelve lines with a title only; there is no `web/public/`; and
the SPA catch-all returns `index.html` with a **200** for any unmatched path, so
`/robots.txt` and `/favicon.ico` serve HTML and a mistyped URL silently lands on
the feed.

**Do:** add `favicon.svg`, `robots.txt`, `og.png` and a web manifest under
`web/public/`. Add `description`, `og:*`, `twitter:card` and `theme-color` to
`index.html`. Make the SPA handler return a real 404 for paths under
`/robots.txt`, `/favicon.ico`, `/sitemap.xml` and any path with a file
extension. Replace the `*` → `/` catch-all route with a real 404 page. Add
`React.lazy` for `privacy`, `terms`, `subscribe` and `app-callback`, which are
dead weight on the feed's first paint. Drop the three unused Radix deps.

### P3-8 — iOS polish
- [x] Done

Each is small and independent:
- Settings toggles fire their handlers on programmatic writes — opening Settings
  syncs `pushOptIn` from the profile, tripping `.onChange` into an authorization
  request and a PUT. Guard with an `isSyncing` flag.
- Round coordinates to two decimals in `LocationModel` before the PUT. The
  privacy manifest declares coarse location; `desiredAccuracy` limits what you
  request, not what you receive. The server rounds anyway.
- Give `AuthController.restore()`'s `siteInfo()` call a 5s timeout, or resolve
  the session first and run the build check concurrently. Today launch sits
  behind URLSession's 60s default on a captive portal.
- Gate `MainTabView`'s split-view switch on `userInterfaceIdiom == .pad`, not
  `horizontalSizeClass` — landscape on a Max-class iPhone is `.regular`, so
  rotating tears down the navigation stack.
- `catch is CancellationError` in `FeedModel.load()` and `refresh()`, as
  `ArtistsModel.runSearch` already does. Switching tabs currently sets an error
  and stops polling.
- Restore `fromDate` / `toDate` in `FiltersSheet`, not just `useDateRange`.
- Drive `EventDetailView` off the model instead of a local `@State` copy, so a
  failed save reverts in both places.
- Add an `AccentColor` asset set to `DesignSystem.accentGreen`, which is
  currently unused while every bookmark and bell renders system blue.
- Check `SecItemAdd`'s `OSStatus` in `Keychain.swift:44`.
- Hide the "update required" App Store button until there is a real app ID; the
  current `itms-apps://apple.com/app` opens the store to nothing on a screen the
  user cannot dismiss.

### P3-9 — Web polish
- [x] Done

Each is small and independent:
- Map errors by status instead of `setErr(await r.text())` in
  `location-bar.tsx:41` and `settings.tsx:34,144,232` — a Go `http.Error` string
  or a proxy's HTML currently lands in the interface.
- Clamp radius to 1–500 on save; an emptied number input currently sends
  `radius_miles: null`.
- Make the location editor a `<form>` with a submit handler, so Enter works.
- Raise mobile nav tap targets to 44px (`layout.tsx:90`), matching the `coarse:`
  treatment used everywhere else.
- Bump `generation.current` at the top of the search effect in
  `subscribe.tsx:51`, not inside the debounce timer — clearing the box currently
  lets a resolved fetch repopulate it.
- Resume the poll on `visibilitychange` / `online`, pause it when the tab is
  hidden, and show a message when it gives up instead of leaving the spinner
  animating.
- Suppress the "try clearing filters" empty message when `isPartial` — the
  partial banner and that message currently contradict each other.
- Use `Link` rather than `<a href>` for in-app navigation
  (`settings.tsx:109`, `login.tsx:29`).
- Make the theme menu a `DropdownMenuRadioGroup` so state is announced.
- Re-render `timeAgo` on an interval; "updated 1m ago" is frozen for the session.
- Add a skip-to-content link.

---

## Phase 4 — Human tasks (not for Claude)

Nothing here is code. Several have multi-week lead times, so they should already
be moving while the phases above are implemented.

- [ ] **Rotate the Ticketmaster API key, and the Songkick one too**, after P0-1
      ships. Assume both are in log history — Songkick's leaked on every
      unexpected status as well as on transport errors.
- [ ] **Escrow `ENCRYPTION_KEY`** somewhere that is not SSM. Every backup of
      `users` is AES-GCM ciphertext; losing that one parameter makes them
      unrecoverable.
- [ ] **Do one timed restore drill** into a scratch Neon branch. The elapsed time
      is the real RTO. The backup has never been restored.
- [ ] **Submit the Spotify Extended Quota Mode application.** Multi-week
      turnaround, approval not guaranteed, and nothing downstream shortens it.
      This is the long pole.
- [ ] **Reissue the APNs key as Sandbox & Production**, then set
      `APNS_ENVIRONMENT=sandbox,production` once. See `docs/ios-app-plan.md` §0.
- [ ] **Build the allowlisted demo account** with plausible listening history and
      verify sign-in end to end on a clean device.
- [ ] **Move Terraform state to an S3 backend with locking.** It is local and
      unlocked today, holding the SES SMTP secret and the break-glass key.
- [ ] **Add port-22 ingress to the security group, or document its absence** —
      the break-glass key pair is attached but unusable, and that is a thing to
      learn before an outage, not during one.
- [ ] **Enable Dependabot** (gomod, npm, docker, actions) and add `govulncheck
      ./...` to the CI test job.
- [ ] **App Store Connect**: privacy labels, screenshots, review notes. §9 and
      §10.1.4 of `docs/ios-app-plan.md` already have the text.
- [ ] **Decide what launch means at ~20 concurrent users.** A public listing
      against Ticketmaster's 5000/day ceiling degrades everyone's feed silently.
      A waitlist with a per-day admission cap is the honest version and doubles
      as capacity control.

---

## Explicitly out of scope for this run

Android. Additional ticketing sources. `since` on `/me/concerts` (your own doc
says measure first — still true). Analytics, which changes the privacy labels and
should wait until after the first submission. Widgets and Live Activities. Any
change to the affinity scoring model itself.

---

## Decisions taken

_Append one line per judgement call, with the task ID._

- **P0-1** — `redactURLError` is duplicated in `internal/ticketmaster` and
  `internal/fallback` rather than shared, per the task's own instruction not to
  add a package for it. It uses `errors.As`, so a `*url.Error` wrapped by
  something else is still found.
- **P0-1** — Songkick's `default:` branch was interpolating the whole request
  URL into its error (`"songkick %d: %s", code, u`), which leaked the key on
  every unexpected status, not just on transport errors. Fixed alongside.
- **P0-2/P0-5** — `docker-compose.prod.yml` now pins
  `image: concertfinder-api:latest` on the api service. Compose otherwise
  derives the name from the project directory, and both the SHA tagging and the
  retention sweep address the image by name. `check-deploy-config.sh` asserts
  the pin.
- **P0-5** — The keep-last-three retention lives in `scripts/prune-images.sh`
  rather than inline in the SSM command list, so `check-deploy-config.sh` can
  `bash -n` it and assert its executable bit like the other never-runs-locally
  scripts. `latest` is excluded from the count because it is a moving pointer
  at the newest SHA and counting it would evict a real rollback target.
- **P0-3** — `var.alert_email` defaults to `""` and falls back to
  `var.ses_verified_recipient` through a local, rather than being a required
  variable. `terraform.tfvars` is gitignored, so a required variable would break
  the existing working copy; the fallback is the same address `CONTACT_EMAIL`
  already uses.
- **P0-3** — The billing alarm gets its own topic (`billing_alerts`) in
  us-east-1. An alarm can only publish to a topic in its own region, and the
  `EstimatedCharges` metric only exists in us-east-1.
- **P0-4** — `BACKUP_HEARTBEAT_URL` is optional and unset by default; the
  monitor to point it at is an account someone has to create, which is a Phase 4
  human task. Documented in `.env.example` and `docs/aws-deploy.md` §7.
- **P1-1** — `APIClient.invalidationHandler` was made **strong**, not left
  weak. Weak made the handler's lifetime a property of whoever happened to hold
  it, and the answer had been "nobody" — a failure with no symptom other than
  401s going unreported forever. Strong makes the client's own retention
  sufficient, so a future call site that forgets to keep a reference cannot
  reintroduce it. The cycle (client → bridge → controller → client) is broken at
  the `[weak auth]` capture in `AppContainer.init`, where it is visible.
- **P1-2** — `AuthController.onSignOut` changed from `(@MainActor () -> Void)?`
  to `(@MainActor () async -> Void)?`, and `signOut()` now awaits it **before**
  clearing the token. Deregistering the APNs device is an authenticated call, so
  a fire-and-forget `Task` raced the token clear and the loser 401'd — stamping
  "your session expired" over a sign-out the user asked for.
- **P1-2** — `PushRegistrar.deregister()` nils `deviceToken` *before* the
  request rather than after. On the expiry path that request 401s and re-enters
  the sign-out handler; a still-set token made that a second identical request.
  Clearing first is also what terminates the re-entry.
- **P1-6** — `onLocationChanged` fires from `saveRadius()` as well as the two
  paths the task named. The radius is applied upstream at fetch time, so a wider
  one is a different result set rather than a different filter over the same one
  — the same defect, same fix. A latent bug was fixed alongside: `location =
  try? await api.setLocation(...)` wiped the saved location to `nil` on any
  failure, which the view reads as "you haven't set a location".
- **P1-8** — Public endpoints are exempted from the 401 sign-out per call site,
  via an explicit `apiFetch(url, init, { publicEndpoint: true })`, not by URL
  matching inside `apiFetch`. A path list in the wrapper would silently
  misclassify any endpoint added later. Two call sites use it: `/api/site-info`
  (privacy + terms, both linked from the login screen) and `/api/auth/me`, whose
  401 *is* the signed-out answer and which would otherwise re-enter the
  provider's own state update from inside its first fetch.
- **P1-10** — `ConcertsList` gained an `awaitsFirstScan` prop (default `true`,
  `false` on the saved page). `/api/me/saved-concerts` never sends
  `computed_at` — `internal/http/saved_concerts.go` leaves `ComputedAt` nil and
  hardcodes `Refreshing: false` — so the new `isFirstTime` would have told a
  user with zero saves that their feed was still being built, contradicting
  P1-9's "Nothing saved yet." on the same screen.
- **P1-12** — The worker's guard is a `withActs` helper rather than an inline
  `continue`, mirroring `forEnvironment` in the same file, so it is testable
  without a database or an APNs client. `notificationFor`'s inconsistent inner
  `len(ev.Acts) > 0` check (which guarded the dedup key while two other
  indexings went unguarded) is gone in favour of a stated precondition.

- **P2-1** — The persist step is an extracted `persistScan` behind a narrow
  `concertStore` interface rather than an inline `context.WithoutCancel`, so
  the behaviour ("an expired scan context still writes") is testable without a
  database or a river client. `enqueueNotifications` got the same detached
  context: it runs after a full `ScanBudget` of searching, and an enqueue
  refused because the scan's own context expired loses the notification with
  nothing revisiting that snapshot's net-new set.
- **P2-2** — The churn ceiling is a new table (`user_location_visits`,
  migration 0020) keyed `(user_id, day, location_key)`, not a second use of
  `rate_ledger`. What is being bounded is set membership; a counter cannot tell
  a revisit from a new location, so someone toggling between home and work
  would spend the allowance twice every morning and be locked out by lunchtime.
  Count and insert are one statement so two tabs cannot straddle the check.
  Coordinates are also validated at the third entry point the task did not name
  — `config.Validate` on `USER_LATITUDE`/`USER_LONGITUDE`, the deployment-wide
  fallback location.
- **P2-3** — `DB_MAX_CONNS` is read in `internal/config`, not in `pool.go`,
  because `os.Getenv` outside `internal/config` appears nowhere else in the
  repo outside tests. Cost: `db.Connect` gained a third parameter (one call
  site). The stats goroutine's lifetime is owned by `main.go` rather than
  `Connect`, since pgxpool exposes no "pool closed" signal and the ctx
  `Connect` receives is a 10s connect deadline that is cancelled immediately.
- **P2-4** — An event is all-or-nothing across devices: one transient rejection
  holds back every act on that bill, so the retry re-sends the whole card
  rather than half of it. The cost is a duplicate on devices that did receive
  it, which `CollapseID` absorbs. This also inverts the crash trade — a crash
  between send and record now costs a duplicate rather than a lost
  notification, which is the milder direction. Recorded in `CLAUDE.md`, which
  had never documented the original ordering.
- **P2-5** — The un-granted remainder is refunded to the *per-user* ledger when
  the account write fails. Not strictly asked for; without it the user stays
  charged for calls the block can never make, draining their own cap over
  someone else's outage.
- **P2-6** — Rate limits: `/api` 20/s burst 60; `/api/healthz` 1/s burst 10;
  `/api/unsubscribe` 2/s burst 20; `/me/artists/search` **per user** 1/s burst
  10; `/api/auth` unchanged at 5/s burst 20. Body cap 4 KiB everywhere (matches
  `devices.go`). Lengths: `dedup_key` 128, `artistID` 64, `display_name` 200,
  search `q` 100. Ceilings: 1000 saved concerts and 500 subscribed artists,
  refused with **409** (waiting does not help; unsaving does), and 10 distinct
  locations per UTC day with **429**. The ceilings are enforced *inside* the
  insert — a handler-side count leaves a window a looping client lives in — and
  re-saving or renaming at the cap still succeeds, so idempotency does not
  start failing once a list fills.
- **P2-6** — Done in Go middleware, not Caddy, contrary to the task's wording.
  The stock `caddy:2-alpine` image ships no `rate_limit` directive; it is a
  third-party module needing an `xcaddy` build, and P2-8 has just pinned that
  image by digest, so adding one would mean owning a custom image and its
  update path. Verified rather than assumed.
- **P2-6** — `UserRateLimit.Middleware` fails **closed** with a 401 when
  mounted outside `RequireUser`. One shared bucket would be a global limit
  wearing a per-user label.
- **P2-7** — `sessions.id` could not be dropped in a later migration as the
  task's expand/contract note assumed: it is the primary key and
  `mobile_auth_codes.session_id` cascades off it. Instead `id` became a fresh
  opaque UUID and the new `token_hash` carries the credential, which achieves
  the same thing (the raw token is never written) with nothing left to drop.
- **P2-7** — The mobile flow got a better arrangement than the brief sketched.
  `/api/auth/callback` writes the app's session row with `token_hash = NULL` —
  it exists, so the FK cascade is untouched, but authenticates nobody — and
  `POST /mobile/exchange` mints the token and claims the row in one
  `UPDATE … WHERE token_hash IS NULL … RETURNING`. `mobile_auth_codes` never
  holds a working credential, and single use is enforced by the same statement.
- **P2-8** — `subnet_id` defaults to `""` and falls back through a `local` to
  the old data-source lookup, matching the `var.alert_email` precedent, because
  `terraform.tfvars` is gitignored and a newly-required variable would break
  the existing working copy.
- **P2-8** — Base images pinned to multi-arch **index** digests, not per-arch
  ones: the build host is arm64 (t4g.small). `node:24-alpine@sha256:e67514e5…`,
  `golang:1.25-alpine@sha256:1ae0735f…`,
  `gcr.io/distroless/static-debian12@sha256:d75cdd72…`,
  `caddy:2-alpine@sha256:5f5c8640…`. Re-resolve with
  `docker buildx imagetools inspect <ref> --format '{{.Manifest.Digest}}'`.
- **P2-10** — `render-env.sh` single-quotes values and **refuses** a value
  containing a single quote rather than escaping it. Verified against
  `docker compose config` rather than reasoned about: the shell's `'\''` idiom
  is a hard parse error for compose (`unexpected character "\" in variable
  name`), and the alternative — double quotes with backslash escapes — agrees
  with bash on `\`, `"` and `$` but diverges on the backtick, where compose
  keeps the backslash and bash strips it and runs the command substitution.
  There is no encoding both parsers read alike, so the one value single quotes
  cannot carry fails at render time, by name, instead of on the instance
  mid-deploy. Two preflight checks cover it, both confirmed to fail without
  the fix.
- **P2-11** — `https`-only is a deliberate coverage loss: MusicBrainz lists
  plenty of `http://` official homepages, and those artists now fail the
  fallback chain instead of being fetched. The SSRF guard lives in
  `DialContext` and dials the *resolved* literal, which covers redirects and
  closes the DNS-rebinding gap between check and connect. The Songkick client's
  hardcoded User-Agent was threaded too — same defect, same fix.
- **P2-9** — `internal/ticketmaster`'s credential redaction was incomplete and
  I closed it rather than only testing it. The unexpected-4xx branch
  interpolates the upstream response *body* into an error that
  `internal/concerts/search.go` logs verbatim; Ticketmaster's own bodies are
  clean, but a WAF or proxy in front of it answers with a page quoting the
  full request URL, `apikey=` included. `Client.scrubKey` removes the client's
  own key by exact match rather than by pattern. The new test fails without it.

- **P3-1** — Only the *pre-warm* was removed. The SWR read path is untouched,
  which is what makes this safe: a feed read on a missing snapshot still
  enqueues the scan, so the scan simply starts when the user names a place
  instead of before they have. `OnLoginSuccess` also now returns on a failed
  location lookup rather than falling back to the deployment default — the
  whole point is not to scan a city nobody asked for.
- **P3-2** — The reader and the writer of `concert_cache` share one constant
  (`concerts.CachePrefixTicketmaster`, threaded through `cacheKey`). A prefix
  that agreed only by coincidence would return an empty list forever, and the
  endpoint swallows its own failures by design, so there would be nothing in a
  log to see.
- **P3-2** — Freshness is `DiscoverCacheMaxAge` (7d), deliberately **not**
  `CONCERT_CACHE_TTL_HOURS` (12h). That TTL decides when a *scan* refetches;
  applying it here would empty the login page every afternoon. Seven days
  matches the janitor's prune horizon, so this reads exactly the rows that
  still exist, and every event in them is filtered against today anyway.
- **P3-2** — `FromCachedTicketmaster` decodes **location-independently** and
  `Near` filters per request. The decoded candidate set is a process-wide
  5-minute cache, so filtering at decode time would serve the second visitor
  the first visitor's city. The refresh also runs on a detached context with
  its own 5s deadline: the result is shared, so one browser navigating away
  mid-load must not cancel the read three others are waiting on. A failed load
  sets a 30s backoff, or a database outage becomes one timeout per request,
  serialised behind the cache mutex.
- **P3-2** — Discover acts carry **no artist ID**. Every other ID in
  `concerts` is Spotify's, and a Ticketmaster attraction ID in that field is a
  save or a subscribe pointed at an artist that does not exist. Both clients
  then drop the star and bell entirely rather than rendering dead ones.
- **P3-3** — `spotify.ArtistSignals` lives in the existing affinity profile
  blob. It is derived data — counts over signals already scored, naming no
  track, album or playlist — so it stores no new Spotify Content, and an old
  profile decodes with every field zero, which reads as "no reason to show"
  rather than a wrong one.
- **P3-3** — The handler calls `affinity.Service.ReasonsFor`, which wraps a
  new `LoadCached`. `LoadOrCompute` on that path would turn the request the
  frontend polls every 10s into a six-endpoint Spotify fan-out with a 60s
  timeout; a missing profile costing one line on a card is the right trade.
- **P3-3** — Deliberately **not** applied to `/me/saved-concerts`. A save
  outlives the profile that produced it — that is why the saved list is read
  from `user_saved_concerts` and not from the snapshot — so "#7 in your top
  artists" there would be a claim about a profile that may no longer contain
  the artist at all.
- **P3-3** — The golden fixtures now carry `reason` on several acts and
  deliberately not on one, and a new `discover.json` fixture pins the
  signed-out shape. Both new response shapes are therefore covered by the
  Go↔Swift contract check rather than by two independent readings of a doc.
- **P3-1 (iOS)** — The gate is first-run only (`!FirstRunTracker.hasCompleted`
  and `is_default`). An established user who never set a location must still
  get a feed, and a failed `/me/location` leaves the flag false, i.e. it loads
  — the safe direction. `continueWithoutLocation()` keeps the step from being
  a dead end for someone who declines and does not know what to type.
- **P3-8 (iOS)** — The Settings toggle guard is **not** an `isSyncing` flag as
  the task worded it. SwiftUI delivers `.onChange` on the *next* update pass,
  by which point a flag set and cleared inside the sync is already false. The
  handler instead refuses a value that already equals the profile's — which is
  exactly what a sync writes and what a tap never does. Stateless, and correct
  on the failure path too, where the revert re-syncs.
- **P3-4 (iOS)** — The prompt is recorded as offered on *showing*, not on
  accepting: "once ever" has to mean the app asks once and takes silence for
  an answer. `SignedInUITests` now suppress it (and the save/subscribe hint)
  by launch argument, or the sheet would cover the feed at exactly the moment
  those tests wait for.
- **P3-3 (iOS)** — The reason line **replaces** the genres line on an act
  rather than adding a third; a six-act festival card is otherwise most of a
  screen. No reason falls back to genres exactly as before.
- **P3-2 (web)** — The signed-out coordinate is hardcoded to New York City,
  matching `.env.example`'s fallback. A visitor has no session, and prompting
  for browser geolocation on the login page is exactly the unannounced dialog
  P3-1 removes — worse there, because the one question the browser allows
  would be spent before anyone has seen what the app does. NYC is also the
  densest US market, which matters for an endpoint that can only return what
  someone else's scan already cached.
- **P3-6 (web)** — On a failure at the start of a new generation the existing
  data is kept only while the backoff retries run; once they are exhausted the
  error replaces it, because what is on screen answers a query the user has
  since changed. `patchActs` carries `stale` through, so an optimistic star
  does not make an in-flight fetch look landed.
- **P3-9 (web)** — The poll's give-up state is a new `pollStopped` flag *and*
  a forced `refreshing: false`: a spinner that merely stops is
  indistinguishable from one that finished. Pausing on a hidden tab also makes
  `MAX_REFRESH_POLLS` count *visible* polls, so a backgrounded tab no longer
  burns its ten-minute ceiling while nobody is watching.
- **P3-7 (web)** — The SPA's new 404 rule is "the path has a file extension",
  not a list of filenames: every route in `App.tsx` is extension-free, so the
  extension is the whole test and it covers `/robots.txt`, `/favicon.ico` and
  `/sitemap.xml` without naming them. The cost — a future route with a dot in
  it would 404 silently — is stated in the comment beside it. An unknown
  extension-free path still answers 200; the new `NotFoundPage` is what tells
  the user, because the handler cannot tell a typo from a route without
  duplicating the route table.
- **P3-7 (web)** — `Handler()` was split into `handlerFor(fs.FS)` so the
  routing rules test against an `fstest.MapFS`. The rule that matters is what
  happens to files that are *absent*, and `static/` holds one placeholder.
- **P3-7 (web)** — `icon-192.png`/`icon-512.png` and an `apple-touch-icon`
  link were added beyond the brief: a manifest whose only icon is an SVG is
  not installable in Chrome and is ignored by iOS Add-to-Home-Screen, so the
  manifest would have been decorative. `og.png` needed a square-wrapper
  workaround because `qlmanage` top-aligns a non-square SVG in a square
  thumbnail; the recipe is a comment in `og.svg` rather than tribal knowledge.

## Deferred

_Append anything skipped, with the task ID and one sentence of why._

- **P1-7** — `FeedModel.canRescan` reads `Date()` in a computed property, so the
  toolbar button does not re-enable at the exact instant `retryAfter` lapses; it
  re-enables on the next model change (a poll, a pull, a filter change, a return
  to the tab). A timer would close the gap and was judged gold-plating.
- **P1-3/P1-5** — The error, throttle and missing-event banners render inside
  `list`, so they are not visible when the feed is completely empty. The empty
  state carries its own error copy and Try again button instead, which covers
  the same ground on that screen.

- **P2-9 / dedup identity** — Ticketmaster event dates are stored as UTC
  *instants* and keyed as UTC *calendar days*, which is wrong for most US
  shows and is not safe to fix inside this run. `internal/ticketmaster/events.go`
  parses `dates.start.dateTime` (which TM sends as `Z`) and never reads
  `localTime` or `dates.timezone` — neither field is in the struct — while
  `concerts.DedupKey`/`EventKey` format `date.UTC()`. A 20:00 Pacific show on
  the 15th has a `dateTime` of `…16T03:00Z` and keys to the **16th**. Worse, a
  `timeTBA` event falls back to `localDate`, which parses to UTC midnight — the
  *local* day — so the same show's `dedup_key` **changes** the moment
  Ticketmaster publishes a set time. `dedup_key` is the primary key of
  `concerts` and half of `user_saved_concerts`', so that orphans the save, and
  because `user_digest_sent` is keyed on it the user is re-notified about a show
  they were already told about. The same divergence exists across sources:
  `fallback.parseTime` reads naive JSON-LD timestamps as UTC, so an official
  site's 20:00 keys to the local day while TM's row for the same show keys to
  the next one, and the merger never collapses them. Fixing it means changing
  the dedup identity and migrating the key across every existing save — a
  product decision, not a hardening task. `TestSearchEventsStartIsAnInstantNot
  ALocalCalendarDay` pins the current behaviour and says explicitly that it
  documents rather than blesses it.
- **P2-9 / `Retry-After` HTTP-date form** — RFC 7231 permits
  `Retry-After: Wed, 21 Oct 2026 07:28:00 GMT`; `retryAfter` parses only the
  delta-seconds form, so such a response falls through to sub-second backoff —
  which `CLAUDE.md` warns turns a soft limit into a ban. A small fix, but a
  behaviour change nobody asked for in this phase; pinned as current behaviour
  in the retry table instead.
- **P2-9 / pagination** — `SearchEvents` sets `size=100` and never reads
  `page.totalPages` or follows `_links.next`, so an attraction with more than
  100 events in radius is silently truncated. Almost certainly harmless at the
  radii in use, but it is unbounded-silent rather than bounded-loud.
- **P2-9 / 429 cancelled mid-backoff** reports "retries exhausted" rather than
  the context error, losing the distinction in exactly the case (scan hit
  `ScanBudget` while rate-limited) where a log would want it. Cosmetic.
- **P2-11 / `musicbrainz.go` default UA** still names
  `github.com/peter3605/concertFinder`. `main.go` always passes a real
  User-Agent so the default is unreachable in production; left alone rather
  than fixed blind, since unlike the other two that repo may actually exist.

- **P3-8 / `EventDetailView` from the Saved tab** — the detail view is now
  driven off `FeedModel`, so a show opened from **Saved** that is not in the
  loaded feed falls back to the pushed event and no longer flips its bookmark
  optimistically. Routing that toggle through `SavedModel` is the real fix and
  is larger than the bullet it came from.
- **P3-2 / discover coverage follows other people's scans.** The endpoint can
  only return what some signed-in user's scan already cached, so the section
  is empty in any city nobody uses the app in — including, on a fresh
  deployment, all of them. That is the correct trade for an endpoint that must
  never reach an upstream API, but it means the login page's most convincing
  element is the one that arrives last.

## Progress log

_One line per session: date, phases completed, anything the next session needs._

- **2026-08-31 — Phase 0 complete** (P0-1…P0-5), on branch `production-hardening`.
  New files: `scripts/prune-images.sh`, `internal/ticketmaster/client_test.go`,
  `internal/fallback/songkick_test.go`. Verified: `go build/vet/test`,
  `npm --prefix web run lint`, `./scripts/check-deploy-config.sh`,
  `terraform fmt -check && terraform validate`. Nothing applied, nothing
  deployed. Two follow-ups for a human, already listed in Phase 4: the
  Ticketmaster **and Songkick** keys should be rotated now that P0-1 has shipped
  (assume both are in log history), and the SNS email subscriptions P0-3 creates
  sit in `PendingConfirmation` until someone clicks the link on the first apply.
  Next: Phase 1, two subagents (iOS lane P1-1…P1-7, web lane P1-8…P1-11), with
  P1-12 done by the main session afterwards.
- **2026-09-01 — Phase 1 complete** (P1-1…P1-12), on branch
  `production-hardening`. Two subagents, iOS lane and web lane, with P1-12 done
  by the main session. New file: `ios/ConcertFinderTests/SessionLifecycleTests.swift`
  (covers P1-1, P1-2, P1-5, P1-6, P1-7). Also added
  `TestWithActsDropsEmptyEventsAndKeepsTheRest` to `internal/jobs/push_test.go`.
  Verified, serially: `go build/vet/test`, `npm --prefix web run lint && npm
  --prefix web run build`, and `xcodegen generate && xcodebuild test` on the
  iPhone 17 simulator — TEST SUCCEEDED, and the new suite confirmed present in
  the xcresult rather than assumed from a green exit. `check-deploy-config.sh`
  and terraform were not run: Phase 1 touched neither lane. Nothing applied,
  nothing deployed.

  Two things Phase 2 or 3 may want to pick up, both found while doing Phase 1
  and both recorded above rather than acted on: `/api/me/saved-concerts` diverges
  from `/me/concerts` by never sending `computed_at` (see the P1-10 decision),
  and `FeedModel.canRescan` is time-dependent without a timer (see *Deferred*).
- **2026-09-01 — Phase 2 complete** (P2-1…P2-12), on branch
  `production-hardening`. Four subagents in three waves (infra lane; Go lane A;
  then Ticketmaster tests and Go lane B), all reviewed hunk by hunk by the main
  session. New files: `migrations/0018_session_token_hash.*`,
  `0019_snapshot_updated_at_index.*`, `0020_location_visits.*`,
  `internal/http/limits.go`, `internal/fallback/fetch_guard_test.go`,
  `internal/auth/{session_hash,ratelimit}_test.go`,
  `internal/http/limits_test.go`, `internal/db/{janitor,sessions,caps}_test.go`,
  and `internal/ticketmaster/{testing,events,attractions,retry}_test.go` with
  eight JSON fixtures under `testdata/`.

  Verified, serially: `gofmt`, `go build`, `go vet`, `go test ./...` — green
  both **with** a live Postgres and without one (the DB-backed tests skip
  cleanly, which is how CI behaves when the service container is missing);
  `./scripts/check-deploy-config.sh` (16 checks); `terraform fmt -check` and
  `terraform validate`. Phase 2 touched no web or iOS code, so those lanes were
  not run. Nothing applied, nothing deployed.

  Three corrections the main session made to subagent work, all found by
  running things rather than reading them:
  1. **P2-10's escaping was wrong in the dangerous direction** and would have
     failed every deploy — see *Decisions taken*. Caught by actually running
     `docker compose config` against a rendered file.
  2. **P2-5's tests were a CI flake.** They induced the account-ledger failure
     by renaming `rate_ledger_account` away, which is a schema change under the
     other two package binaries `go test ./...` runs against the same database
     — the exact shape CI uses. It failed on a cold database and passed on warm
     ones. Replaced with a `search_path` shadow: a pool-local schema holding a
     stub `rate_ledger_account` with none of the columns the upsert names, so
     only the account write fails and nothing another binary can see changes.
     Confirmed across three cold-database full-suite runs.
  3. **The DB-backed tests were silently skipping locally** (no
     `TEST_DATABASE_URL`), which is how the flake stayed hidden. A throwaway
     Postgres is now the standing local check; added
     `internal/db/{sessions,caps}_test.go` to cover the three hand-written CTEs
     nothing else exercised — the mobile session escrow claim, and the capped
     save/subscribe/location inserts. A SQL error in any of those is a login or
     save outage with a green build.

  `CLAUDE.md` gained four entries this phase: session-token hashing and the
  NULL-`token_hash` escrow, the location cap being set membership rather than a
  counter, the fallback fetcher's `https`-only dial guard, and push's
  record-after-send ordering. `DB_MAX_CONNS` added to Appendix A.

  For Phase 4 / a human: **the nightly backup may already be dead.** P2-10 is
  not hypothetical — `APNS_P8_KEY` is written unquoted today and contains
  spaces, so any deployment with APNs configured has an `.env` that
  `scripts/backup-db.sh` cannot source. Check S3 for recent objects before
  assuming there is a restore point. Also still outstanding from Phase 0: the
  Ticketmaster and Songkick keys want rotating, and the SNS subscriptions sit
  in `PendingConfirmation` until someone clicks the link.

  Next: Phase 3, two subagents (iOS lane and web lane), after the main session
  does P3-2's endpoint and P3-3's response field itself.

- **2026-09-01 — Phase 3 complete** (P3-1…P3-9), on branch
  `production-hardening`. Main session did the shared backend first (P3-1's
  server half, P3-2's endpoint, P3-3's response field), then three subagents:
  iOS lane and web lane A concurrently, then web lane B (P3-7, P3-9) once
  lane A's files were free.

  New files: `internal/http/discover.go` + `discover_test.go`,
  `internal/concerts/discover.go` + `discover_test.go`,
  `internal/affinity/reason.go` + `reason_test.go`,
  `internal/db/concert_cache_test.go`, `internal/http/spa/spa_test.go`,
  `ios/ConcertFinder/Features/Feed/DiscoverModel.swift`,
  `ios/ConcertFinderTests/FirstRunTests.swift`,
  `ios/ConcertFinderTests/Fixtures/discover.json`,
  `web/src/pages/{discover,not-found}.tsx`,
  `web/src/components/{location-prompt,save-subscribe-hint,time-ago}.tsx`,
  and `web/public/` (favicon, robots.txt, manifest, og.svg/og.png verified
  1200x630, two PNG icons).

  Verified, serially: `gofmt`, `go build`, `go vet`, `go test ./...` — green
  both with a live Postgres and without one; `npm --prefix web install`
  (lockfile regenerated after the three Radix removals), `npm run lint`,
  `npm run build`; `xcodegen generate && xcodebuild test` on the iPhone 17
  simulator — **TEST SUCCEEDED**, with `FirstRunTests` and the new decoding
  tests confirmed present in the run rather than inferred from a green exit;
  `./scripts/check-deploy-config.sh` (16 checks). Terraform untouched.
  Nothing applied, nothing deployed.

  Two corrections the main session made:
  1. The iOS lane shipped a **compile error** — `SessionLifecycleTests`
     referenced the suite's `static` fixtures unqualified from instance
     methods. The agent had verified with `swiftc -parse`, which does not type
     check. Fixed with `Self.`; the lesson is that only `xcodebuild` counts.
  2. `internal/db/concert_cache_test.go` was added by the main session rather
     than left to the discover handler's unit tests. `ScanCachedConcerts` is
     the whole data path of a view that answers every failure with an empty
     200, so a SQL error there is a permanently empty section with a green
     build and nothing in a log — the exact shape Phase 2 went looking for.

  `CLAUDE.md` gained two entries: what `GET /api/discover` cannot do (no
  upstream call, no rate ledger, location-independent decode, no artist IDs,
  empty-200 on every failure), and that the feed's `reason` line reads the
  cached affinity profile and never computes one. The "When scans run" section
  now records that login pre-warms only for a user who already has a location.

  For a human, in addition to everything still listed in Phase 4: the
  signed-out discover section is only as good as the shared `concert_cache`,
  so it will look empty until real accounts have scanned real cities — worth
  knowing before judging the login page, and worth checking again before App
  Review sees it.
