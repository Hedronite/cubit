# Security policy

## Reporting a vulnerability

Please report it privately through GitHub's "Report a vulnerability" form on this repository's Security tab. Do not open a public issue for it.

Say what you found, how to reproduce it and which version or commit it affects. You should get an acknowledgement within a few days.

## What counts

Cubit walks directories and exports byte counts, so the issues that matter most are:

- reading outside an allowlisted root, by symlink, `..`, a race or any other route;
- reading or reporting a path on the [refuse list](docs/refuse-list.md);
- a filesystem path, host name or other identifier reaching a label, a metric or a report;
- a hostile directory tree that exhausts memory, files or time despite the caps;
- a write outside the configured textfile directory, or a non-atomic write to it;
- binding a non-loopback address without `--unsafe-listen`.

## Model

- Cubit is read-only. It never deletes or changes what it measures. The only file it may write is `cubit.prom` in a directory the operator names with `--textfile-dir`.
- `/metrics` is unauthenticated. It listens on loopback by default. With `--unsafe-listen` it can be reached by anyone on that network, and the operator accepts that.
- Workspace names are directory names from the host that runs Cubit. They are not secrets from the host's own Prometheus, but they are not published by the project and must not appear in this repository.
