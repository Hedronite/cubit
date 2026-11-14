# The Cubit CLI

Command reference for the built commands: `scan`, `validate`, `version`, `serve`. `--textfile-dir` writes the textfile. Help text below is the snapshot-tested `--help` output. Config-finding wording follows the rework agreed at C1a (stable `code` per class); shape — file, line, field, excerpt, fix — is unchanged.

## Global flags

Cobra shows root-level persistent flags on subcommands under `Global Flags`:

| Flag | Default | Meaning |
|---|---|---|
| `--config FILE` | `cubit.yaml` | Config file, `cubit.config/v0`, strictly decoded: an unknown key is an error. Settable on the root command too (`cubit --config other.yaml scan`). |

Per-command flags live under `Flags` on each command.

## `cubit`

```
Cubit measures Cargo disk usage on build hosts and exports it as
Prometheus metrics. It measures; it never cleans.

Usage:
  cubit [command]

Available Commands:
  help        Help about any command
  scan        Scan configured roots once and print the result
  serve       Run the exporter and serve /metrics
  validate    Check the config and report every problem at once
  version     Print the version

Flags:
      --config string   config file (default "cubit.yaml")
  -h, --help            help for cubit
  -v, --version         version for cubit

Use "cubit [command] --help" for more information about a command.
```

## `cubit scan`

```
Scan every configured root once, print the result, and exit.
Human table by default; --json prints cubit.scan/v0 alone on stdout.
With --textfile-dir DIR, also write cubit.prom (node_exporter
textfile format) after the scan. The directory must already exist
and sit outside the refuse list; Cubit never creates it.

Usage:
  cubit scan [flags]

Flags:
  -h, --help               help for scan
      --json               print cubit.scan/v0 instead of the human table
      --textfile-dir DIR   write cubit.prom into DIR after the scan (node_exporter textfile format)

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

A partial scan's sizes are lower bounds. A failed root keeps its last-good values in `/metrics`; a one-shot `scan` has none, so it prints `-`.

## `cubit serve`

```
Run the exporter: serve /metrics and /healthz on --listen.

/metrics serves the last finished scan and never walks on scrape;
before the first scan finishes it serves build info and zero counters
only. /healthz is 200 while the config is valid and the process is
serving; it never touches the scanner. Scans run on an interval
(default 5m), one in flight at a time.

With --textfile-dir DIR, the same bytes /metrics serves are also
written to cubit.prom in DIR after every scan.

The metrics endpoint is unauthenticated. The bind address must be a
loopback address; anything wider requires --unsafe-listen, which logs
one warning with the bound address.

Usage:
  cubit serve [flags]

Flags:
  -h, --help               help for serve
      --listen ADDR        listen on ADDR (default "127.0.0.1:9101")
      --log-level LEVEL    log LEVEL: debug, info, warn or error (default "info")
      --textfile-dir DIR   rewrite cubit.prom in DIR after every scan (node_exporter textfile format)
      --unsafe-listen      allow a non-loopback bind (the metrics endpoint is unauthenticated)

Global Flags:
      --config string   config file (default "cubit.yaml")
