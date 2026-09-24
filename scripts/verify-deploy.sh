#!/usr/bin/env bash
# Post-deploy smoke test. Run on the instance by .github/workflows/deploy.yml
# immediately after `docker compose up -d --wait`.
#
# Why this exists, given that `--wait` already blocks on the api container's
# healthcheck: the healthcheck probes the api from *inside its own container*
# and proves only that the Go process answers itself. It says nothing about
# Caddy — which is the container with the history. Every deploy defect this
# repo has shipped lived in the Caddyfile or the compose file and presented as
# a Caddy crash loop with a perfectly healthy api container beside it. This
# probes the whole serving chain the way a user does: TLS on 443, the
# reverse_proxy hop, the api, and (because /api/healthz pings Postgres) the
# database.
#
# A 200 from /api/healthz means *a* database answered, not *ours*. CF-B17 ran
# production for eight days against an empty dev Postgres with this script
# green on every deploy, because the dev database answered too. So after the
# probe, this compares the host the api container was actually started with
# (`docker inspect` — the image is distroless, there is no shell to `exec env`
# in) against the host in the deployment's .env, and fails on a mismatch.
# That comparison lives here, on the instance, rather than in /api/healthz:
# that endpoint is public, and publishing the database hostname on it would
# be disclosure for nobody's benefit.
#
# Only hosts are ever printed, never a DSN, and only on a mismatch. This output lands in the workflow
# log on failure and the repo is public. db_host refuses anything that is not
# a bare hostname rather than echo it, so a DSN it cannot parse cannot leak its
# password through the error message either.
#
# Exits non-zero on failure, which fails the SSM command, which fails the
# workflow. That is the entire point — the previous arrangement reported
# success for a site that was down.
#
# VERIFY_ENV_FILE overrides which env file supplies the *intended* values
# (default .env). It exists so the mismatch path can be exercised on the
# instance against a scratch copy, without editing the live .env.
set -euo pipefail

# env_value KEY FILE — the first KEY=value in FILE, tolerating surrounding
# quotes, whitespace, and CRLF line endings.
env_value() {
    sed -n "s/^[[:space:]]*$1[[:space:]]*=//p" "$2" |
        head -n1 | tr -d '\r' |
        sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
            -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

# db_host DSN — the lowercased host of a Postgres DSN, URL or key=value form.
# Returns non-zero, printing nothing, when the result is not a bare hostname
# or bracketed IPv6 literal (a unix socket, a multi-host list, or something it
# misparsed — which could otherwise be a fragment of the userinfo).
#
# The authority ends at the first / ? or #; the userinfo ends at the last @
# inside it. A valid URL cannot carry any of /?# raw in its password (pgx's
# url.Parse would reject it too), so this cannot cut a password in half.
db_host() {
    local dsn=$1 rest host
    case $dsn in
        *://*)
            rest=${dsn#*://}
            rest=${rest%%[/?#]*}
            rest=${rest##*@}
            case $rest in
                *,*) return 1 ;; # multi-host: no single host to compare
                \[*) host=${rest%%]*}] ;;
                *) host=${rest%%:*} ;;
            esac
            ;;
        *)
            host=$(printf '%s\n' "$dsn" | tr ' ' '\n' | sed -n 's/^host=//p' | head -n1)
            ;;
    esac
    host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')
    [[ $host =~ ^[a-z0-9.-]+$ || $host =~ ^\[[0-9a-f:.]+\]$ ]] || return 1
    printf '%s\n' "$host"
}

# Sourced by scripts/check-deploy-config.sh to test db_host; stop here.
if [[ ${BASH_SOURCE[0]} != "$0" ]]; then
    return 0
fi

cd "$(dirname "$0")/.."

COMPOSE=(docker compose -f docker-compose.prod.yml)
ENV_FILE=${VERIFY_ENV_FILE:-.env}
ATTEMPTS=12
SLEEP=5

