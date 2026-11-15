#!/bin/bash
#
# Check every App Store Connect field in STORE_COPY against its limit.
#
# A subtitle that is 31 characters is not "nearly right", it is a field App
# Store Connect refuses to save, discovered while pasting rather than while
# writing. Apple counts characters, not bytes, and listing copy is full of em
# dashes and curly apostrophes, so `wc -c` is wrong on exactly the fields most
# likely to be near the limit.
#
# Fields are found by their marker, so adding a field to the document is
# enough to put it under this check:
#
#   <!-- field: subtitle limit: 30 -->
#   ```
#   ...the exact text...
#   ```
#
# Which fields must be present is STORE_COPY_FIELDS in release.conf. Leave
# whats_new out for a 1.0: App Store Connect has no What's New field on a
# first version. Put it back for every release after.
#
# It also fails on placeholder text, Markdown in the plain-text fields, and
# keyword-list waste. It cannot tell whether What's New still describes the
# last release; that is a human's read, once a release.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/release-conf.sh
. "$SCRIPT_DIR/lib/release-conf.sh"

fields=$(IFS=,; echo "${STORE_COPY_FIELDS[*]}")
echo "==> Checking store copy in ${STORE_COPY#"$REPO_ROOT"/}"
exec "${KIT_PY[@]}" store-copy "$STORE_COPY" --fields "$fields"
