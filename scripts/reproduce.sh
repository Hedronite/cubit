#!/usr/bin/env bash
# Prove the release is reproducible: build every archive twice from the same
# commit, in two separate clean worktrees at different paths, and fail if any
# file differs. Then look at what was built: the version stamp, and no
# build-machine path in the binaries.
#
#   scripts/reproduce.sh [COMMIT]     COMMIT defaults to HEAD
#
# In CI on a pull request pass the PR head SHA, not the merge ref the runner
# checks out: the merge commit is a different commit, so its binaries would
# differ from the ones for the commit under review.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"
commit=$(git rev-parse --verify "${1:-HEAD}^{commit}")
tag=v0.0.0-ci
work=$(mktemp -d)
cleanup() {
  for d in "$work/a-parent/checkout" "$work/b-parent/checkout"; do
    git worktree remove --force "$d" 2> /dev/null || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

for side in a b; do
  # Different parents, so a path leaking into a binary shows up as a difference.
  git worktree add --detach -q "$work/$side-parent/checkout" "$commit"
  (cd "$work/$side-parent/checkout" && scripts/release.sh "$tag" > /dev/null)
done
a=$work/a-parent/checkout/dist
b=$work/b-parent/checkout/dist

if ! diff -u "$a/SHA256SUMS" "$b/SHA256SUMS"; then
  echo "reproduce: two builds of ${commit:0:12} differ" >&2
  exit 1
fi
(cd "$a" && sha256sum -c --quiet SHA256SUMS 2> /dev/null || shasum -a 256 -c --quiet SHA256SUMS)
echo "reproduce: two clean builds of ${commit:0:12} are identical ($(wc -l < "$a/SHA256SUMS" | tr -d ' ') files)"
cat "$a/SHA256SUMS"

# The binary says what it is: the tag and this commit.
os=$(go env GOOS)
arch=$(go env GOARCH)
bin="$a/cubit_${tag#v}_${os}_${arch}"
line=$("$bin" version)
case "$line" in
  "cubit $tag (${commit:0:12}, "*) echo "reproduce: $line" ;;
  *) echo "reproduce: version line '$line' does not name $tag and ${commit:0:12}" >&2; exit 1 ;;
esac
if ! "$bin" version --json | grep -q "\"version\":\"$tag\""; then
  echo "reproduce: version --json does not carry $tag" >&2
  exit 1
fi

# No build-machine path, home directory or runner name in any binary.
bad=0
for f in "$a"/cubit_*_*_*; do
  case "$f" in *.tar.gz) continue ;; esac
  if matches=$(LC_ALL=C grep -a -o -E '/(Users|home|runner|root|private/var|tmp)/[A-Za-z0-9._-]+' "$f" | sort -u | head -5) && [ -n "$matches" ]; then
    echo "reproduce: $(basename "$f") contains path-like strings:" >&2
    echo "$matches" >&2
    bad=1
  fi
done
[ "$bad" -eq 0 ] || exit 1

# Archives hold the binary, both licences and the README, owned by root, and nothing else.
arc="$a/cubit_${tag#v}_${os}_${arch}.tar.gz"
listing=$(tar -tvzf "$arc")
echo "$listing"
[ "$(echo "$listing" | wc -l | tr -d ' ')" -eq 5 ] || { echo "reproduce: an archive should hold a directory and four files" >&2; exit 1; }
echo "reproduce: ok"
