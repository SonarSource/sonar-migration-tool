#!/bin/bash

#set -x

CONFIG=ee
PK=""

usage() {
  echo "Usage: $0 [-c <config>] -p <projectKey>" >&2
  echo "  -c <config>      Config name to use (default: ee)" >&2
  echo "  -p <projectKey>  Project key to transfer" >&2
  echo "  -o <org>         SQC org to transfer into" >&2
  exit 1
}

while getopts "c:hp:o:" opt; do
  case "${opt}" in
    c) CONFIG="${OPTARG}" ;;
    p) PK="${OPTARG}" ;;
    o) ORG="${OPTARG}" ;;
    h) usage ;;
    *) usage ;;
  esac
done

if [[ -z "${PK}" ]]; then
    echo "-p <projectKey> option is mandatory"
    usage
fi

./build.sh

if [[ -z "${ORG}" ]]; then
    echo "-o <org> option is mandatory"
    usage
fi

CONFIG_FILE="config-${CONFIG}.json"

./sonar-migration-tool transfer --config "${CONFIG_FILE}" --project_key $PK --default_organization $ORG --debug
