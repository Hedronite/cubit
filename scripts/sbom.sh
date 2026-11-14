#!/usr/bin/env bash
# Write one CycloneDX SBOM per release binary into dist/, next to its archive:
# cubit_<ver>_<os>_<arch>.cdx.json. The release workflow runs this after
# scripts/release.sh; cyclonedx-gomod is installed there at a pinned version and
# is not a dependency of the module. Usage: scripts/sbom.sh TAG
set -euo pipefail
tag=${1:?usage: scripts/sbom.sh TAG}
ver=${tag#v}
cd "$(git rev-parse --show-toplevel)"
command -v cyclonedx-gomod > /dev/null || { echo "sbom: cyclonedx-gomod is not on PATH" >&2; exit 1; }
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${t%/*}
  arch=${t#*/}
  out="dist/cubit_${ver}_${os}_${arch}.cdx.json"
  # The build constraints decide which modules are in the binary, so each target
  # gets its own SBOM, made with the constraints release.sh builds with.
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 GOFLAGS=-mod=readonly \
    cyclonedx-gomod app -json -licenses -packages -main cmd/cubit -output "$out" .
  [ -s "$out" ] || { echo "sbom: $out is empty" >&2; exit 1; }
done
