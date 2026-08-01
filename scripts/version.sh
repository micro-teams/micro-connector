#!/usr/bin/env bash
#
# One place to read or set this project's version.
#
# The repo-root VERSION file is the single source of truth. This script propagates it into every
# artifact that must ship it, and — just as importantly — leaves alone the numbers that are
# deliberately NOT the product version:
#
#   * cli/protocol/protocol.go  const Version = N   -> the WIRE version. It is bumped only when the
#                                                      message set changes in a way an older peer
#                                                      cannot survive, which is a different event
#                                                      from a release, and usually rarer.
#   * .github/workflows/ci.yml  npm:<x.y.z>         -> the pinned Claude Code the tests drive.
#   * testbed/e2e.sh  mockserver:mockserver-<x>     -> the mock we run.
#   * cli/go.mod  go <x.y>                          -> the toolchain.
#
# Two things are published from this repository and they are versioned together, because a screen
# driver and the host that runs it are only ever tested as a pair:
#
#   npm    @micro-teams/connector-applets   published by CI from the tag applets-v<version>
#   go     …/micro-connector/cli            resolved by consumers from the tag cli/v<version>
#           (the cli/ prefix is Go's rule for a module in a subdirectory, not a choice)
#
# Usage:
#   scripts/version.sh              # print the current version and verify every file agrees
#   scripts/version.sh <X.Y.Z>      # set it everywhere
#   scripts/version.sh --tags       # print the git commands that release the current version
#
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION_FILE="$ROOT/VERSION"
APPLETS_PKG="$ROOT/applets/package.json"
GO_VERSION="$ROOT/cli/version.go"

semver_re='^[0-9]+\.[0-9]+\.[0-9]+$'
read_current() { tr -d '[:space:]' < "$VERSION_FILE"; }

pkg_version() { node -p "require('$APPLETS_PKG').version" 2>/dev/null || echo "(node unavailable)"; }
go_version() { perl -ne 'print "$1\n" if /^const Version = "([^"]+)"/' "$GO_VERSION"; }

if [[ "${1:-}" == "--tags" ]]; then
  cur="$(read_current)"
  cat <<TAGS
git tag applets-v$cur   # publishes @micro-teams/connector-applets@$cur to GitHub Packages
git tag cli/v$cur       # makes …/micro-connector/cli@v$cur resolvable
git push origin applets-v$cur cli/v$cur
TAGS
  exit 0
fi

# ---- read / verify ----------------------------------------------------------
if [[ $# -eq 0 ]]; then
  cur="$(read_current)"
  echo "version (VERSION): $cur"
  echo
  echo "as found in each file:"
  printf '  %-28s %s\n' "applets/package.json" "$(pkg_version)"
  printf '  %-28s %s\n' "cli/version.go"       "$(go_version)"
  echo
  echo "independent (NOT touched by this script):"
  printf '  %-28s %s\n' "wire protocol Version" "$(perl -ne 'print "$1\n" if /^const Version = (\d+)/' cli/protocol/protocol.go)"
  printf '  %-28s %s\n' "pinned Claude Code"    "$(perl -ne 'print "$1\n" if /leg: (npm:\S+)/' .github/workflows/ci.yml)"
  printf '  %-28s %s\n' "mock server image"     "$(perl -ne 'print "$1\n" if /mockserver\/mockserver:(\S+) /' testbed/e2e.sh)"
  ok=1
  [[ "$(pkg_version)" == "$cur" ]] || { echo "MISMATCH: applets/package.json"; ok=0; }
  [[ "$(go_version)" == "$cur" ]] || { echo "MISMATCH: cli/version.go"; ok=0; }
  [[ "$ok" == 1 ]] || exit 1
  exit 0
fi

# ---- set --------------------------------------------------------------------
new="$1"
[[ "$new" =~ $semver_re ]] || { echo "error: version must be X.Y.Z (semver), got: $new" >&2; exit 1; }
echo "setting version -> $new"

printf '%s\n' "$new" > "$VERSION_FILE"
node -e "const f='$APPLETS_PKG';const p=require(f);p.version='$new';require('fs').writeFileSync(f, JSON.stringify(p,null,2)+'\n')"
perl -i -pe "s{^(const Version = \")[^\"]+(\")}{\${1}$new\${2}}" "$GO_VERSION"

echo "done. verifying:"
exec "$0"
