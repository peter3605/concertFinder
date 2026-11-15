#!/bin/bash
#
# Did this build write a secret into the build tree?
#
# xcodebuild serialises each task's environment into
# <derivedData>/Build/Intermediates.noindex/XCBuildData/*.xcbuilddata/attachments/*,
# so an exported secret in the launching shell ends up on disk. Nothing in the
# build reports this and nothing fails; the file is just there, indefinitely.
#
#   scripts/release/check-build-secrets.sh [dir]   # defaults to PROJECT_DIR/build
#
# Two passes, because either alone misses things:
#
#   1. Known token shapes. Catches a leak from any source, including one this
#      machine's environment does not currently hold.
#   2. The literal value of every secret-named variable in the current
#      environment. Catches a secret whose format matches no rule at all -
#      which is the case that goes unnoticed for months.
#
# Pass 2 only sees what the invoking shell has, so a clean result is evidence
# about this environment, not a general proof. Values are fed to grep on stdin
# and never as arguments, so they stay out of `ps`.
#
# Exits non-zero if anything is found.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# A path given on the command line is relative to where the caller was standing.
INVOKED_FROM="$PWD"

# shellcheck source=lib/build-env.sh
. "$SCRIPT_DIR/lib/build-env.sh"

if [[ -n "${1:-}" ]]; then
  TARGET="$1"
  [[ "$TARGET" != /* ]] && TARGET="$INVOKED_FROM/$TARGET"
else
  # shellcheck source=lib/release-conf.sh
  . "$SCRIPT_DIR/lib/release-conf.sh"
  TARGET="$PROJECT_ROOT/build"
fi

if [[ ! -d "$TARGET" ]]; then
  # A directory named explicitly and not found is a mistake worth failing on.
  # The default one missing just means nothing has been built yet.
  if [[ -n "${1:-}" ]]; then
    echo "!! no such directory: $TARGET"
    exit 1
  fi
  echo "   no $TARGET/ directory - nothing has been built, so nothing to check"
  exit 0
fi

status=0

hitfile=$(mktemp)
trap 'rm -f "$hitfile"' EXIT

# Pass 1 — recognisable token formats.
#   gh[pousr]_…  GitHub personal access / OAuth / user / server / refresh
#   github_pat_… GitHub fine-grained PAT
#   AKIA…        AWS access key ID
#   sk-…         OpenAI-style secret key
#   xox[baprs]-… Slack
KNOWN='gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9]{32,}|xox[baprs]-[A-Za-z0-9-]{10,}'
echo "==> Scanning $TARGET/ for known token formats"
hits=$(grep -rlaE "$KNOWN" "$TARGET" 2>/dev/null)
if [[ -n "$hits" ]]; then
  echo "!! a recognisable token is present in the build tree:"
  sed 's/^/     /' <<<"$hits"
  status=1
fi

# Pass 2 — the values this shell is actually carrying.
echo "==> Scanning $TARGET/ for values of secret-named variables in this shell"
names=()
values=()
while IFS= read -r name; do
  [[ -z "$name" ]] && continue
  value=$(printenv "$name")
  # A short value is not a secret worth matching, and would produce noise.
  [[ ${#value} -lt 16 ]] && continue
  names+=("$name")
  values+=("$value")
done < <(printenv | cut -d= -f1 | grep -E "$BUILD_SECRET_NAME_PATTERN" || true)

if [[ ${#names[@]} -gt 0 ]]; then
  # One pass for every value at once. Scanning per-variable re-reads the whole
  # tree each time, which on a few hundred megabytes of derived data is minutes
  # rather than seconds. Patterns go in on stdin, never as arguments and never
  # through a temporary file - writing the secrets to /tmp to find out whether
  # they were written to disk would be its own joke.
  printf '%s\n' "${values[@]}" | grep -rlaF -f - "$TARGET" >"$hitfile" 2>/dev/null

  # Attribution runs only against the handful of files that matched, so naming
  # the variable costs nothing.
  while IFS= read -r hit; do
    [[ -z "$hit" ]] && continue
    i=0
    while [[ $i -lt ${#names[@]} ]]; do
      if printf '%s\n' "${values[$i]}" | grep -qaF -f - "$hit" 2>/dev/null; then
        echo "!! \$${names[$i]}'s value was written into:"
        echo "     $hit"
        status=1
      fi
      i=$((i + 1))
    done
  done <"$hitfile"
fi

if [[ $status -eq 0 ]]; then
  echo "   clean - no secret found in $TARGET/"
else
  echo
  echo "The build inherited a secret from the shell that launched it. Delete the"
  echo "build tree, then stop exporting the variable - see \"Secrets in the build"
  echo "environment\" in the release kit's README. Deleting alone is not a fix:"
  echo "the next build captures it again."
fi

exit $status
