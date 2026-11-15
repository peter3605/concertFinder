#!/bin/bash
#
# xcodebuild writes its whole environment to disk, so secrets must not be in it.
#
# Every task in a build has its environment serialised into
# <derivedData>/Build/Intermediates.noindex/XCBuildData/*.xcbuilddata/attachments/*.
# That file is plain enough to grep. So any exported secret in the shell that
# launched the build is written into the build tree, on every build, and stays
# there until the tree is deleted. Where this kit came from, it was found only
# because one token happened to match a gitleaks rule; a second token, whose
# shape matched no rule, sat in the same attachments unnoticed.
#
# The machine-side fix is to stop exporting secrets at all (see the kit's
# README, "Secrets in the build environment"). This is the belt to that
# braces: it makes a build safe on a machine that has not had the fix
# applied, which includes any machine that is not yours.
#
# Source this file and call `xcodebuild_scrubbed` wherever `xcodebuild` would
# have been called.

# Environment variable NAMES that are assumed to carry a secret. Matching is
# on the name only — the value is never inspected, and never appears in an
# argument list where `ps` could read it.
BUILD_SECRET_NAME_PATTERN='TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|API_KEY|ACCESS_KEY|PRIVATE_KEY|_KEY$'

# Run xcodebuild with every secret-shaped variable removed from its
# environment. Unsetting a variable a build genuinely needed would fail loudly
# at build time; leaving one in fails silently and permanently, which is why
# this errs towards removing too much.
xcodebuild_scrubbed() {
  local unsets=() name
  while IFS= read -r name; do
    [[ -n "$name" ]] && unsets+=(-u "$name")
  done < <(printenv | cut -d= -f1 | grep -E "$BUILD_SECRET_NAME_PATTERN" || true)

  # `${a[@]+"${a[@]}"}` because macOS ships bash 3.2, where an empty array
  # expanded under `set -u` is an unbound-variable error rather than nothing.
  env ${unsets[@]+"${unsets[@]}"} xcodebuild "$@"
}
