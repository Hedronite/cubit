# Changelog

All notable changes to Cubit. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/). Before 1.0 the three document contracts (`cubit.config/v0`, `cubit.scan/v0`, `cubit.validate/v0`) and the metric names change only by addition.

## v0.1.1

A consumer that validates documents against a pinned copy of the schemas must refresh it before taking this release: the scan document has new fields.

### Added

- The scan document carries `observed`, `valid_for` (`cubit scan --valid-for SECONDS`, default 1800; `serve` uses twice `scan.interval`) and `declaration`. The config takes an optional `declaration: sha256:<64 hex>` or `null`.

### Fixed

- A refuse entry spelled through a symlink (`/tmp` on macOS, a linked home directory, a linked parent) did not match the real path the walk reads, so the refused subtree was measured. Entries are now matched both as written and resolved. To learn where an entry leads, Cubit reads the links along it when the config is loaded; it still never opens a refused path.
- A cancelled scan was noticed only every 256 entries, so a tree of small directories ran to the end. Cancel is now checked before every entry and directory.

### Changed

- Because refuse entries are now resolved, a root that sits where a refused symlink leads is refused where it was accepted before; `cubit validate` says so. `serve` resolves links once, at startup: restart it after re-pointing one.

## v0.1.0

The first release. Cubit measures Cargo disk use on build hosts and exports it as Prometheus metrics. It measures; it never cleans.

### Added

- `cubit scan`: walk the configured roots once and print a table, or the `cubit.scan/v0` document with `--json`. Exit 0 when every root is complete, 1 when some are partial or failed, 2 when none gave a usable result, 3 and up when it could not run.
- `cubit validate`: check a config against `cubit.config/v0` and say what is wrong with the file, line and fix. `--json` prints `cubit.validate/v0`.
- `cubit serve`: scan on an interval and serve the last finished scan at `/metrics`, with a liveness-only `/healthz`. It binds loopback unless `--unsafe-listen` is given, never walks on a scrape, and keeps a failed root's last good values.
- `--textfile-dir`: write `cubit.prom` atomically for node_exporter's textfile collector, from `serve` or a one-shot `scan` (gauges only).
- Four root modes: `workspace-root`, `target-dir`, `cargo-home` and `sccache`, with per-workspace and per-profile byte gauges, hardlink de-duplication and caps on files and wall time. A capped or partly unreadable scan is reported as partial, a lower bound.
- Containment: every root is opened with `os.Root`, symlinks are never followed, and a built-in refuse list of 41 credential and vault locations (plus the operator's own) is checked before any path is opened.
- The metric set in `docs/metrics.md`, including `cubit_root_info{root,mode}`, and example Prometheus rules for `serve` and for cron mode (warning severity only).
- `docs/vigil.md`: which metric and PromQL fills each field Vigil shows, with a fixture and tests.
- JSON Schemas for the three documents under `schemas/`. Every document carries its version in `apiVersion`.
- Defaults checked against a real tree: `max_files` 2,000,000 and `max_duration` 30s hold with wide headroom (see `docs/performance.md`).
- Release tooling: `scripts/release.sh` builds deterministic archives for darwin and linux on amd64 and arm64, with a `SHA256SUMS` file; the release workflow adds a CycloneDX SBOM per binary and GitHub build-provenance attestations.

### Security

- No authentication on `/metrics`; keep the listener private. See [SECURITY.md](SECURITY.md).
