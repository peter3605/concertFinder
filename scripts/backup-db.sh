#!/usr/bin/env bash
# Nightly logical backup of the Neon database to S3. Run on the instance by the
# concertfinder-backup.timer that infra/ec2.tf's user_data installs (see
# docs/aws-deploy.md §7 -- the manual install there is now a fallback for
# instances that predate it, since user_data does not re-run).
#
# Why this exists: moving Postgres from RDS to Neon gave up
# `backup_retention_period = 7` and the final snapshot. Neon's free plan keeps a
# much shorter restore history than that. Most of this schema does not need
# protecting — concerts, the caches, and the snapshots all rebuild on the next
# scan — but `users` holds the AES-GCM-encrypted Spotify refresh tokens, and
# `user_saved_concerts` / `user_subscribed_artists` / `user_locations` hold
# things the user typed. Losing those means every user re-authorizes with
# Spotify and loses their stars and bells.
#
# The dump is taken whole rather than as a table list, because a table list in a
# backup script is a thing that silently fails to grow when a migration adds a
# table.
set -euo pipefail

ENV_FILE=${ENV_FILE:-/opt/concertfinder/.env}

# pg_dump must be at least the server's major version or it aborts outright
# ("server version: 18.6; pg_dump version: 17.x"), and the host has no Postgres
# client (the api image is distroless — no shell, let alone pg_dump). The Neon
# project reports 18.6, hence 18 here. Pinning the tag rather than tracking
# :latest means a Neon major-version upgrade fails loudly on the next nightly
# run instead of on the night we actually need the dump. Bump it when Neon
# bumps the project; `SHOW server_version` tells you what to bump it to.
PG_IMAGE=${PG_IMAGE:-postgres:18-alpine}

fail() {
    printf '\033[31mBACKUP FAILED\033[0m: %s\n' "$1" >&2
    exit 1
}

[ -r "$ENV_FILE" ] || fail "cannot read $ENV_FILE"

# Source rather than parse: this is the same file docker compose loads, and
# hand-rolling a parser for it is how the two drift. `set -a` exports what it
# defines so the docker invocation below can inherit DATABASE_URL.
set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

[ -n "${DATABASE_URL:-}" ] || fail "DATABASE_URL is not set in $ENV_FILE"
[ -n "${BACKUP_S3_BUCKET:-}" ] || fail "BACKUP_S3_BUCKET is not set in $ENV_FILE"

STAMP=$(date -u +%Y-%m-%d)
KEY="pg/concertfinder-${STAMP}.dump"

TMP=$(mktemp /var/tmp/concertfinder-backup.XXXXXX)
ERR=$(mktemp /var/tmp/concertfinder-backup-err.XXXXXX)
trap 'rm -f "$TMP" "$ERR"' EXIT

# -Fc (custom format) rather than plain SQL: it compresses, and pg_restore can
# then do a selective restore of just `users` without replaying the whole
# schema, which is the realistic recovery case here.
#
# `-e DATABASE_URL` with no `=value` passes the variable by *name*, so the
# connection string is inherited from this script's environment instead of
# appearing in the container's argv — where any user on the box could read it
# out of `ps`. Same reason it is never echoed.
docker run --rm -e DATABASE_URL "$PG_IMAGE" \
    pg_dump --dbname="$DATABASE_URL" --format=custom --no-owner --no-privileges \
    > "$TMP" 2>"$ERR" ||
    fail "pg_dump exited non-zero: $(tail -3 "$ERR")"

# Do not stream pg_dump straight into `aws s3 cp -`. A dump that dies partway
# still produces bytes, and streaming uploads them — leaving a truncated object
# that looks like a backup, under today's key, quite possibly overwriting a good
# one. Dumping to disk first makes the failure land here, before anything is
# uploaded.
[ -s "$TMP" ] || fail "pg_dump produced an empty file"

# Cheap integrity check: pg_restore --list parses the archive's table of
# contents and fails on a truncated or corrupt file. It proves the dump is
# readable, which is more than pg_dump's exit status does.
#
# The archive must be bind-mounted, not piped in. Custom-format archives are
# read by seeking, and pg_restore against a pipe fails with "did not find magic
# string in file header" — which would have failed *every* run, and in the
# fail-safe direction: no upload, no backups at all, discovered whenever someone
# next went looking for one.
docker run --rm -v "$TMP:/dump:ro" "$PG_IMAGE" pg_restore --list /dump >/dev/null 2>"$ERR" ||
    fail "dump did not survive pg_restore --list, refusing to upload it: $(tail -2 "$ERR")"

aws s3 cp "$TMP" "s3://${BACKUP_S3_BUCKET}/${KEY}" --only-show-errors ||
    fail "upload to s3://${BACKUP_S3_BUCKET}/${KEY} failed"

printf 'backup ok: s3://%s/%s (%s bytes)\n' \
    "$BACKUP_S3_BUCKET" "$KEY" "$(wc -c < "$TMP" | tr -d ' ')"

# Dead-man's switch. Everything above fails loudly into the journal, which
# nobody reads -- and the failure mode that matters most is the one that
# produces no output at all: a timer that was never installed, a box that was
# rebuilt, a unit that stopped firing. Only a signal that must ARRIVE catches
# those, so ping an external monitor (healthchecks.io, Better Stack, an SNS
# HTTP subscription) after a verified upload.
#
# Optional: unset, this is a no-op and the backup behaves exactly as before.
# BACKUP_HEARTBEAT_URL comes out of the same .env sourced above.
#
# The ping failing must never fail the backup -- the dump is in S3 by this
# point, and exiting non-zero here would report a successful backup as a
# failure, which is the wrong direction to be wrong in -- so the curl is
# tested rather than left to `set -e`, and the outcome is logged either way.
if [ -n "${BACKUP_HEARTBEAT_URL:-}" ]; then
    if curl -fsS --max-time 10 -o /dev/null "$BACKUP_HEARTBEAT_URL"; then
        echo "heartbeat ok"
    else
        # The URL is not echoed: heartbeat URLs are bearer credentials.
        echo "heartbeat ping failed (backup itself succeeded)" >&2
    fi
fi
