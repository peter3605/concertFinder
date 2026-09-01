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

# 7. backup-db.sh is in that same never-runs-locally category, and worse: it
#    runs from a systemd timer at 03:00 with nobody watching, so a syntax error
#    in it is silent until the night someone needs a dump that was never taken.
#    Check both halves the same way.
if ! out=$(bash -n "$repo/scripts/backup-db.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/backup-db.sh has a syntax error"
fi
pass "backup-db.sh parses"

if [ ! -x "$repo/scripts/backup-db.sh" ]; then
    fail "scripts/backup-db.sh is not executable — run: chmod +x scripts/backup-db.sh"
fi
pass "backup-db.sh is executable"

# 8. render-env.sh is the newest member of the never-runs-locally family, and
#    the most load-bearing: it writes the .env every other container reads, and
#    it runs between `git reset --hard` and `docker compose up`. A syntax error
#    there fails a deploy with the checkout already moved to the new commit.
if ! out=$(bash -n "$repo/scripts/render-env.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/render-env.sh has a syntax error"
fi
pass "render-env.sh parses"

if [ ! -x "$repo/scripts/render-env.sh" ]; then
    fail "scripts/render-env.sh is not executable — run: chmod +x scripts/render-env.sh"
fi
pass "render-env.sh is executable"

# 9. prune-images.sh closes out every deploy, after verify-deploy.sh has
#    already reported success. A syntax error there fails an SSM command for a
#    deploy that actually worked, and a `set -euo pipefail` script that dies
#    mid-way leaves the disk unreclaimed silently.
if ! out=$(bash -n "$repo/scripts/prune-images.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/prune-images.sh has a syntax error"
fi
pass "prune-images.sh parses"

if [ ! -x "$repo/scripts/prune-images.sh" ]; then
    fail "scripts/prune-images.sh is not executable — run: chmod +x scripts/prune-images.sh"
fi
pass "prune-images.sh is executable"

# 10. The compose file must pin the api image name, because prune-images.sh and
#     the deploy's SHA tagging both address it by name. Without `image:`,
#     compose derives it from the project directory, so a rename of
#     /opt/concertfinder would silently orphan every rollback target.
if ! awk '/^  api:/{f=1;next} /^  [a-z]/{f=0} f' "$rendered" | grep -q 'image: concertfinder-api'; then
    fail "the api service does not pin 'image: concertfinder-api:latest' in docker-compose.prod.yml.
      scripts/prune-images.sh and the deploy's SHA tagging address the image by
      that name; without it compose derives the name from the directory."
fi
pass "api service pins its image name"

# 11. The rendered .env has two consumers with two different parsers, and only
#     one of them is exercised by a deploy. docker compose reads it as an
#     env_file; scripts/backup-db.sh does `set -a; . "$ENV_FILE"`, handing it to
#     bash. A value holding a space, a `$`, a backtick or a `;` is fine for the
#     first and is a syntax error or a command substitution for the second — so
#     the site stays healthy and the 03:00 backup nobody watches is the only
#     thing that dies. This drives the real render-env.sh against a stub
#     Parameter Store and then sources the result the way backup-db.sh does.
envwork="$work/render"
mkdir -p "$envwork/bin"

# Stand-in for `aws ssm get-parameters-by-path --output text`, which emits one
# NAME<TAB>VALUE line per parameter. The arguments are ignored on purpose: what
# is under test is what render-env.sh does with a value, not how it asks for
# one.
cat > "$envwork/bin/aws" <<'STUB'
#!/usr/bin/env bash
printf '%s\t%s\n' /concertfinder/NASTY "$CF_TEST_VALUE"
STUB
chmod +x "$envwork/bin/aws"

# One value carrying every character class that breaks `. file`: a space, a
# `$`, a backtick, a `;` and a double quote. Deliberately no single quote —
# that one is refused at render time rather than encoded, and is checked
# separately below.
nasty="sp ace \$HOME \`id\` ;semi \"dq\""

rendered_env="$envwork/.env"
if ! out=$(PATH="$envwork/bin:$PATH" AWS_REGION=us-east-1 \
        CF_TEST_VALUE="$nasty" \
        PARAM_PATH=/concertfinder ENV_FILE="$rendered_env" \
        OWNER="$(id -un)" GROUP="$(id -gn)" \
        "$repo/scripts/render-env.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "render-env.sh failed against a stub parameter store"
fi

# Sourced inside a command substitution, so the subshell is thrown away: if the
# quoting is wrong the value's backtick runs `id` in there and nowhere else.
# Both failure directions are caught — a value that will not parse at all, and
# one that parses into something other than what was stored.
srcerr="$envwork/source.err"
if ! got=$(set -a; . "$rendered_env" 2>"$srcerr"; set +a; printf '%s' "${NASTY-}"); then
    sed 's/^/    /' "$srcerr" >&2
    fail "the .env render-env.sh writes cannot be sourced — scripts/backup-db.sh
      does exactly this, so the nightly backup would die here while the app,
      which uses compose's separate parser, stayed healthy."
fi
if [ "$got" != "$nasty" ]; then
    printf '    stored:  %s\n    sourced: %s\n' "$nasty" "$got" >&2
    fail "a value did not survive being sourced from the rendered .env —
      render-env.sh must single-quote values."
fi
pass 'rendered .env values survive `set -a; . .env`'

# 12. The other consumer. Quoting for bash is only half the job: compose has
#     its own parser, and it is stricter than bash in a way that is easy to
#     miss. The shell's '\'' escape idiom — close, escape, reopen — is a hard
#     parse error for compose ("unexpected character"), so an encoding chosen
#     by reasoning about bash alone takes the whole deploy down. This asserts
#     against the parser that actually reads the file in production.
cat > "$envwork/docker-compose.yml" <<'COMPOSE'
services:
  probe:
    image: alpine
    env_file:
      - path: .env
        required: true
COMPOSE
if ! composed=$( (cd "$envwork" && docker compose config) 2>"$envwork/compose.err"); then
    sed 's/^/    /' "$envwork/compose.err" >&2
    fail "compose cannot parse the .env render-env.sh writes — this is the
      deploy failing outright, on the instance, after the old containers are
      already down."
fi
# The second failure mode, and the quiet one: compose parses the line but keeps
# the quotes, so every secret reaches the container wrapped in them. Compose
# rewrites `$` as `$$` on output, so the value is not compared byte-for-byte
# here; a retained leading quote is the signature and is enough.
if printf '%s' "$composed" | grep -q "NASTY: \"\\?'sp ace"; then
    fail "compose kept the surrounding single quotes — every value would reach
      the container quoted. render-env.sh's quoting and compose's parser
      disagree."
fi
pass "compose parses the rendered .env and strips the quoting"

# 13. The one value single quotes cannot carry. No encoding satisfies both
#     parsers (the '\'' idiom breaks compose; double-quoting with backslash
#     escapes diverges from bash on the backtick, in the direction where bash
#     runs the command substitution), so render-env.sh refuses. Assert it
#     refuses loudly rather than emitting something one of the two will
#     mis-read.
if PATH="$envwork/bin:$PATH" AWS_REGION=us-east-1 \
        CF_TEST_VALUE="has 'a' quote" \
        PARAM_PATH=/concertfinder ENV_FILE="$envwork/.env.sq" \
        OWNER="$(id -un)" GROUP="$(id -gn)" \
        "$repo/scripts/render-env.sh" >/dev/null 2>&1; then
    fail "render-env.sh accepted a value containing a single quote. There is no
      encoding for it that both compose and \`. \$ENV_FILE\` read the same way,
      so it has to fail here, by name, not on the instance mid-deploy."
fi
pass "render-env.sh refuses a value it cannot encode for both parsers"

printf '\n\033[32mDeployment config OK\033[0m\n'
