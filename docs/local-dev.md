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
https://127.0.0.1:3000/callback
```

Copy the Client ID into `.env` as `SPOTIFY_CLIENT_ID`.

## Running

```
docker compose up -d db          # start Postgres in the background
go run ./cmd/server              # migrations apply automatically on startup
cd web && npm install && npm run dev
```

Then open https://127.0.0.1:3000 and click "Log in with Spotify". The first
`/api/me/concerts` request will take longer (cold affinity + cold TM/BIT
caches); subsequent requests are 24h-cached at the affinity layer and 4h at
the concert layer.

## Resetting the local database

```
docker compose down -v           # -v removes the pgdata volume
docker compose up -d db
```

The migration runner will recreate everything on next `go run`.
