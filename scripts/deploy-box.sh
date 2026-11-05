#!/usr/bin/env bash
# Deploy concertFinder on the shared apps box. DORMANT until the cutover: the
# production deploy is still .github/workflows/deploy.yml against the old box,
# which never runs this. See docs/shared-box.md.
#
# Invoked as /opt/concertfinder/deploy.sh (a symlink to this file at the repo
# root) by apps-infra's `concertfinder-deploy` SSM document, which runs it as
# the `concertfinder` system user with
#
#   IMAGE=<account>.dkr.ecr.us-east-1.amazonaws.com/concertfinder-api:<git sha>
#   HOME=/opt/concertfinder  DOCKER_CONFIG=/opt/concertfinder/.docker
#
# /opt/concertfinder is a git checkout of this (public) repository; the
# one-time setup is in docs/shared-box.md. The image tag *is* the commit, so
# the first thing this does is move the checkout to that commit and re-run
# itself from there. That keeps the compose file, render-env.sh and
# verify-deploy.sh in lock step with the image they deploy — including on a
# rollback, which is nothing more than this same document with the previous
# SHA (ECR keeps the last three images).
#
# Steps, after the checkout:
#   1. render .env from SSM /concertfinder/* (scripts/render-env.sh, unchanged;
#      the instance role is what makes it read the apps account)
#   2. record the image in .env so later `docker compose` commands can render
#      the file between deploys
#   3. pull the image, `up -d --wait`
#   4. prove the edge hop: the shared Caddy container fetches /api/healthz from
#      concertfinder-api:8080 by name, which is exactly the route Caddy proxies
#   5. scripts/verify-deploy.sh, through Caddy over TLS, plus its database-host
#      check (CF-B19). See the cutover note at step 5.
#
# No AWS credentials ever reach a container, and none are handled here: the
# instance role (IMDSv2, hop limit 1) is reachable from this host process and
# from nothing inside docker.
set -euo pipefail

