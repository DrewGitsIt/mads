#!/usr/bin/env bash
# The local end-to-end entry point (until W7 packaging does this properly):
# ensures the Data Service and Router are up, then launches the Electron app.
# Services it starts are stopped again when the app exits; services that were
# already running are left alone.
set -euo pipefail
cd "$(dirname "$0")/.."

started=()
cleanup() {
  for pid in "${started[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

up() { curl -sf -o /dev/null "$1" 2>/dev/null; }

if ! up http://localhost:8081/health; then
  go build -o bin/data ./services/data
  ./bin/data >> /tmp/mads-data.log 2>&1 &
  started+=($!)
  until up http://localhost:8081/health; do sleep 0.2; done
  echo "app.sh: started data service (:8081)"
fi

if ! up http://localhost:8080/health; then
  go build -o bin/router ./services/router
  go build -o bin/session ./services/session
  ./bin/router -session-cmd ./bin/session >> /tmp/mads-router.log 2>&1 &
  started+=($!)
  until up http://localhost:8080/health; do sleep 0.2; done
  echo "app.sh: started router (:8080)"
fi

npm --prefix ui start
