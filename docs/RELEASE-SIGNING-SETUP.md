# Release Signing — Vault Access & Setup

The tag-triggered release pipeline (`.github/workflows/build.yml`) signs macOS
binaries on a GitHub-hosted Apple runner that reaches internal Vault via
Cloudflare WARP.

- **GitHub repo:** `SonarSource/sonar-migration-tool`
- **Vault URL:** `https://vault.sonar.build`
- **Vault role (tag/protected refs):** `github-SonarSource-sonar-migration-tool-protected`

## Vault paths used

| Path | Purpose |
|------|---------|
| `development/kv/data/cloudflare/warp-github-runner` | WARP tunnel (macOS runner → Vault) |
| `development/kv/data/sign/apple` | Apple Developer ID + notarization (shared) |
| `development/kv/data/sign` | GPG signing key + passphrase |
| `development/kv/data/sonarcloud` | SonarQube scan token (`test` job) |

macOS code-signing uses the shared Apple credentials at
`development/kv/data/sign/apple`. The binary is still signed with identifier
`sonar-migration-tool`.

Vault policy is managed in
[`re-terraform-aws-vault`](https://github.com/SonarSource/re-terraform-aws-vault)
(`orders/customer-success-technical-advisory-squad.yaml`).

## Re-triggering a release

Only `v*` tag pushes run build/sign/publish. To validate end-to-end:

```bash
TAG="v1.0.0-rc2"
git tag "$TAG" <commit>
git push origin "$TAG"
```

To clean up a failed attempt:

```bash
gh release delete "$TAG" --cleanup-tag --yes
git push origin ":refs/tags/$TAG" && git tag -d "$TAG"
```
