#!/usr/bin/env bash
# Run `cubit serve` against a synthetic tree and lint what /metrics serves with
# promtool. Usage: scripts/metrics-lint.sh PROMTOOL_PATH
set -euo pipefail

promtool=${1:?usage: metrics-lint.sh PROMTOOL_PATH}
root=$(git rev-parse --show-toplevel)
work=$(mktemp -d)
pid=""
cleanup() {
  [ -z "$pid" ] || kill "$pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

(cd "$root" && go build -o "$work/cubit" ./cmd/cubit)

mkdir -p "$work/ws/app/target/debug" "$work/ws/lib/target/release" "$work/cargohome/bin"
: > "$work/ws/app/Cargo.toml"
: > "$work/ws/lib/Cargo.toml"
head -c 50000 /dev/zero > "$work/ws/app/target/debug/blob"
head -c 30000 /dev/zero > "$work/ws/lib/target/release/blob"
head -c 10000 /dev/zero > "$work/cargohome/bin/tool"

cat > "$work/cubit.yaml" <<EOF
schema: cubit.config/v0
host: build-example
scan: {interval: 60s}
roots:
  - {alias: example-projects, path: $work/ws, mode: workspace-root}
  - {alias: example-cargo-home, path: $work/cargohome, mode: cargo-home}
  - {alias: example-missing, path: $work/missing, mode: sccache}
EOF

addr=127.0.0.1:19101
"$work/cubit" serve --config "$work/cubit.yaml" --listen "$addr" 2> "$work/log" &
pid=$!

# Wait for the first scan: the size series appear only after it.
for _ in $(seq 1 50); do
  if curl -fsS "http://$addr/metrics" 2> /dev/null | grep -q '^cubit_root_bytes'; then
    break
  fi
  sleep 0.2
done

curl -fsS "http://$addr/metrics" > "$work/metrics.txt"
grep -q '^cubit_root_bytes{root="example-projects"}' "$work/metrics.txt" || { cat "$work/log" >&2; echo "metrics-lint: no size series after the first scan" >&2; exit 1; }
grep -q '^cubit_scan_success{root="example-missing"} 0' "$work/metrics.txt" || { echo "metrics-lint: the missing root should read success 0" >&2; exit 1; }
"$promtool" check metrics < "$work/metrics.txt"
echo "metrics-lint: ok ($(grep -c '^cubit_' "$work/metrics.txt") samples)"
