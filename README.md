# Cubit

**Cubit measures Cargo disk usage on build hosts and exports it as Prometheus metrics. It measures; it never cleans, deletes or prunes anything.**

Not the Flutter Cubit state library — this is the Hedronite Cargo disk exporter.

## What it is

A Rust-heavy workstation accrues gigabytes inside `target/` directories and shared Cargo caches. `node_exporter` shows total disk only; the question "what is eating this build box?" stays a manual `du`. Cubit walks allowlisted Cargo roots — per-workspace `target/` directories, a shared `CARGO_TARGET_DIR`, `~/.cargo` cache areas, sccache — and exports per-workspace byte gauges with bounded caps. It is a read-only exporter: no thresholds, no severity, no cleanup, no UI. Everything else about disk health happens in Prometheus rules and Vigil.

## Status

**C1a is built:** `cubit scan`, `cubit validate` and `cubit version` run today. Planned: `cubit serve`, the exporter endpoint (C1b), and `--textfile-dir`, the node_exporter textfile write (C1c). The contract — `cubit.config/v0`, `cubit.scan/v0`, the metric table, the refuse list — is in PR #1 and these docs.

## Quickstart

Two synthetic workspaces on a scratch disk, validated and scanned:

```console
$ root=$(mktemp -d)
$ mkdir -p "$root/app-cli/target/debug" "$root/libs-core/target/release"
$ printf 'x' > "$root/app-cli/Cargo.toml"
$ printf 'x' > "$root/libs-core/Cargo.toml"
$ dd if=/dev/zero of="$root/app-cli/target/debug/blob" bs=64k count=1 status=none
$ dd if=/dev/zero of="$root/libs-core/target/release/blob" bs=64k count=1 status=none
$ cat > cubit.yaml <<EOF
schema: cubit.config/v0
host: build-example
roots:
  - alias: example-projects
    path: $root
    mode: workspace-root
EOF
$ cubit validate --config cubit.yaml
cubit.yaml:6: info: roots[0] 'example-projects': mode workspace-root
cubit.yaml: ok, 1 root(s)
$ cubit scan --config cubit.yaml
ROOT              MODE            BYTES   SIZE  STATE
example-projects  workspace-root  131072  128K  ok
  app-cli                         65536    64K
  libs-core                       65536    64K
$ cubit scan --config cubit.yaml --json    # cubit.scan/v0 on stdout
```

Byte counts depend on filesystem block size; the output above is from a real run. `SIZE` is a rounded reading aid (base 1024); `BYTES` and the JSON carry the exact number. Every alias and path in this README is synthetic; real deployments name roots with slugs, never filesystem paths.

For scripted use: `cubit scan --json` prints the `cubit.scan/v0` document alone on stdout (schema in [`schemas/cubit.scan.v0.schema.json`](schemas/cubit.scan.v0.schema.json)); `cubit validate --json` prints findings as JSON. Exit codes: 0–2 mean a scan ran (ok / partial / nothing usable), 3+ mean it could not. See [docs/cli.md](docs/cli.md).

## Metrics

From `cubit serve` at `/metrics` (C1b), or written once per scan with `--textfile-dir` (C1c):

- **Sizes (gauges, bytes):** `cubit_workspace_target_bytes`, `cubit_workspace_profile_bytes`, `cubit_root_bytes`, `cubit_cache_bytes`.
- **Counts (gauges):** `cubit_workspaces`, `cubit_workspaces_unlabeled{,_bytes}`, `cubit_skipped_entries`.
- **Scan health (gauges and counters):** `cubit_scan_duration_seconds`, `cubit_scan_success`, `cubit_scan_partial`, `cubit_scan_last_success_timestamp_seconds`, `cubit_scan_errors_total`, `cubit_scans_total`.
- **Build info:** `cubit_build_info`.

Full table, label enums and semantics: [docs/metrics.md](docs/metrics.md). Values are base units — bytes and seconds, no MiB, no percentages.

## The shared `CARGO_TARGET_DIR` caveat

If a build host sets one shared `CARGO_TARGET_DIR`, every build lands in one directory and Cubit can report it only as a single config-named workspace — it never invents per-workspace attribution. To keep both benefits, point each project at its own directory:

```sh
CARGO_TARGET_DIR=$root/<name> cargo build
```

One directory per project, and each still reads as one workspace to Cubit.

## Security posture

- **Loopback by default.** `serve` (C1b) binds `127.0.0.1:9101`. A non-loopback bind is refused unless `--unsafe-listen` is passed, which logs one WARN line with the bound address.
- **Unauthenticated.** The metrics endpoint has no auth. Treat the listener as private; do not expose it beyond the host without understanding that.
- **Allowlisted roots only.** The walk opens every root with `os.Root`, so a path escape fails at the syscall. Symlinks are never followed.
- **Refuse list.** Credential, vault and sensitive prefixes (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.kube`, …) are refused on every entry, built in and non-removable; config only adds. See [docs/refuse-list.md](docs/refuse-list.md).
- **Bounded.** Per-root caps on files and wall time; a hostile tree cannot exhaust the host. Partial results are reported as lower bounds.
- **One write, optional.** The textfile output (`cubit.prom`) is the only thing Cubit writes, and only when `--textfile-dir` is set (C1c).

## What shows up in Vigil (proposed until C3)

Cubit feeds no screens of its own. The proposed mapping, frozen at C3: Vigil reads `cubit_*` metrics through its Prometheus backend and exposes per-host disk as summary fields in `vigil.summary/v0`; Facet renders those fields next to mesh health. Nothing before C3 is committed. **Anything Cubit-derived is YELLOW, never RED** — thresholds live in Prometheus rules ([examples/rules/](examples/rules/)), and Cubit exports no severity.

## Links

- [docs/metrics.md](docs/metrics.md) — the metric contract.
- [docs/cli.md](docs/cli.md) — commands, flags, exit codes.
- [docs/refuse-list.md](docs/refuse-list.md) — the built-in refuse list and containment.
- [CONTRIBUTING.md](CONTRIBUTING.md) (DCO), [SECURITY.md](SECURITY.md).
