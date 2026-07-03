# Release Signing — Setup

Branch-based release pipeline (`.github/workflows/build.yml`), aligned with
`sonarqube-cli` and Azure Artifact Signing federated-credential patterns.

- **GitHub repo:** `SonarSource/sonar-migration-tool`
- **Vault URL:** `https://vault.sonar.build`
- **Vault role (protected refs):** `github-SonarSource-sonar-migration-tool-protected`

## When jobs run

| Trigger | Test | Build + sign | GitHub Release |
|---------|------|--------------|----------------|
| Pull request | yes | no | no |
| Push to `main` | yes | yes (release profile) | yes |
| Push to `branch-*` | yes | yes (release profile) | no |
| Push to `kilo` | yes | no | no |

Release signing uses **branch** OIDC subjects (`refs/heads/main`, `refs/heads/branch-*`),
not git tags. Tags are created by the publish job on `main` pushes
(e.g. `v2026.07.03-123`).

## Vault paths (GPG + macOS)

| Path | Purpose |
|------|---------|
| `development/kv/data/cloudflare/warp-github-runner` | WARP tunnel (macOS runner → Vault) |
| `development/kv/data/sign/apple` | Apple Developer ID + notarization |
| `development/kv/data/sign` | GPG signing key + passphrase |
| `development/kv/data/sonarcloud` | SonarQube scan token |

## Windows Authenticode (Azure Artifact Signing)

Windows `.exe` files are signed via
[`gh-action_azure-artifact-signing@v1`](https://github.com/SonarSource/gh-action_azure-artifact-signing)
using GitHub OIDC → Entra federated credentials.

**Prerequisites (infra, not in this repo):**

1. [BUILD-11815](https://sonarsource.atlassian.net/browse/BUILD-11815) — second release signing identity (pool B)
2. Onboard `sonar-migration-tool` in
   [`re-service-config/azure_artifact_signing`](https://github.com/SonarSource/re-service-config/tree/master/azure_artifact_signing)
   with `release_branches = ["main", "branch-*"]` on pool B

Until onboarding is applied, the `sign-windows` job will fail OIDC authentication.

## Re-triggering a release

Merge to `main` (or push a signed `branch-*` build for validation without publishing):

```bash
git push origin main
```

To delete a failed GitHub Release:

```bash
gh release delete "$TAG" --cleanup-tag --yes
```
