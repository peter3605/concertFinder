#!/usr/bin/env bash
# Application-level watchdog. Runs on the instance every minute from
# concertfinder-watchdog.timer, decides whether the compose project is actually
# serving, and publishes that verdict to CloudWatch as the custom metric
# ConcertFinder/App ServicesUnhealthy. infra/cloudwatch.tf alarms on it.
#
# Why this exists: every alarm in infra/cloudwatch.tf watches the *instance*.
# `restart: unless-stopped` means a container that exits on every start is a
# crash loop, not an outage the host can see -- EC2's status checks pass, the
# instance is up, and the box looks busy rather than broken. The entire serving
# chain can be down with every AWS-side signal green, which is precisely the
# case docs/aws-deploy.md's "What's not in this setup" named and this closes.
#
# The only thing that ever noticed was scripts/verify-deploy.sh, and it runs
# once per deploy and never again. Anything that broke *between* deploys -- an
# OOM kill, a rotated credential, a Neon suspension taking config.Validate's
# database check down with it, a Caddy certificate renewal that wedged -- was
# silent until somebody visited the site.
#
# This is a metric rather than a direct `aws sns publish` for two reasons that
# are not style. An alarm dedupes on state *transition*, so a stack that stays
# broken for six hours is one email rather than three hundred and sixty; a
# script that publishes would need its own "have I already said this" state
# file, which is a second thing that fails silently. And ok_actions -- the
# "it's back" mail -- cannot be had from a publishing script at all, because
# something that notices things are fine has to have remembered that they
# weren't.
set -euo pipefail

cd "$(dirname "$0")/.."

COMPOSE_FILE=${COMPOSE_FILE:-docker-compose.prod.yml}
COMPOSE=(docker compose -f "$COMPOSE_FILE")

# Must match local.app_metric_namespace in infra/cloudwatch.tf. A mismatch is
# silent in the worst direction -- the metric lands in a namespace nothing
# alarms on, the alarm sees no data, and how that reads depends entirely on
# treat_missing_data. scripts/check-deploy-config.sh asserts the two strings
# are equal, the same way it pins PG_IMAGE across the backup and drill scripts.
NAMESPACE=${WATCHDOG_NAMESPACE:-ConcertFinder/App}
METRIC_NAME=ServicesUnhealthy

# Previous sample's restart counters, keyed by service. See the crash-loop note
# below for what they are for. /var/tmp rather than /run so it survives a
# reboot; a missing file is a normal first run, not an error.
STATE_FILE=${WATCHDOG_STATE_FILE:-/var/tmp/concertfinder-watchdog.state}

# Set to 0 by the test in scripts/check-deploy-config.sh, which exercises the
# detection half against real crash-looping containers and has no AWS account
# to publish into.
WATCHDOG_PUBLISH=${WATCHDOG_PUBLISH:-1}

