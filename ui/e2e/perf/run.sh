#!/bin/sh
# make ui-perf — the perf harness runner (docs/PERF-HARNESS-PLAN.md §6).
#
#   make ui-perf BASE=<ref> CAND=<ref|worktree dir> PROFILE=S|M|L|XL N=5 BROWSER=chromium|electron
#   also: SCENARIOS=1,3b  MODES=on,off (default both)  PERF_DELAYS=<regex>=<ms>,...
#         PERF_HOLD_OUTPUT=0  PERF_CDP_SSE=1  PERF_LEGACY_ROUTE_GUARD=1 (B1 comparison only)
#
# Builds each side's Go binaries and UI (VITE_ROUTER_URL="", same-origin)
# into its own temp dir: a ref is checked out detached with `git worktree
# add` into the temp dir and removed by path afterwards; a worktree dir is
# built from in place (its node_modules installed only if missing; the UI
# build goes to the temp dir). Seeds ONE workload per run with perfseed
# built from BASE's tree (this tree's perfseed source laid over it with
# `go build -overlay`, so the schema is BASE's own); every repetition boots
# on a fresh copy. Writes perf-results/<timestamp>/.
#
# Every stack is hermetic (lib/topology.ts rules): temp HOME/MADS_HOME,
# ephemeral ports, an empty MADS_AGENT_BIN_DIR. Every process a repetition
# starts is listed in the pid registry, and this script kills whatever is
# left there when Playwright exits, however it exits.
set -eu

ROOT=$(cd "$(dirname "$0")/../../.." && pwd -P)
BASE=${BASE:-main}
CAND=${CAND:-}
PROFILE=${PROFILE:-S}
N=${N:-5}
BROWSER=${BROWSER:-chromium}
SCENARIOS=${SCENARIOS:-}
MODES=${MODES:-on,off}
PERF_DELAYS=${PERF_DELAYS:-}
PERF_HOLD_OUTPUT=${PERF_HOLD_OUTPUT:-1}
PERF_CDP_SSE=${PERF_CDP_SSE:-}
PERF_LEGACY_ROUTE_GUARD=${PERF_LEGACY_ROUTE_GUARD:-}
# Checkouts the harness must never run from, build in or install into (the
# live app runs from them): the dirs themselves and anything under them.
PERF_FORBIDDEN_DIRS=${PERF_FORBIDDEN_DIRS:-$HOME/repo/mads:$HOME/repo/mads-mesh}

STATUS=2
die() {
	echo "ui-perf: $*" >&2
	STATUS=2
	exit 2
}

