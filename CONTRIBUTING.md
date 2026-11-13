# Contributing to Cubit

Cubit measures Cargo disk use and never cleans it. Keep changes small and say what they do.

## Sign your commits (DCO)

Every commit needs a `Signed-off-by` line, which certifies the [Developer Certificate of Origin](https://developercertificate.org/):

```
git commit -s
```

The `DCO` check fails a pull request if any commit in it lacks the line. To fix a branch, `git rebase --signoff main` and push the new branch.

## Licensing

Cubit is licensed under either of the Apache License, Version 2.0 or the MIT license, at your option (see `LICENSE-APACHE` and `LICENSE-MIT`). Unless you say otherwise, a contribution you submit is dual licensed the same way, with no additional terms.

## Before you open a pull request

```
go build ./...
go vet ./...
go test -race ./...
golangci-lint run
promtool test rules examples/rules/cubit.rules.test.yml
scripts/scrub.sh
```

CI runs all of these. The tests include a contract suite (`internal/contract`) that keeps the schemas, the metric table, the refuse list and the example rules in agreement. If you change one, change the others in the same pull request.

## Rules of the repo

- **It is public.** Examples, docs, tests and commit messages use synthetic names (`example-projects`, `build-example`). No real paths, addresses, host names, people or credentials. `scripts/scrub.sh` and gitleaks enforce this.
- **Read-only.** Cubit does not delete, prune or clean, and it does not walk outside the roots it is given. A change that adds a write path needs its own security review.
- **Contained.** The walk uses `os.Root`, never follows symlinks, and checks the refuse list on every directory entry.
- **Base units.** Bytes and seconds. No percentages and no MiB.
- **Closed label values.** A new `reason`, `kind`, `profile` or `result` is a contract change: update the schemas and `docs/metrics.md` together.
- **Strict decoding.** An unknown config key is an error, not a warning.
- Error messages name the file and field, show a one-line excerpt, and end with the fix.

## Pull requests

Open a draft pull request from a branch. Do not push to `main`, and do not force-push a branch under review: push a new commit, or a new branch.
