#!/bin/bash
#
# Every release check that needs no signing identity, in one command.
#
#   scripts/release/check-release-ready.sh
#
# This is what a session, or CI on a Mac runner, can prove about a release
# without your Apple account. It runs, and reports every result rather than
# stopping at the first:
#
#   - release.conf is filled in and VERSION_FILE has one whole build number
#   - ExportOptions.plist names a team, exports for App Store Connect, and
#     leaves the build number alone
#   - Info.plist declares ITSAppUsesNonExemptEncryption
#   - the privacy manifest exists and gives a reason for every API it declares
#   - each bundle's .entitlements file asks for what REQUIRED_ENTITLEMENTS needs
#   - store copy fits App Store Connect's limits
#   - no log line is un-redacted outside the allowlist
#   - no secret has been written into the build tree
#
# What it cannot prove is that signing GRANTED those entitlements, or that the
# export carries the right build number. archive.sh checks those, on the
# export, with your signing identity.
#
# --before-archive is how archive.sh calls it; it skips the build-tree scan,
# which archive.sh runs itself on the fresh tree.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/release-conf.sh
. "$SCRIPT_DIR/lib/release-conf.sh"

failed=()
run() {
  local name="$1"; shift
  if ! "$@"; then
    failed+=("$name")
  fi
}

echo "==> Release config"
if versions=$("${KIT_PY[@]}" version "$PROJECT_ROOT/$VERSION_FILE"); then
  echo "   $VERSION_FILE: version ${versions% *}, build ${versions#* }"
else
  echo "$versions"
  failed+=("version")
fi

echo "==> Export options"
if [[ -f "$EXPORT_OPTIONS" ]]; then
  run "export options" "${KIT_PY[@]}" export-options "$EXPORT_OPTIONS"
else
  echo "!! no ExportOptions.plist at $EXPORT_OPTIONS"
  failed+=("export options")
fi

echo "==> Info.plist and privacy manifest"
if [[ -n "$INFO_PLIST" ]]; then
  run "Info.plist" "${KIT_PY[@]}" info-plist "$PROJECT_ROOT/$INFO_PLIST"
else
  echo "!! INFO_PLIST is not set in release.conf"
  failed+=("Info.plist")
fi
if [[ -n "$PRIVACY_MANIFEST" ]]; then
  run "privacy manifest" "${KIT_PY[@]}" privacy-manifest "$PROJECT_ROOT/$PRIVACY_MANIFEST"
else
  echo "!! PRIVACY_MANIFEST is not set in release.conf - apps on the App Store need one"
  failed+=("privacy manifest")
fi

run "entitlements (source)" "$SCRIPT_DIR/verify-entitlements.sh" --source
run "store copy" "$SCRIPT_DIR/check-store-copy.sh"
run "log privacy" "$SCRIPT_DIR/check-log-privacy.sh"
if [[ "${1:-}" != "--before-archive" ]]; then
  run "build secrets" "$SCRIPT_DIR/check-build-secrets.sh"
fi

echo
if [[ ${#failed[@]} -gt 0 ]]; then
  echo "!! Not ready - failed: $(IFS=,; echo "${failed[*]}" | sed 's/,/, /g')"
  exit 1
fi
echo "==> Every check that needs no signing passed."
echo "    The signed half is archive.sh, run by someone with the signing identity."
