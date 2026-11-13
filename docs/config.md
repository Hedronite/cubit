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
- a textfile directory whose temp file would land on another filesystem;
- a `workspace-root` whose top level looks like a target dir (`debug/` or `release/` present): a warning that suggests `mode: target-dir`. `mode` is always required; Cubit never guesses it. A scan that finds no workspaces under a `workspace-root` reports a `no_workspaces` warning for the same reason.

`validate` also prints each root's resolved mode, so the operator sees what Cubit will do.
