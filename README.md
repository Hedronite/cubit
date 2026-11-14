# Cubit

**Cubit measures Cargo disk usage on build hosts and exports it as Prometheus metrics. It measures; it never cleans, deletes or prunes anything.**

Not the Flutter Cubit state library — this is the Hedronite Cargo disk exporter, a single static Go binary.

## What it is

A Rust-heavy workstation accrues gigabytes inside `target/` directories and shared Cargo caches. `node_exporter` shows total disk only; the question "what is eating this build box?" stays a manual `du`. Cubit walks allowlisted Cargo roots — per-workspace `target/` directories, a shared `CARGO_TARGET_DIR`, `~/.cargo` cache areas, sccache — and exports per-workspace byte gauges with bounded caps. It is a read-only exporter: no thresholds, no severity, no cleanup, no UI. Everything else about disk health happens in Prometheus rules and Vigil.

Cubit is **not** a cleaner (it never deletes, prunes or runs `cargo clean`), not a general disk-usage tool (it knows Cargo's layouts and nothing else), and not an alerting system (it exports numbers; thresholds are yours). It reads only the roots you name, and it refuses a built-in list of credential and vault locations even if you name one by mistake.

## Status

**Version v0.1.0 is the first release.** Everything described here is built: `cubit scan`, `cubit validate`, `cubit version` and `cubit serve` (the exporter at `/metrics` and a liveness-only `/healthz`), plus `--textfile-dir` for node_exporter's textfile collector. Before 1.0 the three documents — `cubit.config/v0`, `cubit.scan/v0` and `cubit.validate/v0` — and the metric names change only by addition; see the [changelog](CHANGELOG.md). Supported platforms: Linux and macOS, on amd64 and arm64.

## Install

Take the archive for your platform from the [releases page](https://github.com/Hedronite/cubit/releases): `cubit_<version>_<os>_<arch>.tar.gz`, for `linux` or `darwin` on `amd64` or `arm64`. It holds the `cubit` binary, both licences and this README. Nothing is installed anywhere else, and the binary needs no runtime.

```sh
tar -xzf cubit_0.1.0_linux_amd64.tar.gz
./cubit_0.1.0_linux_amd64/cubit version
# cubit v0.1.0 (<commit>, go1.27.4)
```

To build from source instead you need Go 1.26 or later: `go build -o cubit ./cmd/cubit`. A build that is not a release prints `dev` as its version, never a made-up one.

### Verify a download

Each release carries `SHA256SUMS`, a CycloneDX SBOM per binary, and GitHub build-provenance attestations. There is no signing key to trust: the attestation says which commit and which workflow run built the archive.

```sh
# 1. The checksum. SHA256SUMS lists every file of the build, including ones that
#    are not published, so check only the archive you downloaded.
grep ' cubit_0.1.0_linux_amd64.tar.gz$' SHA256SUMS | sha256sum -c -      # macOS: shasum -a 256 -c -

# 2. The provenance: this archive was built from this repository by its release workflow.
gh attestation verify cubit_0.1.0_linux_amd64.tar.gz --repo Hedronite/cubit

# 3. Optionally, the SBOM bound to the archive.
gh attestation verify cubit_0.1.0_linux_amd64.tar.gz --repo Hedronite/cubit \
  --predicate-type https://cyclonedx.org/bom
```

The archives are reproducible: the same commit built with the same Go release gives the same bytes, on any machine. To check one yourself, install the Go release named in [`scripts/release.env`](scripts/release.env), check out the tag and run `scripts/release.sh v0.1.0`; `dist/SHA256SUMS` should match the published one. [docs/versions.md](docs/versions.md) lists every pinned tool and action.

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
apiVersion: cubit.config/v0
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

For scripted use: `cubit scan --json` prints the `cubit.scan/v0` document alone on stdout (schema in [`schemas/cubit.scan.v0.schema.json`](schemas/cubit.scan.v0.schema.json)); `cubit validate --json` prints the `cubit.validate/v0` document. See [docs/cli.md](docs/cli.md) for every command and flag.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | ok: every root usable and complete |
| 1 | partial: a scan ran but some bytes went unmeasured (a cap or an unreadable subtree), so sizes are lower bounds, or some roots failed |
| 2 | nothing usable: every root failed |
| 3 | usage: a bad flag or argument |
| 4 | invalid or refused config; for `scan`, a `--textfile-dir` preflight failure. Nothing was scanned |
| 5 | `serve` refused to start (a non-loopback bind without `--unsafe-listen`, a failed listen, or a textfile preflight failure) |
| 6 | internal error |
| 7 | `scan --textfile-dir` could not write `cubit.prom` after the scan; the document is still printed |
| 130, 143 | `scan` interrupted by SIGINT or SIGTERM |

Codes 0 to 2 print the document on stdout; codes 3 and up do not. `serve` exits 0 on SIGINT or SIGTERM.

## Serve or cron

| | `cubit serve` | `cubit scan --textfile-dir DIR` from cron |
|---|---|---|
| Runs as | a resident process | a one-shot, on your schedule |
| Prometheus reads | `/metrics` on a loopback port, or `cubit.prom` if you also set `--textfile-dir` | `cubit.prom`, through node_exporter's textfile collector |
| A failed root | keeps its last good sizes, with `cubit_scan_success` 0 | has no sizes, only health series |
| Counters (`cubit_scans_total`, `cubit_scan_errors_total`) | yes, for the life of the process | left out (a fresh process would restart them every run) |
| Example rules | [`examples/rules/cubit.rules.yml`](examples/rules/cubit.rules.yml) | [`examples/rules/cubit-cron.rules.yml`](examples/rules/cubit-cron.rules.yml) |

For continuous disk watching prefer `serve`. Cron mode is for hosts where a daemon is not wanted. In both, Cubit's only write is `cubit.prom`.

## Run it as an exporter

On the build host, start the exporter in the background:

```console
$ cubit serve --config cubit.yaml --listen 127.0.0.1:9101 &
time=2026-11-14T12:00:00.000Z level=INFO msg=listening addr=127.0.0.1:9101 config=cubit.yaml roots=1 interval=5m0s
$ curl -s http://127.0.0.1:9101/healthz
ok
$ curl -s http://127.0.0.1:9101/metrics | head -4
# HELP cubit_build_info Build information.
# TYPE cubit_build_info gauge
cubit_build_info{version="dev",revision="f70cb66",go_version="go1.27.4"} 1
```

Point a local Prometheus at it (synthetic names; scrape from the same host or over a private network only):

```yaml
scrape_configs:
  - job_name: cubit
    scrape_interval: 1m
    static_configs:
      - targets: ['127.0.0.1:9101']
```

**The metrics endpoint is unauthenticated.** `serve` binds a loopback address by default; binding anything wider requires `--unsafe-listen`, which logs one warning — and the warning is the whole point: nothing between Prometheus and Cubit authenticates, so keep the listener private to the host or a network you control. Thresholds and severity live in Prometheus rules, not in Cubit.

## Cron mode

If the build host does not run a resident `cubit serve`, a periodic `cubit scan --textfile-dir DIR` fills the same gap. `DIR/cubit.prom` holds **the same bytes `/metrics` serves** for that scan, in node_exporter textfile format: point node_exporter at the directory (`--collector.textfile.directory=DIR`) and every scrape exposes the Cubit series as if they came from the exporter itself. The write is atomic (temp file and rename), so a scrape never catches a half-written file, and the dotted temp and lock names never end in `.prom`, so node_exporter ignores them. Cubit never creates `DIR` and refuses one on the refuse list; one Cubit writes a directory at a time.

One difference to know: **a failed root has no sizes in cron mode.** A one-shot scan has no last good values to keep, so a root whose scan failed publishes its health series only (`cubit_scan_success` 0), and the process-scoped counters are left out of the file; `serve` keeps last good values between scans. If continuous coverage of a host matters, which it does for disk watching, prefer `serve`; cron mode is for boxes where a daemon is not wanted.

## Metrics

Served by `cubit serve` at `/metrics`:

- **Sizes (gauges, bytes):** `cubit_workspace_target_bytes`, `cubit_workspace_profile_bytes`, `cubit_root_bytes`, `cubit_cache_bytes`.
- **Counts (gauges):** `cubit_workspaces`, `cubit_workspaces_unlabeled{,_bytes}`, `cubit_skipped_entries`.
- **Scan health (gauges and counters):** `cubit_scan_duration_seconds`, `cubit_scan_success`, `cubit_scan_partial`, `cubit_scan_last_success_timestamp_seconds`, `cubit_scan_errors_total`, `cubit_scans_total`.
- **Info (always 1):** `cubit_build_info`, `cubit_root_info{root,mode}`.

Full table, label enums and semantics: [docs/metrics.md](docs/metrics.md). Values are base units — bytes and seconds, no MiB, no percentages.

## Alerting

Cubit judges nothing. [`examples/rules/`](examples/rules/) has example Prometheus rules (`CubitWorkspaceGrowth`, `CubitRootGrowth`, `CubitScanFailing`, `CubitScanPartial`, `CubitSubtreeUnreadable`, `CubitDedupeOverflow`, and for cron mode `CubitTextfileStale`), unit-tested with promtool. Every one is `severity: warning`: **anything derived from Cubit is YELLOW, never RED.** Disk filling is something to look at, and Cubit is a lower bound when a scan is partial, so it is never the thing that pages.

## The shared `CARGO_TARGET_DIR` caveat

If a build host sets one shared `CARGO_TARGET_DIR`, every build lands in one directory and Cubit can report it only as a single config-named workspace — it never invents per-workspace attribution. To keep both benefits, point each project at its own directory:

```sh
CARGO_TARGET_DIR=$root/<name> cargo build
```

One directory per project, and each still reads as one workspace to Cubit.

## Security posture

- **Loopback by default.** `serve` binds `127.0.0.1:9101`. A non-loopback bind is refused unless `--unsafe-listen` is passed, which logs one WARN line with the bound address.
- **Unauthenticated.** The metrics endpoint has no auth. Treat the listener as private; do not expose it beyond the host without understanding that.
- **Allowlisted roots only.** The walk opens every root with `os.Root`, so a path escape fails at the syscall. Symlinks are never followed.
- **Refuse list.** Credential, vault and sensitive prefixes (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.kube`, …) are refused on every entry, built in and non-removable; config only adds. See [docs/refuse-list.md](docs/refuse-list.md).
- **Bounded.** Per-root caps on files and wall time; a hostile tree cannot exhaust the host. Partial results are reported as lower bounds.
- **One write, optional.** The textfile output (`cubit.prom`) is the only thing Cubit writes, and only when `--textfile-dir` is set .

## What shows up in Vigil

Cubit feeds no screens of its own. Vigil reads the `cubit_*` series through its Prometheus backend and shows a `cubit{}` block, data only, with no status and no severity: per host, each root's alias, mode, bytes, success and partial flags, its five largest workspaces and its cache areas. The mapping is frozen in [docs/vigil.md](docs/vigil.md): each field, the metric it comes from and the PromQL that reads it, the freshness inputs (`cubit_scan_last_success_timestamp_seconds` and node_exporter's `node_textfile_mtime_seconds`) and the workspace label pattern. A complete example block is [`testdata/vigil/cubit-block.json`](testdata/vigil/cubit-block.json), and tests keep the page, the fixture and the metrics in step. Cubit-derived health is YELLOW, never RED.

## Links

- [docs/metrics.md](docs/metrics.md) — the metric contract.
- [docs/cli.md](docs/cli.md) — commands, flags, exit codes.
- [docs/vigil.md](docs/vigil.md) — which metric and PromQL fills each field Vigil shows.
- [docs/config.md](docs/config.md) — the config file, every key and finding code.
- [docs/refuse-list.md](docs/refuse-list.md) — the built-in refuse list and containment.
- [docs/performance.md](docs/performance.md) — what a scan costs, and the budgets.
- [docs/versions.md](docs/versions.md) — every pinned Go release, tool and action.
- [CHANGELOG.md](CHANGELOG.md).
- [CONTRIBUTING.md](CONTRIBUTING.md) (DCO), [SECURITY.md](SECURITY.md).

## Licence

Licensed under either of the [Apache License, Version 2.0](LICENSE-APACHE) or the [MIT license](LICENSE-MIT), at your option. Unless you say otherwise, a contribution you submit is dual licensed the same way, with no additional terms.