# The whole body is a function called on the last line, so bash has parsed all
# of it before the checkout below rewrites this very file on disk.
main() {
    local here repo
    here=$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")" && pwd)
    repo=$(cd "$here/.." && pwd)
    cd "$repo"

    fail() {
        printf '\n\033[31mDEPLOY FAILED\033[0m: %s\n\n' "$1" >&2
        exit 1
    }

    [ -n "${IMAGE:-}" ] || fail "IMAGE is not set — run this through the concertfinder-deploy SSM document"

    # The tag is the commit. The SSM document's allowedPattern already limits it
    # to a hex SHA; re-checked here because it reaches git and a file below.
    local sha=${IMAGE##*:}
    [[ $sha =~ ^[0-9a-f]{7,40}$ ]] || fail "IMAGE tag '$sha' is not a git SHA"
    [[ $IMAGE == */concertfinder-api:"$sha" ]] ||
        fail "IMAGE '$IMAGE' is not the concertfinder-api repository"

    # One deploy at a time. Two would share this checkout, one .env and one
    # compose project, and the second's checkout could land under the first's
    # `up`. The fd is inherited across the exec below, so the lock covers both
    # halves. Waiting beats failing: the deploy workflow serializes too, and a
    # queued deploy should still deploy its own commit when it gets the lock.
    if [ -z "${CF_DEPLOY_LOCKED:-}" ]; then
        exec 9>"$repo/.deploy.lock"
        flock -w 600 9 || fail "another deploy held the lock for 10 minutes"
        export CF_DEPLOY_LOCKED=1
    fi

    # --- 0. Check out the image's commit, then continue from that commit's copy
    if [ "${CF_DEPLOY_CHECKED_OUT:-}" != "$sha" ]; then
        echo "==> checking out $sha"
        git fetch --quiet origin
        git -c advice.detachedHead=false checkout --quiet --force --detach "$sha" ||
            fail "commit $sha is not in origin (images are only built from main)"
        export CF_DEPLOY_CHECKED_OUT=$sha
        exec "$repo/scripts/deploy-box.sh" "$@"
    fi

    local compose=(docker compose -f docker-compose.box.yml)

    # --- 1. .env from SSM ------------------------------------------------------
    # Same script, same parameter path (/concertfinder) and same file
    # (/opt/concertfinder/.env) as on the old box. Running as the app user is
    # fine: it chowns to itself.
    echo "==> rendering .env"
    ENV_FILE="$repo/.env" OWNER="$(id -un)" GROUP="$(id -gn)" ./scripts/render-env.sh

    # --- 2. Record the image ---------------------------------------------------
    # docker-compose.box.yml takes its image from CONCERTFINDER_IMAGE, which
    # compose reads from this .env. Appending it here (single-quoted, like
    # every line render-env.sh writes) is what lets `docker compose ps/logs`
    # and the watchdog render the file after this script exits. render-env.sh
    # rewrites the file on every deploy, so this line never goes stale. It
    # also reaches the container's environment, where nothing reads it.
    printf "CONCERTFINDER_IMAGE='%s'\n" "$IMAGE" >> "$repo/.env"

    # --- 3. Pull and start -------------------------------------------------------
    # Pull first and on its own, so a missing tag or an ECR auth failure stops
    # here with the old container still serving.
    echo "==> pulling $IMAGE"
    "${compose[@]}" pull api || fail "could not pull $IMAGE"

    # A --wait failure is not the verdict, for the same reason as in
    # deploy.yml: verify-deploy.sh below is, and it prints `ps` and the logs
    # on the way out, which is the output you actually need.
    echo "==> starting"
    "${compose[@]}" up -d --wait --wait-timeout 240 ||
        echo "compose --wait failed; verify-deploy.sh below is the verdict"

    # --- 4. The edge hop -----------------------------------------------------------
    # verify-deploy.sh probes through Caddy, but before the cutover Caddy has no
    # certificate for concertfinder.app (step 5), so this is the check that
    # always runs. It asks the shared Caddy container itself to fetch the
    # health endpoint by container name over `edge` — the same name and port
    # its reverse_proxy uses. A wrong container_name, a container not on
    # `edge`, or an api that does not answer all fail here. caddy:2 is
    # Alpine-based, so busybox wget is in it.
    echo "==> probing concertfinder-api:8080 from the shared Caddy over edge"
    local ok=
    for attempt in $(seq 1 12); do
        if docker exec shared-caddy wget -q -O /dev/null -T 10 \
            http://concertfinder-api:8080/api/healthz 2>/dev/null; then
            ok=1
            break
        fi
        [ "$attempt" -lt 12 ] && sleep 5
    done
    if [ -z "$ok" ]; then
        echo "--- shared-caddy ---" >&2
        # Status only. Its logs are every site's access log, client IPs
        # included, and this output can end up in a public workflow log.
        docker ps -a --filter name=^shared-caddy$ --format '{{.Names}} {{.Status}}' >&2 || true
        echo "--- concertfinder ---" >&2
        "${compose[@]}" ps >&2 || true
        "${compose[@]}" logs --tail=80 api >&2 || true
        fail "the shared Caddy cannot reach concertfinder-api:8080/api/healthz over the edge network"
    fi
    printf '\033[32m  ok\033[0m  shared Caddy reaches concertfinder-api:8080 over edge\n'

    # --- 5. Through Caddy, and which database ---------------------------------
    # Caddy gets a certificate over HTTP-01, which only works once the A record
    # points at this box. Until the cutover it points at the old one, Caddy has
    # no certificate for the name, and a TLS probe cannot pass even with -k.
    # So the TLS half runs only once DNS names this box; the database-host half
    # of verify-deploy.sh runs either way. After the cutover both halves are
    # mandatory and this branch never fires. If it does fire after the cutover,
    # the A record is wrong, and the message says so.
    local domain public_ip resolved token skip_tls=
    domain=$(sed -n "s/^SITE_DOMAIN='\(.*\)'$/\1/p" "$repo/.env" | head -n1)
    [ -n "$domain" ] || fail "SITE_DOMAIN is not set in SSM /concertfinder"
    token=$(curl -sS -m 5 -X PUT http://169.254.169.254/latest/api/token \
        -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' 2>/dev/null) || true
    public_ip=$(curl -sS -m 5 -H "X-aws-ec2-metadata-token: $token" \
        http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null) || true
    resolved=$(getent ahostsv4 "$domain" 2>/dev/null | awk 'NR==1 {print $1}') || true
    if [ -z "$public_ip" ] || [ "$resolved" != "$public_ip" ]; then
        skip_tls=1
        printf '\033[33mWARNING\033[0m: %s resolves to %s, not this box (%s).\n' \
            "$domain" "${resolved:-nothing}" "${public_ip:-unknown}"
        echo "  Expected before the cutover. After it, this means the A record is wrong."
        echo "  Skipping only the TLS probe through Caddy; the database check still runs."
    fi

    VERIFY_COMPOSE_FILE=docker-compose.box.yml VERIFY_SKIP_TLS_PROBE="$skip_tls" \
        ./scripts/verify-deploy.sh

    printf '\n\033[32mDeployed %s\033[0m\n' "$sha"
}

main "$@"
