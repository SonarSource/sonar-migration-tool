# Building from source

This page covers building `sonar-migration-tool` from source and running directly from the Go module — meant for contributors and operators who can't use the released binary. End users should download the [pre-built release binary](https://github.com/sonar-solutions/sonar-migration-tool/releases) and follow the main [README](../README.md) instead.

## Prerequisites

- **Go 1.25+** (`go version` to check)
- A clone of the repository:
  ```bash
  git clone https://github.com/sonar-solutions/sonar-migration-tool.git
  cd sonar-migration-tool
  ```

The Go module lives in the `go/` subdirectory.

## Building a binary

```bash
cd go
go build -o ../sonar-migration-tool .
cd ..
./sonar-migration-tool --version
```

The resulting binary is a single static executable. Move it onto your `PATH` or invoke it by absolute path.

## Running directly with `go run`

When iterating on the code there's no need to build first — every `sonar-migration-tool` example in the README can be expressed as `go run`:

```bash
# From the go/ directory.
cd go

# Equivalent to: sonar-migration-tool extract --config ../config.json
go run . extract --config ../config.json

# Equivalent to: sonar-migration-tool wizard --export_directory ../files/
go run . wizard --export_directory ../files/
```

The first invocation compiles; subsequent invocations reuse the build cache. Paths in `--config` and `--export_directory` are resolved relative to your current working directory.

## Running the test suite

```bash
cd go
go test ./...
```

Per-package tests:

```bash
go test ./internal/migrate/...
go test ./internal/predict/...
go test ./internal/report/summary/...
```

For the architecture overview and package map, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Regression testing

The end-to-end regression suite lives in [`REGRESSION-TESTING.md`](REGRESSION-TESTING.md). It exercises a full migration against a real SQS + SQC pair and compares the result against a recorded baseline.

## Contributing from a fork
<!-- updated: 2026-07-28_10:25:08 -->

CI behaves slightly differently for pull requests opened from a forked repository.

GitHub does not issue an OIDC ID token to `pull_request` runs originating from a fork.
Every secret in this repository's CI is fetched from Vault over OIDC, so the Vault step
— and the SonarQube Cloud scan that depends on it — cannot run on a fork PR. They are
skipped, and the `Test` job prints an explicit explanation instead of failing.

What this means in practice:

- **Go tests still run in full** on your PR, and are the signal to watch. Run
  `go test ./...` in both `go/` and `lib/sq-api-go/` locally before pushing.
- **No SonarQube Cloud analysis or quality gate reading** appears on a fork PR. A
  maintainer can get one early by pushing your branch to this repository; otherwise
  analysis runs on `main` after the merge.
- **The secret scan (gitleaks) does run** on fork PRs — it uses no secrets.
- Signing, notarization, and release publishing never run on any PR, fork or not.

The condition and its rationale are documented in
[`.github/workflows/README.md`](../.github/workflows/README.md#fork-pull-requests).

## Releasing
<!-- updated: 2026-07-28_10:25:08 -->

Tagged releases are built and published via GitHub Actions; see `.github/workflows/`. To bump the version manually:

1. Update `internal/version/version.go` and the `Makefile` if needed.
2. Tag the release: `git tag vX.Y.Z && git push --tags`.
3. CI builds binaries for Linux / macOS / Windows and uploads them as release assets.
