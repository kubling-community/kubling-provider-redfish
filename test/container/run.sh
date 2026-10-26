#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="${repository_root}/test/container/compose.yaml"
project_name="${COMPOSE_PROJECT_NAME:-kubling-redfish-e2e-$$}"

cleanup() {
    docker compose \
        --project-name "${project_name}" \
        --file "${compose_file}" \
        down --volumes --remove-orphans --rmi local
}
trap cleanup EXIT

docker compose \
    --project-name "${project_name}" \
    --file "${compose_file}" \
    up --build --abort-on-container-exit --exit-code-from verifier
