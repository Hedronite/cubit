# The Cubit CLI

The command reference as contracted so far. No command exists yet. The help text below is the proposed `--help` output, and it will be snapshot-tested once the commands are built.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--config FILE` | `cubit.yaml` | Config file, `cubit.config/v0`, decoded strictly: an unknown key is an error. |
| `--json` | off | Print the document alone on stdout. The human rendering is the default. Errors go to stderr either way. |
| `--listen ADDR` | `127.0.0.1:9101` | `serve` only. Loopback by default. |
| `--unsafe-listen` | off | `serve` only. Allows a non-loopback `--listen`. The metrics are unauthenticated; one warning with the bound address is logged. |
| `--textfile-dir DIR` | off | `scan` and `serve`. Write `cubit.prom` (node_exporter textfile format). The directory must already exist and sit outside the refuse list. Written to a temp file in the same directory, fsynced, renamed, mode 0644, under a single-writer lock. |

## `cubit`

```
Cubit measures Cargo disk usage on build hosts and exports it as
Prometheus metrics. It measures; it never cleans.

Usage:
  cubit [command]

Available Commands:
  scan        Scan configured roots once and print the result
  serve       Run the exporter and serve /metrics
  validate    Check the config and report every problem at once
  version     Print the version
  help        Help about any command

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

Usage:
  cubit scan [flags]

Flags:
      --config string      config file (default "cubit.yaml")
      --json               print cubit.scan/v0 instead of the human table
      --textfile-dir DIR   also write cubit.prom into DIR
  -h, --help               help for scan
```

A sample human table. Workspaces and caches are listed biggest first, ties by name. Sizes are bytes, exactly as in `--json`. A root with no usable result shows `-` and says why below. A one-shot scan has no earlier scan to fall back on, so it has no last-good values; `serve` keeps them.

```
ROOT                    MODE            BYTES        STATE
example-projects        workspace-root  42000000000  partial
  app-cli                               30000000000
  libs/core                             11000000000
  (1 unlabeled)                         1000000000
example-cargo-home      cargo-home      9000000000   ok
  git-db                                3000000000
  registry-cache                        2500000000
  git-checkouts                         2000000000
  cargo-bin                             500000000
  registry-index                        500000000
  registry-src                          500000000
example-shared-target   target-dir      -            failed
warning: example-projects partial: deadline_exceeded; every size is a lower bound
warning: example-projects skipped 2 permission_denied, 14 vanished
warning: example-projects: 1 names entry matched no workspace
error:   example-shared-target failed: root_missing; check the path in cubit.yaml
```

## `cubit serve`

```
Run the exporter: serve /metrics and /healthz on --listen.

/metrics serves the last finished scan and never walks on scrape.
Scans run on an interval (default 5m), one in flight at a time.
/healthz is 200 when the config is valid and the process is serving;
it never touches the scanner. The metrics are unauthenticated; a
non-loopback bind requires --unsafe-listen.

Usage:
  cubit serve [flags]

Flags:
      --config string      config file (default "cubit.yaml")
      --listen ADDR        listen address (default "127.0.0.1:9101")
      --unsafe-listen      allow a non-loopback bind (metrics are unauthenticated)
      --textfile-dir DIR   write cubit.prom after every scan
  -h, --help               help for serve
```

## `cubit validate`

```
Check the config and report every problem at once, not just the first.

Checks include duplicate aliases, nested or overlapping roots, a root
inside a refused prefix, two roots resolving to one real path, and a
textfile directory whose temp file would land on another filesystem.
It also prints each root's resolved mode. The full list is in
docs/config.md.

Usage:
  cubit validate [flags]

Flags:
      --config string   config file (default "cubit.yaml")
      --json            print findings as JSON
  -h, --help            help for validate
```

### Error house style

Every error names the file and field, shows a one-line excerpt, and ends with the fix:

```
cubit.yaml: roots[2].alias duplicates roots[0].alias
  roots[0]: alias: example-projects
  roots[2]: alias: example-projects
  fix: give each root a distinct alias slug.

cubit.yaml: roots[3].path sits inside the built-in refuse list (~/.ssh)
  roots[3]: path: ~/.ssh/agent-workspaces
  fix: move the root outside a refused prefix; the walk will not read ~/.ssh.
```

## `cubit version`

```
Print the version. One stable line by default.

Usage:
  cubit version [flags]

Flags:
      --json    print {"version":...,"revision":...,"go_version":...} instead
  -h, --help    help for version
```

## Exit codes

Only "0 to 2 means a scan ran, 3 and up means it could not" is the public contract.

| Code | Meaning |
|---|---|
| 0 | ok: every root usable and complete |
| 1 | partial: a scan ran, but some bytes were not measured (a cap, or an unreadable subtree), or one root failed while another gave a result. Sizes are lower bounds. |
| 2 | nothing usable: a scan ran but no root gave a result. The document is still printed, because it is the diagnostic. |
| 3 | usage: bad flag or arguments |
| 4 | invalid config: `validate` failures, a refused root, an unreadable config file |
| 5 | `serve` refused to start: bad bind or config |
| 6 | internal error: `main` recovered a panic, so a crash never reads as a scan result |

Codes 0 to 2 print the document on stdout. Codes 3 to 6 print nothing on stdout; the error is on stderr.
