#!/bin/sh
# make ui-perf — the perf harness runner (docs/PERF-HARNESS-PLAN.md §6).
#
#   make ui-perf BASE=<ref> CAND=<ref|worktree dir> PROFILE=S|M|L|XL N=5 BROWSER=chromium|electron
#   also: SCENARIOS=1,3b | bus | git | all (default all, bus in realism; scenarioSets.sh)  MODES=on,off (default both)  PERF_DELAYS=<regex>=<ms>,...
#         PERF_LONGLIVED_N=50000 PERF_LONGLIVED_RATE=2000 (scenario 8's appended events, events/s)
#         PERF_HOLD_OUTPUT=0  PERF_PROBE=0 (lite probe everywhere) or PERF_PROBE_CAND=0 (cand only)
#         CHECKS=a,b,c,d,e|none (default all; none in realism)
#         GITFX=auto|1|0 — the real-git fixture (perfseed gitfx: local clones of
#           GITFX_SRC, default ~/repo/mads, which is only ever cloned FROM, never
#           run in) and the git shim on every stack's PATH. auto: on when a
#           scenario is 9x or a check is f, g, h or i. 9x run with delays off only.
#         REALISM=1 — the workload is a read-only backup of ~/.mads/mads.db
#           (REAL_DB to override), prepared by `perfseed realism` and deleted
#           with the run's temp dir; nothing real is committed or reported.
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
# Resolved up front (aliases, empty = all) so every decision below, the
# git fixture's included, sees the scenarios that will actually run.
# Realism has no git fixture (the two don't mix): its "all" is bus.
. "$ROOT/ui/e2e/perf/scenarioSets.sh"
SCENARIOS=$(resolve_scenarios "${SCENARIOS:-$([ -n "${REALISM:-}" ] && echo bus || echo all)}")
# Real-git scenarios alone: delays off (the mock would add to real git).
DEFAULT_MODES=off
for s in $(echo "$SCENARIOS" | tr ',' ' '); do case "$s" in 9*) ;; *) DEFAULT_MODES=on,off ;; esac; done
MODES=${MODES:-$DEFAULT_MODES}
PERF_DELAYS=${PERF_DELAYS:-}
PERF_HOLD_OUTPUT=${PERF_HOLD_OUTPUT:-1}
REALISM=${REALISM:-}
REAL_DB=${REAL_DB:-$HOME/.mads/mads.db}
CHECKS=${CHECKS:-$([ -n "$REALISM" ] && echo none || echo a,b,c,d,e)}
GITFX=${GITFX:-auto}
GITFX_SRC=${GITFX_SRC:-$HOME/repo/mads}
if [ "$GITFX" = auto ]; then
	GITFX=0
	for s in $(echo "$SCENARIOS" | tr ',' ' '); do case "$s" in 9*) GITFX=1 ;; esac; done
	for c in $(echo "$CHECKS" | tr ',' ' '); do case "$c" in f | f-* | g | g-* | h | h-* | i | i-*) GITFX=1 ;; esac; done
fi
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
if [ "$GITFX" = 1 ]; then
	[ -z "$REALISM" ] || die "GITFX and REALISM don't mix"
	# A file test, not git: the only git run against GITFX_SRC is the clone.
	[ -e "$GITFX_SRC/.git" ] || [ -f "$GITFX_SRC/HEAD" ] || die "GITFX_SRC $GITFX_SRC is not a git repo"
	REAL_GIT=$(command -v git) || die "no git on PATH"
