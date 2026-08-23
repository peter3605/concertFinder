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
`CFAPIBaseURL`. Debug defaults to `https://127.0.0.1:3000`; Release is a
placeholder until there is a deployed origin.

Change it in `project.yml` under the target's `configs:`, then regenerate.

## Before this can run against a real backend

Three things in `project.yml` and the entitlements are placeholders, and all
three fail *silently* if left as they are — the app builds and launches, and
sign-in simply never completes.

1. **`ConcertFinder/Resources/ConcertFinder.entitlements`** —
   `applinks:REPLACE_WITH_YOUR_DOMAIN`. iOS fetches
   `/.well-known/apple-app-site-association` from this domain to decide
   whether the app may claim its universal links. A wrong value is cached by
   the device.
2. **`project.yml`, Release config** — `CF_API_BASE_URL`.
3. **The server side**: `MOBILE_CALLBACK_URL`, `IOS_APP_ID`, and the four
   `APNS_*` variables. `config.Validate` rejects a partial APNs set at
   startup, so a half-configured deployment refuses to boot rather than
   dropping every notification quietly. See `../.env.example`.

The bundle identifier (`com.concertfinder.app`) is a placeholder too, and it
has to match `APNS_BUNDLE_ID` and the second half of `IOS_APP_ID`.

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

UI tests are scoped to what holds with no backend reachable. The signed-in
flows in plan §8 need a live server and an allowlisted Spotify account; they
belong here once M0 lands.

## Not done here

Per plan §7, this repo covers M1–M7 code. Still outstanding and *not*
engineering tasks: deploying the backend (M0), Spotify Extended Quota Mode
(§3.2, the longest pole), and App Store signing, privacy labels, screenshots
and submission (M8). See §10 for the two App Review questions worth raising
before submitting rather than discovering at review.
