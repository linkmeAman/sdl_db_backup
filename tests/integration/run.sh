#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
results=${INTEGRATION_RESULTS_DIR:-"$root/tests/integration/results"}
mkdir -p "$results"
project="sdl-e2e-$(date +%s)-$$"
compose=(docker compose --project-name "$project" --file "$root/tests/integration/compose.yaml")
cleanup() {
  status=$?
  trap - EXIT
  "${compose[@]}" logs --no-color > "$results/compose.log" 2>&1 || true
  "${compose[@]}" cp suite:/results/. "$results/" || true
  "${compose[@]}" down --volumes --remove-orphans || true
  exit "$status"
}
docker info >/dev/null
docker compose version >/dev/null
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
"${compose[@]}" build suite
"${compose[@]}" up --abort-on-container-exit --exit-code-from suite --timeout 15
