#!/usr/bin/env bash
# ensure-electron.sh — make ui/node_modules/electron actually runnable.
#
# Sourced by scripts/app.sh (bash, every launch). scripts/install.sh is POSIX
# sh (so `curl | sh` works on hosts with no bash) and cannot source this file
# — its bashisms (BASH_SOURCE, set -euo pipefail) would run under dash/ash and
# abort — so install.sh instead *executes* it as a subprocess via
# `bash scripts/ensure-electron.sh` and reads the exit status (see the
# standalone block below). Also runnable standalone directly the same way.
#
# Two jobs live here:
#
#  1. Electron >= 42 no longer has an npm postinstall: `npm ci` leaves
#     node_modules/electron with no dist/ and no path.txt, and the binary is
#     fetched lazily the first time something require()s the package. The
#     sandbox probe below runs dist/electron directly, so make sure it exists
#     first by running electron's own installer (install.js: checksum-verified
#     download into ~/.cache/electron, then extract). `make build` runs
#     `npm ci`, which wipes dist/ again, so app.sh does this on every launch;
#     it is a cheap stat when the binary is already there.
#     (This used to be a hand-rolled unzip working around electron/electron
#     #51619, the extract-zip/yauzl stall on Node >= 24.16 / 26.1. Electron 44
#     extracts with @electron-internal/extract-zip instead and installs
#     cleanly on Node 26, so the workaround is gone.)
#
#  2. Chromium sandboxes renderers with unprivileged user namespaces. Some
#     hosts block those (Ubuntu 24.04's AppArmor userns restriction, DGX
#     Spark) and npm cannot ship the setuid chrome-sandbox fallback, because
#     tarballs carry no setuid bit. Callers that get a true from
#     mads_electron_needs_no_sandbox must export ELECTRON_DISABLE_SANDBOX=1.

# Resolve the repo root from this file, not the caller's cwd, so sourcing
# works regardless of where the caller cd'd to.
MADS_ELECTRON_PKG="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/ui/node_modules/electron"

# 0 when the binary is present, executable, and matches the package version —
# exactly the end state electron's own install.js is supposed to leave behind.
mads_electron_ok() {
  local want
  [ -d "$MADS_ELECTRON_PKG" ] || return 1
  [ -f "$MADS_ELECTRON_PKG/path.txt" ] || return 1
  [ -x "$MADS_ELECTRON_PKG/dist/electron" ] || return 1
  [ -f "$MADS_ELECTRON_PKG/dist/version" ] || return 1
  want="$(node -p "require('$MADS_ELECTRON_PKG/package.json').version")" || return 1
  [ "$(tr -d 'v' < "$MADS_ELECTRON_PKG/dist/version")" = "$want" ]
}

# Fetch + extract the binary via electron's own install.js when it is
# missing. No-op and silent when mads_electron_ok already passes.
mads_ensure_electron() {
  mads_electron_ok && return 0

  [ -f "$MADS_ELECTRON_PKG/install.js" ] \
    || { echo "ensure-electron: no electron package — run 'npm --prefix ui ci'" >&2; return 1; }
  echo "ensure-electron: electron binary not installed yet — running electron's installer"
  node "$MADS_ELECTRON_PKG/install.js" \
    || { echo "ensure-electron: electron install failed (network?)" >&2; return 1; }

  mads_electron_ok || { echo "ensure-electron: still broken after install" >&2; return 1; }
  echo "ensure-electron: installed electron $(tr -d 'v' < "$MADS_ELECTRON_PKG/dist/version")"
}

# 0 when the renderer sandbox cannot work on this host and the caller must set
# ELECTRON_DISABLE_SANDBOX=1. Prefers a real sandbox; only the functional test
# (electron --version, which aborts hard when the SUID helper is misconfigured)
# is authoritative.
mads_electron_needs_no_sandbox() {
  if command -v unshare >/dev/null 2>&1 && unshare -Urm true 2>/dev/null; then
    return 1  # userns sandbox works — nothing to disable
  fi
  local helper="$MADS_ELECTRON_PKG/dist/chrome-sandbox"
  if [ -f "$helper" ] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo -n chown root:root "$helper" && sudo -n chmod 4755 "$helper" || true
  fi
  ! "$MADS_ELECTRON_PKG/dist/electron" --version >/dev/null 2>&1
}

# Standalone: install, then report the sandbox verdict — both as an echo (for
# humans watching `bash scripts/ensure-electron.sh` run directly) and as the
# exit status (for install.sh, which cannot source this bash file and instead
# executes it as a subprocess):
#   0 = repaired (or already OK) and the renderer sandbox works
#   3 = repaired (or already OK) but the caller must set
#       ELECTRON_DISABLE_SANDBOX=1
#   *  = repair failed
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  set -euo pipefail
  mads_ensure_electron
  if mads_electron_needs_no_sandbox; then
    echo "ensure-electron: no usable sandbox — launch with ELECTRON_DISABLE_SANDBOX=1"
    exit 3
  else
    echo "ensure-electron: renderer sandbox OK"
    exit 0
  fi
fi
