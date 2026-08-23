# ConcertFinder for iOS

Native SwiftUI client for the ConcertFinder API. Same backend, same
`/api/me/*` handlers, same database as the web SPA — this is a second client,
not a second service. See `../docs/ios-app-plan.md` for the full plan; §1.1
explains why the one-backend decision is load-bearing.

## Build

The `.xcodeproj` is **generated, not committed**. `project.yml` is the source
of truth.

```sh
brew install xcodegen
cd ios
xcodegen generate
open ConcertFinder.xcodeproj
```

Regenerate after adding or moving files. CI does the same thing, so a spec
that does not generate is a red build rather than a surprise later.

```sh
# What CI runs
xcodegen generate
xcodebuild test -project ConcertFinder.xcodeproj -scheme ConcertFinder \
  -destination 'platform=iOS Simulator,name=iPhone 17' CODE_SIGNING_ALLOWED=NO
```

## Pointing at a backend

`CF_API_BASE_URL` is a build setting, read through `Info.plist` as
`CFAPIBaseURL`. Debug points at `https://127.0.0.1:3000`; Release at
`https://concertfinder.app`, the live deployment.

Change it in `project.yml` under the target's `configs:`, then regenerate.

## Before this can run against the real backend

The backend is deployed and serving at `https://concertfinder.app`, and the
domain is already wired into the entitlements and the Release config. What is
still outstanding needs an Apple Developer account, and every item fails
*silently* — the app builds, launches, and sign-in simply never completes.

1. **Apple identifiers.** The bundle identifier in `project.yml`
   (`com.concertfinder.app`) is a guess. It has to match the real App ID, and
   it is also `APNS_BUNDLE_ID` and the second half of `IOS_APP_ID`.
2. **The server side**, in SSM (see `../infra/secrets.tf`, which derives most
   of these from `var.domain` and `var.ios_bundle_id`):
   - `MOBILE_CALLBACK_URL` → `https://concertfinder.app/app/auth/callback`.
     Empty means `/api/auth/login?client=ios` returns 501 rather than
     completing into a session the app cannot read.
   - `IOS_APP_ID` → `<TeamID>.<BundleID>`. Empty means
     `/.well-known/apple-app-site-association` 404s **on purpose** — serving
     an association naming an empty app is worse, because iOS caches it.
   - The four `APNS_*` variables. `config.Validate` rejects a partial set at
     startup, so a half-configured deployment refuses to boot rather than
     dropping every notification quietly.

Until then the app builds and runs against the live API for everything except
sign-in and push.

Note that the currently deployed binary predates this branch: `/api/site-info`
does not yet return `min_ios_build`, and the mobile auth routes are not there.
Deploying `ios-app` is what closes that gap.

## Layout

```
ConcertFinder/
  App/            entry point, root navigation, DI container, deep links
  Core/
    Networking/   APIClient actor, typed errors
    Auth/         AuthController, Keychain, ASWebAuthenticationSession
    Models/       Codable mirrors of web/src/lib/types.ts
    Storage/      offline snapshot cache, filter persistence
    Push/         APNs registration, payload handling
  Features/       Feed, EventDetail, Saved, Artists, Location, Settings
  DesignSystem/   metrics, banners, the Spotify attribution view
```

## Things that will bite you

These are the non-obvious constraints, all of which fail quietly:

- **Save and subscribe are per act, not per card.** A festival is one `Event`
  with several `Act`s, each carrying its own `dedup_key`. Saving "the event"
  saves the wrong artist.
- **Facet values go back verbatim.** Genre matching is exact-tag
  case-insensitive; venue matching runs under the server's normalizer.
  Lowercasing or trimming a facet value turns a pill promising 12 results
  into one returning none.
- **`complete: false` is a UI state, not a log line.** It means the scan did
  not cover every artist. Without surfacing it, a quiet week and a truncated
  scan are indistinguishable.
- **`Event.date` is the earliest act's set time.** It is for sorting and month
  grouping. Do not present it as when a particular act plays on a multi-act
  bill.
- **Polling must suspend on background.** A 10-second timer that survives
  backgrounding is a battery complaint and an App Review question.
- **Register the device token on every launch**, not just on first grant.
  APNs rotates tokens silently and a stale one fails as `BadDeviceToken`.
- **The app never sees a Spotify token.** All Spotify access is
  server-mediated. This is non-negotiable per `docs/design.md` §2 and it is
  also the basis of the Guideline 5.1.1(v) argument in plan §10.1.

## Testing

Unit tests decode **captured** responses from `ConcertFinderTests/Fixtures`
rather than hand-written literals — a literal encodes what we think the server
sends, a fixture encodes what it sent. The set covers a six-act festival, an
empty feed, an incomplete scan with `retry_after`, and a throttled refresh.

UI tests are scoped to what holds with no backend reachable, so they stay
green in CI without secrets. The signed-in flows in plan §8 (feed load, save,
filter) need a Spotify account on the Development Mode allowlist and a build
signed against the real App ID — add them once those exist.

## Not done here

This directory covers the M1–M7 client code. Still outstanding, and none of it
engineering:

- **Spotify Extended Quota Mode** (plan §3.2). The app is in Development Mode,
  so only allowlisted accounts can sign in — including App Review's own
  reviewer, which is why §3.2 says to ship with an allowlisted demo account
  and treat Extended Quota as what makes the app usable by anyone who
  downloads it. Longest lead time in the plan.
- **Apple Developer setup and M8**: App ID, signing, APNs key, privacy labels,
  screenshots, review notes, submission.
- **Deploying this branch.** The backend is live but running an older build;
  the mobile auth routes ship with `ios-app`.

Plan §10.1 and §10.2 are the two App Review questions worth raising in review
notes rather than discovering at review.
