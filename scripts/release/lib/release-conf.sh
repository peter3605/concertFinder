#!/bin/bash
#
# Load release.conf and resolve every path in it. Sourced by each script in the
# kit, after it has set SCRIPT_DIR to the directory it lives in.
#
# Sets, besides the conf's own values:
#   KIT_DIR       the directory holding the scripts and release.conf
#   REPO_ROOT     the git work tree the kit is installed in
#   PROJECT_ROOT  REPO_ROOT/PROJECT_DIR - where the Xcode project lives
#   KIT_PY        the python helper, as a command prefix
#
# A missing or placeholder value fails here, by name, rather than as an
# xcodebuild error three minutes into an archive.

KIT_DIR="$(cd "$SCRIPT_DIR" && pwd)"
CONF="${RELEASE_CONF:-$KIT_DIR/release.conf}"

if [[ ! -f "$CONF" ]]; then
  echo "!! no release.conf at $CONF"
  echo "   Copy release.conf.example beside it and fill it in."
  exit 2
fi

REPO_ROOT="$(git -C "$KIT_DIR" rev-parse --show-toplevel 2>/dev/null || (cd "$KIT_DIR/../.." && pwd))"

# Defaults, overridden by the conf.
PROJECT_DIR="."
GENERATE_CMD=""
VERSION_FILE="project.yml"
EXPORT_OPTIONS=""
BUILD_DIR=""
STORE_COPY="docs/store-copy.md"
STORE_COPY_FIELDS=(name subtitle keywords promotional_text description)
BUNDLES=()
REQUIRED_ENTITLEMENTS=()
LOG_SOURCE_DIRS=()
LOG_PRIVACY_ALLOWED=()
INFO_PLIST=""
PRIVACY_MANIFEST=""

# shellcheck source=/dev/null
. "$CONF"

conf_problems=()
for name in APP_NAME PROJECT SCHEME; do
  value="${!name:-}"
  if [[ -z "$value" || "$value" == *REPLACE_ME* ]]; then
    conf_problems+=("$name is not set")
  fi
done
if [[ ${#BUNDLES[@]} -eq 0 ]]; then
  conf_problems+=("BUNDLES is empty - list the app as \".|<its .entitlements file>\" at least")
fi
if [[ ${#LOG_SOURCE_DIRS[@]} -eq 0 ]]; then
  conf_problems+=("LOG_SOURCE_DIRS is empty")
fi
if [[ ${#conf_problems[@]} -gt 0 ]]; then
  echo "!! $CONF is not filled in:"
  printf '     %s\n' "${conf_problems[@]}"
  exit 2
fi

PROJECT_ROOT="$(cd "$REPO_ROOT/$PROJECT_DIR" 2>/dev/null && pwd)" || {
  echo "!! PROJECT_DIR '$PROJECT_DIR' does not exist under $REPO_ROOT"
  exit 2
}
EXPORT_OPTIONS="${EXPORT_OPTIONS:-$KIT_DIR/ExportOptions.plist}"
[[ "$EXPORT_OPTIONS" != /* ]] && EXPORT_OPTIONS="$REPO_ROOT/$EXPORT_OPTIONS"
BUILD_DIR="${BUILD_DIR:-$PROJECT_DIR/build/release}"
[[ "$BUILD_DIR" != /* ]] && BUILD_DIR="$REPO_ROOT/$BUILD_DIR"
[[ "$STORE_COPY" != /* ]] && STORE_COPY="$REPO_ROOT/$STORE_COPY"

KIT_PY=(python3 -B "$KIT_DIR/lib/release_kit.py")

# -project or -workspace, whichever PROJECT names.
if [[ "$PROJECT" == *.xcworkspace ]]; then
  XCODE_CONTAINER=(-workspace "$PROJECT_ROOT/$PROJECT")
else
  XCODE_CONTAINER=(-project "$PROJECT_ROOT/$PROJECT")
fi

# Regenerate the project from its spec, when it has one. A no-op otherwise.
generate_project() {
  [[ -z "$GENERATE_CMD" ]] && return 0
  local tool="${GENERATE_CMD%% *}"
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "!! $tool not found, and GENERATE_CMD needs it"
    return 1
  fi
  (cd "$PROJECT_ROOT" && eval "$GENERATE_CMD" >/dev/null)
}

# Each BUNDLES entry is "PATH_IN_APP|ENTITLEMENTS_FILE". Split one.
bundle_path() { echo "${1%%|*}"; }
bundle_entitlements() { [[ "$1" == *"|"* ]] && echo "${1#*|}"; }

# The REQUIRED_ENTITLEMENTS that apply to one bundle path.
specs_for_bundle() {
  local bundle="$1" spec
  for spec in ${REQUIRED_ENTITLEMENTS[@]+"${REQUIRED_ENTITLEMENTS[@]}"}; do
    [[ "${spec%%|*}" == "$bundle" ]] && printf '%s\n' "$spec"
  done
}
