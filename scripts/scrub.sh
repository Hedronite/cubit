#!/usr/bin/env bash
# Scrub the tree before it goes public. Cubit's repo is public, so no real
# filesystem paths, addresses, host names or contact details may appear in it.
#
# Usage: scripts/scrub.sh
# Optional: CUBIT_SCRUB_EXTRA is an extended regex (case-insensitive) of further
# strings to refuse, for example host or people names. Keep that value in a
# repository secret (masked in CI logs), never in the tree and never in a
# repository variable.
#
# Portable to the bash and grep that ship with macOS and with Linux runners.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

list=$(mktemp)
trap 'rm -f "$list"' EXIT

# Tracked files, plus untracked ones that are not ignored. Skipped: this script
# (it holds the patterns), the module checksums (hashes), and the license files
# (they carry the copyright holder's legal name by design).
git ls-files -z --cached --others --exclude-standard \
  | { grep -zv -e '^scripts/scrub\.sh$' -e '^go\.sum$' -e '^LICENSE' || true; } > "$list"

if [ ! -s "$list" ]; then
  echo "scrub: no files to check" >&2
  exit 2
fi
nfiles=$(tr -cd '\0' < "$list" | wc -c | tr -d ' ')

fail=0

# check LABEL REGEX [ALLOW_REGEX]
check() {
  local label=$1 regex=$2 allow=${3:-} hits
  hits=$(xargs -0 grep -I -n -H -E -e "$regex" < "$list" 2>/dev/null || true)
  if [ -n "$allow" ] && [ -n "$hits" ]; then
    hits=$(printf '%s\n' "$hits" | grep -v -E -e "$allow" || true)
  fi
  if [ -n "$hits" ]; then
    echo "scrub: $label" >&2
    printf '%s\n' "$hits" | sed 's/^/  /' >&2
    fail=1
  fi
}

check "home directory path" \
  '/(Users|home)/[A-Za-z0-9._-]+' \
  '/(Users|home)/(example|user|you|name)([^A-Za-z0-9._-]|$)'

check "IPv4 address" \
  '([0-9]{1,3}\.){3}[0-9]{1,3}' \
  '(^|[^0-9.])(127\.0\.0\.1|0\.0\.0\.0|192\.0\.2\.[0-9]+|198\.51\.100\.[0-9]+|203\.0\.113\.[0-9]+)([^0-9]|$)'

check "tailnet or local host name" \
  '[A-Za-z0-9-]+\.(ts\.net|local|lan|internal)([^A-Za-z0-9-]|$)'

check "email address" \
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}' \
  '@(example\.(com|org|net)|users\.noreply\.github\.com)|noreply@'

if [ -n "${CUBIT_SCRUB_EXTRA:-}" ]; then
  hits=$(xargs -0 grep -I -n -H -i -E -e "$CUBIT_SCRUB_EXTRA" < "$list" 2>/dev/null || true)
  if [ -n "$hits" ]; then
    # Report where, never what: the pattern list is private.
    echo "scrub: a CUBIT_SCRUB_EXTRA pattern matched in:" >&2
    printf '%s\n' "$hits" | cut -d: -f1,2 | sed 's/^/  /' >&2
    fail=1
  fi
fi

if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "scrub: clean ($nfiles files)"
