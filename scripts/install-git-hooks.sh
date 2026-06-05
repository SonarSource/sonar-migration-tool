#!/bin/sh
# Install this repo's git hooks (pre-commit secret scan via gitleaks).
# Usage: sh scripts/install-git-hooks.sh   (or: make install-hooks)
set -e

ROOT="$(git rev-parse --show-toplevel)"
HOOK_SRC="${ROOT}/scripts/hooks/pre-commit"
HOOK_DST="${ROOT}/.git/hooks/pre-commit"

chmod +x "${HOOK_SRC}"
ln -sf ../../scripts/hooks/pre-commit "${HOOK_DST}"

echo "Installed pre-commit hook → ${HOOK_DST}"
if ! command -v gitleaks >/dev/null 2>&1; then
  echo "NOTE: gitleaks is not installed yet — the hook will skip until you install it:"
  echo "  brew install gitleaks"
fi
