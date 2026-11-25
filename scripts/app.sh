#!/usr/bin/env bash
# The local end-to-end entry point (until W7 packaging does this properly):
# ensures the Data Service, Router and Mesh Service are up, then launches the
# Electron app.
# Services it starts are stopped again when the app exits; services that were
# already running are left alone — but only reused if they were launched from
# THIS checkout (their /health reports it), since the Router serves the UI
# from its own cwd's ui/dist: a Router left running by another checkout would
# show that checkout's UI and backend with no hint. MADS_REUSE_SERVICES=1
# reuses whatever is running anyway; --restart stops whatever is running and
# starts fresh services from this checkout. See usage() below.
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/app.sh [--restart]

Start the Data Service, Router and Mesh Service (built from this checkout)
if they aren't running, then launch the Electron app. Services this script
starts are stopped again when the app exits. The Mesh Service is how other
devices reach this one; it listens only on this device's mesh network
(Settings -> Paired devices), nowhere until one is set up.

First makes sure the install token exists ($MADS_HOME/local.token, default
~/.mads; see docs/LOCAL-FRONT-DOOR-DESIGN.md): the services and the app need it.

A service already answering on its port is reused only if its /health says it
was launched from this checkout (it is not rebuilt; restart it to pick up Go
changes). One from another checkout, or an older build that doesn't say, is
refused. The Mesh Service has no local port: it's found by the pid in
$MADS_HOME/run/mesh.lock, and reused only if it runs from this checkout.

Options:
  --restart   First stop the Mesh Service and whatever is listening on the
              Router and Data Service ports (any checkout, any build;
              SIGTERM, Mesh Service, then Router, then Data Service), then
              build and start fresh ones from this checkout. Stopping the
              Router cuts off any agent turn in progress; sessions start
              again on their next message.
  -h, --help  Show this help.

Environment:
  MADS_REUSE_SERVICES=1  Reuse running services even if they come from
                         another checkout or an older build.
  MADS_DATA_ADDR         Data Service address (default localhost:17374).
  MADS_ROUTER_ADDR       Router address (default localhost:17373).
USAGE
}

restart=0
for arg in "$@"; do
  case "$arg" in
    --restart) restart=1 ;;
    -h | --help) usage; exit 0 ;;
    *)
      echo "app.sh: unknown argument: $arg" >&2
      usage >&2
      exit 2
      ;;
  esac
done

# Physical path (-P): services report os.Getwd(), and the comparison below
# must not trip over a symlinked checkout.
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$REPO_ROOT"

DATA_ADDR="${MADS_DATA_ADDR:-localhost:17374}"
ROUTER_ADDR="${MADS_ROUTER_ADDR:-localhost:17373}"

# Every build here uses exactly go.mod's toolchain, downloading it if this
# machine's Go is another version: services built with different Go
# versions behaved differently over the mesh (HTTP/2 on pinned connections,
# before Go 1.27). GOTOOLCHAIN set by the caller wins.
if [ -z "${GOTOOLCHAIN:-}" ]; then
  GOTOOLCHAIN="$(sed -n 's/^toolchain //p' go.mod)"
  [ -n "$GOTOOLCHAIN" ] && export GOTOOLCHAIN
fi

# The local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md D7): every Router and
# Data Service API call needs the install token in $MADS_HOME/local.token.
# Make sure it exists before anything starts (the services would create it
# too, but this also covers reusing services and Electron's first read), then
# read it for our own /health calls. `mads token ensure` refuses a token file
# other users can read.
MADS_HOME_DIR="${MADS_HOME:-$HOME/.mads}"
go build -o bin/mads ./cmd/mads
MADS_HOME="$MADS_HOME_DIR" ./bin/mads token ensure >/dev/null
TOKEN=""
IFS= read -r TOKEN < "$MADS_HOME_DIR/local.token" || true

# curl with the token, which goes in on stdin (printf is a builtin), never in
# an argv another user could read with ps.
authed_curl() {
  printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" | curl -K - "$@"
}

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
  # root and started_at are only for the token's holder.
  body="$(authed_curl -sf "http://$addr/health" 2>/dev/null || true)"
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
    echo "Run scripts/app.sh --restart to stop it and start this checkout's services${note}, or quit the app and stop that $name${pid:+ (pid $pid)} yourself, or set MADS_REUSE_SERVICES=1 to use it anyway."
  } >&2
  exit 1
}

