# Configuration

Cubit reads one YAML file, `cubit.config/v0`. It is decoded strictly: an unknown key is an error, and there are no environment overrides (nothing here is a secret). The shape is fixed by [`schemas/cubit.config.v0.schema.json`](../schemas/cubit.config.v0.schema.json); [`examples/cubit.yaml`](../examples/cubit.yaml) is a complete synthetic example. The rest of this page covers what a schema cannot say.

## Top level

| Key | Default | Meaning |
|---|---|---|
| `schema` | required | Must be `cubit.config/v0`. |
| `host` | none | A slug for the `host` field of the scan document. Never read from the OS. There is no `host` label. |
| `scan.interval` | `5m` | Time between scans in `serve`, with 10% jitter either way. At least `30s`. |
| `scan.max_workers` | min(4, GOMAXPROCS) | Concurrent walkers. IO bound, so more is rarely better. |
| `refuse.vault` | none | A vault prefix to refuse, reported by alias only. |
| `refuse.extra` | none | More refused prefixes. They only add to [the built-in list](refuse-list.md). |
| `roots` | required | One to 32 roots. |

## Roots

Every root has an `alias` (the `root` label, a slug), a `path` (absolute, or `~/…`) and a `mode`. All roots take `max_files` (default 2,000,000) and `max_duration` (default `30s`); both defaults are provisional and are calibrated against a real tree after C1a. `max_files` counts directory entries read: each entry once, so a stat of it is not a second count. `max_duration` appears in the scan document as `max_duration_seconds`. The depth guard (64) and the worker limit are process-wide; the depth guard is fixed.

**Durations** use integer parts with `ms`, `s`, `m` or `h` (`30s`, `5m`, `1h30m`). Fractions (`1.5h`) and `us` or `ns` are rejected, which is stricter than Go's `time.ParseDuration`, so the decoder checks the grammar itself.