fail() {
    printf '\n\033[31mDEPLOY VERIFICATION FAILED\033[0m: %s\n\n' "$1" >&2
    echo "--- container state ---" >&2
    "${COMPOSE[@]}" ps >&2 || true
    echo >&2
    echo "--- recent logs ---" >&2
    # The reason is almost always in here: config.Validate reports every
    # problem at once on the api side, and Caddy names the bad directive.
    "${COMPOSE[@]}" logs --tail=80 >&2 || true
    exit 1
}

[ -f "$ENV_FILE" ] || fail "$ENV_FILE is missing from $PWD"

# SITE_DOMAIN is Caddy's site block. Read it from the deployment's own .env —
# scripts/check-deploy-config.sh validates against a synthetic one it writes
# itself, so it proves the wiring but structurally cannot see this file.
SITE_DOMAIN=$(env_value SITE_DOMAIN "$ENV_FILE")
[ -n "$SITE_DOMAIN" ] || fail "SITE_DOMAIN is not set in .env — Caddy's site block would collapse into a global options block and the container would crash-loop"

echo "Verifying https://$SITE_DOMAIN/api/healthz ..."

# --resolve pins the connection to this box while keeping the real hostname for
# SNI, so Caddy matches its site block instead of falling through. -k because
# on a first deploy the ACME certificate may still be provisioning; this is a
# loopback connection, so there is no transport to protect here anyway.
for attempt in $(seq 1 "$ATTEMPTS"); do
    if curl -fsS -o /dev/null --max-time 10 -k \
        --resolve "$SITE_DOMAIN:443:127.0.0.1" \
        "https://$SITE_DOMAIN/api/healthz"; then
        printf '\033[32m  ok\033[0m  /api/healthz returned 200 (api up, Postgres reachable, Caddy proxying)\n'
        healthy=1
        break
    fi
    if [ "$attempt" -lt "$ATTEMPTS" ]; then
        echo "  ... attempt $attempt/$ATTEMPTS failed, retrying in ${SLEEP}s"
        sleep "$SLEEP"
    fi
done

[ "${healthy:-}" = 1 ] ||
    fail "/api/healthz did not return 200 after $ATTEMPTS attempts ($((ATTEMPTS * SLEEP))s)"

# Which Postgres answered. The intended URL comes from the env file; the actual
# one from the running container's config, which is what Compose resolved after
# every file and override was merged — the layer CF-B17 went wrong in.
intended_dsn=$(env_value DATABASE_URL "$ENV_FILE") || fail "could not read $ENV_FILE"
[ -n "$intended_dsn" ] || fail "DATABASE_URL is not set in $ENV_FILE"
intended_host=$(db_host "$intended_dsn") ||
    fail "could not parse a database host out of DATABASE_URL in $ENV_FILE (value withheld: it carries credentials)"

# Every command substitution here carries its own `|| fail`: under set -e a
# failing one would otherwise end the script with no message at all.
api_cid=$("${COMPOSE[@]}" ps -q api) || fail "docker compose ps failed"
[ -n "$api_cid" ] || fail "no api container is running"
actual_dsn=$(
    docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$api_cid" |
        sed -n 's/^DATABASE_URL=//p' | head -n1
) || fail "docker inspect of the api container failed"
[ -n "$actual_dsn" ] || fail "the api container has no DATABASE_URL in its environment"
actual_host=$(db_host "$actual_dsn") ||
    fail "could not parse a database host out of the api container's DATABASE_URL (value withheld: it carries credentials)"

if [ "$actual_host" != "$intended_host" ]; then
    fail "the api is connected to the wrong database: container has host '$actual_host', $ENV_FILE says '$intended_host'"
fi
# The host is named only on failure, where it is the diagnosis. On success it
# would add nothing a reader needs, and this output is the deploy workflow's
# log, which is public: printing it there would publish the database endpoint
# on every deploy — the disclosure this check was kept out of /api/healthz to
# avoid.
printf '\033[32m  ok\033[0m  api database host matches %s\n' "$ENV_FILE"

printf '\n\033[32mDeploy verified\033[0m\n'
