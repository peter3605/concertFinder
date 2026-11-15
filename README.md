# ConcertFinder

Builds a personalized feed of upcoming US concerts from the artists and genres
you pick, or from your Spotify listening history: it fans out across ticketing
APIs for their shows and merges the results into one deduplicated list you can
filter, save from, and subscribe to.

Go API + React SPA + PostgreSQL, deployed as one container on a shared EC2 box
behind a shared Caddy (see [apps-infra](https://github.com/peter3605/apps-infra)).

## Status

Phases 1–3 are implemented and deployed, running at
<https://concertfinder.app>. Since the 2026-11-09 cutover it runs on the shared
apps box: an EC2 `t4g.small` in the apps AWS account, with Postgres on the
shared RDS instance, one Caddy terminating TLS for every app on the box, and
secrets rendered from SSM Parameter Store at deploy time. apps-infra owns the
box; [`docs/shared-box.md`](docs/shared-box.md) is concertFinder's runbook for
it. The previous single-app instance and its Neon database were retired on
2026-11-13.

Anyone with an invite can sign in by email and build a feed from artist and
genre picks (`docs/public-path.md`). Spotify sign-in stays at five accounts: the
app is in Spotify's Development Mode, which Spotify does not lift for an app
this size.

A native iOS client lives in `ios/`. Its server-side half — bearer auth, the
mobile code exchange, APNs — is implemented and deployed, and the Apple
Developer configuration has been applied since 2026-08-26: mobile sign-in is
live and push is wired. What is left is the APNs key being reissued for both
environments (it was created Sandbox-only, and a sandbox key against the
production host reads as a dead token) and the App Store submission itself.
**`docs/ios-app-plan.md` §0 is the current status and the ordered list of what
is left** — start there rather than at the top of that document,
whose body is the original plan and reads as aspirational.

One trap worth knowing before touching secrets: `scripts/render-env.sh`
refuses to render while any `/concertfinder/*` SSM parameter still holds the
`REPLACE_ME` placeholder, so a placeholder fails the next deploy.

## Quickstart

Needs Go 1.25+, Node 24+, Docker, and `mkcert`.

```bash
cp .env.example .env                  # then fill in SPOTIFY_CLIENT_ID, TICKETMASTER_API_KEY, ENCRYPTION_KEY
docker compose up -d db               # Postgres on host port 5433
set -a && . ./.env && set +a          # the binary has no dotenv dependency
go run ./cmd/server                   # migrations apply on startup
npm --prefix web install && npm --prefix web run dev
```

Then open <https://127.0.0.1:3000>. Spotify rejects `http://localhost` as a
redirect URI, so local dev runs under HTTPS on `127.0.0.1` with a `mkcert`
certificate — **`docs/local-dev.md` has the full first-time setup**, including
generating that certificate and registering the Spotify app. Skipping it will
not work.

## Commands

```bash
go build ./... && go vet ./... && go test ./...
npm --prefix web run lint && npm --prefix web run build
./scripts/check-deploy-config.sh      # after touching a compose file or a script the box runs
```

## Layout

```
cmd/server          entrypoint (HTTP server + river job workers, one process)
internal/spotify    Spotify client + affinity scoring
internal/concerts   aggregation, dedup, event grouping, scoring
internal/fallback   Phase 2 small-artist chain (MusicBrainz → official site → JSON-LD)
internal/jobs       river workers: scan, digest, affinity refresh, janitor
internal/http       handlers and middleware
internal/rate       per-user and account-wide daily API quota ledgers
internal/push       APNs delivery for the iOS client
internal/db         queries and migrations runner
migrations          SQL migrations
web                 React + TS + Vite SPA
ios                 native SwiftUI client; XcodeGen spec, no committed .pbxproj
infra               Terraform for the retired old box (history; nothing here is applied)
scripts             deploy and restore-drill scripts, plus the retired old box's backup and watchdog
```

`docs/design.md` §2.3 is the complete package list and why each one exists.

Each external API gets its own package with its own types — there is
deliberately no shared `models` package, because external schemas drift
independently.

## Deploys

Merging to `main` deploys. `.github/workflows/image.yml` builds the api image
for `linux/arm64` on an arm64 runner (pull requests only build, with no
credentials), pushes `concertfinder-api:<sha>` and `:latest` to ECR in the apps
account, and runs the `concertfinder-deploy` SSM document on the shared box.
`deploy.yml` runs the checks only. Three **repository variables** (Settings →
Secrets and variables → Actions → *Variables*) drive it:

| Variable | Value | Unset |
|---|---|---|
| `AWS_DEPLOY_ROLE_ARN` | `concertfinder-deploy` role ARN (`terraform output deploy_role_arns` in apps-infra) | The push job emits a notice, then builds and pushes nothing |
| `DEPLOY_TO_BOX` | `true` to run the `concertfinder-deploy` SSM document after the push | Push only, no deploy |
| `BOX_INSTANCE_ID` | The shared box (`terraform output box_instance_id`) | Required when `DEPLOY_TO_BOX` is `true`; the job fails without it |

## Documentation

| Doc | What it covers |
|---|---|
| [`docs/design.md`](docs/design.md) | **Authoritative** on architecture, schema, API choices, and phased scope. Read before implementing anything non-trivial. |
| [`docs/local-dev.md`](docs/local-dev.md) | First-time local setup, running, resetting the database. |
| [`docs/shared-box.md`](docs/shared-box.md) | How concertFinder runs and deploys on the shared apps box. |
| [`docs/aws-deploy.md`](docs/aws-deploy.md) | History: the retired old box (EC2 + Neon). Kept for its incident notes; describes nothing that runs. |
| [`docs/launch-runbook.md`](docs/launch-runbook.md) | The human tasks left before launch, in order, with the commands. Backups, key rotation, the Spotify quota application, APNs, App Store. |
| [`CLAUDE.md`](CLAUDE.md) | Constraints that are easy to violate, and why. Worth reading before changing serving, jobs, or quota code. |

Where `CLAUDE.md` and `docs/design.md` disagree about what the code does
*today*, `CLAUDE.md` wins; the design doc describes intent.

## Constraints worth knowing up front

These come from third-party terms and have real consequences:

- **Raw Spotify listening data is never persisted.** It is held in memory only
  and discarded after the affinity profile is built; only the derived profile
  (artist IDs + scores) is stored, with a 24-hour TTL.
- **No ML training on Spotify data**, including embeddings and similarity
  learning.
- **Refresh tokens are AES-256-GCM encrypted at rest** with a per-token nonce.
- **"Powered by Spotify" attribution** is required on any UI surface showing
  Spotify-derived data.
- **DICE.fm is excluded** from any scraping or fallback work; their terms
  prohibit automated access.
- Several Spotify endpoints were removed in Feb 2026 (`/recommendations`,
  `/audio-features`, `related-artists`, and others) and must not be called.
  Affinity is built entirely from the user's own explicit signals.

## Scale ceiling

Two upstream quotas bound how many people this can serve today, and neither is
fixed by scaling the infrastructure:

- **Ticketmaster** — an account-wide budget of 5000 calls/day against
  `RATE_CAP_TM_PER_USER_DAILY=500` works out to roughly **10 concurrently
  active users**. 500 is what a *cold* scan costs: 200 artists × 2 calls,
  because TM resolution is two-stage and only the second stage is cached
  forever. Halving it would double the user count and hand every new user a
  half-empty first feed. The ledger models both limits — per user *and* per
  account (`RATE_CAP_TM_ACCOUNT_DAILY`, default 5000) — so exhausting the
  shared ceiling surfaces as a dated `retry_after` rather than as upstream
  403s that look exactly like artists with no shows.
- **Spotify** — the app runs in Development Mode, which caps authorized users.
  Lifting it requires Extended Quota Mode, which Spotify grants only to
  organizations with 250k monthly active users, so Spotify sign-in stays at
  five accounts (`SPOTIFY_MAX_USERS`; `docs/public-path.md` §7).

Raising either is a process with a third party, not an engineering task, so
start early if the user count matters.
