#!/bin/bash

set -x

CONFIG=ee
DO_EXTRACT=false
DO_MIGRATE=false

usage() {
  echo "Usage: $0 [-c <config>] [-e] [-m]" >&2
  echo "  -c <config>  Config name to use (default: ee)" >&2
  echo "  -e           Run extract, structure, mappings and copy organizations" >&2
  echo "  -m           Run migrate" >&2
  echo "  (if neither -e nor -m is given, all steps run)" >&2
  exit 1
}

while getopts "c:emh" opt; do
  case "${opt}" in
    c) CONFIG="${OPTARG}" ;;
    e) DO_EXTRACT=true ;;
    m) DO_MIGRATE=true ;;
    h) usage ;;
    *) usage ;;
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
  ./sonar-migration-tool extract --config "${CONFIG_FILE}"
  ./sonar-migration-tool structure --config "${CONFIG_FILE}"
  ./sonar-migration-tool mappings --config "${CONFIG_FILE}"

  cp organizations-${CONFIG}.csv migration-files-${CONFIG}/organizations.csv
fi

if [[ "${DO_MIGRATE}" = true ]]; then
  ./sonar-migration-tool migrate --config "${CONFIG_FILE}"
fi
