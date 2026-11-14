# The Cubit CLI

Command reference for the built commands: `scan`, `validate`, `version` (C1a). `serve` (C1b) and `--textfile-dir` (C1c) are planned and marked as such. Help text below is the snapshot-tested `--help` output; wording of config findings is being reworked separately (`messages.md` in the C1a pairing drafts) and will land as a golden update.

## Global flags

Cobra shows root-level persistent flags on subcommands under `Global Flags`:

| Flag | Default | Meaning |
|---|---|---|
| `--config FILE` | `cubit.yaml` | Config file, `cubit.config/v0`, strictly decoded: an unknown key is an error. Settable on the root command too (`cubit --config other.yaml scan`). |

Per-command flags live under `Flags` on each command (`--json` today; `--listen`, `--unsafe-listen` and `--textfile-dir` arrive with C1b/C1c).

## `cubit`

```
Cubit measures Cargo disk usage on build hosts and exports it as
Prometheus metrics. It measures; it never cleans.

Usage:
  cubit [command]

Available Commands:
  help        Help about any command
  scan        Scan configured roots once and print the result
  validate    Check the config and report every problem at once
  version     Print the version

Flags:
      --config string   config file (default "cubit.yaml")
  -h, --help            help for cubit
  -v, --version         version for cubit

Use "cubit [command] --help" for more information about a command.
```

`serve` joins this list in C1b.

## `cubit scan`

```
Scan every configured root once, print the result, and exit.
Human table by default; --json prints cubit.scan/v0 alone on stdout.

Usage:
  cubit scan [flags]

Flags:
  -h, --help   help for scan
      --json   print cubit.scan/v0 instead of the human table

Global Flags:
      --config string   config file (default "cubit.yaml")
```

Sample human table for `examples/scan.json`. Roots follow the document order (by alias); workspaces and caches are biggest first, ties by name. `BYTES` is exact; `SIZE` is a rounded reading aid.

```
ROOT                   MODE            BYTES        SIZE  STATE
example-cargo-home     cargo-home      9000000000   8.4G  ok
  git-db                               3000000000   2.8G
  registry-cache                       2500000000   2.3G
  git-checkouts                        2000000000   1.9G
  cargo-bin                            500000000    477M
  registry-index                       500000000    477M
  registry-src                         500000000    477M
example-projects       workspace-root  42000000000   39G  partial
  app-cli                              30000000000   28G
  libs/core                            11000000000   10G
  (1 unlabeled)                        1000000000   954M
example-shared-target  target-dir      -               -  failed
warning: example-cargo-home skipped 1 refused
warning: example-projects partial: deadline_exceeded; every size is a lower bound
warning: example-projects skipped 2 permission_denied, 14 vanished
warning: example-projects: 1 unmatched_name in names
error:   example-shared-target failed: root_missing; fix: check that the path of root example-shared-target in cubit.yaml exists
```

A partial scan's sizes are lower bounds. A failed root keeps its last-good values in `/metrics` (C1b); a one-shot `scan` has none, so it prints `-`.

## `cubit validate`

```
Check the config and report every problem at once, not just the first.

Checks include duplicate aliases, nested or overlapping roots, a root
inside a refused prefix, and two roots resolving to one real path.
It also prints each root's resolved mode. The full list is in
docs/config.md.

Usage:
  cubit validate [flags]

Flags:
  -h, --help   help for validate
      --json   print findings as JSON

Global Flags:
      --config string   config file (default "cubit.yaml")
```

## `cubit version`

```
Print the version. One stable line by default.

Usage:
  cubit version [flags]

Flags:
  -h, --help   help for version
      --json   print {"version":...,"revision":...,"go_version":...} instead

Global Flags:
      --config string   config file (default "cubit.yaml")
```

Output today:

```
$ cubit version
cubit dev (f70cb66f823c, go1.27.4)
$ cubit version --json
{"version":"dev","revision":"f70cb66f823c","go_version":"go1.27.4"}
```

## Exit codes

Only "0–2 means a scan ran, 3+ means it could not" is the public contract. Codes 0 to 2 still print the document (table or `--json`).

| Code | Meaning |
|---|---|
| 0 | ok — every root usable, no cap hit |
| 1 | partial — a scan ran but some bytes went unmeasured (a cap or an unreadable subtree); sizes are lower bounds |
| 2 | nothing usable — a scan ran but every root failed |
| 3 | usage — bad flag or arguments |
| 4 | invalid config — `validate` findings, unreadable or refused config |
| 5 | `serve` refused to start (C1b) |
| 6 | internal error — `main` recovered a panic; a crash never reads as a scan result |

## Error house style

Every finding names the file and field, shows a one-line excerpt, and ends with the fix. Real output today:

```
bad.yaml:3: error: 'Example_Projects' does not match pattern '^[a-z0-9][a-z0-9-]{0,31}$'
  | - alias: Example_Projects
  fix: use the format docs/config.md gives for this field.

cubit: cannot read config nope.yaml: no such file or directory; pass --config, or create it from examples/cubit.yaml
cubit: unknown flag: --frob
Run 'cubit --help' for usage.
```

The schema-derived wording is under rework; the target wording per finding class is in `messages.md` (C1a pairing drafts). The shape above — file, line, field, excerpt, fix — does not change.

## Global Flags: keep as is

Keep cobra's default `Global Flags` block rather than a custom template. Reasons: `--config` is defined once on the root, so `cubit --config other.yaml scan` and `cubit scan --config other.yaml` both work; one default template means no per-command template to maintain or re-snapshot; and the one wart, `--config` appearing under `version`, is harmless and stable. If it ever bothers anyone, hide it on `version` alone — do not restructure all commands for it.

## Planned (C1b, C1c)

- `cubit serve` — `/metrics` and `/healthz` on `--listen` (default `127.0.0.1:9101`); non-loopback binds require `--unsafe-listen`; metrics are unauthenticated; scans run on an interval, one in flight, and `/metrics` never walks on scrape.
- `--textfile-dir DIR` on `scan` and `serve` — writes `cubit.prom` (node_exporter textfile format) after a scan; the directory must exist and sit outside the refuse list; temp+rename in the same directory, fsync, mode 0644, single-writer `flock`.

## Finding format

`validate` and `scan` report config problems as `file:line: severity: message`, the source line as an excerpt, and a fix that says what to do. Errors come first, then warnings, then info lines. Every finding has a stable `code` (listed in [config.md](config.md#finding-codes)), shown in `validate --json`:

```
bad.yaml:5: error: roots[0].mode 'workspace-rot' is not a mode
  | mode: workspace-rot
  fix: use one of workspace-root, target-dir, cargo-home, sccache; 'workspace-root' looks closest.
bad.yaml: not ok: 1 error(s)
```

Schema problems are reported first. The cross-field checks (duplicate aliases, overlapping roots, refused roots) run once the file decodes, so a file can show a second round of findings after the first is fixed.

## Planned: `cubit serve`

`serve` is C1b and `--textfile-dir` is C1c; neither is built. The contract is unchanged: `serve` listens on `127.0.0.1:9101` by default, refuses a non-loopback `--listen` without `--unsafe-listen` (the metrics are unauthenticated), serves `/metrics` from the last finished scan and never walks on scrape, answers `/healthz` without touching the scanner, and rescans every `scan.interval`. `--textfile-dir DIR` writes `cubit.prom` after a scan, through a temp file and rename, under a single-writer lock.
