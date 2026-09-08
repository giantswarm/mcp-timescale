#!/usr/bin/env bash
# Runs the integration suite against a throwaway TimescaleDB container.
#
#   make test-integration            # default image timescale/timescaledb:2.29.2-pg17
#   TIMESCALE_IMAGE=... make test-integration
#   MCP_TIMESCALE_TEST_DSN=postgres://... make test-integration   # reuse a running database
#
# The container listens on an ephemeral loopback port and is removed on exit.
set -euo pipefail

IMAGE="${TIMESCALE_IMAGE:-timescale/timescaledb:2.29.2-pg17}"
PASSWORD="${TIMESCALE_TEST_PASSWORD:-mcp-timescale-it}"
GO_TEST_ARGS="${GO_TEST_ARGS:--count=1 -run Integration -v ./...}"

if [[ -n "${MCP_TIMESCALE_TEST_DSN:-}" ]]; then
  echo "using MCP_TIMESCALE_TEST_DSN from the environment"
  # shellcheck disable=SC2086
  exec go test ${GO_TEST_ARGS}
fi

command -v docker >/dev/null || { echo "docker is required (or set MCP_TIMESCALE_TEST_DSN)"; exit 1; }

NAME="mcp-timescale-it-$$"
cleanup() { docker rm -f "${NAME}" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "starting ${IMAGE} as ${NAME}"
docker run -d --rm --name "${NAME}" \
  -e POSTGRES_PASSWORD="${PASSWORD}" \
  -e POSTGRES_DB=timescaledb \
  -p 127.0.0.1:0:5432 \
  "${IMAGE}" >/dev/null

PORT="$(docker port "${NAME}" 5432/tcp | head -n1 | sed 's/.*://')"
DSN="postgres://postgres:${PASSWORD}@127.0.0.1:${PORT}/timescaledb?sslmode=disable"

echo -n "waiting for postgres on 127.0.0.1:${PORT} "
# The image's init phase runs a socket-only server before the real one; a TCP
# connection only succeeds once the final server listens.
for _ in $(seq 1 60); do
  if docker exec "${NAME}" psql -h 127.0.0.1 -U postgres -d timescaledb -tAc 'SELECT 1' >/dev/null 2>&1; then
    echo " ready"
    break
  fi
  echo -n "."
  sleep 1
done

# shellcheck disable=SC2086
MCP_TIMESCALE_TEST_DSN="${DSN}" go test ${GO_TEST_ARGS}
