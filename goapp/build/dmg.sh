#!/bin/bash
# Builds tuissh.app + .dmg via the MyGo CLI (bundles libghostty-vt, signs
# the app ad-hoc). Output in dist/darwin-arm64/.
set -euo pipefail
cd "$(dirname "$0")/.."

go tool mygo build "$@"

echo
echo "==> dist/darwin-arm64/tuissh.app + tuissh *.dmg"
echo "    unsigned: first open via right-click → Open"
