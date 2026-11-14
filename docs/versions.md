# Versions and pins

Everything the build, CI and release pipelines depend on, with the exact version or commit. Dependabot is declined: a pin moves by hand, in the same pull request that updates this page. Tests check that every action in `.github/workflows/` is pinned to a full commit SHA and listed here, and that the Go and tool versions below match the workflows.

## Go

| What | Version | Where |
|---|---|---|
| Release toolchain | `go1.27.4` | `scripts/release.env`; `go-version` in `release.yml` and in the `reproducible` job of `ci.yml` |
| Language floor | `go 1.26` | `go.mod` |
| Test matrix | `stable` and `oldstable` | `ci.yml` (`test`) |

The release archives are built only with the release toolchain: `scripts/release.sh` refuses any other Go, because a different Go gives different bytes. To reproduce a published archive, use the same commit and the same Go release (see [the README](../README.md#verify-a-download)).

## Module dependencies

Direct: `github.com/santhosh-tekuri/jsonschema/v6`, `github.com/spf13/cobra`, `go.yaml.in/yaml/v3`. The exact versions, with checksums, are in `go.mod` and `go.sum`. The SBOM that ships with each release lists every module in the binary.

## Tools

| Tool | Version | Used by |
|---|---|---|
| golangci-lint | `v2.15.0` | `ci.yml` (`lint`), through its action |
| govulncheck | `v1.9.0` | `ci.yml` (`vuln`) |
| promtool (Prometheus) | `3.16.0`, archive pinned by sha256 | `ci.yml` (`rules`) |
| gitleaks | `8.30.1`, archive pinned by sha256 | `ci.yml` (`scrub`) |
| cyclonedx-gomod | `v1.12.0` | `release.yml` only; not a module dependency |
| benchstat (`golang.org/x/perf`) | `v0.0.0-20261009192801-be2c69fb417e` | by hand, for `docs/performance.md`; CI does not run it |

## GitHub Actions

Each is pinned to a full commit SHA; the tag is the version the SHA was taken from.

| Action | Commit | Tag | Used in |
|---|---|---|---|
| `actions/checkout` | `3d3c42e5aac5ba805825da76410c181273ba90b1` | v7.0.1 | `ci.yml`, `release.yml` |
| `actions/setup-go` | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` | v7.0.0 | `ci.yml`, `release.yml` |
| `golangci/golangci-lint-action` | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` | v9.3.0 | `ci.yml` |
| `actions/attest-build-provenance` | `4d101475d8b20a2381f78447822ac1eab6504dd8` | v4.2.2 | `release.yml` |
| `actions/attest-sbom` | `c604332985a26aa8cf1bdc465b92731239ec6b9e` | v4.1.0 | `release.yml` |

## Moving a pin

1. Change the version or SHA in the workflow or script.
2. Change it here, in the same commit.
3. For the Go release: also `scripts/release.env`. A new Go release changes the bytes of every archive, which is a release-note item.