log() { printf '%s watchdog: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1"; }

# Expected services come from the compose file, not from whatever happens to be
# running. That direction matters: a container that was removed and never
# recreated does not appear in `docker compose ps` at all, so counting what
# exists would score a stack with no containers as zero-unhealthy -- the same
# "absent reads as fine" bug this whole script exists to close, reproduced
# inside it.
# `compose config` parses the file locally and does not consult the daemon, so
# this branch is about the compose file being missing or invalid -- not about
# docker being down. A dead daemon takes the path below instead, where every
# service reads as "no container" and the count published is the whole stack,
# which is the right answer rather than a special case.
#
# Failing here exits non-zero and publishes nothing, which the alarm reads as a
# breach via treat_missing_data. The explicit branch exists so the journal says
# so; under bare `set -e` this line would abort with no output at all, on
# exactly the incident where someone is reading the journal.
services=$("${COMPOSE[@]}" config --services 2>/dev/null) || {
    log "cannot read services from $COMPOSE_FILE (missing or invalid); not publishing"
    exit 1
}
[ -n "$services" ] || {
    log "compose file $COMPOSE_FILE declares no services"
    exit 1
}
service_count=$(printf '%s\n' "$services" | wc -l | tr -d ' ')

# Looked up with awk rather than held in an associative array: those need
# bash 4, and macOS still ships 3.2 -- which would make this script work in CI
# and on the instance but not on the laptop of whoever is debugging it.
prev_restarts_for() {
    [ -r "$STATE_FILE" ] || return 0
    awk -v s="$1" '$1 == s { print $2; exit }' "$STATE_FILE"
}

unhealthy=0
reasons=()
new_state=""

for svc in $services; do
    # --all so that a container which has exited is still found. It does not
    # change the verdict -- an unlisted container yields an empty id and the
    # branch below counts that as unhealthy either way -- it changes the
    # reason, and the reason is the whole content of the alert. "exited" says
    # somebody stopped it or it died; "no container" reads like the compose
    # file is wrong. Whoever is woken by this reads that line first.
    cid=$("${COMPOSE[@]}" ps -q --all "$svc" 2>/dev/null | head -n1 || true)

    if [ -z "$cid" ]; then
        unhealthy=$((unhealthy + 1))
        reasons+=("$svc: no container")
        continue
    fi

    # One inspect rather than `ps --format json`: compose has emitted both a
    # JSON array and newline-delimited objects from that flag depending on its
    # version, and AL2023 ships no jq to paper over the difference. A Go
    # template is stable across both and needs nothing installed.
    #
    # The failure is a branch rather than an `|| echo "... 0"` fallback, because
    # a fabricated zero would be *written to the state file* and read next run
    # as a baseline -- reporting a restart from 0 to the container's real count
    # and inventing a crash loop out of one transient inspect failure. On
    # failure this service contributes no state line at all, and re-baselines
    # next run.
    if inspect=$(docker inspect --format \
        '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.RestartCount}}' \
        "$cid" 2>/dev/null); then
        read -r status health restarts <<< "$inspect"
        new_state+="$svc $restarts"$'\n'
    else
        # compose just listed this container, so a daemon that will not describe
        # it is not a healthy state.
        status=unreadable
        health=none
        restarts=0
    fi

    # "starting" is the healthcheck's start_period, which is 60s for the api
    # container and covers app + river migrations against a cold database. A
    # deploy passes through it every time, so counting it would alarm on every
    # successful deploy and teach the operator to ignore this address.
    if [ "$status" != "running" ]; then
        unhealthy=$((unhealthy + 1))
        reasons+=("$svc: status=$status")
        continue
    fi
    if [ "$health" = "unhealthy" ]; then
        unhealthy=$((unhealthy + 1))
        reasons+=("$svc: health=unhealthy")
        continue
    fi

    # A crash loop sampled at the wrong instant looks fine. Docker's restart
    # backoff starts at 100ms, so early in a loop the container really is
    # "running" for a slice of every second, and a once-a-minute sample can
    # land in one. RestartCount rising between two samples is the signal that
    # does not depend on that timing.
    #
    # A deploy recreates the container, which resets the counter to 0 against a
    # previous sample's higher number -- that is a decrease, not an increase,
    # so a deploy does not read as a loop.
    #
    # It is skipped on the first run after a service was absent or unreadable,
    # since there is no baseline to compare against. That window is covered by
    # the status check above: a container in that situation reads `restarting`
    # or `exited` anyway.
    prev=$(prev_restarts_for "$svc")
    if [ -n "$prev" ] && [ "$restarts" -gt "$prev" ]; then
        unhealthy=$((unhealthy + 1))
        reasons+=("$svc: restarted $prev->$restarts since last check")
    fi
done

# Written whole via a temp file: a half-written state file read by the next run
# would compare this run's counts against a truncated line and could miss a
# restart exactly once, which is unreproducible and looks like a flake.
#
# And it is non-fatal, which matters more than it looks. This file feeds only
# the restart-delta heuristic -- a secondary signal, behind status and health --
# so it must never be able to stop the metric going out. /var/tmp is sticky, so
# one `sudo ./watchdog.sh` run leaves the file owned by root, after which `mv`
# fails with EPERM for the service user. Under bare `set -e` that would kill the
# run before it published, every minute, forever: treat_missing_data =
# "breaching" would turn a bookkeeping file nobody thinks about into a permanent
# alarm about nothing, and it would strand an orphaned temp file per minute on
# the way. Losing the delta is the correct price; losing the metric is not.
if tmp_state=$(mktemp "${STATE_FILE}.XXXXXX" 2>/dev/null); then
    if ! { printf '%s' "$new_state" > "$tmp_state" && mv -f "$tmp_state" "$STATE_FILE"; }; then
        rm -f "$tmp_state"
        log "could not update $STATE_FILE; restart-delta detection disabled until it is writable"
    fi
else
    log "could not create a temp file beside $STATE_FILE; restart-delta detection disabled"
fi

if [ "$unhealthy" -gt 0 ]; then
    # :- because bash 3.2 treats ${empty[*]} under `set -u` as an unbound
    # variable and aborts. Every site that increments the counter also appends a
    # reason, so this is unreachable today -- and one careless edit away from
    # killing the watchdog on exactly the runs where something is wrong, and
    # only on a Mac.
    log "UNHEALTHY ($unhealthy): ${reasons[*]:-no detail}"
else
    log "all $service_count services healthy"
fi

# Machine-readable and last, so the test can assert on it without parsing the
# human lines above.
echo "unhealthy=$unhealthy"

[ "$WATCHDOG_PUBLISH" = "1" ] || exit 0

# IMDSv2. The region and instance ID are read here rather than baked into the
# unit file so a rebuilt instance needs no edit -- and the InstanceId dimension
# has to match aws_instance.app.id, which is what the alarm in cloudwatch.tf
# keys on.
token=$(curl -fsS --max-time 5 -X PUT http://169.254.169.254/latest/api/token \
    -H "X-aws-ec2-metadata-token-ttl-seconds: 60") || {
    log "cannot reach IMDS for a token; not publishing"
    exit 1
}
imds() { curl -fsS --max-time 5 -H "X-aws-ec2-metadata-token: $token" "http://169.254.169.254/latest/meta-data/$1"; }

instance_id=$(imds instance-id) || { log "cannot read instance-id from IMDS"; exit 1; }
region=$(imds placement/region) || { log "cannot read region from IMDS"; exit 1; }

# Failing here is safe in the direction that matters: no datapoint arrives, and
# the alarm's treat_missing_data = "breaching" turns the silence into the alert
# rather than into an all-clear. Exiting non-zero puts the reason in the
# journal for whoever goes looking after the mail lands.
aws cloudwatch put-metric-data \
    --region "$region" \
    --namespace "$NAMESPACE" \
    --metric-name "$METRIC_NAME" \
    --unit Count \
    --value "$unhealthy" \
    --dimensions "InstanceId=$instance_id" ||
    {
        log "put-metric-data failed; the alarm will fire on the missing datapoint"
        exit 1
    }
