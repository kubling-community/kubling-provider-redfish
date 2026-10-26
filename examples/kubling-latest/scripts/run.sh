#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
compose_file="${repository_root}/examples/kubling-latest/compose.yaml"
project_name="${COMPOSE_PROJECT_NAME:-kubling-redfish-sample-$$}"

compose() {
  docker compose \
    --project-name "${project_name}" \
    --file "${compose_file}" \
    "$@"
}

diagnostics() {
  compose ps --all || true
  compose logs --no-color --tail 200 || true
}

cleanup() {
  status=$?
  trap - EXIT
  compose down --volumes --remove-orphans --rmi local || true
  exit "${status}"
}
trap cleanup EXIT

if ! compose up --build --pull always --wait --wait-timeout 180; then
  diagnostics
  exit 1
fi

if ! compose --profile test run --rm --no-deps smoke-test; then
  diagnostics
  exit 1
fi
