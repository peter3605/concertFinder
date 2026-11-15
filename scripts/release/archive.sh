#!/bin/bash
#
# Build the App Store release: bump, archive, export, check.
#
#   scripts/release/archive.sh
#
# Leaves a checked .ipa in $BUILD_DIR/export/ and the bumped build number
# uncommitted in VERSION_FILE. MARKETING_VERSION is not touched: a release is
# named by hand before this runs.
#
# The checks run against the EXPORT because that is what ships. Under automatic
# signing the archive is development-signed and Xcode re-signs it on export, so
# an archive that passes says nothing about the entitlements in the .ipa.
#
# Needs a signing identity and the App Store profiles on this machine. Like the
# development ones, only Xcode.app can create them without an App Store Connect
# API key: build once from Xcode on a device, and distribute once from
# Organizer. That is why this is a human step and never a CI one.
#
# Upload the .ipa this leaves with Transporter. Organizer re-signs the archive
# itself, so what it uploads is not the file these checks read.
#
# Exits non-zero on any failure, and puts the build number back so that a
# failed attempt does not use one up.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/release-conf.sh
. "$SCRIPT_DIR/lib/release-conf.sh"
# An archive is the tree that gets exported, zipped and uploaded, so this is
# the build where a secret in the environment matters most.
# shellcheck source=lib/build-env.sh
. "$SCRIPT_DIR/lib/build-env.sh"

VERSION_PATH="$PROJECT_ROOT/$VERSION_FILE"
ARCHIVE="$BUILD_DIR/$APP_NAME.xcarchive"
EXPORT="$BUILD_DIR/export"
UNPACKED="$BUILD_DIR/unpacked"

# Every check that needs no signature runs first: a release that is going to
# fail on store copy or a log line should fail before it spends a build number
# and ten minutes of archiving.
"$SCRIPT_DIR/check-release-ready.sh" --before-archive

# A command substitution, not `read < <(...)`: under `set -e` a failed process
# substitution is ignored, and an empty PREVIOUS would quietly make this build 1.
versions=$("${KIT_PY[@]}" version "$VERSION_PATH")
VERSION="${versions% *}"
PREVIOUS="${versions#* }"
BUILD=$((PREVIOUS + 1))

set_build_number() {
  "${KIT_PY[@]}" set-build "$VERSION_PATH" "$1" && generate_project
}

finished=0
restore() {
  if [[ $finished -eq 0 ]]; then
    echo "!! Failed - build number put back to $PREVIOUS"
    set_build_number "$PREVIOUS" || echo "!! could not restore it; check $VERSION_PATH"
  fi
}
trap restore EXIT

# Removed first, so that every check below can only be reading this run's output.
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

echo "==> Bumping build number: $VERSION ($PREVIOUS) -> $VERSION ($BUILD)"
set_build_number "$BUILD"

echo "==> Archiving Release"
xcodebuild_scrubbed archive "${XCODE_CONTAINER[@]}" -scheme "$SCHEME" \
  -configuration Release -destination 'generic/platform=iOS' \
  -derivedDataPath "$BUILD_DIR/DerivedData" -archivePath "$ARCHIVE" -quiet

echo "==> Exporting for App Store Connect"
xcodebuild_scrubbed -exportArchive -archivePath "$ARCHIVE" \
  -exportPath "$EXPORT" -exportOptionsPlist "$EXPORT_OPTIONS" -quiet

IPA=$(find "$EXPORT" -maxdepth 1 -name '*.ipa' | head -1)
if [[ -z "$IPA" ]]; then
  echo "!! The export produced no .ipa in $EXPORT"
  exit 1
fi

# The re-signed bundles exist only inside the .ipa.
ditto -x -k "$IPA" "$UNPACKED"
APP="$UNPACKED/Payload/$APP_NAME.app"

echo "==> Checking the build number in the export"
for entry in "${BUNDLES[@]}"; do
  bundle="$APP/$(bundle_path "$entry")"
  actual=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$bundle/Info.plist")
  if [[ "$actual" != "$BUILD" ]]; then
    echo "!! $(basename "$bundle") carries build '$actual', expected '$BUILD'"
    exit 1
  fi
  echo "   $(basename "$bundle"): $actual"
done

"$SCRIPT_DIR/verify-entitlements.sh" --app "$APP"

# All of BUILD_DIR: the unpacked export, the archive, and the derived data
# where xcodebuild writes the environment of every task.
"$SCRIPT_DIR/check-build-secrets.sh" "$BUILD_DIR"

finished=1
echo
echo "==> $VERSION ($BUILD) is ready: $IPA"
echo "    Upload that file with Transporter, then commit the build number in"
echo "    $VERSION_FILE. Turn on phased release if this version migrates stored data."
