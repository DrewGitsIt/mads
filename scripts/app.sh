#!/usr/bin/env bash
# The local end-to-end entry point (until W7 packaging does this properly):
# ensures the Data Service and Router are up, then launches the Electron app.
# Services it starts are stopped again when the app exits; services that were
# already running are left alone.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

DATA_ADDR="${MADS_DATA_ADDR:-localhost:17374}"
ROUTER_ADDR="${MADS_ROUTER_ADDR:-localhost:17373}"

started=()
cleanup() {
  for pid in "${started[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT

up() { curl -sf -o /dev/null "$1" 2>/dev/null; }

# Wait for a service we just spawned, bounded. Three ways this used to hang or
# lie, all of which cost real debugging time:
#   - the child dies instantly (port already taken by something else) and the
#     old `until up ...; do sleep 0.2; done` spun forever, so we check liveness;
#   - a FOREIGN server owns the port, in which case the child is dead but the
#     probe is answered by the impostor — the liveness check fires first;
#   - it is merely slow, so we cap the wait instead of blocking forever.
# On failure, print the service's own log: that log is where the actual cause
# ("bind: address already in use") is written, and nobody thinks to look.
wait_up() { # <url> <pid> <name> <logfile>
  local url="$1" pid="$2" name="$3" logfile="$4" i
  for ((i = 0; i < 150; i++)); do          # 150 * 0.2s = 30s
    if up "$url"; then return 0; fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "app.sh: $name exited during startup — last lines of $logfile:" >&2
      tail -n 10 "$logfile" >&2 || true
      return 1
    fi
    sleep 0.2
  done
  echo "app.sh: $name did not answer $url within 30s — last lines of $logfile:" >&2
  tail -n 10 "$logfile" >&2 || true
  return 1
}

if ! up "http://$DATA_ADDR/health"; then
  go build -o bin/data ./services/data
  ./bin/data -addr "$DATA_ADDR" >> /tmp/mads-data.log 2>&1 &
  started+=($!)
  wait_up "http://$DATA_ADDR/health" "$!" "data service" /tmp/mads-data.log
  echo "app.sh: started data service ($DATA_ADDR)"
fi

if ! up "http://$ROUTER_ADDR/health"; then
  go build -o bin/router ./services/router
  go build -o bin/session ./services/session
  ./bin/router -addr "$ROUTER_ADDR" -session-cmd ./bin/session >> /tmp/mads-router.log 2>&1 &
  started+=($!)
  wait_up "http://$ROUTER_ADDR/health" "$!" "router" /tmp/mads-router.log
  echo "app.sh: started router ($ROUTER_ADDR)"
fi

# `npm ci` (via `make build`) re-runs electron's postinstall, which silently
# no-ops on Node >= 24.16 / 26.1 and leaves a partial dist/ — so the binary
# can be broken again since the last launch. Repair here rather than trusting
# install day; it's a cheap stat when nothing is wrong. Same call decides
# whether this host can sandbox the renderer at all.
# shellcheck source=scripts/ensure-electron.sh
. "$REPO_ROOT/scripts/ensure-electron.sh"
mads_ensure_electron
if mads_electron_needs_no_sandbox; then
  echo "app.sh: no usable renderer sandbox on this host — ELECTRON_DISABLE_SANDBOX=1" >&2
  export ELECTRON_DISABLE_SANDBOX=1
fi

# The Electron window loads the Router's own address; keep it on ROUTER_ADDR
# so MADS_ROUTER_ADDR moves the whole app, not just the backend. Exported
# before `npm start` so it reaches both `vite build` and `electron .`.
export VITE_ROUTER_URL="${VITE_ROUTER_URL:-http://$ROUTER_ADDR}"

npm --prefix ui start
