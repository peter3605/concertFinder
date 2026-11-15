#!/usr/bin/env bash
# Validate the deployment config that nothing else exercises.
#
# docker-compose.box.yml and the scripts the shared box runs (deploy-box.sh,
# render-env.sh, verify-deploy.sh) never execute during local development:
# `docker-compose.yml` is a different stack, and `go run` reads none of them.
# Every deploy defect this repo has shipped was in a file of that kind, and
# each presented as a failed or silently wrong deploy rather than as an error
# anyone read. This is the only place they are exercised before production.
#
# The retired old box's files (docker-compose.prod.yml, this repo's Caddyfile,
# infra/, backup-db.sh, prune-images.sh, watchdog.sh) and the checks that
# pinned them were deleted in CF-B29. The shared Caddy, the box's backup,
# watchdog and image prune now live in apps-infra and are checked there.
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
cp docker-compose.yml docker-compose.box.yml "$work/"
# An RDS-shaped DATABASE_URL, distinct from docker-compose.yml's dev value so
# check 4b can tell which one won. The `&` is deliberate: it proves a query
# string with more than one parameter survives interpolation intact.
db_url='postgres://u:p@shared-postgres.synthetic.us-east-1.rds.amazonaws.com:5432/concertfinder?sslmode=require&connect_timeout=10'
# docker-compose.box.yml takes its image from CONCERTFINDER_IMAGE, which
# scripts/deploy-box.sh appends to the box's .env. An ECR-shaped value at a
# full SHA, so check 10b can assert it arrives unchanged.
box_image='426433020884.dkr.ecr.us-east-1.amazonaws.com/concertfinder-api:0123456789abcdef0123456789abcdef01234567'
printf 'SITE_DOMAIN=example.com\nDATABASE_URL=%s\nCONCERTFINDER_IMAGE=%s\n' "$db_url" "$box_image" > "$work/.env"

fail() { printf '\n\033[31mFAIL\033[0m: %s\n' "$1" >&2; exit 1; }
pass() { printf '\033[32m  ok\033[0m  %s\n' "$1"; }

echo "Checking deployment config..."

# 4. The dev compose file too, so a typo there doesn't wait for someone's
#    next `docker compose up`.
if ! (cd "$repo" && docker compose -f docker-compose.yml config) >/dev/null 2>"$work/err2"; then
    sed 's/^/    /' "$work/err2" >&2
    fail "docker-compose.yml is not valid"
fi
pass "docker-compose.yml parses"