```

Endpoints: `GET /metrics` (`text/plain; version=0.0.4; charset=utf-8`), `GET /healthz` (200 `ok`), HEAD allowed, other methods 405, other paths 404. The listener comes up before the first scan, so a bad bind fails fast. Logs are `slog` text on stderr; the line inventory is in `docs/logging` wording (frontend draft `logs.md`, C1b pairing).

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

### `validate --json`

Prints document `cubit.validate/v0` (schema in `schemas/cubit.validate.v0.schema.json`), in **both** exit cases — 0 (no errors) and 4 (errors found):

```json
{
  "apiVersion": "cubit.validate/v0",
  "ok": false,
  "findings": [
    {
      "code": "slug_pattern",
      "severity": "error",
      "file": "cubit.yaml",
      "line": 4,
      "field": "roots[0].alias",
      "message": "roots[0].alias 'Example_Projects': use lowercase letters, digits and '-', start with a letter or digit, at most 32 characters",
      "excerpt": "- alias: Example_Projects",
      "fix": "rename it to 'example-projects'."
    }
  ]
}
```

`findings[]` is sorted by line within severity (errors first, then warnings, then infos), matching the human output. `code` and `severity` come from the closed sets in the schema; `excerpt` is one line, truncated with an ellipsis marker when longer than ~72 characters.

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

Output:

```
$ cubit version
cubit dev (f70cb66f823c, go1.27.4)
$ cubit version --json
{"version":"dev","revision":"f70cb66f823c","go_version":"go1.27.4"}
```

## The textfile: `--textfile-dir DIR`

After each scan, write `cubit.prom` into `DIR` (node_exporter textfile format), the same bytes `/metrics` serves. `DIR` must already exist, be a directory, be writable, and sit outside the refuse list; Cubit never creates it. The write is atomic: a temp file in the same directory, `fsync`, a rename over `cubit.prom`, an `fsync` of the directory, mode 0644 whatever the umask. A reader, `node_exporter` or anything else, sees the old file or the new one, never a partial one, because of the rename. Cubit does not claim more than that about a crash: `fsync` is not a durability guarantee on every platform (macOS needs `F_FULLFSYNC`), and the file is regenerated every scan, so a crash leaves the previous file or an incomplete one that the next scan replaces. The temp and lock files are dotted and do not end in `.prom`, so node_exporter ignores them; a temp file left by a crash is removed at the next write.

One writer per directory: Cubit takes a non-blocking `flock` on `.cubit.prom.lock` and holds it for the life of the process (`serve`) or the span of one run (`scan`). A second Cubit on the same `DIR` fails its preflight at once; a crashed holder releases the lock, so it never blocks a restart. The lock file (mode 0600, so only its owner can take it) stays in the directory between runs; a leftover `.cubit.prom.lock` is normal and is not evidence of a crash. The lock needs a filesystem that supports `flock`; on one that does not, Cubit refuses to start rather than write without it.

- `DIR` must not be writable by everyone (anyone could replace `cubit.prom` with false numbers: preflight fails), an existing `cubit.prom` must be a file owned by the user running cubit, and a group-writable `DIR` opens with a warning. `DIR` is opened once and pinned: if it is replaced or moved while `serve` runs, `serve` logs one warning and keeps writing to the original directory, which node_exporter no longer reads, so restart it to follow.
- A preflight problem is exit 4 on `scan` (nothing is scanned) and exit 5 on `serve` (the listener never opens).
- A write that fails after a `scan` prints the document anyway, says so on stderr first, and exits 7. On `serve` it logs one Error line on the first failure (the errno, never the directory) and one Info line on recovery, and keeps serving; every scan retries the write from scratch.
- In cron mode a failed root has no last good values to keep, so it publishes health series only: its size series are gone from the file, not stale. `CubitScanFailing` covers the gap. `serve` keeps last good between scans. A cron-mode file also leaves out `cubit_scans_total` and `cubit_scan_errors_total`: counters are process-scoped, and a file written by a fresh process would restart them at 0 or 1 on every run, which Prometheus reads as a reset on every scrape. `serve`'s file is byte for byte what `/metrics` serves, counters included.

### Cron mode

```
*/5 * * * * cubituser  cubit scan --config /etc/cubit/cubit.yaml --textfile-dir /var/lib/node_exporter/text --json >/dev/null 2>>/var/log/cubit-scan.log
```

```
node_exporter --collector.textfile.directory=/var/lib/node_exporter/text
```

Exit codes still apply: 4 (preflight) and 7 (write failure) land in the stderr log; 0 to 2 are normal scan outcomes. The example growth rules in `examples/rules/cubit.rules.yml` use the error counters, so they are written for `serve`. For cron mode use `examples/rules/cubit-cron.rules.yml`: growth held on `max_over_time(cubit_scan_partial[24h]) == 0`, a cap told from an unreadable subtree by the skip counts, and a staleness alert on node_exporter's `node_textfile_mtime_seconds` for `cubit.prom`, since a dead cron leaves a fresh-looking last file.

## Things `serve` does not do

- **No config reload.** A scan is a function of the config as it was read at start; changing a root needs a restart. There is no `SIGHUP` handler.
- **`/healthz` is liveness only.** It answers 200 while the process serves, whatever the scans are doing, and it never reads the scanner or the collector. That is deliberate: a supervisor that restarts on it would turn a full disk into a restart loop that never finishes a scan. Staleness is `cubit_scan_last_success_timestamp_seconds`, and a failing root is `cubit_scan_success`.
- **No authentication and no TLS.** Bind a loopback address, or keep the listener on a private network.

## Exit codes

Only "0–2 means a scan ran, 3+ means it could not" is the public contract. Codes 0 to 2 still print the document (table or `--json`).

| Code | Meaning |
|---|---|
| 0 | ok — every root usable, no cap hit |
| 1 | partial — a scan ran but some bytes went unmeasured (a cap or an unreadable subtree); sizes are lower bounds |
| 2 | nothing usable — a scan ran but every root failed |
| 3 | usage — bad flag or arguments |
| 4 | invalid config: `validate` findings, an unreadable or refused config, and (for `scan`) a `--textfile-dir` preflight failure (a missing, refused, world-writable or locked directory, or a `cubit.prom` that is not ours); nothing was scanned |
| 5 | `serve` refused to start: a non-loopback bind without `--unsafe-listen`, a listen that failed, or a `--textfile-dir` preflight failure |
| 6 | internal error: `main` recovered a panic; a crash never reads as a scan result |
| 7 | `scan --textfile-dir` could not write `cubit.prom` after the scan; the document is still printed on stdout and the failure is one line on stderr |
| 130 | `scan` interrupted by SIGINT: nothing on stdout, stderr says `interrupted` |
| 143 | `scan` interrupted by SIGTERM: the same |

`serve` stopping on SIGINT or SIGTERM is a clean shutdown and exits 0. 3 and up mean no usable result, so a stopped `scan` is 130 or 143, not 6, which is reserved for an internal error.

Every finding carries a stable `code`; they are listed in [config.md](config.md#finding-codes).

## Error house style

Every finding names the file and field, shows a one-line excerpt, and ends with the fix. Exit-5 and interrupt messages are single stderr lines prefixed `cubit:`:

```
cubit: cannot read config nope.yaml: no such file or directory; pass --config, or create it from examples/cubit.yaml
cubit: --listen :9101 has no host and would bind every interface; pass --unsafe-listen to allow that (the metrics are unauthenticated), or use a loopback address such as 127.0.0.1:9101
cubit: unknown flag: --frob
Run 'cubit --help' for usage.
```

