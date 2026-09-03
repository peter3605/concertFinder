#!/usr/bin/env bash
# Reclaim disk on the instance without destroying the rollback path.
#
# Runs as the last step of every deploy. Three things share the instance's
# 20 GiB root volume — container logs, the buildkit cache, and the images
# themselves — and a full root volume is not a degradation: the api cannot
# write, Caddy cannot renew a certificate, and nothing in the pipeline reports
# it. Logs are capped in docker-compose.prod.yml; this handles the other two.
#
# What it deliberately does NOT do is `docker image prune -a`. Each deploy tags
# the built image with its commit SHA precisely so a rollback is a retag and a
# restart, not a rebuild on a 2 GiB box at the moment the site is already
# unhappy. Keeping three of them is the point.
set -euo pipefail

image="${IMAGE_REPO:-concertfinder-api}"
keep="${KEEP_IMAGES:-3}"

# `docker image ls` lists newest first. `latest` is excluded from the count: it
# is a moving pointer at whichever SHA deployed most recently, so counting it
# would evict one real rollback target every time.
mapfile -t tags < <(docker image ls "$image" --format '{{.Tag}}' \
    | grep -v '^latest$' | grep -v '^<none>$' || true)

if [ "${#tags[@]}" -gt "$keep" ]; then
    for tag in "${tags[@]:$keep}"; do
        echo "pruning $image:$tag"
        docker image rm "$image:$tag" || true
    done
fi
echo "kept ${image} tags: ${tags[*]:0:$keep}"

# Untagged layers only, and only ones a week old — a bare `image prune -f`
# would drop the intermediate layers of an image built minutes ago on a box
# where several deploys can land in one day.
docker image prune -f --filter "until=168h"

# `image prune` does not touch the buildkit cache, which is the larger of the
# two on a box that builds a Node bundle and a Go binary on every deploy.
docker builder prune -f --filter "until=168h"
