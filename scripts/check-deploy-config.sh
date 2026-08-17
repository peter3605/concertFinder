#!/usr/bin/env bash
# Validate the deployment config that nothing else exercises.
#
# docker-compose.prod.yml and the Caddyfile are the only files in this repo
# that never run during local development — `docker-compose.yml` has no Caddy
# service, and `go run` doesn't read either one. Three separate defects have
# shipped in them as a result, each presenting identically: Caddy exits on a
# config error, `restart: unless-stopped` turns that into a crash loop, and the
# api container beside it looks perfectly healthy the whole time.
#
#   1. `header_up` written at site level instead of inside `reverse_proxy`.
#   2. No `env_file` on the caddy service, so SITE_DOMAIN was empty inside the
#      container and `{$SITE_DOMAIN} {` collapsed into a global options block.
#   3. (the general case) any Caddyfile edit, since nothing parses it until it
#      is already in production.
#
# Run from the repo root. Requires docker.
set -euo pipefail

cd "$(dirname "$0")/.."
repo="$PWD"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Work on copies with a synthetic .env: never read the developer's real one,
# and never print it — `docker compose config` echoes every variable it
# resolves, secrets included.
cp docker-compose.prod.yml Caddyfile "$work/"
printf 'SITE_DOMAIN=example.com\n' > "$work/.env"

fail() { printf '\n\033[31mFAIL\033[0m: %s\n' "$1" >&2; exit 1; }
pass() { printf '\033[32m  ok\033[0m  %s\n' "$1"; }

echo "Checking deployment config..."

# 1. The prod compose file parses and its env_file resolves.
rendered="$work/rendered.yml"
if ! (cd "$work" && docker compose -f docker-compose.prod.yml config) > "$rendered" 2>"$work/err"; then
    sed 's/^/    /' "$work/err" >&2
    fail "docker-compose.prod.yml is not valid"
fi
pass "docker-compose.prod.yml parses"

# 2. The caddy service actually receives SITE_DOMAIN. `docker compose config`
#    folds env_file entries into `environment:`, so its absence here means the
#    variable never reaches the container — which is defect 2 above, and is
#    invisible to a plain `caddy validate` run with the variable exported.
if ! awk '/^  caddy:/{f=1;next} /^  [a-z]/{f=0} f' "$rendered" | grep -q 'SITE_DOMAIN'; then
    fail "the caddy service never receives SITE_DOMAIN — add env_file to it in docker-compose.prod.yml.
      Without it the Caddyfile's {\$SITE_DOMAIN} expands to nothing, the site
      block degenerates into a global options block, and Caddy refuses to start."
fi
pass "caddy service receives SITE_DOMAIN"

# 3. The Caddyfile adapts. This is the check that would have caught defect 1.
if ! out=$(docker run --rm -e SITE_DOMAIN=example.com \
        -v "$work/Caddyfile":/etc/caddy/Caddyfile:ro \
        caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile \
        --adapter caddyfile 2>&1); then
    echo "$out" | grep -i error | sed 's/^/    /' >&2
    fail "Caddyfile is not valid"
fi
pass "Caddyfile adapts cleanly"

# 4. The dev compose file too, so a typo there doesn't wait for someone's
#    next `docker compose up`.
if ! (cd "$repo" && docker compose -f docker-compose.yml config) >/dev/null 2>"$work/err2"; then
    sed 's/^/    /' "$work/err2" >&2
    fail "docker-compose.yml is not valid"
fi
pass "docker-compose.yml parses"

# 5. verify-deploy.sh belongs to the same category as everything above: it only
#    ever executes on the instance, mid-deploy. A syntax error in it would
#    surface as a failed deploy of an otherwise fine build — and, because it is
#    the step that decides whether a deploy succeeded, it would do so right
#    after the new containers were already up. `bash -n` is not a substitute for
#    running it, but it catches the whole class of typo that would.
if ! out=$(bash -n "$repo/scripts/verify-deploy.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/verify-deploy.sh has a syntax error"
fi
pass "verify-deploy.sh parses"

# 6. It also has to be executable — the deploy invokes it as
#    `./scripts/verify-deploy.sh`, and git tracks the bit. Losing it turns
#    "site verified" into "permission denied" at the last step of a deploy.
if [ ! -x "$repo/scripts/verify-deploy.sh" ]; then
    fail "scripts/verify-deploy.sh is not executable — run: chmod +x scripts/verify-deploy.sh"
fi
pass "verify-deploy.sh is executable"

printf '\n\033[32mDeployment config OK\033[0m\n'
