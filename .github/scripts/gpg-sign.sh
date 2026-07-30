#!/usr/bin/env bash
# Create a detached, ASCII-armored GPG signature (<binary>.asc) for a binary.
#
# Usage: .github/scripts/gpg-sign.sh <path-to-binary>
#
# Required environment variables:
#   GPG_SIGNING_KEY         ASCII-armored private key (see note below if base64)
#   GPG_SIGNING_PASSPHRASE  passphrase protecting the private key
#
# Mirrors the `bun build-scripts/sign.ts <binary>` step used by the
# SonarSource/sonarqube-cli build workflow.
set -euo pipefail

binary="${1:?usage: gpg-sign.sh <binary>}"

: "${GPG_SIGNING_KEY:?GPG_SIGNING_KEY is required}"
: "${GPG_SIGNING_PASSPHRASE:?GPG_SIGNING_PASSPHRASE is required}"

# Use a throwaway GnuPG home so the runner keyring is never polluted.
GNUPGHOME="$(mktemp -d)"
export GNUPGHOME
chmod 700 "$GNUPGHOME"
echo "allow-loopback-pinentry" > "$GNUPGHOME/gpg-agent.conf"
trap 'rm -rf "$GNUPGHOME"' EXIT

# Import the signing key, accepting either an ASCII-armored key or a
# base64-encoded one (Vault may store it in either form).
import_signing_key() {
  if printf '%s' "$GPG_SIGNING_KEY" | gpg --batch --import 2>/dev/null; then
    echo "gpg-sign: imported ASCII-armored signing key"
    return 0
  fi
  if printf '%s' "$GPG_SIGNING_KEY" | base64 --decode 2>/dev/null | gpg --batch --import 2>/dev/null; then
    echo "gpg-sign: imported base64-encoded signing key"
    return 0
  fi
  echo "gpg-sign: could not import GPG_SIGNING_KEY (expected ASCII-armored or base64)" >&2
  return 1
}
import_signing_key

printf '%s' "$GPG_SIGNING_PASSPHRASE" | gpg --batch --yes --pinentry-mode loopback \
  --passphrase-fd 0 --detach-sign --armor --output "${binary}.asc" "$binary"

gpg --verify "${binary}.asc" "$binary"
