#!/usr/bin/env bash
# Print the CHANGELOG.md section for a tag: the lines under "## vX.Y.Z" up to the
# next "## " heading. Fails when the section is missing or empty, so a tag with
# no notes cannot be released. Usage: scripts/changelog-notes.sh TAG
# (CHANGELOG_FILE overrides the file, for tests.)
set -euo pipefail
tag=${1:?usage: scripts/changelog-notes.sh TAG}
cd "$(git rev-parse --show-toplevel)"
notes=$(awk -v tag="$tag" '
  $0 ~ "^## " tag "( |$)" { on = 1; next }
  /^## / { on = 0 }
  on { print }
' "${CHANGELOG_FILE:-CHANGELOG.md}" | sed -e '/./,$!d')
if [ -z "$notes" ]; then
  echo "changelog-notes: CHANGELOG.md has no section for $tag" >&2
  exit 1
fi
printf '%s\n' "$notes"