| Mode | Treats the root as | Extra keys |
|---|---|---|
| `workspace-root` | a directory whose children are workspaces: a directory holding both `Cargo.toml` and `target/` | `discovery_depth` (default 3, at most 6), `max_workspaces` (default 64, at most 256), `names`, `profiles` |
| `target-dir` | one target dir (a shared `CARGO_TARGET_DIR`, or a workspace's own), reported as a single workspace | `name` (default: the alias), `profiles` |
| `cargo-home` | `CARGO_HOME`: registry index, cache and source, git db and checkouts, `bin` | none |
| `sccache` | an sccache directory | none |

Discovery never descends into a discovered `target/`, `.git` or `node_modules`. A workspace member with no `target/` of its own is not a workspace; the one that has it is.

A shared `CARGO_TARGET_DIR` leaves no per-workspace directory to label. Cubit reports it as one workspace named in config and never invents attribution. To keep both the shared cache and the attribution, give each workspace its own `CARGO_TARGET_DIR=$root/<name>` and point a `workspace-root` at `$root`.

## Workspace labels

The `workspace` label is, in order:

1. the slug declared for it in `names`, keyed by its relative path under the root as it is on disk;
2. otherwise the relative path under the root, lowercased, with every character outside `[a-z0-9._-]` (other than the `/` between components) replaced by `_`, at most 64 bytes;
3. if two paths fold to the same string, each later one gets `_<first 8 hex of its sha256>` appended, so the result is deterministic.

`names` keys are matched against the relative path exactly as it is on disk (no case folding, no Unicode normalization). A key may not hold `.` or `..` components, a leading or trailing `/`, a backslash or NUL. A key that matches no workspace is not an error, since the tree can change: the scan reports it as an `unmatched_name` warning.

When a root has more workspaces than `max_workspaces`, the largest by bytes are labelled, ties broken by name. The rest are counted in `cubit_workspaces_unlabeled` and `cubit_workspaces_unlabeled_bytes`. A workspace can move in or out of the labelled set as sizes change. There is no `other` workspace. A target dir reached twice is counted once, for the first root in alias order, and is settled before this selection.

## What `cubit validate` checks

Beyond the schema, and all at once rather than stopping at the first error, each naming the root alias and line:

- duplicate aliases;
- nested or overlapping roots;
- a root, or a discovery root, inside a refused prefix (an error);
- a refused prefix inside a root (an info line, since it is legal);
- two roots that resolve to the same real path;
- two `names` entries that declare the same slug, or a declared slug equal to a derived label;
- `scan.interval` under 30s, or a root's `max_duration` at or above it;
- a `workspace-root` whose top level looks like a target dir (`debug/` or `release/` present): a warning that suggests `mode: target-dir`. `mode` is always required; Cubit never guesses it. A scan that finds no workspaces under a `workspace-root` reports a `no_workspaces` warning for the same reason.

`validate` also prints each root's resolved mode, so the operator sees what Cubit will do.

## Finding codes

Every finding `validate` or `scan` reports carries a stable `code`, shown in `cubit validate --json` with the `severity`, `file`, `line`, `field`, `message`, `excerpt` and `fix`. A finding's wording may improve; its code does not change, and a new code is a contract change. Errors are listed first, then warnings, then info, each by line. A message names the field and a refused location only by its class (`builtin` or `config`), never by path.

| Code | Severity | Meaning |
|---|---|---|
| `yaml_syntax` | error | The file is not valid YAML. |
| `file_shape` | error | Empty, over 1 MiB, more than one document, an anchor or alias, or a non-string key. |
| `bad_schema` | error | `schema` is not `cubit.config/v0`. |
| `missing_key` | error | A required key is absent (`schema`, `roots`, a root's `alias`, `path` or `mode`). |
| `unknown_key` | error | A key that belongs nowhere here. The fix names the nearest real key, or where the key lives. |
| `key_for_mode` | error | A real key, but for another root mode (`discovery_depth` on a `target-dir`). |
| `slug_pattern` | error | An alias, `host` or slug outside the grammar. The fix suggests one. |
| `path_relative` | error | A path that is not absolute or `~/…`. |
| `bad_duration` | error | A duration outside the grammar. |
| `bad_enum` | error | `mode` is not one of the four. The fix names the closest. |
| `out_of_range` | error | A number or list outside its limits. |
| `names_key` | error | A `names` key that is not a usable relative path. |
| `wrong_type` | error | A value of the wrong type. |
| `invalid_value` | error | Anything else the schema rejects. |
| `duplicate_alias` | error | Two roots share an alias. |
| `overlapping_roots` | error | One root lies inside another. |
| `same_real_path` | error | Two roots resolve to one directory. |
| `refused_root` | error | A root, or the place it resolves to, is inside a refused prefix. |
| `path_invalid` | error | A path with a `.` or `..` part, or `~/` with no home. |
| `not_a_directory` | error | A root that is a file. |
| `interval_floor` | error | `scan.interval` under 30s. |
| `duration_ge_interval` | error | A root's `max_duration` at or above `scan.interval`. |
| `duplicate_slug` | error | Two `names` entries declare one slug, or a declared slug equals a derived label. |
| `home_unknown` | error | A `~/` refuse entry and no home directory. |
| `root_missing` | warning | A root's path does not exist now. A scan reports `root_missing`. |
| `root_unreadable` | warning | A root's path cannot be resolved now. |
| `root_is_workspace` | warning | A `workspace-root` that is itself a Cargo workspace (holds `Cargo.toml` and `target/`): it measures the workspaces below it, so it finds none. Point a `target-dir` root at its `target/`. |
| `looks_like_target` | warning | A `workspace-root` holding `debug/` or `release/` at the top. |
| `refused_inside` | info | A refused prefix lies inside a root. Legal: it is skipped and counted. |
| `mode_echo` | info | The root's resolved mode, one line per root. |

## Measuring notes

- **Hardlinks** are counted once per workspace (or cache area), not across them. A file hardlinked into two workspaces counts in both, so a root can total more than `du -s` of the same directories. The reason is determinism: one unit is walked by one goroutine in name order, so which workspace owns a link never depends on timing. A unit's hardlink set holds about a million entries; beyond that, later hardlinks are counted and reported as `dedupe_overflow`.
- **A partial scan is not reproducible.** When a cap trips, which workspaces finished first depends on scheduling, so two partial scans of one tree can differ. Only a complete scan is deterministic. The numbers of a partial scan are lower bounds, nothing more.
- **`max_files` bounds entries read, not syscalls.** `max_duration` is the bound on wall time and so on syscalls. Workspace discovery and the measurement of the workspaces it finds share one `max_files` budget, so a `workspace-root` measures with what discovery left. `max_duration` is checked between entries: a hung filesystem can hold one directory read past it, so the cap bounds responsiveness, not a stalled mount. Alert on `cubit_scan_duration_seconds` for that.
- **`workspaces_found` counts the workspaces that were measured.** One that was found but could not be measured (removed, swapped, refused) or that duplicates an earlier target is in `skipped[]`, not in the count.
- **A symlinked `target/`** is followed through every hop that stays inside its root, with the refuse list checked on each hop (up to 40 hops). One that leaves the root, is absolute, loops, or lands on a refused path is skipped as `symlink_escape`, `symlink_loop` or `refused`. To measure a shared target that workspaces link to from outside their root, give it its own `target-dir` root.
- **A symlinked cache area** under `cargo-home` (for example `registry/cache` linked to another disk) is not followed. It reads zero and counts as `symlink_escape`. Point a `target-dir`-style root at the real location instead.
- **Names case.** The refuse list folds case with Unicode lowercasing and does not normalize Unicode; an APFS volume that matches names by normalization could still show a spelling the list does not.
