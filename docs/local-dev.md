# Local development setup

## Prerequisites

- Go 1.22+
- Node 20+
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

Check that `DATABASE_URL`'s port matches what compose actually publishes —
`.env.example` says 5432, and if `docker-compose.yml` maps the container to a
different host port you'll need to override it:

```
export DATABASE_URL='postgres://concertfinder:concertfinder@127.0.0.1:5433/concertfinder?sslmode=disable'
```

Then open https://127.0.0.1:3000 and click "Log in with Spotify". The first
`/api/me/concerts` request returns an empty list with `refreshing: true` while
a background scan runs — the frontend polls every 10s and fills in when the
snapshot lands. That first scan is the slow one (cold affinity, cold
Ticketmaster and MusicBrainz caches); afterwards the affinity profile is
cached 24h and upstream responses `CONCERT_CACHE_TTL_HOURS` (12h).

## Resetting the local database

```
docker compose down -v           # -v removes the pgdata volume
docker compose up -d db
```

The migration runner will recreate everything on next `go run`.
