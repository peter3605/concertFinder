# ConcertFinder

Builds a personalized concert feed from your Spotify listening history: it
scores the artists you actually engage with, fans out across ticketing APIs for
their upcoming US shows, and merges the results into one deduplicated list you
can filter, save from, and subscribe to.

Go API + React SPA + PostgreSQL, deployed as two containers on a single EC2
instance behind Caddy.

## Status

Phases 1–3 are implemented and the application is feature-complete. **It is not
yet deployed** — the AWS infrastructure has not been provisioned, so the
`deploy` job in CI fails at credential federation by design. See
`docs/aws-deploy.md` for what standing it up requires.

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
./scripts/check-deploy-config.sh      # after touching Caddyfile or either compose file
```

## Layout

```
cmd/server          entrypoint (HTTP server + river job workers, one process)
internal/spotify    Spotify client + affinity scoring
internal/concerts   aggregation, dedup, event grouping, scoring
internal/fallback   Phase 2 small-artist chain (MusicBrainz → official site → JSON-LD)
internal/jobs       river workers: scan, digest, affinity refresh, janitor
internal/http       handlers and middleware
internal/db         queries and migrations runner
migrations          SQL migrations
web                 React + TS + Vite SPA
infra               Terraform for the AWS deployment
```

Each external API gets its own package with its own types — there is
deliberately no shared `models` package, because external schemas drift
independently.

## Documentation

| Doc | What it covers |
|---|---|
| [`docs/design.md`](docs/design.md) | **Authoritative** on architecture, schema, API choices, and phased scope. Read before implementing anything non-trivial. |
| [`docs/local-dev.md`](docs/local-dev.md) | First-time local setup, running, resetting the database. |
| [`docs/aws-deploy.md`](docs/aws-deploy.md) | Provisioning and deploying to AWS; the manual fallback to Terraform. |
| [`infra/README.md`](infra/README.md) | Terraform config — the authoritative path for standing up infrastructure. |
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
  `RATE_CAP_TM_PER_USER_DAILY=250` works out to roughly **20 concurrently
  active users**. The rate ledger enforces per-user limits only; it does not
  model the account total, so exceeding this degrades feeds rather than
  erroring.
- **Spotify** — the app runs in Development Mode, which caps authorized users.
  Lifting it requires an approved Extended Quota Mode application, which has
  not been started (design §9.1, §10.2).

Raising either is a process with a third party, not an engineering task, so
start early if the user count matters.