# under <path>: is it a forbidden dir or inside one? (path components, not string prefixes)
under_forbidden() {
	p=$1
	old_ifs=$IFS
	IFS=:
	for f in $PERF_FORBIDDEN_DIRS; do
		[ -n "$f" ] || continue
		fr=$( (cd "$f" 2>/dev/null && pwd -P) || echo "$f")
		case "$p/" in "$fr"/*)
			IFS=$old_ifs
			return 0
			;;
		esac
	done
	IFS=$old_ifs
	return 1
}

# --- Validate everything before touching anything.
under_forbidden "$ROOT" && die "refusing to run from $ROOT: it is (or is under) a live checkout ($PERF_FORBIDDEN_DIRS)"
PROFILE_JSON="$ROOT/ui/e2e/perf/profiles/$PROFILE.json"
[ -f "$PROFILE_JSON" ] || die "no profile $PROFILE_JSON"
case "$BROWSER" in chromium | electron) ;; *) die "BROWSER must be chromium or electron" ;; esac
case "$N" in '' | *[!0-9]*) die "N must be a number" ;; esac
for m in $(echo "$MODES" | tr ',' ' '); do case "$m" in on | off) ;; *) die "MODES: $m is not on or off" ;; esac; done

# resolve <spec> → "dir <path>" or "ref <sha>"
resolve() {
	if [ -d "$1" ]; then
		d=$(cd "$1" && pwd -P)
		under_forbidden "$d" && die "refusing to build in $d: it is (or is under) a live checkout"
		git -C "$d" rev-parse --verify HEAD >/dev/null 2>&1 || die "$1 is a directory but not a git checkout"
		echo "dir $d"
	else
		sha=$(git -C "$ROOT" rev-parse --verify --quiet "$1^{commit}") || die "no such ref or directory: $1"
		echo "ref $sha"
	fi
}
BASE_RES=$(resolve "$BASE")
CAND_RES=""
[ -n "$CAND" ] && CAND_RES=$(resolve "$CAND")

# --- From here on, everything made is cleaned up.
STAMP=$(date +%Y%m%d-%H%M%S)
RUN_DIR="$ROOT/perf-results/$STAMP"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/mads-perf.XXXXXX")
TMP=$(cd "$TMP" && pwd -P)
PIDS="$TMP/pids"
mkdir -p "$PIDS" "$TMP/tmp"
WORKTREES=""
STATUS=1

# Kills a pid and every descendant: TERM, a grace period, then KILL.
killtree() {
	kids=$(pgrep -P "$1" 2>/dev/null || true)
	for k in $kids; do killtree "$k"; done
	kill -TERM "$1" 2>/dev/null || true
}
# verified <pid> <lstart> <command>: is that pid still the process we
# registered (same start time, same full command), not a recycled one?
verified() {
	[ "$(ps -ww -o lstart= -p "$1" 2>/dev/null | sed 's/^ *//;s/ *$//')" = "$2" ] &&
		[ "$(ps -ww -o command= -p "$1" 2>/dev/null | sed 's/^ *//;s/ *$//')" = "$3" ]
}
# The pid registry: a file per repetition still running or never torn down
# (a clean teardown deletes its own). Lines: pid<TAB>pid<TAB>lstart<TAB>name<TAB>command, dir<TAB>path.
reap() {
	for sig in TERM KILL; do
		for f in "$PIDS"/*.txt; do
			[ -f "$f" ] || continue
			while IFS="$(printf '\t')" read -r kind a b _name c; do
				[ "$kind" = pid ] || continue
				if verified "$a" "$b" "$c"; then
					if [ $sig = TERM ]; then killtree "$a"; else kill -KILL "$a" 2>/dev/null || true; fi
				fi
			done <"$f"
		done
		[ $sig = TERM ] && sleep 2
	done
	for f in "$PIDS"/*.txt; do
		[ -f "$f" ] || continue
		while IFS="$(printf '\t')" read -r kind a _rest; do
			[ "$kind" = dir ] && rm -rf "$a"
		done <"$f"
		rm -f "$f"
	done
	# Anything still running out of this run's temp tree (binaries, browser
	# profiles under TMPDIR) is ours.
	pkill -KILL -f "$TMP/" 2>/dev/null || true
}
cleanup() {
	trap - EXIT INT TERM
	reap
	for wt in $WORKTREES; do
		git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
	done
	rm -rf "$TMP"
	# A refused or failed run leaves no empty results dir behind.
	if [ -d "$RUN_DIR" ] && [ ! -d "$RUN_DIR/raw" ]; then rm -rf "$RUN_DIR"; fi
	exit "$STATUS"
}
trap cleanup EXIT
trap 'STATUS=130; cleanup' INT TERM

# ui deps for the harness itself, only if missing (never a symlink).
[ -d "$ROOT/ui/node_modules" ] || npm --prefix "$ROOT/ui" ci >&2

# build <label> <resolved> → $TMP/build-<label>.json and $TMP/src-<label>.path
build() {
	label=$1
	kind=${2%% *}
	what=${2#* }
	out="$TMP/build-$label"
	mkdir -p "$out/bin"
	if [ "$kind" = dir ]; then
		src=$what
		desc="worktree $src @ $(git -C "$src" rev-parse --short HEAD)$(git -C "$src" diff --quiet HEAD || echo ' (dirty)')"
	else
		src="$TMP/src-$label"
		git -C "$ROOT" worktree add --detach "$src" "$what" >/dev/null 2>&1
		WORKTREES="$WORKTREES $src"
		desc="$( [ "$label" = base ] && echo "$BASE" || echo "$CAND") @ $(git -C "$ROOT" rev-parse --short "$what")"
	fi
	echo "$src" >"$TMP/src-$label.path"
	echo "ui-perf: building $label: $desc" >&2
	for svc in data router session pipeline; do
		(cd "$src" && go build -o "$out/bin/$svc" "./services/$svc")
	done
	if [ ! -d "$src/ui/node_modules" ]; then
		# Never a symlink (an `npm ci` through one emptied a checkout): a
		# copy-on-write clone of this tree's modules when the lockfile is the
		# same, a real `npm ci` otherwise — into the temp worktree, or into a
		# worktree dir only because it has none.
		if cmp -s "$src/ui/package-lock.json" "$ROOT/ui/package-lock.json" && cp -cR "$ROOT/ui/node_modules" "$src/ui/node_modules" 2>/dev/null; then
			:
		else
			rm -rf "$src/ui/node_modules"
			npm --prefix "$src/ui" ci >&2
		fi
	fi
	# Same-origin build: the page calls whatever origin served it (the delay
	# proxy), never the localhost:17373 default.
	(cd "$src/ui" && VITE_ROUTER_URL="" npx vite build --outDir "$out/dist" --emptyOutDir --logLevel warn >&2)
	# B1a: a bundle that names a live port could reach the live app. Refuse it.
	if grep -rlE '1737[34]|17380|localhost:173' "$out/dist" >/dev/null 2>&1; then
		die "the $label UI bundle names a live port ($(grep -rlE '1737[34]|17380|localhost:173' "$out/dist" | head -3 | tr '\n' ' ')): was it built without VITE_ROUTER_URL=\"\"?"
	fi
	uidir=""
	if [ "$BROWSER" = electron ]; then
		(cd "$src/ui" && npx tsc -p electron/tsconfig.json >&2)
		uidir="$src/ui"
	fi
	printf '{"label":"%s","binDir":"%s","uiDist":"%s","uiDir":"%s","desc":"%s"}' "$label" "$out/bin" "$out/dist" "$uidir" "$desc" >"$TMP/build-$label.json"
}

mkdir -p "$RUN_DIR"
build base "$BASE_RES"
BUILDS="[$(cat "$TMP/build-base.json")"
if [ -n "$CAND_RES" ]; then
	build cand "$CAND_RES"
	BUILDS="$BUILDS,$(cat "$TMP/build-cand.json")"
fi
BUILDS="$BUILDS]"

# perfseed from BASE's tree: this tree's perfseed source overlaid onto it, so
# it links BASE's own store and migrations (the workload's schema is BASE's;
# a newer CAND migrates its copy at boot). Nothing is written into BASE.
BASE_SRC=$(cat "$TMP/src-base.path")
OVERLAY="$TMP/perfseed-overlay.json"
{
	printf '{"Replace":{'
	sep=""
	for f in "$ROOT"/services/data/cmd/perfseed/*.go; do
		case "$f" in *_test.go) continue ;; esac
		printf '%s"%s":"%s"' "$sep" "$BASE_SRC/services/data/cmd/perfseed/$(basename "$f")" "$f"
		sep=","
	done
	printf '}}'
} >"$OVERLAY"
(cd "$BASE_SRC" && go build -overlay "$OVERLAY" -o "$TMP/perfseed" ./services/data/cmd/perfseed)

# One workload per run: a git repo for the workspace, then the database and
# the parent's delegate transcript.
WL="$TMP/workload"
mkdir -p "$WL/repo"
git -C "$WL/repo" init -q -b main
git -C "$WL/repo" -c user.name=perf -c user.email=perf@perf.test commit -q --allow-empty -m "perf workspace"
"$TMP/perfseed" seed -db "$WL/mads.db" -profile "$PROFILE_JSON" -repo "$WL/repo" -transcripts "$WL/claude-projects" -manifest "$WL/manifest.json"
cp "$WL/manifest.json" "$RUN_DIR/manifest.json"

scen_json="[]"
[ -n "$SCENARIOS" ] && scen_json="[\"$(echo "$SCENARIOS" | sed 's/,/","/g')\"]"
modes_json="[\"$(echo "$MODES" | sed 's/,/","/g')\"]"
cat >"$RUN_DIR/plan.json" <<EOF
{
  "runDir": "$RUN_DIR",
  "seededDB": "$WL/mads.db",
  "manifestPath": "$WL/manifest.json",
  "transcriptsDir": "$WL/claude-projects",
  "builds": $BUILDS,
  "scenarios": $scen_json,
  "n": $N,
  "browser": "$BROWSER",
  "modes": $modes_json,
  "delays": "$PERF_DELAYS",
  "holdOutput": $([ "$PERF_HOLD_OUTPUT" = 0 ] && echo false || echo true),
  "cdpSse": $([ -n "$PERF_CDP_SSE" ] && echo true || echo false),
  "legacyRouteGuard": $([ -n "$PERF_LEGACY_ROUTE_GUARD" ] && echo true || echo false),
  "meta": {"profile": "$PROFILE", "harness": "$(git -C "$ROOT" rev-parse --short HEAD)", "host": "$(uname -sm)"}
}
EOF

export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"
[ "$BROWSER" = chromium ] && npx --prefix "$ROOT/ui" playwright install chromium >&2
# TMPDIR inside the run's temp tree: browser profiles, Electron user data and
# every stack's scratch dirs land there, so reap() can find and remove them.
set +e
(cd "$ROOT/ui" && TMPDIR="$TMP/tmp" PERF_PIDS_DIR="$PIDS" PERF_PLAN="$RUN_DIR/plan.json" \
	npx playwright test --config e2e/perf/playwright.perf.config.ts)
pw=$?
set -e
echo "ui-perf: results in $RUN_DIR"
[ -f "$RUN_DIR/report.md" ] && cat "$RUN_DIR/report.md"
ok=$(node -e 'try{process.stdout.write(String(require(process.argv[1]).allOk))}catch{process.stdout.write("false")}' "$RUN_DIR/results.json")
if [ "$pw" -eq 0 ] && [ "$ok" = true ]; then STATUS=0; else
	echo "ui-perf: run incomplete or had failures (playwright exit $pw, all ok: $ok)" >&2
	STATUS=1
fi
