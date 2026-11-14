#!/usr/bin/env bash
# Rewrite DIST/SHA256SUMS over every file in DIST (binaries, archives and, in the
# release workflow, the SBOMs). Usage: scripts/sums.sh [DIST]
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
exec go run ./scripts/relhelper sums -dir "${1:-dist}"
