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
# The watchdog check below brings a throwaway compose project up; cleaning it
# from the same trap means a failed assertion cannot leave containers running
# on the developer's machine or the runner.
wd_project="cfwatchdogcheck$$"
cleanup() {
    docker compose -p "$wd_project" -f "$work/watchdog-test.yml" down -t 1 >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

# Work on copies with a synthetic .env: never read the developer's real one,
# and never print it — `docker compose config` echoes every variable it
# resolves, secrets included.
cp docker-compose.yml docker-compose.prod.yml Caddyfile "$work/"
# A Neon-shaped DATABASE_URL, distinct from docker-compose.yml's dev value so
# check 4b can tell which one won. The `&` is deliberate: real Neon URLs carry
# channel_binding alongside sslmode.
neon_url='postgres://u:p@ep-synthetic.us-east-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require'
printf 'SITE_DOMAIN=example.com\nDATABASE_URL=%s\n' "$neon_url" > "$work/.env"

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

# 4b. Every prod compose form reaches the database .env names (CF-B17). Run with the
#     dev file first and the prod file second, docker-compose.yml's
#     `environment: DATABASE_URL` (the compose `db` container) used to outrank
#     the prod file's env_file, so `up` quietly swapped Neon for an empty dev
#     Postgres. It did, for eight days, with every health check green. The
#     prod file restating DATABASE_URL in `environment:` is what fixes it;
#     this is what notices if that line goes.
#
#     Every form that can start production is pinned, not only the one that
#     broke (CF-B18): the prod file alone is what deploy.yml runs, the two-file
#     form is what a session types by hand, and a check covering one says
#     nothing about the other. Removing the prod file's `environment:` entry
#     fails the two-file row; the prod-only row is what fails if the api ever
#     stops reading .env at all.
api_db_url() {
    (cd "$work" && docker compose "$@" config --format json) 2>"$work/err4b" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["api"]["environment"]["DATABASE_URL"])'
}
for form in "docker-compose.prod.yml" "docker-compose.yml docker-compose.prod.yml"; do
    args=()
    for f in $form; do args+=(-f "$f"); done
    if ! got=$(api_db_url "${args[@]}"); then
        sed 's/^/    /' "$work/err4b" >&2
        fail "\`${args[*]} config\` does not render"
    fi
    if [ "$got" != "$neon_url" ]; then
        printf '    .env:     %s\n    rendered: %s\n' "$neon_url" "$got" >&2
        fail "\`${args[*]}\` does not give the api the DATABASE_URL from .env.
      \`up\` in that form would run production against whatever it rendered
      instead — docker-compose.prod.yml must set DATABASE_URL in the api
      service's \`environment:\`."
    fi
    pass "\`${args[*]}\` gives the api .env's DATABASE_URL"
done

# ...and the dev file on its own still reaches the dev database, whatever the
# .env says: the local .env holds the host-side URL for \`go run\`, which is
# wrong inside the compose network.
if ! got=$(api_db_url -f docker-compose.yml) || [ "${got#*@}" != "db:5432/concertfinder?sslmode=disable" ]; then
    printf '    rendered: %s\n' "${got:-<nothing>}" >&2
    fail "docker-compose.yml alone no longer points the api at the compose db service"
fi
pass "docker-compose.yml alone still gives the api the dev database"

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

# 6a. verify-deploy.sh decides whether the api reached the right database by
#     comparing hosts parsed out of two DSNs (CF-B19), and its output is dumped
#     into a public workflow log on failure. So the parser is pinned on both
#     things that matter: it extracts the host, and it refuses — printing
#     nothing — anything that is not a bare host, so no credential can escape
#     through an error message. Sourcing stops at the script's main guard.
(
    # shellcheck source=verify-deploy.sh
    . "$repo/scripts/verify-deploy.sh"
    check_host() {
        local got
        got=$(db_host "$1") || got="<refused>"
        [ "$got" = "$2" ] || { printf '    db_host gave %s, want %s\n' "$got" "$2" >&2; exit 1; }
    }
    check_host "$neon_url" ep-synthetic.us-east-1.aws.neon.tech
    check_host 'postgres://concertfinder:concertfinder@db:5432/concertfinder?sslmode=disable' db
    check_host 'postgresql://u:p@127.0.0.1:5433/x' 127.0.0.1
    check_host 'postgres://EP-Mixed.Neon.Tech/db' ep-mixed.neon.tech
    check_host 'postgres://u:p@[::1]:5432/db' '[::1]'
    check_host 'postgres://u:p%40ss@h.example/db?options=a@b' h.example
    check_host 'host=h.example user=u password=secret dbname=d' h.example
    check_host 'postgres://u:p@/db?host=/var/run/postgresql' '<refused>'
    check_host 'postgres://u:p@h1:5432,h2:5432/db' '<refused>'
    check_host 'not a dsn' '<refused>'
) || fail "verify-deploy.sh's db_host misparses a DSN"
pass "verify-deploy.sh extracts database hosts and refuses the rest"

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

# 9a. restore-drill.sh is the far end of the same family, and the one with the
#     longest fuse: it is not run by a deploy or by a timer, it is run by a
#     human on the worst day of the quarter. Nothing else would ever discover a
#     syntax error in it. Parsing it here costs nothing and is the only
#     automated attention it will ever get.
if ! out=$(bash -n "$repo/scripts/restore-drill.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/restore-drill.sh has a syntax error"
fi
pass "restore-drill.sh parses"

if [ ! -x "$repo/scripts/restore-drill.sh" ]; then
    fail "scripts/restore-drill.sh is not executable — run: chmod +x scripts/restore-drill.sh"
fi
pass "restore-drill.sh is executable"

# 9b. The drill restores what backup-db.sh dumps, with the same tool, so the
#     two must agree on the Postgres major version. pg_restore refuses an
#     archive from a newer server, and the drill is exactly the moment that
#     mismatch must not be discovered.
backup_img=$(grep -o 'PG_IMAGE:-[^}]*' "$repo/scripts/backup-db.sh" | head -1 | cut -d- -f2-)
drill_img=$(grep -o 'PG_IMAGE:-[^}]*' "$repo/scripts/restore-drill.sh" | head -1 | cut -d- -f2-)
if [ "$backup_img" != "$drill_img" ]; then
    fail "backup-db.sh pins PG_IMAGE=$backup_img but restore-drill.sh pins $drill_img.
      pg_restore refuses an archive produced by a newer server, so a drill
      against the real dumps would fail for a reason that has nothing to do
      with the backups."
fi
pass "backup-db.sh and restore-drill.sh pin the same Postgres image ($backup_img)"

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

# 14. The watchdog is in the same family as the scripts above — it only ever
#     runs on the instance, from a systemd timer, where a syntax error would
#     surface as a missing CloudWatch datapoint rather than as an error anyone
#     reads.
if ! out=$(bash -n "$repo/scripts/watchdog.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/watchdog.sh has a syntax error"
fi
pass "watchdog.sh parses"

if [ ! -x "$repo/scripts/watchdog.sh" ]; then
    fail "scripts/watchdog.sh is not executable — run: chmod +x scripts/watchdog.sh"
fi
pass "watchdog.sh is executable"

# 15. The watchdog publishes into a namespace and infra/cloudwatch.tf alarms on
#     one. If they disagree the metric lands somewhere nothing watches, the
#     alarm sees no data at all, and because it is configured
#     treat_missing_data = "breaching" the result is an alert that fires
#     forever for a reason that has nothing to do with the application. Neither
#     terraform validate nor a deploy can see the mismatch — it is two string
#     literals in two languages. Same reasoning as the PG_IMAGE pin above.
#
#     The `|| true` on each extraction is load-bearing, not defensive habit.
#     Under `set -euo pipefail` a grep that matches nothing exits 1, pipefail
#     carries that out of the pipeline, and set -e kills the script *at the
#     assignment* -- so the emptiness guard below would never run, and a renamed
#     variable would show a bare exit 1 with no FAIL line. That is precisely the
#     "this check stopped comparing anything" outcome the message exists to
#     explain, arriving with the message suppressed.
wd_ns=$(grep -o 'WATCHDOG_NAMESPACE:-[^}]*' "$repo/scripts/watchdog.sh" | head -1 | cut -d- -f2- || true)
tf_ns=$(grep -o 'app_metric_namespace[[:space:]]*=[[:space:]]*"[^"]*"' "$repo/infra/cloudwatch.tf" |
    head -1 | sed 's/.*"\(.*\)"/\1/' || true)
if [ -z "$wd_ns" ] || [ -z "$tf_ns" ]; then
    fail "could not read the metric namespace out of scripts/watchdog.sh ($wd_ns) and
      infra/cloudwatch.tf ($tf_ns) — one of them changed shape, so this check
      stopped comparing anything."
fi
if [ "$wd_ns" != "$tf_ns" ]; then
    fail "watchdog.sh publishes to '$wd_ns' but infra/cloudwatch.tf alarms on '$tf_ns'.
      The alarm would never see a datapoint, and treat_missing_data =
      \"breaching\" turns that into a permanent alert about nothing."
fi
pass "watchdog.sh and cloudwatch.tf agree on the metric namespace ($wd_ns)"

# 16. And the detection itself, against real containers. This is the one
#     assertion here that tests behaviour rather than config, and it earns the
#     seconds it costs: the whole point of the watchdog is to notice a state
#     that every other signal in this deployment reports as healthy, so
#     "does it actually notice" cannot be taken on faith. A crash loop under
#     `restart: unless-stopped` is a specific thing docker does, and the
#     matching assertion is that a *healthy* project still scores zero —
#     a watchdog that always alarms gets muted, and then reports nothing.
cat > "$work/watchdog-test.yml" <<'YML'
services:
  api:
    image: alpine:3
    command: ["sh", "-c", "echo booting; exit 1"]
    restart: unless-stopped
  caddy:
    image: alpine:3
    command: ["sh", "-c", "sleep 300"]
    restart: unless-stopped
YML

if ! docker compose -p "$wd_project" -f "$work/watchdog-test.yml" up -d >/dev/null 2>&1; then
    fail "could not start the throwaway compose project for the watchdog check"
fi
# Long enough for docker to have restarted the crashing container at least once.
sleep 4

wd_out=$(COMPOSE_PROJECT_NAME="$wd_project" COMPOSE_FILE="$work/watchdog-test.yml" \
    WATCHDOG_PUBLISH=0 WATCHDOG_STATE_FILE="$work/wd.state" \
    "$repo/scripts/watchdog.sh" 2>&1) || {
    echo "$wd_out" | sed 's/^/    /' >&2
    fail "scripts/watchdog.sh exited non-zero against the test project"
}
wd_count=$(echo "$wd_out" | sed -n 's/^unhealthy=//p' | tail -1 || true)
if [ "${wd_count:-0}" -lt 1 ]; then
    echo "$wd_out" | sed 's/^/    /' >&2
    fail "watchdog.sh reported unhealthy=${wd_count:-<none>} for a crash-looping container.
      That is the exact state it exists to catch — a container that exits on
      every start while the host stays healthy — so the alarm in
      infra/cloudwatch.tf would never fire for the failure it was built for."
fi
# A count alone is not enough: 2 would also pass, and 2 is what a watchdog that
# found no containers at all reports. Naming the crashing service is what
# distinguishes "noticed the crash loop" from "could not see the project".
if ! echo "$wd_out" | grep -q 'api:'; then
    echo "$wd_out" | sed 's/^/    /' >&2
    fail "watchdog.sh reported unhealthy=$wd_count but never named the api service.
      A run that cannot see the project at all also reports a non-zero count,
      so the count on its own does not show that the crash loop was detected."
fi
pass "watchdog.sh detects a crash-looping container (unhealthy=$wd_count)"

docker compose -p "$wd_project" -f "$work/watchdog-test.yml" down -t 1 >/dev/null 2>&1 || true

cat > "$work/watchdog-test.yml" <<'YML'
services:
  api:
    image: alpine:3
    command: ["sh", "-c", "sleep 300"]
    restart: unless-stopped
  caddy:
    image: alpine:3
    command: ["sh", "-c", "sleep 300"]
    restart: unless-stopped
YML

if ! docker compose -p "$wd_project" -f "$work/watchdog-test.yml" up -d >/dev/null 2>&1; then
    fail "could not start the healthy compose project for the watchdog check"
fi
sleep 2

wd_out=$(COMPOSE_PROJECT_NAME="$wd_project" COMPOSE_FILE="$work/watchdog-test.yml" \
    WATCHDOG_PUBLISH=0 WATCHDOG_STATE_FILE="$work/wd-healthy.state" \
    "$repo/scripts/watchdog.sh" 2>&1) || {
    echo "$wd_out" | sed 's/^/    /' >&2
    fail "scripts/watchdog.sh exited non-zero against the healthy test project"
}
wd_count=$(echo "$wd_out" | sed -n 's/^unhealthy=//p' | tail -1 || true)
if [ "${wd_count:-1}" -ne 0 ]; then
    echo "$wd_out" | sed 's/^/    /' >&2
    fail "watchdog.sh reported unhealthy=$wd_count for a project where every container
      is running. A watchdog that alarms on a healthy stack gets muted, and a
      muted alarm reports nothing at all."
fi
pass "watchdog.sh scores a healthy project clean"

printf '\n\033[32mDeployment config OK\033[0m\n'
