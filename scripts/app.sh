#!/usr/bin/env bash
# The local end-to-end entry point (until W7 packaging does this properly):
# ensures the Data Service and Router are up, then launches the Electron app.
# Services it starts are stopped again when the app exits; services that were
# already running are left alone — but only reused if they were launched from
# THIS checkout (their /health reports it), since the Router serves the UI
# from its own cwd's ui/dist: a Router left running by another checkout would
# show that checkout's UI and backend with no hint. MADS_REUSE_SERVICES=1
# reuses whatever is running anyway.
set -euo pipefail

# Physical path (-P): services report os.Getwd(), and the comparison below
# must not trip over a symlinked checkout.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
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

# One string field out of /health's flat JSON object. No jq dependency; the
# values are paths and timestamps, so a plain unescaped match is enough.
health_field() { # <json> <key>
  printf '%s' "$1" | sed -n 's/.*"'"$2"'":"\([^"]*\)".*/\1/p'
}

# The pid listening on addr's port, or empty — best effort (lsof may be absent).
listener_pid() { # <addr>
  command -v lsof >/dev/null 2>&1 || return 0
  lsof -nP -iTCP:"${1##*:}" -sTCP:LISTEN -t 2>/dev/null | head -n 1 || true
}

# A pid's cwd, for builds whose /health predates `root` — best effort.
pid_cwd() { # <pid>
  [ -n "$1" ] || return 0
  if [ -e "/proc/$1/cwd" ]; then
    readlink "/proc/$1/cwd" 2>/dev/null || true
  elif command -v lsof >/dev/null 2>&1; then
    lsof -a -p "$1" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -n 1 || true
  fi
}

# A service already answers on addr: reuse it only if it was launched from
# this checkout, else stop with what's running and how to get out of it.
check_reuse() { # <addr> <display name> <extra stop note>
  local addr="$1" name="$2" note="$3" body root started pid where
  body="$(curl -sf "http://$addr/health" 2>/dev/null || true)"
  root="$(health_field "$body" root)"
  started="$(health_field "$body" started_at)"
  [ -n "$root" ] && [ -d "$root" ] && root="$(cd "$root" && pwd -P)"
  if [ "$root" = "$REPO_ROOT" ] || [ "${MADS_REUSE_SERVICES:-}" = 1 ]; then
    echo "app.sh: reusing the $name already running on $addr${root:+ from $root}${started:+ (started $started)} — not rebuilt, so restart it to pick up Go changes"
    return 0
  fi
  pid="$(listener_pid "$addr")"
  local why="This checkout is $REPO_ROOT, so you'd be looking at the other checkout's code."
  if [ -n "$root" ]; then
    where="a $name from $root is already running on $addr${started:+ (started $started)}"
  else
    where="a $name is already running on $addr, but it is an older build that doesn't report which checkout it is from"
    why="This checkout is $REPO_ROOT; that one may be serving another checkout's code, and is at least older than this one's."
    local cwd
    cwd="$(pid_cwd "$pid")"
    [ -n "$cwd" ] && where="$where (its working directory is $cwd)"
  fi
  {
    echo "app.sh: $where."
    echo "$why"
    echo "Quit the app and stop that $name${pid:+ (pid $pid)} first${note}, or set MADS_REUSE_SERVICES=1 to use it anyway."
  } >&2
  exit 1
}

# Check both before starting either, so a refusal leaves nothing behind.
data_running=0 router_running=0
if up "http://$DATA_ADDR/health"; then
  data_running=1
  check_reuse "$DATA_ADDR" "Data Service" ""
fi
if up "http://$ROUTER_ADDR/health"; then
  router_running=1
  check_reuse "$ROUTER_ADDR" "Router" \
    " (stopping it also ends the agent sessions and pipeline runs it is running: a turn in progress is cut off, and sessions wake again on your next message)"
fi

if [ "$data_running" = 0 ]; then
  go build -o bin/data ./services/data
  ./bin/data -addr "$DATA_ADDR" >> /tmp/mads-data.log 2>&1 &
  started+=($!)
  wait_up "http://$DATA_ADDR/health" "$!" "data service" /tmp/mads-data.log
  echo "app.sh: started data service ($DATA_ADDR)"
fi

if [ "$router_running" = 0 ]; then
  go build -o bin/router ./services/router
  go build -o bin/session ./services/session
  ./bin/router -addr "$ROUTER_ADDR" -session-cmd ./bin/session >> /tmp/mads-router.log 2>&1 &
  started+=($!)
  wait_up "http://$ROUTER_ADDR/health" "$!" "router" /tmp/mads-router.log
  echo "app.sh: started router ($ROUTER_ADDR)"
fi

# Electron has no postinstall and `npm ci` (via `make build`) wipes its
# lazily-downloaded binary, so it can be missing again since the last launch.
# Install it here rather than trusting install day; it's a cheap stat when
# nothing is wrong. Same call decides
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
