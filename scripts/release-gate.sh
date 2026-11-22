#!/usr/bin/env sh
# mads release gate (W7.7): proves a candidate build is releasable before it
# ships. Builds the cross-platform matrix (build-only for non-native
# combos), health-checks the native candidate, then runs the install/
# first-run/uninstall smoke (W7.2) and the update-apply smoke (W7.5) against
# it. Exits non-zero on the first failing step.
#
#   sh scripts/release-gate.sh
#
# Env overrides:
#   MADS_RELEASE_CANDIDATE_DIR   skip the build matrix; test this dir instead
#                                 (must contain a "mads" binary — used by the
#                                 negative-proof test in scripts/releasegate
#                                 so it doesn't have to redo a multi-minute
#                                 cross-compile just to prove a broken
#                                 binary fails the gate)
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

PLATFORMS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"
# The programs a release ships (scripts/releasegate/devtools_test.go reads
# this line too).
RELEASE_PKGS="cmd/mads services/data services/router services/session services/pipeline"
# Package trees no release program may link (dev-only: the perf harness's
# tools, its seeder, the UI's e2e suites). Keep in step with devOnlyTrees
# in scripts/releasegate/devtools_test.go.
MODULE="$(go list -m)"
DEV_ONLY_PKGS="^$MODULE/(tools(/|\$)|services/data/cmd/perfseed(/|\$)|ui/e2e(/|\$))"
NATIVE_GOOS="$(go env GOOS)"
NATIVE_GOARCH="$(go env GOARCH)"

log() { printf 'release-gate: %s\n' "$*" >&2; }
die() { log "$*"; exit 1; }

build_matrix() {
    dist="$ROOT/dist"
    native="$dist/native"
    rm -rf "$dist"
    mkdir -p "$native"

    for platform in $PLATFORMS; do
        os="${platform%/*}"
        arch="${platform#*/}"
        if [ "$os" = "$NATIVE_GOOS" ] && [ "$arch" = "$NATIVE_GOARCH" ]; then
            log "building native candidate ($os/$arch) -> $native"
            for pkg in $RELEASE_PKGS; do
                name="$(basename "$pkg")"
                CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o "$native/$name" "./$pkg" \
                    || die "build $pkg for $os/$arch failed"
            done
        else
            log "build-only check: $os/$arch"
            CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o /dev/null ./cmd/mads \
                || die "build ./cmd/mads for $os/$arch failed"
        fi
    done

    check_no_perf_strings "$native"
    printf '%s\n' "$native"
}

# The fake provider ships, but its perf-only dialects (providers/fake/perf.go,
# -tags perf) must not: no release binary may carry their marker strings.
check_no_perf_strings() {
    for bin in "$1"/*; do
        if grep -aqE 'MADS_FAKE_START_DELAY_MS|the permission dialect always asks' "$bin"; then
            die "FATAL: $bin contains perf-only fake-provider code (built with -tags perf?)"
        fi
    done
    log "no release binary contains perf-only code"
}

# Dev-only code must never reach a release: no release program's dependency
# graph (as released: no build tags) may contain a dev-only package. Plus
# services/mesh, which `make build` builds too.
check_no_dev_deps() {
    for pkg in $RELEASE_PKGS services/mesh; do
        deps="$(go list -deps -f '{{.ImportPath}}' "./$pkg")" || die "go list -deps ./$pkg failed"
        bad="$(printf '%s\n' "$deps" | grep -E "$DEV_ONLY_PKGS" || true)"
        if [ -n "$bad" ]; then
            log "FATAL: release program $pkg links dev-only packages:"
            printf '%s\n' "$bad" | sed 's/^/release-gate:     /' >&2
            die "dev-only code (tools/, services/data/cmd/perfseed, ui/e2e) must never ship"
        fi
    done
    log "no release program links dev-only packages"
}
check_no_dev_deps

if [ -n "${MADS_RELEASE_CANDIDATE_DIR:-}" ]; then
    log "using pre-built candidate: $MADS_RELEASE_CANDIDATE_DIR"
    candidate="$MADS_RELEASE_CANDIDATE_DIR"
else
    candidate="$(build_matrix)"
fi

log "health check: $candidate/mads --version"
"$candidate/mads" --version >/dev/null 2>&1 || die "health check failed: $candidate/mads --version exited non-zero"

log "install/first-run/uninstall smoke (W7.2)"
MADS_RELEASE_CANDIDATE_DIR="$candidate" go test -count=1 ./scripts/smoketest/... || die "install/first-run/uninstall smoke failed"

log "update-apply smoke (W7.5)"
go test -count=1 -run TestUpdateApply ./cmd/mads/... || die "update-apply smoke failed"

log "release gate passed"
