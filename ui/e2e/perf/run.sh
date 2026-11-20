#!/bin/sh
# make ui-perf — the perf harness runner (docs/PERF-HARNESS-PLAN.md §6).
#
#   make ui-perf BASE=<ref> CAND=<ref|worktree dir> PROFILE=S|M|L|XL N=5 BROWSER=chromium|electron
#   (also SCENARIOS=1,3 to pick scenarios, PERF_DELAYS=off|<regex>=<ms>,...)
#
# Builds each ref's Go binaries and UI (VITE_ROUTER_URL="", same-origin) into
# its own temp dir — a ref is checked out with `git worktree add` into a
# temp dir and removed afterwards; a worktree dir is built in place (only its
# node_modules may be installed, and the UI build goes to the temp dir).
# Seeds ONE workload database per run with perfseed from this tree; every
# repetition boots on a fresh copy of it. Writes perf-results/<timestamp>/.
#
# Every stack is hermetic (lib/topology.ts rules): temp HOME/MADS_HOME,
# ephemeral ports, an empty MADS_AGENT_BIN_DIR. Nothing here reads or writes
# ~/.mads or the live ports.
set -eu

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
BASE=${BASE:-main}
CAND=${CAND:-}
PROFILE=${PROFILE:-S}
N=${N:-5}
BROWSER=${BROWSER:-chromium}
SCENARIOS=${SCENARIOS:-}
PERF_DELAYS=${PERF_DELAYS:-}
PERF_CDP_SSE=${PERF_CDP_SSE:-}
# Checkouts the harness must never build in or install into (the live app
# runs from them). Colon-separated; override for another machine.
PERF_FORBIDDEN_DIRS=${PERF_FORBIDDEN_DIRS:-$HOME/repo/mads:$HOME/repo/mads-mesh}

PROFILE_JSON="$ROOT/ui/e2e/perf/profiles/$PROFILE.json"
[ -f "$PROFILE_JSON" ] || { echo "ui-perf: no profile $PROFILE_JSON" >&2; exit 2; }
case "$BROWSER" in chromium|electron) ;; *) echo "ui-perf: BROWSER must be chromium or electron" >&2; exit 2;; esac

STAMP=$(date +%Y%m%d-%H%M%S)
RUN_DIR="$ROOT/perf-results/$STAMP"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/mads-perf.XXXXXX")
mkdir -p "$RUN_DIR"
WORKTREES=""

