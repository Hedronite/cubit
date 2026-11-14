# Reading Cubit from Vigil

Vigil shows a `cubit{}` block: data about disk use on the build hosts, with no status and no severity. This page says which Cubit metric fills each field and with which PromQL, so the reader and the exporter stay in step. Nothing here is code Cubit runs: Cubit writes the metrics (see [metrics.md](metrics.md)) and Vigil asks for them.

## Transport

Prometheus, and only Prometheus. Each build host runs Cubit in one of two ways:

- `cubit serve --textfile-dir DIR`, which keeps `cubit.prom` current, or
- `cubit scan --textfile-dir DIR` from cron.

`DIR` is node_exporter's textfile directory. node_exporter exposes the file, Prometheus scrapes it, and Vigil runs the queries below against Prometheus. There is no Cubit file to ship and no `--out` flag. Cubit sets no host label of its own; the Prometheus `instance` label tells the hosts apart, and Vigil maps it to a host slug (see `host` below).

## The block

```json
{
  "hosts": [
    {
      "host": "lab",
      "generated_at": "2026-01-15T12:00:00Z",
      "roots": [
        {
          "alias": "example-projects",
          "mode": "workspace-root",
          "bytes": 42000000000,
          "success": true,
          "partial": true,
          "top": [{"name": "app-cli", "bytes": 30000000000}],
          "caches": []
        }
      ]
    }
  ]
}
```

A complete sample, built from `examples/scan.json`, is [`testdata/vigil/cubit-block.json`](../testdata/vigil/cubit-block.json). A Go test renders that document through the collector, applies the table below to what comes out, and compares the result to the fixture.

## Mapping

Every query carries `job="<node_exporter job>"`, the scrape job of node_exporter, so that a series of the same name from another job can never be read as Cubit's. `<node_exporter job>` is a placeholder: Vigil's config owns the real job name, and the tests write it as `node`. Each row is one field. The first two columns are read by a test: every field in the fixture must have a row, and the metric must be the one the test uses. Every query is also run by promtool against sample data in [`testdata/vigil/queries.test.yml`](../testdata/vigil/queries.test.yml).

| Field | Metric | PromQL |
|---|---|---|
| `hosts[].host` | `cubit_build_info` | `group by (instance) (cubit_build_info{job="<node_exporter job>"})` |
| `hosts[].generated_at` | `cubit_scan_last_success_timestamp_seconds` | `max by (instance) (cubit_scan_last_success_timestamp_seconds{job="<node_exporter job>"})` |
| `hosts[].roots[].alias` | `cubit_root_info` | `cubit_root_info{job="<node_exporter job>"}` |
| `hosts[].roots[].mode` | `cubit_root_info` | `cubit_root_info{job="<node_exporter job>"}` |
| `hosts[].roots[].bytes` | `cubit_root_bytes` | `cubit_root_bytes{job="<node_exporter job>"}` |
| `hosts[].roots[].success` | `cubit_scan_success` | `cubit_scan_success{job="<node_exporter job>"}` |
| `hosts[].roots[].partial` | `cubit_scan_partial` | `cubit_scan_partial{job="<node_exporter job>"}` |
| `hosts[].roots[].top[].name` | `cubit_workspace_target_bytes` | `cubit_workspace_target_bytes{job="<node_exporter job>"}` |
| `hosts[].roots[].top[].bytes` | `cubit_workspace_target_bytes` | `cubit_workspace_target_bytes{job="<node_exporter job>"}` |
| `hosts[].roots[].caches[].kind` | `cubit_cache_bytes` | `cubit_cache_bytes{job="<node_exporter job>"}` |
| `hosts[].roots[].caches[].bytes` | `cubit_cache_bytes` | `cubit_cache_bytes{job="<node_exporter job>"}` |

How each field is read from the result:

