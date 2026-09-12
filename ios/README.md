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
domain is already wired into the entitlements and the Release config. The App
ID was registered 2026-08-25 — `com.concertfinder.ph`, team `L3MY7DN27B` — so
`project.yml` carries the real bundle identifier rather than a guess, and it
is now effectively permanent: it is also `APNS_BUNDLE_ID` and the second half
of `IOS_APP_ID`, which iOS caches from the association file.

**The server side is live.** The apply landed 2026-08-26 and the values are in
SSM — `../infra/terraform.tfvars` holds them and `../infra/secrets.tf` derives
the rest from `var.domain` and `var.ios_bundle_id`. Both of the values below
answer for real today, and you can check that from anywhere:

- `MOBILE_CALLBACK_URL` → `https://concertfinder.app/app/auth/callback`. Empty
  would make `/api/auth/login?client=ios` return 501; it does not.
- `IOS_APP_ID` → `L3MY7DN27B.com.concertfinder.ph`. Empty would make
  `/.well-known/apple-app-site-association` 404 **on purpose** — serving an
  association naming an empty app is worse, because iOS caches it. It serves.

**Signing is configured.** `project.yml` sets `DEVELOPMENT_TEAM: L3MY7DN27B`
with `CODE_SIGN_STYLE: Automatic`, so a device build does not need a team
picked by hand.

What is still outstanding, and both fail *silently*:

1. **Spotify is in Development Mode**, so sign-in completes only for
   allowlisted accounts. For everyone else the handshake runs and ends without
   a session. This is the gate, not the backend config — see "Not done here".
2. **The APNs key is authorized for sandbox only** (`apns_environment =
   "sandbox"`). `aps-environment` in the entitlements is `development`, which
   is what a debug build off Xcode produces, so debug push works. Xcode
   rewrites that entitlement to `production` for TestFlight and App Store
   builds, and those devices are **skipped with a log** until the key covers
   production too.

That second one is worth stating precisely, because the mechanism changed and
the old note here described a server that no longer exists. `apns_environment`
is **not a host selector**: it names which environments the `.p8` is
*authorized* for, a property of how Apple issued the key. Each notification is
routed to its own host from the `Environment` stamped on the device row, so a
single deployment serves debug and TestFlight builds at once — given a key
issued as "Sandbox & Production". Reissue the key before the first TestFlight
upload and set `apns_environment = "sandbox,production"`; there is no flip and
no matching entitlement change to remember.

The app otherwise builds and runs against the live API.

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
- **Apple Developer setup and M8**: the App ID is registered and signing is
  configured; what is left is the APNs key reissue, the App Store Connect app
  record, privacy labels, screenshots, review notes and submission.

Plan §10.1 and §10.2 are the two App Review questions worth raising in review
notes rather than discovering at review.
