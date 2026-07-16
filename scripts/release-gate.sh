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
            for pkg in cmd/mads services/data services/router services/session services/pipeline; do
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

    printf '%s\n' "$native"
}

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
