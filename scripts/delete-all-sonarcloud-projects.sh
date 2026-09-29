#!/usr/bin/env bash
set -euo pipefail

# Default config is config.json at project root; pass a path as $1 to override.
CONFIG_FILE="${1:-$(cd "$(dirname "$0")" && pwd)/../config.json}"

if [[ ! -f "$CONFIG_FILE" ]]; then
  echo "Error: config not found at $CONFIG_FILE"
  echo "Usage: $0 [path/to/config.json]"
  exit 1
fi

# Returns the value of the first config key that is actually present.
# jq -r prints the literal string "null" for a missing path, so reading a key
# blindly hands "null" to curl instead of failing; treat null/empty as absent.
cfg_first() {
  local QUERY VALUE
  for QUERY in "$@"; do
    VALUE=$(jq -r "$QUERY // empty" "$CONFIG_FILE")
    if [[ -n "$VALUE" && "$VALUE" != "null" ]]; then
      echo "$VALUE"
      return 0
    fi
  done
  echo ""
}

# Prefer the unified source/target layout, fall back to the legacy
# sonarcloud.organizations[] one.
ORG_KEY=$(cfg_first '.target.default_organization' '.target.organization_key' '.sonarcloud.organizations[0].key')
SC_TOKEN=$(cfg_first '.target.token' '.sonarcloud.organizations[0].token')
SC_URL=$(cfg_first '.target.url' '.sonarcloud.organizations[0].url')

if [[ -z "$ORG_KEY" ]]; then
  echo "Error: no SonarCloud organization key in $CONFIG_FILE"
  echo "Tried: .target.default_organization, .target.organization_key, .sonarcloud.organizations[0].key"
  exit 1
fi

if [[ -z "$SC_TOKEN" ]]; then
  echo "Error: no SonarCloud token in $CONFIG_FILE"
  echo "Tried: .target.token, .sonarcloud.organizations[0].token"
  exit 1
fi

# Only fall back to the public host when neither layout names a URL.
SC_URL="${SC_URL:-https://sonarcloud.io}"
SC_URL="${SC_URL%/}"

echo "SonarCloud Org : $ORG_KEY"
echo "SonarCloud URL : $SC_URL"
echo ""

# --- Fetch all project keys ---
echo "Fetching ALL projects from SonarCloud..."
SC_PAGE=1
SC_PAGE_SIZE=500
SC_KEYS=()

while true; do
  SC_RESPONSE=$(curl -s \
    -H "Authorization: Bearer $SC_TOKEN" \
    "$SC_URL/api/projects/search?organization=$ORG_KEY&ps=$SC_PAGE_SIZE&p=$SC_PAGE")

  KEYS=$(echo "$SC_RESPONSE" | jq -r '.components[].key // empty')

  if [[ -z "$KEYS" ]]; then
    break
  fi

  while IFS= read -r KEY; do
    SC_KEYS+=("$KEY")
  done <<< "$KEYS"

  SC_TOTAL=$(echo "$SC_RESPONSE" | jq -r '.paging.total // 0')
  if [[ "${#SC_KEYS[@]}" -ge "$SC_TOTAL" ]]; then
    break
  fi

  SC_PAGE=$(( SC_PAGE + 1 ))
done

echo "Found ${#SC_KEYS[@]} project(s) in SonarCloud."
echo ""

if [[ "${#SC_KEYS[@]}" -eq 0 ]]; then
  echo "No projects found. Nothing to delete."
  exit 0
fi

echo "Will delete ALL ${#SC_KEYS[@]} project(s):"
for KEY in "${SC_KEYS[@]}"; do
  echo "  - $KEY"
done
echo ""

read -r -p "Are you sure you want to permanently delete ALL ${#SC_KEYS[@]} project(s)? [yes/N] " CONFIRM
if [[ "$CONFIRM" != "yes" ]]; then
  echo "Aborted."
  exit 0
fi

echo ""
WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

for KEY in "${SC_KEYS[@]}"; do
  (
    HTTP_STATUS=$(curl -s -o /dev/null -w "%{http_code}" \
      -X POST \
      -H "Authorization: Bearer $SC_TOKEN" \
      "$SC_URL/api/projects/delete?project=$KEY")

    if [[ "$HTTP_STATUS" = "204" || "$HTTP_STATUS" = "200" ]]; then
      echo "[DELETED] $KEY"
      touch "$WORK_DIR/deleted_${$}_$RANDOM"
    else
      echo "[FAILED]  $KEY (HTTP $HTTP_STATUS)"
      touch "$WORK_DIR/failed_${$}_$RANDOM"
    fi
  ) &
done

wait

DELETED=$(find "$WORK_DIR" -name 'deleted_*' 2>/dev/null | wc -l | tr -d ' ')
FAILED=$(find "$WORK_DIR" -name 'failed_*' 2>/dev/null | wc -l | tr -d ' ')

echo ""
echo "Done. Deleted: $DELETED  |  Failed: $FAILED"
