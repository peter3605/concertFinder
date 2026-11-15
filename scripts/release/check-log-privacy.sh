#!/bin/bash
#
# Does any log line put user content in the clear?
#
# `Logger` string interpolation defaults to `.private` and is redacted in the
# system log. `privacy: .public` un-redacts it. Put a place name, a search term
# or an email address behind it and the device keeps a persistent record of it,
# readable by anyone with Console.app and a cable, and captured into any
# sysdiagnose the user files.
#
# This is a source-level property with no runtime signal: a redacted log line
# and an un-redacted one are the same call, and nothing inside the app can see
# which it made. So it is checked by a script rather than a unit test. It needs
# no signing, so it belongs in CI.
#
# Scans LOG_SOURCE_DIRS. Fails on any `privacy: .public` outside
# LOG_PRIVACY_ALLOWED in release.conf, where each entry is
# "FILE:SUBSTRING" (FILE relative to the repo root) and should carry a comment
# saying why that value provably holds no user content. Adding to that list is
# a deliberate act with a reason attached, which is the whole point.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/release-conf.sh
. "$SCRIPT_DIR/lib/release-conf.sh"

echo "==> Checking log privacy"
exec "${KIT_PY[@]}" log-privacy --root "$REPO_ROOT" --dirs "${LOG_SOURCE_DIRS[@]}" \
  --allow ${LOG_PRIVACY_ALLOWED[@]+"${LOG_PRIVACY_ALLOWED[@]}"}
