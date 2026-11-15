#!/bin/bash
#
# Does the signed app actually carry the entitlements it needs?
#
# A capability missing from the signature fails silently: an App Group that is
# not there makes shared storage return nil and a widget show nothing forever;
# a missing aps-environment means no push token and no error; a missing
# associated domain means universal links open Safari. None of it is visible
# from source - the entitlement is in the signature - so it is checked here.
#
#   scripts/release/verify-entitlements.sh              # device build (default)
#   scripts/release/verify-entitlements.sh --app <path> # check a built .app, no build
#   scripts/release/verify-entitlements.sh --source     # check the .entitlements files only
#
# --app is what archive.sh uses on the export: a release is re-signed on
# export, so the bundle that ships is not the one a device build produces.
#
# --source needs no signing, so it is the one CI and the preflight can run. It
# proves the project ASKS for each entitlement; only a signed build proves it
# was granted.
#
# There is no simulator mode: a simulator build consults no provisioning
# profile and comes out with an empty entitlements dict, so it would report
# MISSING on a correctly configured machine.
#
# What is required is REQUIRED_ENTITLEMENTS in release.conf, per bundle.
# Exits non-zero if anything required is missing from any bundle.
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
INVOKED_FROM="$PWD"
# shellcheck source=lib/release-conf.sh
. "$SCRIPT_DIR/lib/release-conf.sh"
# shellcheck source=lib/build-env.sh
. "$SCRIPT_DIR/lib/build-env.sh"

status=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

check_bundle() {
  local plist="$1" mode="$2" bundle="$3"
  local specs=()
  while IFS= read -r spec; do
    [[ -n "$spec" ]] && specs+=("$spec")
  done < <(specs_for_bundle "$bundle")
  if [[ ${#specs[@]} -eq 0 ]]; then
    echo "   $bundle: nothing required"
    return
  fi
  "${KIT_PY[@]}" entitlements "$plist" $mode "${specs[@]}" || status=1
}

if [[ "${1:-}" == "--source" ]]; then
  echo "==> Checking the entitlements the project asks for"
  for entry in "${BUNDLES[@]}"; do
    bundle=$(bundle_path "$entry")
    file=$(bundle_entitlements "$entry")
    if [[ -z "$file" ]]; then
      if [[ -n "$(specs_for_bundle "$bundle")" ]]; then
        echo "!! $bundle: entitlements are required but BUNDLES names no .entitlements file for it"
        status=1
      fi
      continue
    fi
    if [[ ! -f "$PROJECT_ROOT/$file" ]]; then
      echo "!! $bundle: $file not found under $PROJECT_DIR"
      status=1
      continue
    fi
    check_bundle "$PROJECT_ROOT/$file" --source "$bundle"
  done
  exit $status
fi

if [[ "${1:-}" == "--app" ]]; then
  if [[ -z "${2:-}" ]]; then
    echo "!! --app needs the path to a .app"
    exit 2
  fi
  APP="$2"
  [[ "$APP" != /* ]] && APP="$INVOKED_FROM/$APP"
elif [[ -n "${1:-}" ]]; then
  echo "!! unknown argument: $1 (try --app <path> or --source)"
  exit 2
else
  DERIVED="$BUILD_DIR/../verify-entitlements"
  generate_project || exit 1
  echo "==> Building for a generic iOS device"
  if ! xcodebuild_scrubbed "${XCODE_CONTAINER[@]}" -scheme "$SCHEME" \
       -destination "generic/platform=iOS" -derivedDataPath "$DERIVED" -quiet build; then
    echo "!! Build failed. If it stopped at signing with \"doesn't include the ...
   capability\", the managed profiles aren't on this machine yet: open the
   project in Xcode, pick a physical device, and build once (Cmd-B)."
    exit 1
  fi
  APP="$DERIVED/Build/Products/Debug-iphoneos/$APP_NAME.app"
fi

if [[ ! -d "$APP" ]]; then
  echo "!! No $APP_NAME.app at $APP"
  exit 1
fi

echo "==> Checking signed entitlements in $(basename "$APP")"
for entry in "${BUNDLES[@]}"; do
  bundle=$(bundle_path "$entry")
  target="$APP/$bundle"
  [[ "$bundle" == "." ]] && target="$APP"
  if [[ ! -e "$target" ]]; then
    echo "!! $bundle: not found at $target"
    status=1
    continue
  fi
  # `:-` writes the entitlements plist as XML to stdout; codesign's own
  # chatter goes to stderr.
  out="$tmp/$(echo "$bundle" | tr '/.' '__').plist"
  if ! codesign -d --entitlements :- "$target" >"$out" 2>/dev/null; then
    echo "!! $bundle: codesign could not read a signature"
    status=1
    continue
  fi
  check_bundle "$out" "" "$bundle"
done

if [[ $status -ne 0 ]]; then
  echo
  echo "An entitlement is not in the signature. Most likely this machine has no"
  echo "managed profile carrying it yet - only Xcode.app can mint one without an"
  echo "App Store Connect API key:"
  echo "  1. Open the project, select a physical device, press Cmd-B."
  echo "  2. For an export, distribute once from Organizer as well."
  echo "  3. Re-run this script."
  echo
  echo "If that doesn't fix it, check the App ID in the developer portal: the"
  echo "capability has to be enabled AND configured (an App Group assigned, a"
  echo "domain listed). Enabled but unassigned looks identical to this failure."
fi

exit $status