# 4b. Every box compose form reaches the database .env names (CF-B17). Run
#     with the dev file first and the box file second, docker-compose.yml's
#     `environment: DATABASE_URL` (the compose `db` container) outranks a bare
#     env_file, so `up` would quietly swap the production database for an
#     empty dev Postgres. On the old box it did, for eight days, with every
#     health check green. The box file restating DATABASE_URL in
#     `environment:` is what fixes it; this is what notices if that line goes.
#
#     Both forms are pinned, not only the one that broke (CF-B18): the box
#     file alone is what scripts/deploy-box.sh runs, the two-file form is what
#     a session types on the box by hand, and a check covering one says
#     nothing about the other. Removing the `environment:` entry fails the
#     two-file row; the box-only row is what fails if the api ever stops
#     reading .env at all.
api_db_url() {
    (cd "$work" && docker compose "$@" config --format json) 2>"$work/err4b" \
        | python3 -c 'import json,sys; print(json.load(sys.stdin)["services"]["api"]["environment"]["DATABASE_URL"])'
}
for form in "docker-compose.box.yml" "docker-compose.yml docker-compose.box.yml"; do
    args=()
    for f in $form; do args+=(-f "$f"); done
    if ! got=$(api_db_url "${args[@]}"); then
        sed 's/^/    /' "$work/err4b" >&2
        fail "\`${args[*]} config\` does not render"
    fi
    if [ "$got" != "$db_url" ]; then
        printf '    .env:     %s\n    rendered: %s\n' "$db_url" "$got" >&2
        fail "\`${args[*]}\` does not give the api the DATABASE_URL from .env.
      \`up\` in that form would run production against whatever it rendered
      instead — docker-compose.box.yml must set DATABASE_URL in the api
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
    check_host "$db_url" shared-postgres.synthetic.us-east-1.rds.amazonaws.com
    check_host 'postgres://concertfinder:concertfinder@db:5432/concertfinder?sslmode=disable' db
    check_host 'postgresql://u:p@127.0.0.1:5433/x' 127.0.0.1
    check_host 'postgres://Shared-Postgres.Example.RDS.amazonaws.com/db' shared-postgres.example.rds.amazonaws.com
    check_host 'postgres://u:p@[::1]:5432/db' '[::1]'
    check_host 'postgres://u:p%40ss@h.example/db?options=a@b' h.example
    check_host 'host=h.example user=u password=secret dbname=d' h.example
    check_host 'postgres://u:p@/db?host=/var/run/postgresql' '<refused>'
    check_host 'postgres://u:p@h1:5432,h2:5432/db' '<refused>'
    check_host 'not a dsn' '<refused>'
) || fail "verify-deploy.sh's db_host misparses a DSN"
pass "verify-deploy.sh extracts database hosts and refuses the rest"


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

# 9b. The drill restores apps-infra's weekly dumps, and pg_restore refuses an
#     archive produced by a newer server, so restore-drill.sh's PG_IMAGE must
#     stay equal to the image apps-infra's backup writes them with:
#     postgres:<rds_engine_version>-alpine (backup-sync.tf). That file is in
#     another repo, so CI cannot compare the two; what it can pin is that the
#     drill's image is that shape at all, so a bump has to be a deliberate
#     edit made with apps-infra's variables.tf open beside it.
drill_img=$(grep -o 'PG_IMAGE:-[^}]*' "$repo/scripts/restore-drill.sh" | head -1 | cut -d- -f2- || true)
if ! printf '%s' "$drill_img" | grep -Eq '^postgres:[0-9]+-alpine$'; then
    fail "restore-drill.sh pins PG_IMAGE='$drill_img', want postgres:<major>-alpine.
      It must equal apps-infra's postgres:<rds_engine_version>-alpine, or a
      drill against the real dumps fails for a reason that has nothing to do
      with the backups."
fi
pass "restore-drill.sh pins a postgres:<major>-alpine image ($drill_img); keep it equal to apps-infra's rds_engine_version"


# 9c. The drill's size guard is the one piece of logic in these scripts that is
#     not a syntax check away from being right, and it has already been wrong
#     once in production. It compared each dump only against the previous one,
#     so when the nightly payload fell 19x on 2026-09-10 and stayed down, every
#     ~108 KB dump sat beside another ~108 KB dump and it reported "99%" and
#     printed "Backup present and fresh" for five consecutive nights. A
#     derivative cannot see a level.
#
#     --size-verdict takes dump sizes oldest-first and judges the last one,
#     touching neither S3 nor docker, which is what makes this runnable here.
#     Both directions are asserted: a guard that never passes gets disabled,
#     and a guard that never fails is what was already there.
if "$repo/scripts/restore-drill.sh" --size-verdict 2000000 2000000 106000 >/dev/null 2>&1; then
    fail "restore-drill.sh --size-verdict accepted a series ending 2.0 MB, 2.0 MB, 106 KB.
      That is the 2026-09-10 payload cliff exactly, and it must be rejected."
fi
pass "restore-drill.sh rejects a payload cliff"

if ! out=$("$repo/scripts/restore-drill.sh" --size-verdict \
        2000000 2000000 2000000 2000000 2000000 2000000 2000000 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "restore-drill.sh --size-verdict rejected seven healthy ~2.0 MB dumps.
      A backup check that cries wolf is a backup check that gets muted."
fi
pass "restore-drill.sh accepts a steady series"

# A dump at or below schema-only size must fail on the absolute floor even
# with no history to compare against — the case a median cannot cover, because
# the first dump after a rebuilt bucket has no window.
if "$repo/scripts/restore-drill.sh" --size-verdict 39851 >/dev/null 2>&1; then
    fail "restore-drill.sh --size-verdict accepted a lone 39,851-byte dump.
      That is the size of a --schema-only dump of this schema: a restore that
      produced tables and no rows. The absolute floor exists for exactly this."
fi
pass "restore-drill.sh rejects a schema-sized dump with no history"

# 10b. docker-compose.box.yml (CF-B21) is concertFinder's compose project on
#      the shared apps box, live since the cutover. It never runs anywhere
#      but the box, so everything that matters about
#      it is asserted here against the rendered config:
#
#      - exactly one service, `api` — TLS belongs to the box's shared Caddy;
#      - the image is CONCERTFINDER_IMAGE verbatim, never built here;
#      - container_name concertfinder-api, which is the name apps-infra's
#        caddy/Caddyfile proxies to (that file is in another repo, so this is
#        the only half CI can pin);
#      - on the external `edge` network and nothing else, with nothing
#        published to the host — 80/443 belong to the shared Caddy, and a
#        published 8080 would be a way in with none of its headers;
#      - the healthcheck runs the server binary as CMD (the image is
#        distroless: no shell to run CMD-SHELL in), restart unless-stopped,
#        a 45s stop_grace_period (main.go shuts down over 30s; Docker's 10s
#        default kills in-flight river jobs) and capped json-file logs (the
#        root volume is shared by every app on the box). These used to be
#        pinned by equality with the old box's compose file; they are stated
#        here instead now that file is gone.
box_json="$work/box.json"
if ! (cd "$work" && docker compose -f docker-compose.box.yml config --format json) > "$box_json" 2>"$work/errbox"; then
    sed 's/^/    /' "$work/errbox" >&2
    fail "docker-compose.box.yml is not valid"
fi
pass "docker-compose.box.yml parses"

if ! out=$(python3 - "$box_json" "$box_image" <<'PY' 2>&1
import json, sys
box = json.load(open(sys.argv[1]))
want_image = sys.argv[2]
errors = []
services = sorted(box.get("services", {}))
if services != ["api"]:
    errors.append(f"services are {services}, want exactly ['api'] (no caddy: the box's Caddy is shared)")
api = box["services"].get("api", {})
if api.get("image") != want_image:
    errors.append(f"api image renders as {api.get('image')!r}, want CONCERTFINDER_IMAGE ({want_image!r})")
if "build" in api:
    errors.append("api has a build: section; the box pulls from ECR and never builds")
if api.get("container_name") != "concertfinder-api":
    errors.append(f"container_name is {api.get('container_name')!r}, want 'concertfinder-api' (apps-infra's Caddyfile proxies to that name)")
if sorted(api.get("networks") or {}) != ["edge"]:
    errors.append(f"api networks are {sorted(api.get('networks') or {})}, want exactly ['edge']")
if not box.get("networks", {}).get("edge", {}).get("external"):
    errors.append("network edge is not external; `down` here could remove it from under every other app")
if api.get("ports"):
    errors.append(f"api publishes ports {api['ports']}; nothing may be published on the box")
hc = api.get("healthcheck") or {}
if hc.get("test") != ["CMD", "/server", "-healthcheck"]:
    errors.append(f"healthcheck test is {hc.get('test')!r}, want ['CMD', '/server', '-healthcheck']")
if api.get("restart") != "unless-stopped":
    errors.append(f"restart is {api.get('restart')!r}, want 'unless-stopped'")
if api.get("stop_grace_period") != "45s":
    errors.append(f"stop_grace_period is {api.get('stop_grace_period')!r}, want '45s'")
log = api.get("logging") or {}
opts = log.get("options") or {}
if log.get("driver") != "json-file" or not opts.get("max-size") or not opts.get("max-file"):
    errors.append(f"logging is {log!r}, want json-file with max-size and max-file set")
if errors:
    print("\n".join(errors))
    sys.exit(1)
PY
); then
    echo "$out" | sed 's/^/    /' >&2
    fail "docker-compose.box.yml does not match the shape the shared box needs"
fi
pass "box file: api only, image from CONCERTFINDER_IMAGE, concertfinder-api on external edge, nothing published"
pass "box file: CMD healthcheck, unless-stopped, 45s grace period, capped logs"

# 10c. Both `:?` guards must refuse to render, not fall back. Without
#      CONCERTFINDER_IMAGE compose would otherwise resolve an empty image;
#      without DATABASE_URL the container would start with no database (or,
#      in the two-file form, the dev one — CF-B17). --env-file replaces the
#      .env used for interpolation, so each run lacks exactly one variable.
printf 'SITE_DOMAIN=example.com\nDATABASE_URL=%s\n' "$db_url" > "$work/noimage.env"
printf 'SITE_DOMAIN=example.com\nCONCERTFINDER_IMAGE=%s\n' "$box_image" > "$work/nodb.env"
for case in "noimage.env:CONCERTFINDER_IMAGE" "nodb.env:DATABASE_URL"; do
    envf=${case%%:*}
    var=${case#*:}
    # The refusal must be the guard's own, naming the variable. Without the
    # image guard compose still refuses today, but only because an empty image
    # leaves the service with "neither an image nor a build context" — add a
    # build: section and it would quietly build instead. So a refusal for any
    # other reason does not count.
    if (cd "$work" && env -u CONCERTFINDER_IMAGE -u DATABASE_URL \
            docker compose --env-file "$envf" -f docker-compose.box.yml config) >/dev/null 2>"$work/err10c"; then
        fail "docker-compose.box.yml rendered without $var. Its \`:?\` guard is gone, so
      \`up\` would start the api without it instead of refusing."
    fi
    if ! grep -q "$var must be set" "$work/err10c"; then
        sed 's/^/    /' "$work/err10c" >&2
        fail "docker-compose.box.yml refused to render without $var, but not through its
      \`:?\` guard (no \"$var must be set\" in the error) — the guard is gone."
    fi
    pass "box file refuses to render without $var"
done

# 10d. scripts/deploy-box.sh is what the box's SSM document runs, as
#      /opt/concertfinder/deploy.sh — a symlink at the repo root. Same family
#      as the scripts below: it only ever runs on the instance. A broken
#      symlink or a lost executable bit fails the document's `test -x` before
#      anything happens, which is safe but is the whole deploy.
if ! out=$(bash -n "$repo/scripts/deploy-box.sh" 2>&1); then
    echo "$out" | sed 's/^/    /' >&2
    fail "scripts/deploy-box.sh has a syntax error"
fi
pass "deploy-box.sh parses"
[ -x "$repo/scripts/deploy-box.sh" ] ||
    fail "scripts/deploy-box.sh is not executable — run: chmod +x scripts/deploy-box.sh"
if [ ! -L "$repo/deploy.sh" ] || [ "$(readlink "$repo/deploy.sh")" != "scripts/deploy-box.sh" ] || [ ! -x "$repo/deploy.sh" ]; then
    fail "deploy.sh at the repo root must be an executable symlink to scripts/deploy-box.sh.
      apps-infra's concertfinder-deploy document runs /opt/concertfinder/deploy.sh."
fi
pass "deploy.sh is an executable symlink to scripts/deploy-box.sh"

# 10e. deploy-box.sh moves a git checkout to the SHA in IMAGE's tag, so a tag
#      it did not validate reaches git. Run a copy with a stub git that records
#      any call: a bad IMAGE must be refused before git is touched at all.
#      Run against a copy, never this checkout, because the failure this
#      catches is precisely a checkout of the wrong thing.
boxrepo="$work/boxrepo"
mkdir -p "$boxrepo/scripts" "$boxrepo/bin"
cp "$repo/scripts/deploy-box.sh" "$boxrepo/scripts/"
cat > "$boxrepo/bin/git" <<'STUB'
#!/usr/bin/env bash
echo called >> "$GIT_STUB_LOG"
exit 1
STUB
chmod +x "$boxrepo/bin/git"
for bad in "" "426433020884.dkr.ecr.us-east-1.amazonaws.com/concertfinder-api:main" \
        "426433020884.dkr.ecr.us-east-1.amazonaws.com/concertfinder-api:abc;id" \
        "426433020884.dkr.ecr.us-east-1.amazonaws.com/edgefinder-api:0123456"; do
    : > "$work/git.log"
    if PATH="$boxrepo/bin:$PATH" GIT_STUB_LOG="$work/git.log" IMAGE="$bad" \
            CF_DEPLOY_LOCKED=1 "$boxrepo/scripts/deploy-box.sh" >/dev/null 2>&1; then
        fail "deploy-box.sh accepted IMAGE='$bad'"
    fi
    if [ -s "$work/git.log" ]; then
        fail "deploy-box.sh called git for IMAGE='$bad' before refusing it"
    fi
done
pass "deploy-box.sh refuses an IMAGE that is not concertfinder-api at a SHA, before touching git"

# 11. The rendered .env has two consumers with two different parsers, and only
#     one of them is exercised by a deploy. docker compose reads it as an
#     env_file; anything that does `set -a; . "$ENV_FILE"` hands it to bash.
#     The old box's nightly backup did exactly that, and nothing on the shared
#     box does today, but an operator sourcing the file by hand or the next
#     box-side script will. A value holding a space, a `$`, a backtick or a `;`
#     is fine for compose and is a syntax error or a command substitution for
#     bash — so the site stays healthy and whatever sources the file is the
#     only thing that dies. This drives the real render-env.sh against a stub
#     Parameter Store and then sources the result.
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
    fail "the .env render-env.sh writes cannot be sourced — anything that does
      \`set -a; . .env\` would die here while the app, which uses compose's
      separate parser, stayed healthy."
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
