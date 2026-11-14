#!/usr/bin/env bash
# Measure Cubit on a generated tree and print the numbers docs/performance.md
# records: wall time and peak RSS of one `cubit scan --json`, the latency of a
# `serve` scrape, and the binary size. Nothing here scans a real tree.
#
#   scripts/perf.sh            builds ./cmd/cubit the way the release does
#
# The tree is the budget tree: ten workspaces whose targets hold 10,000 files in
# 1,000 directories, one byte each. Peak RSS comes from /usr/bin/time (-l on macOS,
# -v on GNU).
set -euo pipefail

root=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
pid=""
cleanup() {
  [ -z "$pid" ] || kill "$pid" 2> /dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

(cd "$root" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o "$work/cubit" ./cmd/cubit)

tree="$work/tree"
for w in 00 01 02 03 04 05 06 07 08 09; do
  mkdir -p "$tree/ws$w/src"
  : > "$tree/ws$w/Cargo.toml"
  for d in $(seq -f '%03g' 0 99); do
    dir="$tree/ws$w/target/debug/d$d"
    mkdir -p "$dir"
    for f in 0 1 2 3 4 5 6 7 8 9; do printf 'x' > "$dir/f$f"; done
  done
done
files=$(find "$tree" -type f | wc -l | tr -d ' ')
dirs=$(find "$tree" -type d | wc -l | tr -d ' ')

cat > "$work/cubit.yaml" <<YAML
apiVersion: cubit.config/v0
host: build-example
scan: {interval: 1h}
roots:
  - {alias: example-projects, path: $tree, mode: workspace-root}
YAML

echo "tree: $files files, $dirs directories"
echo "binary: $(wc -c < "$work/cubit" | tr -d ' ') bytes (this host, stripped)"

# Wall time and peak RSS of five one-shot scans.
if /usr/bin/time -l true > /dev/null 2>&1; then rss_flag=-l; else rss_flag=-v; fi
TIMEFORMAT=%R
for i in 1 2 3 4 5; do
  : > "$work/time.txt"
  start=$(date +%s)
  secs=$({ time /usr/bin/time $rss_flag -o "$work/time.txt" "$work/cubit" scan --config "$work/cubit.yaml" --json > "$work/scan.json"; } 2>&1 | tail -1)
  rss=$(awk '/maximum resident set size/ {print $1} /Maximum resident set size/ {print $NF*1024}' "$work/time.txt")
  echo "scan $i: ${secs}s wall, peak RSS $((rss / 1048576)) MiB ($rss bytes)"
done
grep -q '"success": *true' "$work/scan.json" || grep -q '"success":true' "$work/scan.json" || { echo "perf: the scan did not succeed" >&2; exit 1; }

# One serve: the first scan, then scrapes from the snapshot.
addr=127.0.0.1:19102
"$work/cubit" serve --config "$work/cubit.yaml" --listen "$addr" 2> "$work/serve.log" &
pid=$!
for _ in $(seq 1 100); do
  if curl -fsS "http://$addr/metrics" 2> /dev/null | grep -q '^cubit_root_bytes'; then break; fi
  sleep 0.1
done
for _ in $(seq 1 50); do curl -fsS -o /dev/null -w '%{time_total}\n' "http://$addr/metrics"; done | sort -n > "$work/scrapes.txt"
echo "scrape x50: median $(sed -n 25p "$work/scrapes.txt")s, worst $(tail -1 "$work/scrapes.txt")s (curl time_total, loopback)"
