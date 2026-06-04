# Security Best Practices
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

## Overview
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

This tool handles sensitive credentials (admin tokens for both SonarQube Server and SonarCloud). Follow these practices to keep your tokens safe.

## Never Commit Secrets
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- Never commit `migration-config.json` or any config file with tokens to version control.
- Config files with secrets are already in `.gitignore`.
- Only `.example.json` files (in the `examples/` folder) should be committed.
- If you accidentally commit a token, rotate it immediately.

## Protect Your Tokens
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

- Store tokens in a secure location (password manager, secret manager).
- Tokens have full admin access — treat them as highly sensitive.
- Consider creating temporary tokens that you revoke after migration.
- Create a dedicated migration user in SonarQube Cloud with only the necessary permissions.

## Token Permissions Reference
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

| Environment | Token Type | Required Permissions |
|-------------|------------|---------------------|
| SonarQube Server | Admin Token | Administer System, Administer Quality Gates, Administer Quality Profiles, Browse all projects |
| SonarQube Cloud | User Token | Enterprise admin + Organization admin for all target orgs |

## File Permissions
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

Restrict access to config files containing tokens:

```bash
chmod 600 migration-config.json
```

## Environment Variables in CI/CD
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

For automated pipelines, use environment variables instead of hardcoded tokens:

```bash
export SONAR_TOKEN="your-token"
cat > migration-config.json <<EOF
{
  "sonarqube": {
    "token": "$SONAR_TOKEN"
  }
}
EOF
```

## Export Directory Security
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

The tool restricts the `export_directory` to the current working directory or `/tmp` for security. This prevents path traversal attacks.

## Client Certificates
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

For SonarQube instances behind mTLS:

- Store certificate files with restricted permissions (`chmod 600`).
- Use `--pem_file_path` and `--key_file_path` to provide certificates.
- Use `--cert_password` for password-protected keys (the password is passed as a CLI argument — be aware it may appear in shell history).

## Cleaning Up After Migration
<!-- updated: 2026-06-04_01:14:00.000 by Claude -->

1. Revoke or delete temporary migration tokens.
2. Delete config files containing credentials.
3. Review `requests.log` files — they contain API URLs but not token values.
4. Remove the `files/` directory if you no longer need the exported data.
