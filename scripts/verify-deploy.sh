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
# Exits non-zero on failure, which fails the SSM command, which fails the
# workflow. That is the entire point — the previous arrangement reported
# success for a site that was down.
set -euo pipefail

cd "$(dirname "$0")/.."

COMPOSE=(docker compose -f docker-compose.prod.yml)
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

[ -f .env ] || fail ".env is missing from $PWD"

# SITE_DOMAIN is Caddy's site block. Read it from the deployment's own .env —
# scripts/check-deploy-config.sh validates against a synthetic one it writes
# itself, so it proves the wiring but structurally cannot see this file.
# Tolerates surrounding quotes, whitespace, and CRLF line endings.
SITE_DOMAIN=$(
    sed -n 's/^[[:space:]]*SITE_DOMAIN[[:space:]]*=//p' .env |
        head -n1 | tr -d '\r' |
        sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' \
            -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
)
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
        printf '\n\033[32mDeploy verified\033[0m\n'
        exit 0
    fi
    if [ "$attempt" -lt "$ATTEMPTS" ]; then
        echo "  ... attempt $attempt/$ATTEMPTS failed, retrying in ${SLEEP}s"
        sleep "$SLEEP"
    fi
done

fail "/api/healthz did not return 200 after $ATTEMPTS attempts ($((ATTEMPTS * SLEEP))s)"
