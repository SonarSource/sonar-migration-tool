# GitHub Actions Workflows
<!-- updated: 2026-07-28_10:25:08 -->

## Active Workflows
<!-- updated: 2026-07-28_10:25:08 -->

### 1. `build.yml` - Test + Release
<!-- updated: 2026-07-28_10:25:08 -->

**Triggers:**
- Push to `main` or `branch-*` — tests, build, sign, and (on `main` only) GitHub Release publish
- Push to `kilo` — tests and SonarQube scan only
- Pull requests from this repository — tests and SonarQube scan
- Pull requests from a fork — tests only (see [Fork pull requests](#fork-pull-requests))

**What it does:**
- Runs Go library and migration tool tests with coverage
- Runs SonarQube Cloud analysis (skipped on fork PRs)
- On `main` / `branch-*` pushes: cross-compiles 6 platform binaries, GPG-signs all,
  Apple code-signs + notarizes macOS, Authenticode-signs Windows (Azure Artifact Signing),
  and publishes a GitHub Release on **`main` only**

**Release binaries:**

| Platform | Architecture | Filename |
|----------|-------------|----------|
| Linux    | x64         | `sonar-migration-tool-linux-amd64` |
| Linux    | ARM64       | `sonar-migration-tool-linux-arm64` |
| macOS    | x64         | `sonar-migration-tool-darwin-amd64` |
| macOS    | ARM64       | `sonar-migration-tool-darwin-arm64` |
| Windows  | x64         | `sonar-migration-tool-windows-amd64.exe` |
| Windows  | ARM64       | `sonar-migration-tool-windows-arm64.exe` |

Each binary has a matching `.asc` GPG signature.

See [docs/RELEASE-SIGNING-SETUP.md](../../docs/RELEASE-SIGNING-SETUP.md) for Vault and Azure onboarding.

### 2. `test.yml` - Manual Test Run
<!-- updated: 2026-07-28_10:25:08 -->

**Trigger:** Manual dispatch (`workflow_dispatch`)  
**Purpose:** On-demand test run with SonarQube Cloud scan

`workflow_dispatch` can only be triggered against a ref in this repository, so this
workflow always has OIDC available and is unaffected by the fork restriction below.

### 3. `gitleaks.yml` - Secret Scan
<!-- updated: 2026-07-28_10:25:08 -->

**Triggers:** Push to `main` / `kilo`, and all pull requests  
**Purpose:** Scans full git history for committed secrets with the gitleaks CLI

Uses no secrets, no Vault, and no OIDC, so it runs identically on fork pull requests.

## Fork pull requests
<!-- updated: 2026-07-28_10:25:08 -->

GitHub **does not issue an OIDC ID token** to workflow runs triggered by a
`pull_request` event from a forked repository. Every Vault lookup in this repo
authenticates to `vault.sonar.build` over OIDC, so on a fork PR the
`SonarSource/vault-action-wrapper` step fails with:

```
##[error]Error message: Unable to get ACTIONS_ID_TOKEN_REQUEST_URL env variable
```

No amount of configuration on the contributor's side can satisfy this — it is a
platform restriction. Before this was handled, the whole `Test` job went red on fork
PRs even though every Go test had passed (see PR #487 / issue #486).

**How it is handled.** The `test` job computes one job-level `env` flag:

```yaml
env:
  SONAR_ANALYSIS_ENABLED: ${{ github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository }}
```

`Fetch Vault secrets` and `SonarQube Scan` are gated on
`if: env.SONAR_ANALYSIS_ENABLED == 'true'`; a companion step gated on the inverse
prints an explicit explanation to the log and the job summary so the skip is never
silent.

> **Note:** job-level `env` is readable from a **step-level** `if`, but *not* from a
> **job-level** `if`. That is why the `build-binaries`, `sign-*`, and `publish` jobs
> still inline their conditions — see the comments in `build.yml`.

| Trigger | `SONAR_ANALYSIS_ENABLED` | Go tests | Vault + SonarQube scan |
|---------|--------------------------|----------|------------------------|
| Push to `main` | `true` | run | run |
| Push to `branch-*` / `kilo` | `true` | run | run |
| PR from this repository | `true` | run | run |
| PR from a fork | `false` | run | **skipped, with an explanation** |

Behaviour on same-repo pushes and PRs is unchanged: the analysis that enforces the
quality gate still runs on every one of them.

**What this means for reviewers.** A fork PR gets no SonarQube Cloud analysis and
therefore no quality gate reading. Analysis runs on `main` after the merge. To get a
gate reading *before* merging, push the contributor's branch to this repository (e.g.
`git fetch <fork> <branch> && git push origin FETCH_HEAD:refs/heads/branch-review-NNN`)
and open an internal PR — that run has OIDC and scans normally.

**What is deliberately *not* done.** `pull_request_target` would give fork PRs access
to secrets while checking out untrusted code, which is a well-known privilege-escalation
pattern. It is not used here, and the skip condition depends only on repository
identity — never on anything a contributor can change from their branch.
