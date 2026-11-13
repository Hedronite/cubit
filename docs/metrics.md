# Metrics

Everything Cubit exports, from `cubit serve` at `/metrics` and from `--textfile-dir` as `cubit.prom`. Values are base units: bytes and seconds. There are no percentages, no MiB, and no severity.

Every value is rendered from the same scan result that `cubit scan --json` prints (see [`schemas/cubit.scan.v0.schema.json`](../schemas/cubit.scan.v0.schema.json)), so a person, Prometheus and a script read the same numbers. The table below is parsed by the contract tests; keep the first column in backticks.

| Metric | Type | Labels | Emitted for | Meaning |
|---|---|---|---|---|
| `cubit_workspace_target_bytes` | gauge | `root`, `workspace` | `workspace-root`, `target-dir` | Allocated bytes under one workspace's `target/`. |
| `cubit_workspace_profile_bytes` | gauge | `root`, `workspace`, `profile` | `workspace-root`, `target-dir` | The same bytes split by profile. The five profiles sum to the workspace value. Omitted when the root sets `profiles: false`. |
| `cubit_root_bytes` | gauge | `root` | every mode | All measured bytes in the root. Workspace modes: the labelled workspaces plus `cubit_workspaces_unlabeled_bytes`. Cache modes: the sum of `cubit_cache_bytes`. Source trees and skipped subtrees are counted nowhere. |
| `cubit_cache_bytes` | gauge | `root`, `kind` | `cargo-home`, `sccache` | Allocated bytes of one fixed cache area. |
| `cubit_workspaces` | gauge | `root` | `workspace-root`, `target-dir` | Workspaces found, labelled or not. |
| `cubit_workspaces_unlabeled` | gauge | `root` | `workspace-root`, `target-dir` | Workspaces beyond `max_workspaces` that got no label. |
| `cubit_workspaces_unlabeled_bytes` | gauge | `root` | `workspace-root`, `target-dir` | Their bytes together. There is no `other` workspace. |
| `cubit_skipped_entries` | gauge | `root`, `reason` | every mode | Entries or subtrees skipped in the last scan. It is a gauge, not a `_total` counter, because it resets every scan. |
| `cubit_scan_duration_seconds` | gauge | `root` | every mode | Wall time of the last scan of this root. |
| `cubit_scan_success` | gauge | `root` | every mode | 1 when the last scan gave a usable result, complete or partial. 0 when it failed and the size series hold the last good values. |
| `cubit_scan_partial` | gauge | `root` | every mode | 1 when some bytes were not measured (a cap cut the scan short, or a subtree was unreadable). Read every size as a lower bound. Never 1 when `cubit_scan_success` is 0. |
| `cubit_scan_last_success_timestamp_seconds` | gauge | `root` | every mode | Unix time of the last scan with `cubit_scan_success` = 1. A stale value with `cubit_scan_success` = 0 is a root that has stopped measuring. |
| `cubit_scan_errors_total` | counter | `root`, `reason` | every mode | Scans of this root that failed or were partial, by cause. A scan increments every reason that applied. Accumulates for the life of the process. |
| `cubit_scans_total` | counter | `result` | process | Scan rounds finished, by result. |
| `cubit_build_info` | gauge | `version`, `revision`, `go_version` | process | Always 1. |

## Label values

These are closed enums, and the schemas use the same values. A new value is a contract change.

- `profile`: `debug`, `release`, `doc`, `incremental`, `other`
- `kind`: `registry-cache`, `registry-src`, `registry-index`, `git-db`, `git-checkouts`, `cargo-bin`, `sccache`
- `skip_reason`: `refused`, `symlink_escape`, `symlink_loop`, `permission_denied`, `foreign_device`, `duplicate_target`, `vanished`, `depth_exceeded`, `dedupe_overflow`, `io_error`
- `error_reason`: `root_missing`, `root_unreadable`, `deadline_exceeded`, `max_files_exceeded`, `subtree_unreadable`
- `warning_reason`: `no_workspaces`, `unmatched_name`
- `result`: `ok`, `partial`, `failed`

`cubit_skipped_entries` takes a `skip_reason`. `cubit_scan_errors_total` takes an `error_reason`. `warning_reason` appears only in the scan document.

Only `root` and `workspace` carry text from the operator. `root` is a config alias. `workspace` is a declared name or the sanitized relative path under the root, at most 64 bytes, capped at `max_workspaces` per root; it is never an absolute path. There is no `host` label (Prometheus already sets `instance`), and no label ever holds an absolute filesystem path.

## What each value means

- **Allocated, not apparent.** Sizes are `st_blocks * 512`, the figure `du` reports. APFS clones make it approximate. `cubit_root_bytes` is not comparable to `du -s <root>`: it counts only the measured parts (targets and caches), not source trees and not skipped subtrees.
- **Profiles.** A path component equal to `incremental` puts the bytes under `incremental`. The first component after `target/` otherwise names the profile if it is `debug`, `release` or `doc`. Cross-compile `<triple>/` directories, `tmp` and `package` land in `other`.
- **Duplicate targets.** If two workspaces, or two roots, reach one target dir, the first root in alias order owns it. Later ones skip it as `duplicate_target`. This is settled before the largest-first `max_workspaces` selection, so a duplicate never takes a label slot.
- **Skipped.** Skips never add to any byte total.
  - `refused`: a path on the [refuse list](refuse-list.md).
  - `symlink_escape`, `symlink_loop`: Cubit resolves a symlink only for a configured root or a discovered `target/`. One that escapes the root, or loops, is skipped. Symlinks inside a tree are measured as link bytes and never classified.
  - `permission_denied`, `io_error`: the process could not read it.
  - `foreign_device`: a directory on another device.
  - `duplicate_target`: see above.
  - `vanished`: removed while the walk ran, which is normal in a live build tree.
  - `depth_exceeded`: below the fixed depth guard of 64.
  - `dedupe_overflow`: the hardlink set filled, so later hardlinks may be counted twice.
- **Partial.** `permission_denied`, `io_error` and `depth_exceeded` skips leave bytes unmeasured, so they make the root partial, as does a cap hit (`deadline_exceeded`, `max_files_exceeded`). Other skips do not.
- **First success.** A root's size series (`cubit_workspace_*`, `cubit_root_bytes`, `cubit_cache_bytes`, the workspace counts) appear only after its first successful scan. Before that only the health series exist, so a never-measured root cannot read as zero bytes. A series disappears afterwards only when its workspace does.
- **Failed and partial.** A *failed* root keeps its last good values, with `cubit_scan_success` 0. A *partial* root publishes what it measured, with `cubit_scan_partial` 1. A partial scan never falls back to last-good, because a stale number is worse than a lower bound.
- **Exit codes.** `cubit scan` exits 0 when every root is ok and complete, 2 when no root produced a usable result, and 1 in between (some root partial or failed). Codes 0 to 2 print the document on stdout; codes 3 to 6 print nothing there.
- **`result`.** `ok` is every root complete, `partial` is the in-between, `failed` is nothing usable.
- **Never walks on scrape.** `/metrics` serves the last finished scan.

## Alerting

Cubit judges nothing. Thresholds live in Prometheus rules, and anything built on these metrics is YELLOW, never RED. See [`examples/rules/cubit.rules.yml`](../examples/rules/cubit.rules.yml). Growth alerts stay quiet for 24 hours after any partial scan, because a lower bound followed by a full scan looks like growth.
