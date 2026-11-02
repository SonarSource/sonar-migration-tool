#!/bin/bash

set -x

CONFIG=ee
DO_EXTRACT=false
DO_MIGRATE=false
HISTORY="--migrate_history"
HISTORY=""
TOOL="./sonar-migration-tool"

EXTRA_ARGS=()

usage() {
  echo "Usage: $0 [-c <config>] [-e] [-m] [other options...]" >&2
  echo "  -c <config>  Config name to use (default: ee)" >&2
  echo "  -e           Run extract, structure, mappings and copy organizations" >&2
  echo "  -m           Run migrate" >&2
  echo "  (if neither -e nor -m is given, all steps run)" >&2
  echo "  Any other option is passed through verbatim to sonar-migration-tool" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -c)
      CONFIG="$2"
      shift 2
      ;;
    -e)
      DO_EXTRACT=true
      shift
      ;;
    -m)
      DO_MIGRATE=true
      shift
      ;;
    -h)
      usage
      ;;
    *)
      EXTRA_ARGS+=("$1")
      shift
      ;;
  esac
done

./build.sh

# If no step flag is given, run everything.
if [[ "${DO_EXTRACT}" = false ]] && [[ "${DO_MIGRATE}" = false ]]; then
  DO_EXTRACT=true
  DO_MIGRATE=true
fi

CONFIG_FILE="config-${CONFIG}.json"

if [[ "${DO_EXTRACT}" = true ]]; then
  ${TOOL} extract --config "${CONFIG_FILE}" ${HISTORY} "${EXTRA_ARGS[@]}"
  ${TOOL} structure --config "${CONFIG_FILE}"
  ${TOOL} mappings --config "${CONFIG_FILE}"

  cp organizations-${CONFIG}.csv migration-files-${CONFIG}/organizations.csv
fi

if [[ "${DO_MIGRATE}" = true ]]; then
  ${TOOL} migrate --config "${CONFIG_FILE}" ${HISTORY} "${EXTRA_ARGS[@]}"
fi
