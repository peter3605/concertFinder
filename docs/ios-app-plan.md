# ConcertFinder for iOS — Project Plan

**Status:** implemented and deployed as of 2026-08-23 (`35ed72c`, PR #14).
Everything in this document that is code has been built; what remains is
gated on third-party accounts. See [§0](#0-where-this-stands) before reading
further — the body below is the original plan, annotated where reality has
moved past it.
**Target:** a native SwiftUI app, feature-parity with the web SPA plus push
notifications, distributed through the App Store.
**Written against:** `257bbfc` (`Merge pull request #11 from
peter3605/secrets-in-parameter-store`).

Read `docs/design.md` first — it is authoritative on architecture, and this
plan deliberately does not restate it. Read `CLAUDE.md` before touching
serving, jobs, or quota code; several constraints below exist only because
that file explains what breaks without them.

---

## 0. Where this stands

*Added 2026-08-23, after the implementation landed. Read this first; the rest
of the document is the plan as written, and its tense is aspirational.*

### Done and live

The backend half is deployed at `https://concertfinder.app`:

- **§4.1 mobile auth** — bearer resolution in `RequireUser`, CSRF pass-through
  for header-authenticated requests only, the one-time code exchange, the AASA
  route, and a real SPA page at `/app/auth/callback`.
- **§4.2 push** — migration `0016`, `internal/push`, `SendPushWorker`, the
  device endpoints, `push_opt_in`, and the channel-keyed ledger.
- **§4.3 API surface** — `X-CF-Client` logged, `min_ios_build` on
  `/api/site-info`. On versioning the plan offered two options; the
  **additive-only policy** was taken and is written into `CLAUDE.md`. There is
  no `/api/v1/`.
- **§5–§6 the app** — all seven screens, offline cache, first-run experience,
  iPad layout, push registration. `ios/`, built from `ios/project.yml`.
- **§8 testing** — the Go/Swift contract check (`TestGoldenFixtures`), the
  database-backed ledger tests, Swift unit and UI tests. CI runs all of it.

Two things the plan predicted were confirmed live in production before the fix
shipped: `/.well-known/apple-app-site-association` was serving SPA HTML, and
`/app/auth/callback` was bouncing silently to the feed.

### The four gating items, and none of them is engineering

*Items 2 and 3 closed on 2026-08-25/26; they are kept in place, marked, because
each records a trap that recurs on any rebuild.*

**1. Spotify Extended Quota Mode ([§3.2](#32-spotify-development-mode)) —
start this first.** Multi-week turnaround, approval not guaranteed, and
nothing downstream shortens it. Until it is granted, only allowlisted accounts
can sign in — *including App Review's own reviewer*, which is why §3.2 says to
submit with an allowlisted demo account and treat Extended Quota as the thing
that makes the app usable by anyone who downloads it.

**2. Apple Developer account — done 2026-08-25.** The App ID is registered as
an explicit `com.concertfinder.ph` under team `L3MY7DN27B`, with Push
Notifications and Associated Domains enabled, and an APNs auth key
(`42KZTQHRRH`). `ios/project.yml`, `Keychain.swift`, and
`infra/terraform.tfvars` carry the real values; the bundle identifier is now
effectively permanent, since iOS caches `IOS_APP_ID` from the association file.

| Placeholder | Where | Note |
|---|---|---|
| ~~Bundle identifier~~ | `ios/project.yml` | Resolved: `com.concertfinder.ph`. |
| ~~Spotify logo asset~~ | `ios/ConcertFinder/DesignSystem/DesignSystem.swift` | Resolved 2026-08-31: the official full logo from Spotify's brand resource kit, in the asset catalogue as `SpotifyLogo`. See below. |
| Signing | Xcode / App Store Connect | Team is available; no team is set in `project.yml`, so a device build needs one selected once. |

The APNs key was created restricted to **Sandbox**, which is why
`apns_environment = "sandbox"`. A Sandbox-only key cannot send to the
production host, so TestFlight needs a **Sandbox & Production** key — reissue
it before the first upload.

*Amended 2026-08-31.* This used to say the reissue had to happen "alongside
flipping `apns_environment`", with both halves moving together or push failing
as `BadDeviceToken` with nothing in the logs. There is no longer a flip.
`APNS_ENVIRONMENT` now names which environments the **key** is authorized for
— `sandbox`, `production`, or `sandbox,production` — and the server picks the
host per device from `user_devices.environment`, which the app has always
reported by reading its own `aps-environment` entitlement. One key covers
both, so a single deployment serves TestFlight and a developer's debug build
at the same time, and nothing has to stay in step with anything. After the
reissue, set `sandbox,production` once and leave it.

**3. Server-side iOS configuration — applied and deployed 2026-08-26.** All
eight parameters exist under `/concertfinder/`, `APNS_P8_KEY` holds the real
key, and nothing holds the `REPLACE_ME` sentinel. Verified live through Caddy
after the deploy:

| Check | Result |
|---|---|
| `/.well-known/apple-app-site-association` | 200 `application/json`, no redirect, `L3MY7DN27B.com.concertfinder.ph` |
| `/api/site-info` | returns `min_ios_build: 0` |
| `/api/auth/login?client=ios` | 400 "app_challenge is required" — i.e. enabled; with a challenge it 302s to Spotify with the right `redirect_uri` |

> ⚠️ Kept because it still applies to any future rebuild: `APNS_P8_KEY` is in
> `operator_secrets`, so `terraform apply` creates it holding `REPLACE_ME`, and
> `render-env.sh` refuses to write the env file while any parameter holds that
> sentinel. **Applying before you have the key breaks the next deploy.** Run
> `./scripts/set-secrets.sh` in the same sitting — it offers `[f]` to read the
> `.p8` from a file, or `[-]` to mark it unused. Runbook:
> `docs/aws-deploy.md` §7a.

`MOBILE_CALLBACK_URL` and `IOS_APP_ID` are derived by Terraform from
`var.domain` and `var.ios_bundle_id`, so they cannot drift from the domain the
certificate is issued for.

**The `.p8` is stored with escaped newlines**, and that is not cosmetic:
`render-env.sh` parses `--output text` line by line, so a stored newline
truncates the value at `-----BEGIN PRIVATE KEY-----` and turns each remaining
PEM line into a junk `.env` entry. `config.Validate` only checks the key is
non-empty, so the truncation passes validation and surfaces later as
`push.New` failing on "not valid PEM" — a half-configured push path, which is
exactly what the partial-set check exists to prevent. `set-secrets.sh` escapes
on the way in as of `fc0b673`.

Two pieces of unrelated drift were caught by the plan and fixed in
`terraform.tfvars` rather than applied: `EMAIL_DELIVERY_MODE` would have been
reset from `smtp` to the `log` default, silently ending every digest and
instant-notify email, and the SES verification resource would have been
destroyed because the previous apply passed `-var ses_dns_records_created=true`
on the command line while the file said `false`.

**4. M8 submission.** Privacy labels, screenshots, demo account, review notes.
[§9](#9-app-store-submission) is the checklist. Raise
[§10.1](#101-guideline-511v-and-server-side-spotify-tokens) (server-side
third-party tokens) and [§10.2](#102-guideline-48-and-sign-in-with-apple)
(Sign in with Apple) in review notes rather than discovering them at review.

### Two open items worth a decision, not just execution

- ~~**§10.1's fallback architecture.**~~ **Decided 2026-08-31.** Ship as
  built; the clause is scoped to social networks and the paragraph names its
  own examples. The fallback is costed in
  [§10.1.3](#1013-the-fallback-if-a-reviewer-insists-anyway) and the review
  notes are written in [§10.1.4](#1014-review-notes-text-ready-to-paste).
  Reading the guideline in full also turned up a requirement this plan had
  missed — the same paragraph demands a way to revoke the credential from
  inside the app, and today only account deletion does that. See
  [§10.1.2](#1012-the-revoke-clause-which-this-plan-had-missed---built-2026-08-31),
  built 2026-08-31 as `DELETE /api/me/spotify-connection`.
- ~~**The account-total quota ceiling ([§3.3](#33-upstream-quota-against-an-open-download-button)).**~~
  **Built 2026-09-01** (production-hardening, P2-5): `rate_ledger_account`,
  migration 0017, keyed `(source, day)` with no `user_id`, with
  `RATE_CAP_TM_ACCOUNT_DAILY` / `RATE_CAP_SONGKICK_ACCOUNT_DAILY` defaulting to
  5000. Exhaustion now surfaces as a dated `retry_after` instead of upstream
  403s that look exactly like artists with no shows.

  **The decision it was standing in for is still open**, and it is a product
  one: how many people to admit per day. A visible ceiling is not a bigger
  ceiling — a public listing against 5000 calls/day is roughly 25 cold scans in
  the worst case, and considerably more once `concert_cache` is warm in a city.
  See `docs/production-plan.md` Phase 4, "Decide what launch means".

### What is deliberately not built

- **`since` on `/me/concerts`** ([§4.3](#43-api-surface-adjustments)) — "Not
  required for v1. Measure before building."
- **An affinity view.** `/api/me/affinity` is the one endpoint no client
  calls; the web app does not call it either, and there is no affinity screen
  in the seven-page parity target. §6's passing reference to "the affinity
  view" describes where attribution would go if one existed.
- Everything in [§11](#11-explicitly-out-of-scope).

---

## Table of Contents

0. [**Where this stands**](#0-where-this-stands) — current status and what is left
1. [Summary](#1-summary) · [One backend, two clients](#11-one-backend-two-clients)
2. [What the app is building on](#2-what-the-app-is-building-on)
3. [The three things that gate this project](#3-the-three-things-that-gate-this-project)
4. [Backend work required before any Swift is written](#4-backend-work-required-before-any-swift-is-written)
5. [iOS app architecture](#5-ios-app-architecture)
6. [Screen inventory and endpoint mapping](#6-screen-inventory-and-endpoint-mapping)
7. [Milestones](#7-milestones)
8. [Testing](#8-testing)
9. [App Store submission](#9-app-store-submission)
10. [Risks and open questions](#10-risks-and-open-questions)
11. [Explicitly out of scope](#11-explicitly-out-of-scope)
12. [Appendix A: new configuration](#appendix-a-new-configuration)
13. [Appendix B: proposed new endpoints](#appendix-b-proposed-new-endpoints)

---

## 1. Summary

The backend is in good shape for a second client. `/api/me/*` is already a
clean JSON API with no HTML coupling, the SWR snapshot read path means the
app never waits on a fan-out, and event grouping means a festival is one card
rather than six. Roughly 80% of what an iOS app needs is already served.

The remaining 20% is not evenly distributed. Three pieces of backend work
are genuinely required before the first Swift file is useful. (The third has
since been resolved; the first two are implemented on the `ios-app` branch and
ship when it does.)

1. **Authentication has no non-browser path.** Sessions are an `HttpOnly`
   cookie established by a 302 redirect, and mutations require a
   double-submit CSRF token read from a second cookie by JavaScript. Neither
   mechanism is reachable from `URLSession`.
2. **Push notifications do not exist.** The notification channel today is
   SMTP-over-SES, delivered by two river workers. APNs is a new package, a
   new table, a new worker, and — least obviously — a change to the
   already-sent ledger, which currently cannot distinguish "emailed" from
   "pushed."
3. ~~**Nothing is deployed.**~~ **Resolved.** This was true when the plan was
   written against `257bbfc`. The backend is now live at
   `https://concertfinder.app` — EC2 `t4g.small` behind an Elastic IP, Neon
   Postgres, Caddy TLS — so the OAuth redirect target, the universal-link
   domain, and the API base URL all exist. See [§3.1](#31-the-backend-is-deployed).

Beyond the engineering, two third-party gates bound what "App Store release"
can mean. Spotify Development Mode caps who can authorize the app, which
means **App Review's own reviewer cannot sign in** unless you either get
Extended Quota Mode approved or supply an allowlisted demo account. And App
Store Review Guideline 5.1.1(v) contains a clause about storing third-party
credentials off-device that this architecture, read literally, violates.
Both are in [§10](#10-risks-and-open-questions). Neither is an engineering
task; both have long lead times; start them in week one.

**Rough size:** 9–13 weeks of focused part-time work to TestFlight, plus an
indeterminate wait on Spotify Extended Quota Mode before the App Store is
reachable. Milestone-level estimates are in [§7](#7-milestones).

### 1.1 One backend, two clients

This is a foundational assumption of the whole plan, so it is worth stating
flatly: **the iOS app talks to the same Go binary, on the same box, against
the same database, through the same `/api/me/*` handlers as the web SPA.**
There is no mobile BFF, no second service, no forked handler, and no separate
deployment. The app is a second client of the API that already exists.

That is the right call here for reasons specific to this codebase. The
handlers already return clean JSON with no HTML coupling. The expensive work
— affinity hydration, the Ticketmaster fan-out, the fallback chain — lives in
river workers writing to `user_concert_snapshots`, so both clients read the
same precomputed snapshot and neither triggers a fan-out on the read path.
Quota accounting is per *user*, not per client, so a user who reads their feed
on both their phone and their laptop spends one allowance either way — which
is only true because there is one ledger behind one API. And every ToS
constraint in `CLAUDE.md` (no raw Spotify persistence, encrypted refresh
tokens, no direct client access to third-party APIs) is enforced in exactly
one place. A second backend would mean enforcing all of it twice.

Concretely, the work in [§4](#4-backend-work-required-before-any-swift-is-written)
splits three ways:

**Shared unchanged — the app calls these as-is.** Every endpoint in
[§2.1](#21-the-api-as-it-stands): the concerts feed, refresh, location, saved
concerts, subscribed artists, artist search, email prefs, account deletion,
affinity, site info, the auth handlers. Same JSON, same filters, same facets,
same `Event`/`Act` shapes. The Swift models are transliterations of
`web/src/lib/types.ts` precisely because there is nothing to translate.

**Additive — new routes on the same server that the web client simply never
calls.** `POST /api/auth/mobile/exchange`, `POST`/`DELETE /api/me/devices`,
`/.well-known/apple-app-site-association`, and a `?client=ios` branch inside
the existing `/api/auth/login` handler.

**Modified in place — small changes to shared code, with the web path
untouched.** `RequireUser` gains an `Authorization: Bearer` fallback *before*
the existing cookie lookup; `CSRF` becomes a pass-through only when
authentication came from that header, so cookie-authenticated mutations are
still guarded exactly as today; `oauth_handshakes` gains a nullable column;
`users` gains `push_opt_in`; `/api/site-info` gains `min_ios_build`. Nothing
here changes an existing response shape.

**The cost of sharing, stated honestly.** One API with two clients means a
field rename that is a one-commit operation today becomes a breaking change
for builds already on people's phones. That is the entire reason
[§4.3](#43-api-surface-adjustments) exists — versioning, an
`X-CF-Client` header, and a minimum-build check are the price of this
decision, and they are cheap to pay before M2 and expensive after M8. The
alternative (a separate mobile service) costs far more and buys nothing this
app needs.

The one scenario that would break the shared model is the App Store fallback
architecture in [§10.1](#101-guideline-511v-and-server-side-spotify-tokens) —
if Apple insists the app hold the Spotify token itself, affinity hydration
moves onto the device and the two clients stop computing profiles the same
way. That is a reason to resolve §10.1 early, not a reason to build two
backends now.

---

## 2. What the app is building on

### 2.1 The API as it stands

Everything below already exists and works. `internal/http` handlers, mounted
in `cmd/server/main.go`.

**Public**

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/healthz` | Pings Postgres; 503 if unreachable. |
| `GET` | `/api/site-info` | Contact email + policy effective date. Backs the privacy/terms pages. |
| `GET` | `/api/unsubscribe` | Renders a confirmation page only. |
| `POST` | `/api/unsubscribe` | The actual opt-out. HMAC-signed token; RFC 8058 one-click. |

**Auth** — mounted under `/api/auth`, IP-rate-limited at 5 req/s burst 20.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/auth/login` | Sets `cf_handshake`, 302s to Spotify with PKCE S256. |
| `GET` | `/api/auth/callback` | Exchanges the code, upserts the user, sets `cf_session`, 302s to `PostLoginURL`. |
| `POST` | `/api/auth/logout` | Deletes the session row, clears the cookie, 204. |
| `GET` | `/api/auth/me` | Current user. Served from the context the middleware already populated. |

**Authenticated** — `/api/me/*`, behind `RequireUser` + `CSRF`.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/me/concerts` | The SWR read. Filters: `genre`, `venue`, `date_from`, `date_to`, `weekday`. |
| `POST` | `/api/me/concerts/refresh` | Manual refresh. 429 with `retry_after` + `reason` when throttled. |
| `GET` | `/api/me/affinity` | The derived affinity profile. |
| `GET` `PUT` | `/api/me/location` | Lat/lng + radius, or a `query` string to geocode. |
| `GET` | `/api/me/saved-concerts` | Past shows floored out, same as the feed. |
| `POST` | `/api/me/saved-concerts` | Save by `dedup_key`. |
| `DELETE` | `/api/me/saved-concerts/{dedupKey}` | Unsave. |
| `GET` | `/api/me/subscribed-artists` | Artists the user gets instant alerts for. |
| `POST` `DELETE` | `/api/me/subscribed-artists/{artistID}` | Subscribe / unsubscribe. |
| `GET` | `/api/me/artists/search` | Artist typeahead for the subscribe screen. |
| `PUT` | `/api/me/email-prefs` | `digest_opt_in`, `instant_notify_opt_in`. |
| `DELETE` | `/api/me/account` | Full account deletion. |

Unknown `/api/*` paths return JSON 404 rather than falling through to the SPA
catch-all — which means the app gets a diagnosable error instead of HTML.
There is **no CORS middleware anywhere in the tree**, which is correct today
(same-origin SPA) and irrelevant to a native client (`URLSession` is not
subject to CORS).

### 2.2 The wire model

`web/src/lib/types.ts` is the de facto schema. The Swift models are a direct
transliteration; the shapes worth internalizing:

- **`Event`** is the unit of display — one card, one night out. It carries
  `event_key`, a `date` (the *earliest* act's set time, for sorting and month
  grouping only — not a claim about when any particular act plays), venue and
  city fields, `links`, and `acts`.
- **`Act`** is one of the user's artists on that bill, each with its own
  `dedup_key`, `saved`, and `subscribed`. Save and subscribe are per-act even
  though the acts share a card. Getting this wrong produces a festival card
  where tapping the bookmark saves the wrong artist.
- **`ConcertsResponse`** carries three fields the UI must actually respond to:
  `refreshing` (a scan is in flight — poll), `complete` (false means the scan
  behind these results did not cover every artist), and `retry_after` (set
  when the shortfall was the daily upstream quota).
- **`facets`** are `{genres, venues}` with counts. A facet's count equals what
  clicking it returns — genre matching is exact-tag case-insensitive, venue
  matching is under `concerts.Normalize`. The app must send the facet value
  back verbatim; do not "helpfully" lowercase or trim it.

### 2.3 Background machinery

River jobs in `internal/jobs`, on wall-clock UTC schedules (`DAILY_*_HOUR_UTC`,
defaults 06/07/09/10): affinity refresh, scan fanout, digest fanout, janitor.
`ScanConcertsWorker` does the Ticketmaster + fallback fan-out with a
`ScanBudget` of 5 minutes and `ScanMaxAttempts` of 3, and chains
`SendInstantNotifyArgs` when the user has `instant_notify_opt_in` and an
email. That chain point is where push hooks in ([§4.2](#42-push-notifications)).

### 2.4 The web client, for reference

React 18 + TypeScript + Vite 5 + Tailwind 3 + Radix primitives, ~1700 lines
across `web/src/pages` and `web/src/components`, embedded into the Go binary
with `go:embed`. Seven pages: concerts, saved, subscribe, settings, login,
privacy, terms. That is the parity target.

---

## 3. The three things that gate this project

### 3.1 The backend is deployed

**This section is resolved, and no longer gates anything.** It is kept rather
than deleted because the reasoning still explains *why* the domain matters.

When this plan was written against `257bbfc`, Terraform had never been applied
and the GitHub Actions deploy had never succeeded. That changed shortly
afterwards — see `1ec3a4a` (config and secrets into SSM Parameter Store),
`7086cc2` (Postgres from RDS to Neon), `1cabc7a` (DNS at the registrar), and
`267535d` (the IAM path fix that made the parameter read work).

The deployment today: `https://concertfinder.app`, an EC2 `t4g.small`
(`i-00127627308ea91e1`) behind an Elastic IP, Neon Postgres over the direct
endpoint, Caddy terminating TLS, secrets rendered from SSM at deploy time.
`GET /api/healthz` returns `{"status":"ok"}`.

The requirement it described stands and is now met: an iOS app needs a stable
HTTPS origin for three separate reasons — the Spotify redirect URI, the
universal-link domain that receives the OAuth callback, and the API base URL
itself, none of which can be `127.0.0.1`.

What remains here is narrower: the deployed binary predates the mobile auth
work, and the iOS-specific configuration in
[Appendix A](#appendix-a-new-configuration) is unset, so
`/api/auth/login?client=ios` returns 501 and the association file 404s until
that branch ships and those variables are filled in.

### 3.2 Spotify Development Mode

The app runs in Development Mode, which caps authorized users to an allowlist
maintained by hand in the Spotify dashboard. Commit `64bf229` added a
specific 403 for this case, so the failure is at least legible now.

For an App Store submission this is disqualifying by default: the App Review
engineer signs in with their own credentials, is not on the allowlist, and
gets the 403. There are exactly two ways through, and both take time:

- **Extended Quota Mode.** An application to Spotify. `docs/design.md` §10.2
  lists starting it as Phase 2 scope; it was never started. Turnaround is
  measured in weeks and approval is not guaranteed.
- **An allowlisted demo account.** Create a dedicated Spotify account with a
  plausible listening history, add it to the dashboard allowlist, and supply
  the credentials in App Review notes. This satisfies Apple's demo-account
  requirement and is the realistic path to a *first* approval — but it does
  not let you ship to actual users, because they will hit the same 403.

Practically: use the demo account to get through review, and treat Extended
Quota Mode as the thing that makes the app usable by anyone who downloads it.
Start the application now; it is the longest pole in this plan.

### 3.3 Upstream quota, against an open download button

Ticketmaster's account-wide budget is 5000 calls/day. `internal/config`
defaults `RATE_CAP_TM_PER_USER_DAILY` to 250 (≈20 concurrently active users);
`.env.example` ships 500, with a comment acknowledging that halves it to ≈10.
Either way, the rate ledger enforces **per-user limits only** — it does not
model the account total, so exceeding it degrades feeds silently rather than
erroring.

The web app's exposure is bounded by the Spotify allowlist. An App Store
listing is not bounded by anything.

*Amended 2026-09-01.* Both halves of that are now addressed. The ledger has an
account-total ceiling (`rate_ledger_account`, migration 0017), and hitting it
reuses the per-user `retry_after` path — which turned out to be the right
answer rather than a shortcut, because the remedy is identical: wait for the
UTC day to roll over. `README.md` now quotes both figures and says which is
which (500 is what `.env.example` ships, 250 is the code default) instead of
picking one and disagreeing with the other file.

---

## 4. Backend work required before any Swift is written

### 4.1 A non-browser authentication path

**The problem.** Three mechanisms make the current flow browser-only:

- `cf_session` is `HttpOnly`, `Secure`, `SameSite=Lax`, set by
  `setSessionCookie` during a 302 to `PostLoginURL`. An
  `ASWebAuthenticationSession` completes that redirect inside a web context
  whose cookie jar the app cannot read.
- `cf_handshake` bridges `/login` and `/callback`, so the two calls must
  share a cookie jar.
- `auth.CSRF` requires `X-CSRF-Token` on POST/PUT/DELETE/PATCH, matching a
  `cf_csrf` cookie that is deliberately *not* `HttpOnly` because browser JS
  reads it. A native client has no reason to carry a CSRF token at all —
  CSRF exists because cookies are ambient authority, and a bearer token is
  not.

**The proposal.** Four surgical changes, none of which alter web behaviour.

**(a) Accept the session from an `Authorization` header.** `auth.RequireUser`
resolves the session ID from `Authorization: Bearer <session_id>` first,
falling back to the `cf_session` cookie. The `sessions` table needs no
change — the cookie value *is* a 32-byte random session ID, and it is
already the credential. Record on the context which mechanism authenticated
the request.

**(b) Make `auth.CSRF` a pass-through for header-authenticated requests.**
Read the flag (a) set. Do not skip CSRF on "the request looks like it's from
an app" — a `User-Agent` check would be a CSRF bypass with extra steps. The
only safe signal is that authentication came from a header the browser
cannot be tricked into sending ambiently.

**(c) A one-time code exchange, so the session never rides in a URL.**

```
1. App generates a random verifier; sends S256(verifier) as `app_challenge`.
     GET /api/auth/login?client=ios&app_challenge=<b64url>
   The challenge is stored on the existing oauth_handshakes row — extend the
   record rather than adding a table; it already has the right key and TTL.

2. Spotify PKCE proceeds exactly as today, inside ASWebAuthenticationSession.

3. /api/auth/callback sees the handshake was app-initiated. Instead of
   redirecting to PostLoginURL, it mints a single-use code (60s TTL, bound to
   the new session) and redirects to the universal link:
     https://<domain>/app/auth/callback?code=<code>

4. ASWebAuthenticationSession returns that URL to the app.

5. POST /api/auth/mobile/exchange {code, verifier}
   → 200 {session_token, expires_at, user: {...}}
   Server checks S256(verifier) == stored challenge, burns the code, returns
   the session ID as the bearer token.
```

Why the extra round trip rather than putting the session in the callback URL:
callback URLs get logged, appear in `ASWebAuthenticationSession` diagnostics,
and — if you use a custom scheme rather than a universal link — can be
claimed by another installed app. The verifier means a stolen `code` alone is
useless. Use the **universal link** form (an HTTPS URL your domain claims via
`apple-app-site-association`), not `concertfinder://`; custom schemes are
first-come-first-served on iOS.

**(d) Sessions the app can end.** `POST /api/auth/logout` already deletes the
session row; it just needs to accept the bearer token. `SessionCreatedTTL` is
90 days, which is a reasonable mobile session lifetime — but the app must
handle a 401 at any moment (session expired, account deleted, or the user
revoked the Spotify grant) by clearing the Keychain and returning to login.

**Serving the AASA file.** `https://<domain>/.well-known/apple-app-site-association`,
`Content-Type: application/json`, no redirects, no query string, served over
valid TLS. Two candidate homes: a route in the chi tree before the SPA
catch-all, or a Caddy `handle` block. Prefer the Go route — the Caddyfile is
already covered by `./scripts/check-deploy-config.sh` and adding
app-identifier config there splits the app's identity across two files.

Note that `/app/auth/callback` currently falls through the chi `NotFound`
handler to the SPA, whose router sends unknown paths to `/`. That is harmless
when the app is installed — iOS intercepts the URL before Safari sees it —
but a user who somehow lands there in a browser gets a silent bounce to the
feed. Give the path a real SPA route saying "open this in the ConcertFinder
app", or a small server-rendered page. It costs ten minutes and it is the
kind of thing that is impossible to debug from a bug report.

### 4.2 Push notifications

**New table** (migration `0016`):

```sql
CREATE TABLE IF NOT EXISTS user_devices (
  user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  device_token TEXT NOT NULL,
  environment  TEXT NOT NULL,          -- 'sandbox' | 'production'
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  disabled_at  TIMESTAMPTZ,            -- set on APNs 410 Gone
  PRIMARY KEY (user_id, device_token)
);
```

`ON DELETE CASCADE` matters: `DELETE /me/account` must take device tokens
with it, or a deleted user's phone keeps receiving pushes.

**New endpoints:** `POST /api/me/devices` (register/refresh a token — call it
on every launch, not just on first grant; APNs tokens rotate) and
`DELETE /api/me/devices/{token}` (on logout).

**New package `internal/push`.** Token-based APNs auth: an ES256 JWT signed
with a `.p8` key, regenerated at most hourly, sent over HTTP/2 to
`api.push.apple.com` (`api.sandbox.push.apple.com` for debug builds). Go's
stdlib does HTTP/2 natively — no third-party APNs library is required, which
keeps `/internal` free of vendor dependencies in the same spirit as the
no-AWS-SDK rule. Handle `410 Gone` and `BadDeviceToken` by stamping
`disabled_at`; the janitor prunes disabled rows.

**New river worker `SendPushWorker`**, mirroring `SendInstantNotifyWorker`
and enqueued from the same point in `ScanConcertsWorker` (`workers.go:231`).

**The non-obvious part — the already-sent ledger.** `user_digest_sent` has
primary key `(user_id, dedup_key)` and no channel column. Email digest and
instant-notify already share it deliberately, so a show emailed once is not
emailed twice. If push writes to the same table it will **suppress the
email**, and if it reads from it, a user who is opted into both gets exactly
one of the two, non-deterministically depending on which worker ran first.
Neither failure raises an error. Migration `0016` must therefore either add a
`channel` column and widen the primary key to `(user_id, dedup_key, channel)`,
or create a separate `user_push_sent`. The former is tidier; the latter
touches less existing code. Pick one deliberately and write the reasoning
down — this is precisely the class of silent-suppression bug `CLAUDE.md`
catalogues.

**New preference.** Add `push_opt_in` to `users` rather than reusing
`instant_notify_opt_in`. A user who wants a push but no email is a normal
preference, and overloading the existing column makes it unexpressible.
`PUT /me/email-prefs` grows a field; `GET /api/auth/me` returns it. Note that
`Scopes` already includes `user-read-email`, so no re-auth is needed for this.

**Payload.** Keep it thin — `{event_key, artist_name, venue, date}` in the
`aps` payload plus custom keys — and deep-link into the event via the
universal link scheme. Do not put the whole event in the payload; APNs caps
at 4KB and a festival card is not small.

### 4.3 API surface adjustments

- **Version the API before there are two clients.** Today `/api/me/concerts`
  has exactly one consumer, so changing a field name is a one-commit
  operation. The moment an App Store build is in someone's pocket that stops
  being true, and old app versions live for months. Introduce `/api/v1/...`
  as the canonical path with the current paths aliased, or commit to an
  additive-only policy for `/api/me/*` and write it into `CLAUDE.md`. Either
  is fine; drifting into two clients with no policy is not.
- **A client header.** `X-CF-Client: ios/1.0.0 (build 42)` on every request,
  logged by `requestLogger`. When something breaks for app users only you
  will want to have had this from day one.
- **A minimum-version check.** `GET /api/site-info` grows
  `min_ios_build`. The app compares on launch and can show a blocking
  "update required" screen. This is the escape hatch that makes a breaking
  server change survivable.
- **Consider a `since` parameter on `/me/concerts`.** Not required for v1 —
  the payload gzips ~5x and the snapshot is bounded — but a mobile client
  polling every 10 seconds on cellular is a different cost profile from a
  browser tab. Measure before building.

---

## 5. iOS app architecture

### 5.1 Stack

- **Swift 6**, strict concurrency. **SwiftUI**, iOS 17+ as the deployment
  target (gets `Observable`, `ContentUnavailableView`, and the current
  navigation APIs without back-compat shims).
- **No third-party dependencies** for v1. `URLSession` covers networking,
  `AuthenticationServices` covers the OAuth handshake, SwiftData or a small
  Codable-to-disk cache covers offline. Adding Alamofire or Kingfisher here
  buys nothing and costs a supply chain.
- **Xcode project generated by XcodeGen or Tuist**, with the project file
  checked in as YAML rather than `.pbxproj`. Merge conflicts in a
  `.pbxproj` are miserable, and this repo already prefers config as text.

### 5.2 Where it lives

A separate `ios/` directory in this repo, not a separate repo. The app and
the API change together — the auth work in [§4.1](#41-a-non-browser-authentication-path)
is a single logical change spanning Go and Swift — and one repo means one PR.
CI grows a job that builds and tests the app on `macos-latest`; keep it
independent of the existing Go/web jobs so a Swift failure does not block a
backend deploy.

```
ios/
  project.yml                 XcodeGen spec
  ConcertFinder/
    App/                      entry point, root navigation, deep-link routing
    Core/
      Networking/             APIClient, endpoints, error mapping
      Auth/                   AuthController, Keychain storage, ASWebAuthenticationSession
      Models/                 Codable mirrors of web/src/lib/types.ts
      Storage/                snapshot cache, filter persistence
      Push/                   registration, payload handling
    Features/
      Feed/                   list, month sections, event card, filters sheet
      EventDetail/            acts, ticket links, save/subscribe
      Saved/
      Artists/                subscribed list + search
      Location/
      Settings/
    DesignSystem/             colors, typography, spacing, the Spotify attribution view
  ConcertFinderTests/
  ConcertFinderUITests/
```

### 5.3 Networking

One `APIClient` actor. Responsibilities: attach the bearer token, attach
`X-CF-Client`, decode, and map failures onto a typed error enum rather than
letting `URLError` leak into views. Specific behaviours worth designing
rather than discovering:

- **401 → sign out.** One place, not thirteen.
- **429 on refresh** carries `retry_after` and `reason`; surface both.
- **The SWR poll.** When `refreshing: true`, poll every 10s — matching the
  web client — but with a hard ceiling (the web client bounds it; the app
  must too) and **suspend polling when the app backgrounds**. A 10-second
  timer that survives backgrounding is a battery complaint and an App Review
  question.
- **`complete: false` is a UI state, not a log line.** It means the scan
  behind these results did not cover every artist. A quiet Tuesday and a
  truncated scan look identical otherwise, and the whole point of the flag
  is that the user can tell them apart.

### 5.4 Auth on the client

`ASWebAuthenticationSession` with `prefersEphemeralWebBrowserSession = false`
so a user already signed into Spotify in Safari does not retype a password.
The session token goes in the Keychain with
`kSecAttrAccessibleAfterFirstUnlock` — not `WhenUnlocked`, because push
handling may need to run before first unlock in some paths, and not with
iCloud sync, because a session is device-specific.

The app never sees a Spotify token. That is a property of the existing
architecture worth preserving deliberately: all Spotify access is
server-mediated, which is what `docs/design.md` §2 calls non-negotiable, and
it is also the only reason the App Store analysis in
[§10.1](#101-guideline-511v-and-server-side-spotify-tokens) is arguable at all.

### 5.5 Offline and cold start

Cache the last successful `/me/concerts` response to disk and render it
immediately on launch, with the `computed_at` timestamp shown as
"updated 3h ago." This is nearly free — the response *is* a snapshot — and it
converts a cold launch from a spinner into content.

The first launch after login is the hard one. `OnLoginSuccess` enqueues a
pre-warm scan, but a cold 200-artist profile measured ~250s of MusicBrainz
plus ~86s of Nominatim against a 300s `ScanBudget`. That is minutes of empty
state on a first run, on a device the user is holding. It needs a designed
first-run experience — progressive disclosure, something to do while waiting
(set location, pick artists to subscribe to), or a partial render as results
land. A spinner will not survive contact with a real user.

---

## 6. Screen inventory and endpoint mapping

| Screen | Endpoints | Notes |
|---|---|---|
| **Login** | `GET /api/auth/login?client=ios`, `POST /api/auth/mobile/exchange` | `ASWebAuthenticationSession`. Handle the Development Mode 403 with its own copy — it is a configuration state, not a user error. |
| **Feed** | `GET /api/me/concerts` | Month-sectioned list of `Event` cards. Pull-to-refresh → `POST /me/concerts/refresh`. Poll on `refreshing`. Banners for `complete:false` and `retry_after`. |
| **Filters** | facets from the feed response | Genre and venue pills with counts, date range, weekday/weekend. Send facet values back verbatim. Persist locally; the server holds no filter state. |
| **Event detail** | — (from `Event`) | All `acts` with per-act save/subscribe. Ticket `links` open in `SFSafariViewController`, labelled from `SOURCE_LABELS`. Add to Calendar. Venue → Maps. |
| **Saved** | `GET/POST/DELETE /api/me/saved-concerts` | Delete is by `dedup_key` in the path. Past shows already floored server-side. |
| **Artists** | `GET /api/me/subscribed-artists`, `GET /api/me/artists/search`, `POST/DELETE /api/me/subscribed-artists/{id}` | Typeahead search; debounce it — every call is server-side quota. |
| **Location** | `GET/PUT /api/me/location` | CoreLocation one-shot with a clear purpose string, or a text query the server geocodes. `is_default: true` drives a "set your location" prompt rather than silently showing someone else's city. |
| **Settings** | `GET /api/auth/me`, `PUT /api/me/email-prefs`, `POST /api/auth/logout`, `DELETE /api/me/account` | Digest, instant-notify, and the new push toggle. Account deletion needs a confirmation and must actually delete — see [§9](#9-app-store-submission). |
| **Privacy / Terms** | `GET /api/site-info` | Can be native text or a `SFSafariViewController` to the web pages. Native is better for review. |

**"Powered by Spotify" attribution** is required on any surface showing
Spotify-derived data — which is the feed, the event cards, the artists
screen, and the affinity view. Build it as one `DesignSystem` component so it
cannot be forgotten on a screen added later, and follow Spotify's current
brand guidelines for the logo asset and minimum sizing.

---

## 7. Milestones

Estimates assume one engineer working part-time and are ranges, not
commitments. M0 is complete ([§3.1](#31-the-backend-is-deployed)); the Spotify
application in [§3.2](#32-spotify-development-mode) is now the longest pole and
should start immediately if it has not already.

| # | Milestone | Deliverable | Est. |
|---|---|---|---|
| ~~**M0**~~ | ~~**Deploy the backend**~~ | **Done.** Terraform applied, EC2 + Neon + Caddy live at `https://concertfinder.app`, deploy green, health check passing. `docs/aws-deploy.md` is the runbook. | — |
| **M1** | **Mobile auth on the server** | Bearer resolution in `RequireUser`, CSRF pass-through, one-time code exchange, AASA served, `X-CF-Client` logged. Go tests for each. | 1 wk |
| **M2** | **iOS skeleton** | XcodeGen project, CI job, `APIClient`, Keychain, full login round-trip to a screen that prints the user's display name. | 1 wk |
| **M3** | **Feed** | Month-sectioned events, event detail, filters, facets, SWR polling, pull-to-refresh, the `complete`/`retry_after` states, offline cache. The bulk of the app. | 2–3 wk |
| **M4** | **The rest of parity** | Saved, artists + search, location with CoreLocation, settings, account deletion, privacy/terms. | 1–2 wk |
| **M5** | **Push, server side** | Migration 0016, `internal/push`, `SendPushWorker`, device endpoints, the ledger decision from [§4.2](#42-push-notifications). Verified against APNs sandbox. | 1–1.5 wk |
| **M6** | **Push, client side** | Registration, permission priming (ask *after* showing value, not on launch), payload handling, deep link into event detail, settings toggle. | 0.5 wk |
| **M7** | **Polish** | Empty and error states, Dynamic Type, VoiceOver, dark mode, iPad layout, launch performance, the designed first-run experience from [§5.5](#55-offline-and-cold-start). | 1–2 wk |
| **M8** | **TestFlight → App Store** | Signing, App Store Connect setup, privacy labels, screenshots, demo account, review notes, submission. | 1 wk + review |

**To TestFlight: 9–13 weeks.** To the App Store: that plus review, gated on
[§3.2](#32-spotify-development-mode), which is not on your critical path to
control.

---

## 8. Testing

- **Go.** The existing suite (`go test ./...`) covers the new auth paths:
  bearer resolution, CSRF pass-through *and* that CSRF still fires for
  cookie-authenticated mutations, code exchange (including a wrong verifier,
  a replayed code, and an expired one), and the push ledger's channel
  separation. That last one deserves an explicit test asserting that a user
  opted into both channels receives both — it is the failure mode that
  produces no error.
- **Swift unit tests.** Model decoding against captured real responses —
  including a festival with six acts, an empty feed, `complete: false`, and a
  429 with `retry_after`. Fixture files, not hand-written JSON literals.
- **Swift UI tests.** Login, feed load, save, filter. Enough to catch a
  navigation regression, not an exhaustive suite.
- **Contract check.** A CI step that decodes `internal/http`'s response
  structs into the Swift models — or, more cheaply, a golden-JSON fixture
  generated by a Go test and consumed by a Swift test. Two clients and no
  contract test is how field renames reach the App Store.
- **Manual, against the real backend.** Cold start with no snapshot, a
  quota-capped account, a revoked Spotify grant, airplane mode, a session
  expiring mid-session.

---

## 9. App Store submission

**Account deletion — Guideline 5.1.1(v).** "If your app supports account
creation, you must also offer account deletion within the app."
`DELETE /api/me/account` already exists, so this is a Settings screen and a
confirmation dialog. It must be reachable without contacting support and must
actually delete, not deactivate.

**Privacy nutrition labels.** Declare honestly: Spotify user ID and display
name (identifiers, linked, app functionality), email (contact info, linked,
app functionality — used for the digest), coarse location (linked, app
functionality), and device token (identifiers, linked, app functionality).
The strong story here is that raw listening data is *never* persisted — held
in memory and discarded after profile construction, per the ToS constraints
in `CLAUDE.md` — and only the derived affinity profile is stored with a
24-hour TTL. Say that plainly in the privacy policy; it is unusually good.

**Privacy manifest — `ios/ConcertFinder/Resources/PrivacyInfo.xcprivacy`.**
Separate from the labels above and mandatory since May 2024 for any binary
touching a "required reason" API. This one does: `UserDefaults`, in
`ThemeStore` and `SnapshotCache`, declared as
`NSPrivacyAccessedAPICategoryUserDefaults` with reason `CA92.1` (an app's own
configuration and state — the app-group reasons do not apply while there is no
app group and no extension).

It is on this checklist rather than in the milestones because of *when* it
fails. The upload succeeds, processing appears to run, and App Store Connect
then emails ITMS-91053 and never makes the build available — so nothing local,
and nothing in CI, sees it. `ConcertFinderTests/AppBundleTests.swift` asserts
against `Bundle.main`, which catches the manifest being dropped from the
Resources build phase; it cannot catch a *newly added* required-reason API, so
adding one means editing the manifest by hand.

The manifest also carries `NSPrivacyCollectedDataTypes`, which must agree with
the nutrition labels above — deliberately, so the two do not drift. Change
both together.

**Demo account.** Required, and it must work. See
[§3.2](#32-spotify-development-mode) — the account has to be on the Spotify
dashboard allowlist or the reviewer gets a 403 and a rejection.

**Review notes.** Proactively explain three things: (1) why the app requires
a Spotify account (it is a client for the user's own Spotify data — this is
the 4.8 exemption you are relying on, see [§10.2](#102-guideline-48-and-sign-in-with-apple));
(2) that no Spotify credentials are stored on device and no listening data is
retained; (3) the demo account credentials.

**Also on the checklist:** an App Privacy policy URL (the `/privacy` page
exists), `NSLocationWhenInUseUsageDescription` with a real explanation,
push permission requested in context rather than at launch (4.5.4), no
functionality gated behind push, screenshots at whichever device sizes App
Store Connect currently requires (they change — check at upload), and an App
Store description that does not imply a Spotify partnership.

Two of those are settled in the repo rather than in App Store Connect.
`TARGETED_DEVICE_FAMILY` is `"1,2"`, and an iPad app that does not claim
`UIRequiresFullScreen` must support all four orientations, so `Info.plist`
carries `UISupportedInterfaceOrientations~ipad` alongside the base key. The
suffixed key is the point: putting all four on the base key would clear the
warning by also letting an iPhone sit upside down. Claiming full screen would
be the other way out, but it drops multitasking for a layout that already
adapts.

---

## 10. Risks and open questions

### 10.1 Guideline 5.1.1(v) and server-side Spotify tokens

**Decided 2026-08-31 — see [§10.1.1](#1011-the-decision-made-2026-08-31).**
This was the highest-consequence unknown in the plan; what follows is the
framing as originally written, kept because the two arguments are still the
two arguments, with the decision and its reasoning after it.

Guideline 5.1.1(v) states, verbatim:

> An app may not store credentials or tokens to social networks off of the
> device and may only use such credentials or tokens to directly connect to
> the social network from the app itself while the app is in use.

ConcertFinder does the opposite by design and for good reasons: the refresh
token is AES-256-GCM encrypted in Postgres, and the nightly affinity refresh
and scan fanout use it precisely when the app is *not* in use. That is not
incidental — it is the architecture, and removing it removes the daily
digest, instant notifications, push, and the pre-warmed snapshot that makes
the feed instant.

**The argument that it does not apply:** the clause is scoped to *social
networks*, and the surrounding paragraph frames it as being about apps whose
core functionality is a specific social network. Spotify is a music streaming
service; ConcertFinder's core functionality is concert discovery. Many
shipping apps store third-party OAuth refresh tokens server-side for exactly
this kind of background sync.

**The argument that it does:** a reviewer reading "social networks" broadly —
Spotify has social features, follows, and shared playlists — would find a
literal violation, and the app cannot function without the thing that
violates it.

### 10.1.1 The decision, made 2026-08-31

**Ship the architecture as built. Do not move tokens on-device pre-emptively.
State the scope argument in review notes on the first submission.**

Re-reading the guideline in full moved this from a coin-flip to a defensible
position, because the sentence the plan quoted is the *last* sentence of a
paragraph, and the paragraph names its own subject:

> If your core app functionality is not related to a specific social network
> (e.g. Facebook, WeChat, Weibo, X, etc.), you must provide access without a
> login or via another mechanism. Pulling basic profile information, sharing
> to the social network, or inviting friends to use the app are not considered
> core app functionality. The app must also include a mechanism to revoke
> social network credentials and disable data access between the app and
> social network from within the app. **An app may not store credentials or
> tokens to social networks off of the device** […]

Every sentence is about social networks, and the enumerated examples are all
social networks. Spotify is a music streaming service and is not among them.
Guideline 4.8 corroborates that Apple has a category in mind for this app —
its exemption for "a client for a specific third-party service" where "users
are required to sign in to their mail, social media, or **other third-party
account** directly to access their content" would be incoherent if every such
client were forbidden from holding a token server-side.

Quoted in isolation the sentence reads like a flat prohibition. Quoted with
its paragraph it reads as scoped, and that is the form to put in review notes.

**What this does not settle.** The clause is a reviewer judgment call, so the
fallback below stays costed and ready. The point of deciding now is that no
milestone depends on guessing right.

### 10.1.2 The revoke clause, which this plan had missed — built 2026-08-31

The same paragraph carries a second requirement, and it is the actionable one:

> The app must also include a mechanism to revoke social network credentials
> and disable data access between the app and social network from within the
> app.

Today the *only* thing that removes a stored Spotify grant is
`DELETE /me/account`. `handleLogout` deletes the session row and leaves
`users.encrypted_refresh_token` exactly where it was — so a user who signs out
has no way, from inside either client, to stop us holding their Spotify
credential short of destroying their saved concerts and subscriptions too.

Account deletion is arguably a mechanism that satisfies this: it revokes the
credential and ends all data access, from within the app, without contacting
support. But it is the maximal form of it, and "the only way to disconnect is
to delete everything" is a bad answer to a reviewer who asks — as well as
being worse for users than it needs to be.

**Built.** `DELETE /api/me/spotify-connection` (`db.DisconnectSpotify`) zeroes
the credential, drops the cached access token, and deletes the affinity
profile, snapshots, sessions and device tokens in one transaction. Saves,
subscriptions, location and email preferences survive, so signing back in
restores the account rather than starting an empty one. Both clients have a
Settings entry, above the delete-account section and deliberately not styled
destructive.

Two things found in the building that the estimate above did not anticipate:

- **The snapshot delete is load-bearing, not tidiness.**
  `FanoutSendDigestWorker` selects users on `digest_opt_in` and a non-empty
  email, with no session or connection check at all. A disconnected user whose
  `user_concert_snapshots` row survived would have kept receiving a nightly
  digest built from the Spotify profile they had just revoked — no error, no
  log. `TestDisconnectStopsTheNightlyDigest` fails if the delete is removed.
- **The empty-credential check prevents a panic, not a confusing error.**
  `gcm.Open` *panics* when the nonce is not exactly `NonceSize` bytes, so
  without the guard in `AccessTokenFor` the first job or request for a
  disconnected user would take down its goroutine.
  `TestDecryptingAnEmptyCredentialPanics` pins that.

One pre-existing defect surfaced alongside it: the iOS client's
`deleteAccount()` sent **no request body**, while the handler decodes
`confirm_name` unconditionally — so every in-app account deletion failed with
400 "invalid body: EOF". Guideline 5.1.1(v) requires in-app account deletion
that works, so the one path a reviewer is guaranteed to exercise was the
broken one. Fixed in the same change.

One thing the flow says plainly, because it is easy to overclaim: deleting our
copy of the token stops *us* using it, but it does not revoke the grant at
Spotify. That happens at
[spotify.com/account/apps](https://www.spotify.com/account/apps), and both
clients link there rather than implying the connection is gone from both ends.

### 10.1.3 The fallback, if a reviewer insists anyway

Decided in advance so it is never designed under submission pressure. The app
holds the Spotify token itself, hydrates the six signals on device, and posts
only the derived artist-ID/score profile to the server, which still does the
concert fan-out.

What that actually costs, beyond "it is a large change":

- `internal/spotify` hydration moves into Swift — six paginated sources with
  their own page caps, the failure-tolerance rule that a dead source degrades
  rather than destroys the profile, and the scoring weights. The Go
  implementation would have to stay for the web client, so the scoring becomes
  two implementations that must agree, which is the kind of duplication
  `TestGoldenFixtures` exists to police.
- Background freshness degrades to whatever the app can do in the background.
  The nightly affinity refresh and the pre-warmed snapshot both depend on
  using the grant while the app is closed, so the daily digest, instant
  notifications and push all become best-effort behind an app launch.
- The web client is unaffected — it is not an App Store product — which means
  the two clients would stop sharing an affinity path. That is the real cost:
  it breaks the "one backend, two clients" property in §1.1 that the whole
  plan is built on.

Worth asking App Review through the pre-submission channels before betting a
milestone on either reading.

### 10.1.4 Review-notes text, ready to paste

> ConcertFinder is a client for the user's own Spotify listening data. Users
> sign in to Spotify to access their own content — their followed artists,
> saved music and listening history — which is the entire product, so we
> understand Guideline 4.8's exemption for "a client for a specific
> third-party service" to apply and have not added a separate login service.
>
> On Guideline 5.1.1(v): we store an encrypted Spotify refresh token on our
> server, because the app's core feature is a nightly search for concerts by
> artists the user listens to, which necessarily runs while the app is closed.
> We understand the clause about storing tokens off-device to be scoped to
> social networks — the paragraph's own examples are Facebook, WeChat, Weibo
> and X — and Spotify is a music streaming service, not the social network
> this app is a client for. We are happy to discuss this if you read it
> differently.
>
> No Spotify credentials are stored on the device. The token is AES-256-GCM
> encrypted at rest. Raw listening data is never persisted: it is held in
> memory only and discarded once the derived affinity profile is built, which
> itself expires after 24 hours. Users can disconnect Spotify and delete their
> account from within the app, in Settings.
>
> Demo account credentials: [see the demo account section].

The last paragraph's claim about disconnecting became true on 2026-08-31, when
§10.1.2 shipped.

### 10.2 Guideline 4.8 and Sign in with Apple

4.8 requires apps using a third-party login for the primary account to also
offer a privacy-preserving alternative. It exempts, verbatim:

> Your app is a client for a specific third-party service and users are
> required to sign in to their mail, social media, or other third-party
> account directly to access their content.

ConcertFinder fits that exemption cleanly — the user signs into Spotify to
access their own listening data, which is the entire product — so Sign in
with Apple should not be required. But it is a reviewer judgment call, so
state the exemption explicitly in review notes. The contingency is
unattractive: adding Sign in with Apple means a second identity per user, an
account-linking flow, and a Spotify connection step, all for a user who
cannot use the app without Spotify anyway.

### 10.3 Two clients, one unversioned API

Covered in [§4.3](#43-api-surface-adjustments). The cost of acting is one
afternoon before M2; the cost of not acting is discovered months later when an
old build in someone's pocket breaks.

### 10.4 Cold-start latency on a phone

Covered in [§5.5](#55-offline-and-cold-start). Minutes of empty state on
first run is the single most likely reason a TestFlight user does not come
back on day two.

### 10.5 Open questions

- Does the app need to work at all for a signed-out user? Guideline 5.1.1(v)
  says "if your app doesn't include significant account-based features, let
  people use it without a login." The feed is inherently personal, so a login
  wall is defensible — but a browsable "shows near you" view without Spotify
  would strengthen both the review position and the first-run experience.
- iPad: universal app, or iPhone-only for v1? Universal is cheap in SwiftUI
  and Apple reviewers do open the iPad build.
- Widgets and Live Activities: post-v1, but a "next saved show" widget is a
  strong retention feature and worth designing the data layer around now.
- Does the app get its own bundle-level analytics? There is none today, and
  adding it changes the privacy labels.

---

## 11. Explicitly out of scope

- Android. React Native was considered and rejected in favour of native
  SwiftUI; revisiting Android means revisiting that decision, not porting.
- Playing music, or any Spotify playback SDK integration.
- Ticket purchase in-app. Links open in Safari; anything more raises
  in-app-purchase questions with Apple.
- New concert sources (SeatGeek et al. — `docs/design.md` §10.4).
- International expansion. US-only, as the backend is.
- Any change to the affinity scoring model. The app is a client.

---

## Appendix A: New configuration

| Variable | Purpose | Notes |
|---|---|---|
| `APNS_KEY_ID` | APNs auth key ID | |
| `APNS_TEAM_ID` | Apple Developer team ID | |
| `APNS_BUNDLE_ID` | App bundle identifier | Also used in the AASA file. |
| `APNS_P8_KEY` | The `.p8` private key | SSM SecureString, per `1ec3a4a`. Never log. |
| `APNS_ENVIRONMENT` | `sandbox`, `production`, or both, comma-separated | What the *key* is authorized for, not a host. Devices are routed individually. |
| `IOS_APP_ID` | `<TeamID>.<BundleID>` | For `apple-app-site-association`. |
| `MOBILE_CALLBACK_URL` | Universal link the callback redirects to | e.g. `https://<domain>/app/auth/callback`. |
| `MIN_IOS_BUILD` | Minimum accepted client build | Returned by `/api/site-info`. |

Follows the existing pattern: process env regardless of source, no AWS SDK
imports in `/internal`.

## Appendix B: Proposed new endpoints

| Method | Path | Auth | Purpose |
|---|---|---|---|
| `GET` | `/api/auth/login?client=ios&app_challenge=` | none | Existing handler, app-aware branch. |
| `POST` | `/api/auth/mobile/exchange` | none | `{code, verifier}` → `{session_token, expires_at, user}`. IP-rate-limited with the rest of `/api/auth`. |
| `POST` | `/api/me/devices` | bearer | Register or refresh an APNs token. Idempotent. |
| `DELETE` | `/api/me/devices/{token}` | bearer | Deregister on logout. |
| `GET` | `/.well-known/apple-app-site-association` | none | Universal-link association. `application/json`, no redirect. |

Every existing `/api/me/*` endpoint additionally accepts
`Authorization: Bearer <session_id>` and skips the CSRF check when it does.
