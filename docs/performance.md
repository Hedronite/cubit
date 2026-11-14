# Performance

What a scan costs, measured on a generated tree, and the budgets the tests hold. Nothing here comes from scanning a real tree; the calibration of `max_files` and `max_duration` defaults on real build hosts is a separate, later exercise, so those defaults are unchanged.

## Budgets

| Budget | Limit | Held by |
|---|---|---|
| Goroutines left running after `Scan` returns, finished or canceled, one worker or four | none | `TestScanLeavesNoGoroutinesRunning` in `internal/scan` |
| A `/metrics` scrape, served from the last finished scan, on the 10,000 file tree | p99 under 50 ms, and it never walks | `TestAScrapeIsServedFromTheSnapshotInsideTheBudget` in `internal/serve` (the tree is deleted after the first scan and every scrape still answers the same bytes) |
| Peak resident memory of one `cubit scan` of the 10,000 file tree | under 64 MiB | measured by `scripts/perf.sh`; not a CI gate, because memory on a shared runner is noisy |

The benchmarks do not gate CI either: timing on shared runners is noisy, and a regression is something to read in a diff, not a red check.

## The trees

All trees are generated, one byte per file, in a temporary directory.

- **Budget tree** (`10kfiles_1kdirs`): a workspace root of 10 workspaces; their targets hold 10,000 files in 1,000 directories, 10,010 files and 1,041 directories in all.
- **Deep** (`deep`): one workspace whose target is a chain of 60 directories, a file in each (the depth guard is 64).
- **Wide** (`wide`): one workspace whose target holds 10,000 files in one directory.

## Numbers

Machine class: an Apple M4 Max laptop-class machine, 14 cores, 36 GiB, internal SSD with APFS, macOS on arm64, Go 1.27.4. Other machines differ; the budgets have wide margins for that reason.

### `BenchmarkScan`

`go test -run '^$' -bench BenchmarkScan -benchmem -count 10 ./internal/scan`, summarised with benchstat:

| Tree | Time per scan | Allocated per scan | Allocations |
|---|---|---|---|
| Budget tree | 16.2 ms ± 1% | 6.2 MiB | 69,650 |
| Deep | 1.83 ms ± 0% | 243 KiB | 1,891 |
| Wide | 11.4 ms ± 3% | 3.3 MiB | 40,676 |

These are warm-cache numbers: the benchmark scans the same tree repeatedly, so the directories are in the page cache.

### One `cubit scan --json` of the budget tree

Five runs of the built binary (`scripts/perf.sh`):

- **Wall time:** 27 to 30 ms warm. The first run, with a cold cache, took 0.30 s.
- **Peak RSS:** 20 to 21 MiB, against a 64 MiB budget.

### One `serve` scrape

200 scrapes over HTTP in the test: median 0.18 ms, p99 0.34 ms, worst 0.44 ms. 50 scrapes with curl against a running `serve`: median 0.20 ms, worst 0.36 ms. A scrape copies the last finished scan's rendered bytes; the cost is the HTTP round trip. Against the 50 ms budget there are two orders of magnitude of margin.

### Binary size

Release binaries are stripped (`-s -w`) and built with `CGO_ENABLED=0`. Archive sizes are `.tar.gz` with the licences and the README.

| Target | Binary | Archive |
|---|---|---|
| `darwin/arm64` | 10.7 MB | 4.3 MB |
| `darwin/amd64` | 11.5 MB | 4.7 MB |
| `linux/arm64` | 10.4 MB | 4.2 MB |
| `linux/amd64` | 11.3 MB | 4.6 MB |

## What was changed because of it

Nothing. No measurement came near a budget, and the budgets are not a reason to tune: the walk is bounded by `max_files` and `max_duration` on every root, and a real build host's cost will be set by its cache state and disk, which a generated tree does not reproduce.

## Reproduce

```sh
go test -run '^$' -bench BenchmarkScan -benchmem -count 10 ./internal/scan > bench.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e bench.txt
go test -run 'Goroutines|Snapshot' -v ./internal/scan ./internal/serve
scripts/perf.sh
```

The pinned benchstat is a development tool only: it is not a module dependency and CI does not run it.