# The Mesh Service: one per mads home, holding $MADS_HOME/run/mesh.lock
# with its pid (it has no local port to find it by, and may listen nowhere).
MESH_LOCK="$MADS_HOME_DIR/run/mesh.lock"

# The running Mesh Service's pid, or empty: the lock file's pid, if that
# process is alive and is a mesh binary (a stale file's pid may be reused).
mesh_pid() {
  local pid comm
  [ -f "$MESH_LOCK" ] || return 0
  IFS= read -r pid < "$MESH_LOCK" || true
  case "$pid" in '' | *[!0-9]*) return 0 ;; esac
  kill -0 "$pid" 2>/dev/null || return 0
  comm="$(ps -o comm= -p "$pid" 2>/dev/null || true)"
  case "$comm" in mesh | */mesh) echo "$pid" ;; esac
}

# --restart: SIGTERM the running Mesh Service and wait for it to go.
stop_mesh() {
  local pid root i
  pid="$(mesh_pid)"
  [ -n "$pid" ] || return 0
  root="$(pid_cwd "$pid")"
  if ! kill -TERM "$pid" 2>/dev/null; then
    echo "app.sh: could not signal the Mesh Service (pid $pid); stop it yourself and run app.sh again." >&2
    exit 1
  fi
  for ((i = 0; i < 50; i++)); do # 50 * 0.2s = 10s
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "app.sh: stopped Mesh Service pid $pid${root:+ from $root}"
      return 0
    fi
    sleep 0.2
  done
  echo "app.sh: the Mesh Service (pid $pid) is still running 10s after SIGTERM; stop it yourself and run app.sh again." >&2
  exit 1
}

# A Mesh Service is already running: reuse it only if it runs from this
# checkout (its working directory), as for the others.
check_mesh_reuse() { # <pid>
  local pid="$1" cwd
  cwd="$(pid_cwd "$pid")"
  [ -n "$cwd" ] && [ -d "$cwd" ] && cwd="$(cd "$cwd" && pwd -P)"
  if [ "$cwd" = "$REPO_ROOT" ] || [ "${MADS_REUSE_SERVICES:-}" = 1 ]; then
    echo "app.sh: reusing the Mesh Service already running (pid $pid)${cwd:+ from $cwd} — not rebuilt, so restart it to pick up Go changes"
    return 0
  fi
  {
    echo "app.sh: a Mesh Service is already running (pid $pid)${cwd:+ from $cwd}."
    echo "This checkout is $REPO_ROOT, so other devices would reach the other checkout's code."
    echo "Run scripts/app.sh --restart to stop it and start this checkout's, or stop pid $pid yourself, or set MADS_REUSE_SERVICES=1 to use it anyway."
  } >&2
  exit 1
}

ROUTER_STOP_NOTE="stopping the Router also ends the agent sessions and pipeline runs it is running: a turn in progress is cut off, and sessions start again on their next message"

# --restart: SIGTERM whatever LISTENS on addr's port — by port, never by
# process name — and wait for the port to free. SIGTERM only: the Router
# needs it to shut its session/pipeline children down cleanly, so a process
# that won't go is reported, not SIGKILLed.
stop_listener() { # <addr> <display name>
  local addr="$1" name="$2" pid body root started i
  if ! command -v lsof >/dev/null 2>&1; then
    if up "http://$addr/health"; then
      echo "app.sh: --restart needs lsof to find the $name listening on $addr; stop it yourself and run app.sh again." >&2
      exit 1
    fi
    return 0
  fi
  pid="$(listener_pid "$addr")"
  if [ -z "$pid" ]; then
    if up "http://$addr/health"; then
      echo "app.sh: something answers on $addr but lsof shows no listening pid for it (another user's process?); stop it yourself and run app.sh again." >&2
      exit 1
    fi
    return 0
  fi
  body="$(authed_curl -sf "http://$addr/health" 2>/dev/null || true)"
  root="$(health_field "$body" root)"
  started="$(health_field "$body" started_at)"
  [ -n "$root" ] || root="$(pid_cwd "$pid")"
  if ! kill -TERM "$pid" 2>/dev/null; then
    echo "app.sh: could not signal the $name (pid $pid) on $addr; stop it yourself and run app.sh again." >&2
    exit 1
  fi
  for ((i = 0; i < 50; i++)); do # 50 * 0.2s = 10s
    if [ -z "$(listener_pid "$addr")" ]; then
      echo "app.sh: stopped $name pid $pid${root:+ from $root}${started:+ (started $started)}"
      return 0
    fi
    sleep 0.2
  done
  echo "app.sh: the $name (pid $pid) on $addr is still running 10s after SIGTERM; stop it yourself and run app.sh again." >&2
  exit 1
}

