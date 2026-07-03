# GitHub Actions Workflows

## Active Workflows

### 1. `build.yml` - Test + Release

**Triggers:**
- Push to `main` or `branch-*` — tests, build, sign, and (on `main` only) GitHub Release publish
- Push to `kilo` — tests and SonarQube scan only
- Pull requests — tests and SonarQube scan only

**What it does:**
- Runs Go library and migration tool tests with coverage
- Runs SonarQube Cloud analysis
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

**Trigger:** Manual dispatch (`workflow_dispatch`)  
**Purpose:** On-demand test run with SonarQube Cloud scan
