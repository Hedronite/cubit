# Cubit

Cubit measures how much disk Cargo build output uses. It is a Prometheus exporter that walks allowlisted `target/` and shared Cargo directories and reports bytes per workspace. It only measures: it never deletes or cleans anything.

Status: pre-release. Nothing to install yet. This repository holds the contract so far: the config schema, the scan document, the metric table, the refuse list and example Prometheus rules. The commands are built next.

Not related to the Flutter Cubit state-management library.

## What it is for

A Rust-heavy build host accrues gigabytes inside `target/` directories and shared Cargo caches. `node_exporter` shows total disk only, so "what is eating this host?" stays a manual `du`. Cubit walks the roots you allow (per-workspace `target/` directories, a shared `CARGO_TARGET_DIR`, the `CARGO_HOME` cache areas, sccache) and exports per-workspace byte gauges, with bounded caps so a hostile tree cannot exhaust the host. It has no thresholds, no severity, no cleanup and no UI. Alerting is Prometheus rules ([`examples/rules/`](examples/rules/)).

## Quickstart (planned)

This is what the first release will do. Nothing below runs yet. Two synthetic workspaces in a scratch directory, scanned once:

```console
$ root=$(mktemp -d)
$ mkdir -p "$root/projects/app-cli/target/debug" "$root/projects/libs-core/target/release"
$ touch "$root/projects/app-cli/Cargo.toml" "$root/projects/libs-core/Cargo.toml"
$ cat > cubit.yaml <<EOF
schema: cubit.config/v0
host: build-example
roots:
  - alias: example-projects
    path: $root/projects
    mode: workspace-root
EOF
$ cubit validate --config cubit.yaml
$ cubit scan --config cubit.yaml            # human table
$ cubit scan --config cubit.yaml --json     # cubit.scan/v0 on stdout
```

Every name in this repository is synthetic. Roots are named by slug aliases (`example-projects`), never by filesystem path. [`examples/cubit.yaml`](examples/cubit.yaml) is a fuller configuration; [`docs/config.md`](docs/config.md) explains each key.

## Metrics

From `cubit serve` at `/metrics`, or written once per scan with `--textfile-dir`:

- **Sizes (gauges, bytes):** `cubit_workspace_target_bytes`, `cubit_workspace_profile_bytes`, `cubit_root_bytes`, `cubit_cache_bytes`.
- **Counts (gauges):** `cubit_workspaces`, `cubit_workspaces_unlabeled`, `cubit_workspaces_unlabeled_bytes`, `cubit_skipped_entries`.
- **Scan health:** `cubit_scan_duration_seconds`, `cubit_scan_success`, `cubit_scan_partial`, `cubit_scan_last_success_timestamp_seconds`, `cubit_scan_errors_total`, `cubit_scans_total`.
- **Build info:** `cubit_build_info`.

The full table, the label values and what each number means: [`docs/metrics.md`](docs/metrics.md). Values are base units, bytes and seconds. A scan that could not measure everything says so (`cubit_scan_partial`) and reads as a lower bound; a root that fails keeps its last good values and says so (`cubit_scan_success`).

## A shared `CARGO_TARGET_DIR`

If a host sets one shared `CARGO_TARGET_DIR`, every build lands in one directory and Cubit can only report it as a single workspace named in config; it never invents per-workspace attribution. To keep both the shared cache and the attribution, give each project its own directory:

```sh
CARGO_TARGET_DIR=$root/<name> cargo build
```

and point a `workspace-root` at `$root`.

## Security posture

- **Loopback by default.** `cubit serve` binds `127.0.0.1:9101`. A non-loopback bind is refused unless `--unsafe-listen` is given, which logs one warning with the bound address. The metrics are unauthenticated; treat the listener as private.
- **Allowlisted roots only.** The walk opens every root with `os.Root`, so a path that would escape fails at the system call. Symlinks are never followed.
- **A refuse list.** Credential, keychain, vault and other sensitive paths are skipped on every directory entry, and config can only add to the list. See [`docs/refuse-list.md`](docs/refuse-list.md).
- **Bounded.** Per-root caps on entries read and on wall time, and bounded concurrency.
- **One optional write.** `cubit.prom`, in a directory you name with `--textfile-dir`, is the only file Cubit writes.

Reporting a vulnerability: [`SECURITY.md`](SECURITY.md).

## What shows up in Vigil (proposed)

Cubit has no screen of its own. The proposed mapping is that Vigil reads `cubit_*` metrics through its Prometheus backend and exposes per-host disk as fields in `vigil.summary/v0`, which Facet shows. Nothing is committed until that mapping is reviewed. Anything built on Cubit is a warning, never critical: thresholds are Prometheus rules, and Cubit exports no severity.

## Documentation

- [`docs/cli.md`](docs/cli.md): commands, flags and exit codes.
- [`docs/config.md`](docs/config.md): the configuration file and what `validate` checks.
- [`docs/metrics.md`](docs/metrics.md): the metric contract.
- [`docs/refuse-list.md`](docs/refuse-list.md): the refuse list and containment.
- [`schemas/`](schemas/): the JSON Schemas for `cubit.config/v0` and `cubit.scan/v0`.
- [`CONTRIBUTING.md`](CONTRIBUTING.md): how to contribute, with sign-off (DCO).

## License

Licensed under either of [Apache License, Version 2.0](LICENSE-APACHE) or [MIT license](LICENSE-MIT), at your option.

Unless you explicitly state otherwise, any contribution you intentionally submit for inclusion in this work, as defined in the Apache-2.0 license, shall be dual licensed as above, without any additional terms or conditions.