fi

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
# Ask Spotlight not to index the run's tree (the git fixture is re-copied
# every repetition; indexing it loads the machine under the measurement).
touch "$TMP/.metadata_never_index"
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
	# Never errexit in here: one failing command (a kill of a watcher that
	# already exited) used to end the trap before the worktrees and $TMP —
	# in realism, the real database's copy — were removed.
	set +e
	[ -n "${WATCH_PID:-}" ] && kill "$WATCH_PID" 2>/dev/null || true
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
	# The hermetic fake provider is harness infrastructure: this tree's
	# providers/fake is laid over the build (go build -overlay; nothing is
	# written into it), so every build has the dialects the checks drive
	# (e.g. "permission"). The product code under test is the build's own.
	ov="$TMP/fake-overlay-$label.json"
	{
		printf '{"Replace":{'
		sep=""
		for f in "$ROOT"/providers/fake/*.go; do
			case "$f" in *_test.go) continue ;; esac
			printf '%s"%s":"%s"' "$sep" "$src/providers/fake/$(basename "$f")" "$f"
			sep=","
		done
		printf '}}'
	} >"$ov"
	for svc in data router session pipeline; do
		(cd "$src" && go build -overlay "$ov" -o "$out/bin/$svc" "./services/$svc")
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

WL="$TMP/workload"
mkdir -p "$WL"
if [ -n "$REALISM" ]; then
	# Phase 4: a read-only online backup of the real database into this
	# run's temp dir (deleted with it). The live file is only ever opened
	# with -readonly; no other ~/.mads file is touched. perfseed then
	# prepares the COPY: schema stepped down to BASE's, every record path
	# pointed at an empty scratch dir, shadow dirs dropped.
	[ -f "$REAL_DB" ] || die "no database at $REAL_DB"
	sqlite3 -readonly "$REAL_DB" ".backup '$WL/mads.db'"
	mkdir -p "$WL/safe"
	"$TMP/perfseed" realism -db "$WL/mads.db" -safe-root "$WL/safe" -manifest "$WL/manifest.json" -summary "$RUN_DIR/workload.json"
	TRANSCRIPTS=""
	PROFILE=realism
else
	# One workload per run: a git repo for the workspace, then the database
	# and the parent's delegate transcript.
	mkdir -p "$WL/repo"
	git -C "$WL/repo" init -q -b main
	git -C "$WL/repo" -c user.name=perf -c user.email=perf@perf.test commit -q --allow-empty -m "perf workspace"
	"$TMP/perfseed" seed -db "$WL/mads.db" -profile "$PROFILE_JSON" -repo "$WL/repo" -transcripts "$WL/claude-projects" -manifest "$WL/manifest.json"
	TRANSCRIPTS="$WL/claude-projects"
	if [ "$GITFX" = 1 ]; then
		# The real-git fixture: clones of GITFX_SRC (read only: one clone
		# --mirror --no-local) into the run's temp tree, sessions and
		# ledgers into the workload. Every repetition restores it from a
		# pristine APFS clone (stack.ts restoreGitFx).
		echo "ui-perf: building the git fixture from $GITFX_SRC (clone only)" >&2
		mkdir -p "$TMP/gitfx"
		"$TMP/perfseed" gitfx -db "$WL/mads.db" -manifest "$WL/manifest.json" -shape "$ROOT/ui/e2e/perf/gitfx.shape.json" \
			-src "$GITFX_SRC" -root "$TMP/gitfx/live" -guard "$TMP"
		cp -cRp "$TMP/gitfx/live" "$TMP/gitfx/pristine"
		(cd "$ROOT" && go build -o "$TMP/gitshim/git" ./ui/e2e/perf/gitshim)
	fi
	cp "$WL/manifest.json" "$RUN_DIR/manifest.json"
fi

scen_json="[]"
[ -n "$SCENARIOS" ] && scen_json="[\"$(echo "$SCENARIOS" | sed 's/,/","/g')\"]"
modes_json="[\"$(echo "$MODES" | sed 's/,/","/g')\"]"
checks_json="[]"
[ "$CHECKS" != none ] && [ -n "$CHECKS" ] && checks_json="[\"$(echo "$CHECKS" | sed 's/,/","/g')\"]"
cat >"$RUN_DIR/plan.json" <<EOF
{
  "runDir": "$RUN_DIR",
  "seededDB": "$WL/mads.db",
  "manifestPath": "$WL/manifest.json",
  "transcriptsDir": "$TRANSCRIPTS",
  "builds": $BUILDS,
  "scenarios": $scen_json,
  "n": $N,
  "browser": "$BROWSER",
  "modes": $modes_json,
  "delays": "$PERF_DELAYS",
  "holdOutput": $([ "$PERF_HOLD_OUTPUT" = 0 ] && echo false || echo true),
  "checks": $checks_json,
  "realism": $([ -n "$REALISM" ] && echo true || echo false),
  "gitFx": $([ "$GITFX" = 1 ] && printf '{"root": "%s", "pristine": "%s"}' "$TMP/gitfx/live" "$TMP/gitfx/pristine" || echo null),
  "meta": {"profile": "$PROFILE", "harness": "$(git -C "$ROOT" rev-parse --short HEAD)", "host": "$(uname -sm)", "probe": "$([ "${PERF_PROBE:-}" = 0 ] && echo "lite (markers only) on every side" || ([ "${PERF_PROBE_CAND:-}" = 0 ] && echo "full on base, lite (markers only) on cand" || echo full))"}
}
EOF

export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"
[ "$BROWSER" = chromium ] && npx --prefix "$ROOT/ui" playwright install chromium >&2
# TMPDIR inside the run's temp tree: browser profiles, Electron user data and
# every stack's scratch dirs land there, so reap() can find and remove them.
set +e
# Realism: watch every stack process for git/agent processes, files outside
# the run's temp tree, and non-loopback connections, the whole run long.
WATCH_STOP="$TMP/watch.stop"
if [ -n "$REALISM" ]; then
	sh "$ROOT/ui/e2e/perf/isolationWatch.sh" "$PIDS" "$TMP" "$WATCH_STOP" "$RUN_DIR/isolation.json" &
	WATCH_PID=$!
fi
# GIT_CEILING_DIRECTORIES: no git a stack runs can discover a repo above the run's temp tree.
# PERF_PERFSEED / PERF_PROFILE_JSON: scenario 8 appends live events with
# `perfseed append` (not in realism: there is no profile to draw from).
PERF_PROFILE_FOR_APPEND=$([ -n "$REALISM" ] || echo "$PROFILE_JSON")
(cd "$ROOT/ui" && TMPDIR="$TMP/tmp" PERF_PIDS_DIR="$PIDS" PERF_PLAN="$RUN_DIR/plan.json" PERF_GIT_CEILING="$TMP" \
	PERF_PERFSEED="$TMP/perfseed" PERF_PROFILE_JSON="$PERF_PROFILE_FOR_APPEND" \
	PERF_GIT_SHIM_DIR="$([ "$GITFX" = 1 ] && echo "$TMP/gitshim")" PERF_REAL_GIT="${REAL_GIT:-}" \
	PERF_STACK_LOG_DIR="$([ -z "$REALISM" ] && echo "$RUN_DIR/stack-logs")" \
	npx playwright test --config e2e/perf/playwright.perf.config.ts)
pw=$?
if [ -n "$REALISM" ]; then
	touch "$WATCH_STOP"
	wait "$WATCH_PID"
	cat "$RUN_DIR/isolation.json" 2>/dev/null
fi
set -e
echo "ui-perf: results in $RUN_DIR"
[ -f "$RUN_DIR/report.md" ] && cat "$RUN_DIR/report.md"
ok=$(node -e 'try{process.stdout.write(String(require(process.argv[1]).allOk))}catch{process.stdout.write("false")}' "$RUN_DIR/results.json")
if [ "$pw" -eq 0 ] && [ "$ok" = true ]; then STATUS=0; else
	echo "ui-perf: run incomplete or had failures (playwright exit $pw, all ok: $ok)" >&2
	STATUS=1
fi
