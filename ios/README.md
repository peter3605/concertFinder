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
# What CI runs (it picks an available simulator rather than naming one —
# device line-ups change between Xcode releases)
xcodegen generate
xcodebuild test -project ConcertFinder.xcodeproj -scheme ConcertFinder \
  -destination "id=$(xcrun simctl list devices available | \
    grep -m1 -o '[0-9A-F-]\{36\}')" CODE_SIGNING_ALLOWED=NO
```

**Regenerate after adding a file, or it is not in the target.** A test file
that is not in the project does not fail — it silently does not run.

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

**The fixtures in `ConcertFinderTests/Fixtures` are generated, not written.**
`TestGoldenFixtures` in `internal/http` marshals the real Go response structs
into them; the Swift tests decode them. That is the contract check, and both
halves bite: rename a `json` tag in Go and the Go test fails, regenerate
without updating the Swift models and the Swift tests fail.

```sh
go test ./internal/http -run TestGoldenFixtures -update   # after a Go shape change
```

Do not hand-edit them. The first run of the generator caught a fabricated
`is_default` field that the server never sends — and a Swift test asserting
it, passing against the fabrication.

UI tests come in two sets. `LaunchUITests` needs no backend and runs in CI.
`SignedInUITests` covers the flows plan §8 names — feed load, save, filter,
event detail — and skips unless a session is injected:

```sh
CF_UI_TEST_SESSION_TOKEN=<session id> \
CF_UI_TEST_API_BASE_URL=https://concertfinder.app \
  xcodebuild test -only-testing:ConcertFinderUITests/SignedInUITests ...
```

The handshake runs in `ASWebAuthenticationSession` against Spotify's own login
page, so it cannot be automated without driving a third party's web UI and
storing their credentials. Injecting an already-obtained session tests
everything after login instead. The injection is `#if DEBUG` only — in a
Release binary an environment variable that installs a session would be a
vulnerability, not a convenience.

The Go side has its own database-backed tests for the notification ledger;
see the repo root README.

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
