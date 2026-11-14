#!/usr/bin/env bash
# Build the release archives for a tag, deterministically.
#
#   scripts/release.sh TAG        e.g. scripts/release.sh v0.1.0
#
# Run it in a clean checkout of the commit to release. It writes dist/:
#
#   cubit_<ver>_<os>_<arch>            the binary (not a release asset)
#   cubit_<ver>_<os>_<arch>.tar.gz     binary, both licences and the README
#   SHA256SUMS                         over everything in dist/
#
# The same commit built with the same Go gives the same bytes on any machine:
# -trimpath, no build id, no build date, an archive with sorted names, a fixed
# mtime (the commit's), uid/gid 0 and no gzip name or time. The tag is stamped
# into the binary; the commit is read from Go's own VCS stamp.
set -euo pipefail

tag=${1:?usage: scripts/release.sh TAG (for example v0.1.0)}
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]]; then
  echo "release: '$tag' is not a version tag like v0.1.0" >&2
  exit 2
fi
ver=${tag#v}

root=$(git rev-parse --show-toplevel)
cd "$root"

# shellcheck source=scripts/release.env
. scripts/release.env
have=$(GOTOOLCHAIN=local go env GOVERSION)
if [ "$have" != "go$GO_VERSION" ]; then
  echo "release: this checkout is built with go$GO_VERSION (scripts/release.env), not $have." >&2
  echo "release: a different Go gives different bytes; use the pinned release." >&2
  exit 3
fi

# A clean checkout of the exact commit, so Go stamps vcs.modified=false.
if [ -n "$(git status --porcelain)" ]; then
  echo "release: the working tree is not clean; build from a clean checkout." >&2
  exit 3
fi
commit=$(git rev-parse HEAD)
if tagged=$(git rev-parse -q --verify "refs/tags/$tag^{commit}") && [ "$tagged" != "$commit" ]; then
  echo "release: tag $tag points at ${tagged:0:12}, but HEAD is ${commit:0:12}." >&2
  exit 3
fi
epoch=$(git log -1 --format=%ct HEAD)

targets="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"
helper="go run ./scripts/relhelper"

rm -rf dist
mkdir -p dist
for t in $targets; do
  os=${t%/*}
  arch=${t#*/}
  name="cubit_${ver}_${os}_${arch}"
  # Everything that can change the bytes is set here, not inherited.
  env -i HOME="$HOME" PATH="$PATH" \
    GOTOOLCHAIN=local GOFLAGS=-mod=readonly CGO_ENABLED=0 \
    GOOS="$os" GOARCH="$arch" GOAMD64=v1 GOARM64=v8.0 \
    GOCACHE="$(go env GOCACHE)" GOMODCACHE="$(go env GOMODCACHE)" \
    GOPROXY="$(go env GOPROXY)" GOSUMDB="$(go env GOSUMDB)" \
    go build -trimpath -buildvcs=true \
      -ldflags "-s -w -buildid= -X github.com/Hedronite/cubit/internal/version.Version=$tag" \
      -o "dist/$name" ./cmd/cubit
  $helper archive -o "dist/$name.tar.gz" -dir "$name" -mtime "$epoch" \
    "dist/$name=cubit:0755" LICENSE-APACHE:0644 LICENSE-MIT:0644 README.md:0644
done
$helper sums -dir dist
echo "release: $tag at ${commit:0:12} built with $have; see dist/SHA256SUMS"