if [ "$restart" = 1 ]; then
  # Mesh Service, then Router, so neither runs against a missing service.
  stop_mesh
  stop_listener "$ROUTER_ADDR" "Router"
  stop_listener "$DATA_ADDR" "Data Service"
fi

# Check all three before starting any, so a refusal leaves nothing behind.
data_running=0 router_running=0 mesh_running=0
if up "http://$DATA_ADDR/health"; then
  data_running=1
  check_reuse "$DATA_ADDR" "Data Service" ""
fi
if up "http://$ROUTER_ADDR/health"; then
  router_running=1
  check_reuse "$ROUTER_ADDR" "Router" " ($ROUTER_STOP_NOTE)"
fi
running_mesh="$(mesh_pid)"
if [ -n "$running_mesh" ]; then
  mesh_running=1
  check_mesh_reuse "$running_mesh"
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
  ./bin/router -addr "$ROUTER_ADDR" -data-url "http://$DATA_ADDR" -session-cmd ./bin/session >> /tmp/mads-router.log 2>&1 &
  started+=($!)
  wait_up "http://$ROUTER_ADDR/health" "$!" "router" /tmp/mads-router.log
  echo "app.sh: started router ($ROUTER_ADDR)"
fi

if [ "$mesh_running" = 0 ]; then
  go build -o bin/mesh ./services/mesh
  ./bin/mesh -router-url "http://$ROUTER_ADDR" -data-url "http://$DATA_ADDR" >> /tmp/mads-mesh.log 2>&1 &
  started+=($!)
  # No local port to wait on: it's up once it holds the lock (it then
  # follows the Data Service and Router by itself).
  mesh_up=0
  for ((i = 0; i < 50; i++)); do # 50 * 0.2s = 10s
    if ! kill -0 "$!" 2>/dev/null; then
      echo "app.sh: Mesh Service exited during startup — last lines of /tmp/mads-mesh.log:" >&2
      tail -n 10 /tmp/mads-mesh.log >&2 || true
      exit 1
    fi
    if [ "$(mesh_pid)" = "$!" ]; then mesh_up=1; break; fi
    sleep 0.2
  done
  if [ "$mesh_up" = 0 ]; then
    echo "app.sh: Mesh Service didn't take $MESH_LOCK within 10s — last lines of /tmp/mads-mesh.log:" >&2
    tail -n 10 /tmp/mads-mesh.log >&2 || true
    exit 1
  fi
  echo "app.sh: started Mesh Service"
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
# On macOS, launch the copy of Electron.app branded as mads (Dock label,
# menu bar, icon); a failure there is cosmetic, so fall back to the stock one.
app_bin="$(mads_app_bin)"
if ! mads_brand_app; then
  echo "app.sh: launching the stock Electron.app instead" >&2
  app_bin="$(mads_electron_bin)"
fi

# The Electron window loads the Router's own address; keep it on ROUTER_ADDR
# so MADS_ROUTER_ADDR moves the whole app, not just the backend. Exported
# before the build so it reaches both `vite build` and Electron.
export VITE_ROUTER_URL="${VITE_ROUTER_URL:-http://$ROUTER_ADDR}"

# What `npm --prefix ui start` does (build, then `electron .` from ui/), but
# with the binary chosen above rather than the one in electron's path.txt.
npm --prefix ui run build
(cd ui && "$app_bin" .)
