# GitHub Actions Workflows

## Active Workflows

### 1. `build.yml` - Test

**Triggers:**
- Push to `main`, `branch-*`, or `kilo` — tests and SonarQube scan
- Pull requests — tests and SonarQube scan

**What it does:**
- Runs Go library and migration tool tests with coverage
- Runs SonarQube Cloud analysis

No binaries are built and no GitHub Release is published from this workflow —
see `release.yml` below for that.

### 2. `release.yml` - Manual Release

**Trigger:** Manual dispatch (`workflow_dispatch`), from whichever branch/tag
is selected in the Actions UI.

**What it does:**
- Re-runs the test + SonarQube scan job as a gate
- Cross-compiles 6 platform binaries, GPG-signs all of them
- Apple code-signs + notarizes macOS binaries
- Authenticode-signs Windows binaries (Azure Artifact Signing) and re-GPG-signs them
- Publishes a dated GitHub Release with every signed binary attached

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

### 3. `test.yml` - Manual Test Run

**Trigger:** Manual dispatch (`workflow_dispatch`)  
**Purpose:** On-demand test run with SonarQube Cloud scan
