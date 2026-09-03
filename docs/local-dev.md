# Local development setup

## Prerequisites

- Go 1.25+ (matches `go.mod`, the Dockerfile build stage, and CI — older
  minor versions will not build)
- Node 24+ (active LTS; Node 20 went end-of-life 2026-04-30 and no longer
  matches the Dockerfile or CI)
- Docker (for Postgres)
- `mkcert` — install with `brew install mkcert` on macOS, or see https://github.com/FiloSottile/mkcert

## First-time setup

### 1. Copy the env template

```
cp .env.example .env
# then fill in SPOTIFY_CLIENT_ID, TICKETMASTER_API_KEY, ENCRYPTION_KEY, etc.
```

Generate the encryption key with:

```
openssl rand -hex 32
```

### 2. Generate a local TLS certificate for Spotify auth

Spotify rejects `http://localhost` as a redirect URI. Local dev must run under
HTTPS on `127.0.0.1`. Vite reads the cert at boot; if the files are missing it
falls back to HTTP and prints a warning (auth won't work in that state).

```
mkcert -install
mkdir -p web/certs
mkcert -cert-file web/certs/localhost-cert.pem \
       -key-file  web/certs/localhost-key.pem \
       127.0.0.1
```

`web/certs/` is gitignored.

### 3. Register the Spotify app

At https://developer.spotify.com/dashboard, create an app and set the redirect
URI to exactly:

```
https://127.0.0.1:3000/api/auth/callback
```

Copy the Client ID into `.env` as `SPOTIFY_CLIENT_ID`.

## Running

```
docker compose up -d db                    # start Postgres in the background
set -a && . ./.env && set +a               # see below — the binary won't read .env for you
go run ./cmd/server                        # migrations apply automatically on startup
cd web && npm install && npm run dev
```

**Source `.env` yourself.** The server reads its configuration from the process
environment and has no dotenv dependency; under `docker compose` it is compose
that loads the file. Run `go run ./cmd/server` in a shell that hasn't sourced
it and the config comes up empty, failing on `DATABASE_URL`.

`.env.example` already points `DATABASE_URL` at **port 5433**, which is where
`docker-compose.yml` publishes the db container (5432 inside, 5433 on the host,
so it can't collide with a local Postgres). If you changed that mapping, change
this to match.

`DB_MAX_CONNS` (optional, default 20) sizes the pgx pool. It is set in code
rather than in the connection string because pgx's own default is
`max(4, NumCPU)` — four on the production instance — and that pool is shared by
river's LISTEN/NOTIFY notifier, which holds one connection open indefinitely,
its elector, producer and completer, five job workers, and every HTTP request.
Exhaustion never raises: callers block inside `Acquire` until their own context
expires, so it reads as slow requests and scans that spend their budget waiting
for a connection. The server logs `db pool` every 60s with `empty_acquires` and
`empty_acquire_wait`, which are the two numbers that distinguish a saturated
pool from a slow query.

Then open https://127.0.0.1:3000 and click "Log in with Spotify". The first
`/api/me/concerts` request returns an empty list with `refreshing: true` while
a background scan runs — the frontend polls every 10s and fills in when the
snapshot lands. That first scan is the slow one (cold affinity, cold
Ticketmaster and MusicBrainz caches); afterwards the affinity profile is
cached 24h and upstream responses `CONCERT_CACHE_TTL_HOURS` (12h).

## Before you push

```
go build ./... && go vet ./... && go test ./...
npm --prefix web run lint && npm --prefix web run build
```

CI also runs `govulncheck ./...` in the same job the deploy depends on, so a
known-vulnerable dependency blocks the ship rather than annotating it. It
reports against whichever toolchain is in PATH, so a standard-library finding
locally usually means your Go is behind, not that the tree is:

```
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

If you touched `Caddyfile`, either compose file, or any of the scripts that
only ever run on the instance — `verify-deploy.sh`, `render-env.sh`,
`backup-db.sh`, `prune-images.sh`, `restore-drill.sh` — also run:

```
./scripts/check-deploy-config.sh          # requires docker
```

Those files are the only ones in the repo that never execute during local
development — `docker-compose.yml` has no Caddy service and `go run` reads none
of them — so nothing else catches a defect in them until it is already in
production, where the symptom is a Caddy crash loop with a healthy-looking api
container beside it. The script parses each one and asserts its executable bit.
`restore-drill.sh` is the extreme case: nothing runs it on a schedule, so its
only automated attention is this check and the day it is needed is the worst
day to find a typo. CI runs the same script in the `deploy-config` job.

## Resetting the local database

```
docker compose down -v           # -v removes the pgdata volume
docker compose up -d db
```

The migration runner will recreate everything on next `go run`.