- **`host`** is the Vigil host slug mapped from the `instance` label through Vigil's overlay `hosts[]`. `instance` is an address (`host:port`) and is never passed through. An `instance` with no entry in `hosts[]` is left out of `cubit{}` altogether, and Vigil records `detail_code: unmapped_instance` on the Cubit source. Every other query below is keyed by `instance` and re-keyed to the slug the same way.
- **`roots: []`** means no data. A host whose Cubit has not finished its first scan exports `cubit_build_info` and zeroed counters, with no size or health series, so it appears with `roots: []` and no `generated_at`, and its row in Vigil's `sources[]` is `unavailable` with `detail_code: no_series`. It is never read as zero bytes.
- **`generated_at`** is the largest value of `cubit_scan_last_success_timestamp_seconds` across that host's roots, as RFC 3339 UTC to the second. It is the time of the newest measurement anywhere on the host. A host where no root has ever succeeded has no such series, and `generated_at` is omitted rather than set to zero.
- **`alias` and `mode`** are the `root` and `mode` labels of `cubit_root_info`, one row per root. The series exists from a root's first finished scan, failed or not. `mode` is one of `workspace-root`, `target-dir`, `cargo-home`, `sccache`.
- **`bytes`** is `cubit_root_bytes` for the root, omitted until the root's first successful scan (a never-measured root must not read as zero). After a failed scan it keeps the last good value, and `success` says so. After a partial scan it is a lower bound, and `partial` says so.
- **`success` and `partial`** are `cubit_scan_success` and `cubit_scan_partial`, with 1 read as `true`. `partial` is never true when `success` is false.
- **`top[]`** is the five largest workspaces of the root. The query returns every labelled workspace series of every root (at most `max_workspaces` per root; Vigil bounds the fetch with its own series limit): `name` is the `workspace` label, `bytes` the value. Vigil sorts by bytes descending, then name ascending, and cuts at five itself, so the cut is deterministic at a tie; `topk` in PromQL would leave a tie at the cut arbitrary, which is why the query does not use it (the promtool tests show it once as an illustration). It holds labelled workspaces only: the ones beyond `max_workspaces` are in `cubit_workspaces_unlabeled_bytes` and are not named. It is empty for `cargo-home` and `sccache` roots.
- **`caches[]`** is every `cubit_cache_bytes` series of the root: `kind` is the `kind` label. Sort by `kind`. It is empty for `workspace-root` and `target-dir` roots.

Hosts are sorted by `host`, roots by `alias`. Both arrays under a root are always present, empty when they do not apply.

## What is not in the block

- **No status.** Whether a source is `stale`, `unavailable` or `partial` is Vigil's to say, from the fields above and from the freshness inputs below. Cubit emits `warning` in its own alert rules and nothing else; the block carries no severity at all.
- **No paths.** No label ever holds an absolute filesystem path, and none of these queries reads one. `workspace` is the sanitized relative path under the root, at most 64 bytes, and matches `^[a-z0-9._-]+(/[a-z0-9._-]+)*$`: no leading slash, no empty segment, no `.` or `..` segment. A slash inside a name is legal (`libs/core`), so a validator must use this pattern and not ban `/`. `root` is a config alias, matching `^[a-z0-9][a-z0-9-]{0,31}$`.
- **No profile, skip or error detail.** They are in the metrics for Prometheus and rules; the block stays small on purpose.

## Freshness inputs

These two queries are not block fields. They are what Vigil can use to decide whether the data is current.

| Input | PromQL | Says |
|---|---|---|
| Age of the last measurement | `time() - max by (instance) (cubit_scan_last_success_timestamp_seconds{job="<node_exporter job>"})` | How long since any root on the host was successfully scanned, whatever happened to the file since. Seconds. |
| Age of the textfile | `time() - max by (instance) (node_textfile_mtime_seconds{job="<node_exporter job>",file=~".*cubit\\.prom"})` | How long since the writer last rewrote `cubit.prom`. Seconds. Absent for a host with no `cubit.prom`. |

Both are needed. node_exporter keeps serving the last file it saw, and Prometheus stamps those samples at scrape time, so a stopped Cubit leaves gauges that look fresh. The first input catches a Cubit that runs but cannot measure; the second catches one that has stopped writing. A healthy `serve` rewrites the file every scan interval (5 minutes by default, ±10% jitter), so a staleness limit should be a small multiple of the interval. The example alert in [`examples/rules/cubit-cron.rules.yml`](../examples/rules/cubit-cron.rules.yml) uses the same textfile selector.

If a query returns nothing for a host, the host has no Cubit data: either node_exporter is not scraping it or the textfile collector is off, or the job name in Vigil's config does not match. That is "unavailable", not "zero".
