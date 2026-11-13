#!/usr/bin/env bash
# Check that every non-merge commit in BASE..HEAD carries a Signed-off-by line.
# Usage: scripts/dco.sh BASE_SHA [HEAD_SHA]
set -euo pipefail

base=${1:?usage: dco.sh BASE_SHA [HEAD_SHA]}
head=${2:-HEAD}

missing=0
count=0
while read -r sha; do
  [ -n "$sha" ] || continue
  count=$((count + 1))
  if ! git show -s --format=%B "$sha" | grep -qE '^Signed-off-by: .+ <.+>$'; then
    echo "dco: ${sha:0:12} has no Signed-off-by line (use git commit -s)" >&2
    missing=1
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ "$count" -eq 0 ]; then
  echo "dco: no commits between $base and $head" >&2
  exit 1
fi
if [ "$missing" -ne 0 ]; then
  exit 1
fi
echo "dco: $count commit(s) signed off"
