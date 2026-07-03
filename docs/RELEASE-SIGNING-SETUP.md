# Release Signing — Vault Access Request & Setup

<!-- updated: 2026-07-03_17:21:06 -->

This document is the actionable request to the Release Engineering / build-infra
team to provision the Vault access that the tag-triggered release pipeline
(`.github/workflows/build.yml`) needs in order to code-sign, notarize, and
publish `sonar-migration-tool` binaries. It also records the evidence and the
current partial state so the request is self-contained.

## TL;DR for the RE / build-infra team

<!-- updated: 2026-07-03_17:21:06 -->

The GitHub repo `SonarSource/sonar-migration-tool` runs a release workflow on
`v*` tag pushes that signs macOS binaries on a GitHub-hosted Apple runner. That
runner reaches internal Vault (`https://vault.sonar.build`) via Cloudflare WARP.
The repo's Vault role currently **lacks read access to the Cloudflare WARP runner
secret and (unverified) the Apple signing certificate path**, so the signing job
fails and no release is published.

Please grant the role below read access to the two paths listed under
"Access required". This mirrors the setup already in place for
`SonarSource/sonarqube-cli`, whose `build-binaries` / `sign-macos` jobs this
workflow was modeled on.

- **GitHub repo:** `SonarSource/sonar-migration-tool`
- **Vault URL:** `https://vault.sonar.build`
- **Vault role (auto-selected for tag/protected refs):** `github-SonarSource-sonar-migration-tool-protected`

## Access required

<!-- updated: 2026-07-03_17:21:06 -->

The role `github-SonarSource-sonar-migration-tool-protected` needs read
(`read` capability on the KV v2 data path) for:

1. `development/kv/data/cloudflare/warp-github-runner`
   — Cloudflare WARP client credentials so the GitHub-hosted macOS runner can
   tunnel to internal Vault. Consumed by the `Setup Cloudflare WARP` step
   (`SonarSource/gh-action_setup-cloudflare-warp`) in the `sign-macos` job.
   **This is the path that was explicitly DENIED (see Evidence).**

2. `development/kv/data/sign/sonar-migration-tool`
   — Apple Developer ID signing + notarization credentials. Keys read by the
   workflow: `CERTIFICATE`, `CSC_KEY_PASSWORD`, `APPLE_TEAM_ID`, `APPLE_ID`,
   `APPLE_APP_SPECIFIC_PASSWORD`. Consumed by the `Vault Secrets` →
   `Code-sign and notarize macOS binaries` steps in the `sign-macos` job.
   **This path was not reached in the failing run (the WARP step failed first),
   so its existence/access is UNVERIFIED and should be confirmed as part of this
   request.** If the correct project segment is not `sonar-migration-tool`,
   please advise the correct segment and we will update the workflow.

## Already working (partial provisioning)

<!-- updated: 2026-07-03_17:21:06 -->

These paths are already accessible to the repo and succeeded in the same run,
which confirms the repo is partially onboarded to Vault — only the two paths
above are missing:

- `development/kv/data/sonarcloud token` → used by the `test` job (SonarQube
  scan). Succeeded.
- `development/kv/data/sign key` and `development/kv/data/sign passphrase` →
  GPG signing keys used by the `build-binaries` job. Succeeded; all six
  platform binaries were GPG-signed.

Note the Linux jobs (`test`, `build-binaries`) reach Vault directly and do not
need WARP. Only the macOS runner (`macos-latest-xlarge`) requires the WARP path.

## Evidence

<!-- updated: 2026-07-03_17:21:06 -->

First live run of the signing pipeline: tag `v1.0.0`, workflow run
`28650002595` (event `push`, ref `refs/tags/v1.0.0`), 2026-07-03.

Job results:

- `Test` — success
- `Build All Binaries` (×6 platforms) — success (cross-compiled + GPG-signed)
- `Sign and Notarize macOS Binaries` — **failure**
- `Publish Release` — skipped (depends on the failed job → no release created)

Failing step and error (from the `sign-macos` job log):

```
Auto-selected role: github-SonarSource-sonar-migration-tool-protected (ref: refs/tags/v1.0.0, protected: true)
##[error]Response code 403 (Forbidden)
##[error]  DENIED  development/kv/data/cloudflare/warp-github-runner
##[error]Vault secret access denied for 1 path(s).
```

Step trace (root cause is step 3; steps 6/8 were downstream fallout — see
"Workflow hardening" below):

```
3  failure  Setup Cloudflare WARP        <- Vault denied cloudflare/warp path (403)
4  skipped  Download macOS binaries
5  skipped  Vault Secrets                <- signing certs never fetched
6  failure  Code-sign and notarize
8  failure  Re-GPG-sign macOS binaries
```

## Re-triggering after access is granted

<!-- updated: 2026-07-03_17:21:06 -->

Only a `v*` tag push runs the binary/sign/publish jobs (they are gated on
`if: startsWith(github.ref, 'refs/tags/v')`). Branch and PR pushes only run
`test`. To validate end-to-end without polluting the releases page, push a
pre-release tag first:

```bash
TAG="v1.0.0-rc1"           # or a date-scheme tag, e.g. v$(date +%Y.%m.%d-%H%M%S)
git tag "$TAG" <commit>
git push origin "$TAG"
```

If it goes green, push the real tag. To clean up a failed attempt:

```bash
gh release delete "$TAG" --cleanup-tag --yes   # if a release was created
git push origin ":refs/tags/$TAG" && git tag -d "$TAG"
```

## Workflow hardening (already applied in this repo)

<!-- updated: 2026-07-03_17:21:06 -->

The three signing steps previously inlined `${{ fromJSON(steps.secrets.outputs.vault).X }}`
in their `env:` blocks. When Vault returns nothing (access denied / step
skipped), `fromJSON('')` raises an opaque `The template is not valid ... Error
reading JToken` error that masked the real cause. Those steps now read the raw
Vault JSON via `jq` inside the script and fail with an actionable
`::error::` message that names the role and the missing paths and points here.
Affected steps: `GPG-sign binary` (build-binaries), `Code-sign and notarize
macOS binaries`, and `Re-GPG-sign macOS binaries` (sign-macos).

Not yet hardened: the `test` job's `SONAR_TOKEN` still uses the inline
`fromJSON` pattern. It is a separate job on a working Vault path, so it was left
out of scope; harden it the same way if desired.