cleanup() {
	for wt in $WORKTREES; do
		git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || rm -rf "$wt"
	done
	git -C "$ROOT" worktree prune >/dev/null 2>&1 || true
	rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

forbidden() {
	real=$(cd "$1" && pwd -P)
	old_ifs=$IFS
	IFS=:
	for f in $PERF_FORBIDDEN_DIRS; do
		[ -d "$f" ] || continue
		if [ "$real" = "$(cd "$f" && pwd -P)" ]; then IFS=$old_ifs; return 0; fi
	done
	IFS=$old_ifs
	return 1
}

# build <label> <ref-or-dir> → writes the build's plan JSON to $TMP/build-<label>.json.
# Runs in this shell, not a subshell: WORKTREES must survive for cleanup.
build() {
	label=$1
	spec=$2
	out="$TMP/build-$label"
	mkdir -p "$out/bin"
	if [ -d "$spec" ]; then
		src=$(cd "$spec" && pwd -P)
		forbidden "$src" && { echo "ui-perf: refusing to build in live checkout $src" >&2; exit 2; }
		desc="worktree $src @ $(git -C "$src" rev-parse --short HEAD)$(git -C "$src" diff --quiet HEAD || echo ' (dirty)')"
	else
		sha=$(git -C "$ROOT" rev-parse --verify "$spec^{commit}")
		src="$TMP/src-$label"
		git -C "$ROOT" worktree add --detach "$src" "$sha" >/dev/null
		WORKTREES="$WORKTREES $src"
		desc="$spec @ $(git -C "$ROOT" rev-parse --short "$sha")"
	fi
	echo "ui-perf: building $label: $desc" >&2
	for svc in data router session pipeline; do
		(cd "$src" && go build -o "$out/bin/$svc" "./services/$svc")
	done
	if [ ! -d "$src/ui/node_modules" ]; then
		# Never a symlink (an `npm ci` through one emptied a checkout): a
		# copy-on-write clone of this tree's modules when the lockfile is the
		# same, a real `npm ci` otherwise.
		if cmp -s "$src/ui/package-lock.json" "$ROOT/ui/package-lock.json" && [ -d "$ROOT/ui/node_modules" ] &&
			cp -cR "$ROOT/ui/node_modules" "$src/ui/node_modules" 2>/dev/null; then
			:
		else
			rm -rf "$src/ui/node_modules"
			npm --prefix "$src/ui" ci >&2
		fi
	fi
	# Same-origin build: the page calls whatever origin served it (the delay
	# proxy), never the localhost:17373 default.
	(cd "$src/ui" && VITE_ROUTER_URL="" npx vite build --outDir "$out/dist" --emptyOutDir --logLevel warn >&2)
	uidir=""
	if [ "$BROWSER" = electron ]; then
		(cd "$src/ui" && npx tsc -p electron/tsconfig.json >&2)
		uidir="$src/ui"
	fi
	printf '{"label":"%s","binDir":"%s","uiDist":"%s","uiDir":"%s","desc":"%s"}' "$label" "$out/bin" "$out/dist" "$uidir" "$desc" >"$TMP/build-$label.json"
}

build base "$BASE"
BUILDS="[$(cat "$TMP/build-base.json")"
if [ -n "$CAND" ]; then
	build cand "$CAND"
	BUILDS="$BUILDS,$(cat "$TMP/build-cand.json")"
fi
BUILDS="$BUILDS]"

# One workload per run: a git repo for the workspace, then the database.
WL="$TMP/workload"
mkdir -p "$WL/repo"
git -C "$WL/repo" init -q -b main
git -C "$WL/repo" -c user.name=perf -c user.email=perf@perf.test commit -q --allow-empty -m "perf workspace"
(cd "$ROOT" && go build -o "$TMP/perfseed" ./services/data/cmd/perfseed)
"$TMP/perfseed" seed -db "$WL/mads.db" -profile "$PROFILE_JSON" -repo "$WL/repo" -manifest "$WL/manifest.json"
cp "$WL/manifest.json" "$RUN_DIR/manifest.json"

scen_json="[]"
if [ -n "$SCENARIOS" ]; then
	scen_json="[\"$(echo "$SCENARIOS" | sed 's/,/","/g')\"]"
fi
cat >"$RUN_DIR/plan.json" <<EOF
{
  "runDir": "$RUN_DIR",
  "seededDB": "$WL/mads.db",
  "manifestPath": "$WL/manifest.json",
  "builds": $BUILDS,
  "scenarios": $scen_json,
  "n": $N,
  "browser": "$BROWSER",
  "delays": "$PERF_DELAYS",
  "cdpSse": $([ -n "$PERF_CDP_SSE" ] && echo true || echo false),
  "meta": {"profile": "$PROFILE", "harness": "$(git -C "$ROOT" rev-parse --short HEAD)", "host": "$(uname -sm)"}
}
EOF

export PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-$HOME/.cache/ms-playwright}"
[ "$BROWSER" = chromium ] && npx --prefix "$ROOT/ui" playwright install chromium >&2
status=0
(cd "$ROOT/ui" && PERF_PLAN="$RUN_DIR/plan.json" npx playwright test --config e2e/perf/playwright.perf.config.ts) || status=$?
echo "ui-perf: results in $RUN_DIR"
[ -f "$RUN_DIR/report.md" ] && cat "$RUN_DIR/report.md"
exit $status
