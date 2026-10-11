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

# The Electron executable inside dist/, per OS — the same layout install.js
# records in path.txt. Every path to the binary goes through this.
mads_electron_bin() {
  case "$(uname -s)" in
    Darwin) echo "$MADS_ELECTRON_PKG/dist/Electron.app/Contents/MacOS/Electron" ;;
    *)      echo "$MADS_ELECTRON_PKG/dist/electron" ;;
  esac
}

# 0 when the binary is present, executable, and matches the package version —
# exactly the end state electron's own install.js is supposed to leave behind.
mads_electron_ok() {
  local want
  [ -d "$MADS_ELECTRON_PKG" ] || return 1
  [ -f "$MADS_ELECTRON_PKG/path.txt" ] || return 1
  [ -x "$(mads_electron_bin)" ] || return 1
  [ -f "$MADS_ELECTRON_PKG/dist/version" ] || return 1
  want="$(node -p "require('$MADS_ELECTRON_PKG/package.json').version")" || return 1
  [ "$(tr -d 'v' < "$MADS_ELECTRON_PKG/dist/version")" = "$want" ]
}

# Darwin fallback for mads_ensure_electron: fetch the checksum-verified zip
# through @electron/get (a cache hit after install.js ran) and extract it with
# ditto, then write path.txt the way install.js would (no trailing newline —
# index.js path.join()s it verbatim).
mads_electron_ditto_install() {
  local zip
  zip="$(cd "$MADS_ELECTRON_PKG" && node -e '
      const {downloadArtifact} = require("@electron/get");
      const os = require("os");
      downloadArtifact({
        version: require("./package.json").version,
        artifactName: "electron",
        platform: os.platform(),
        arch: os.arch(),
        checksums: require("./checksums.json"),
      }).then((p) => console.log(p)).catch((e) => {
        console.error(e.message); process.exit(1);
      });')" || return 1
  rm -rf "$MADS_ELECTRON_PKG/dist"
  mkdir -p "$MADS_ELECTRON_PKG/dist"
  ditto -x -k "$zip" "$MADS_ELECTRON_PKG/dist" || return 1
  chmod +x "$(mads_electron_bin)" 2>/dev/null || true
  printf 'Electron.app/Contents/MacOS/Electron' > "$MADS_ELECTRON_PKG/path.txt"
}

# Fetch + extract the binary via electron's own install.js when it is
# missing. No-op and silent when mads_electron_ok already passes.
mads_ensure_electron() {
  mads_electron_ok && return 0

  [ -f "$MADS_ELECTRON_PKG/install.js" ] \
    || { echo "ensure-electron: no electron package — run 'npm --prefix ui ci'" >&2; return 1; }
  echo "ensure-electron: electron binary not installed yet — running electron's installer"
  if ! node "$MADS_ELECTRON_PKG/install.js"; then
    [ "$(uname -s)" = Darwin ] \
      || { echo "ensure-electron: electron install failed (network?)" >&2; return 1; }
    # install.js extracts through a native binding that an OS policy can
    # refuse to load (electron/electron#52481). Re-extract the zip it cached
    # with ditto, which keeps the symlinks Electron.app's frameworks need
    # (python3's zipfile drops them).
    echo "ensure-electron: electron's installer failed — extracting with ditto" >&2
    mads_electron_ditto_install \
      || { echo "ensure-electron: electron install failed (network?)" >&2; return 1; }
  fi

  mads_electron_ok || { echo "ensure-electron: still broken after install" >&2; return 1; }
  echo "ensure-electron: installed electron $(tr -d 'v' < "$MADS_ELECTRON_PKG/dist/version")"
}

# --- mads.app: the branded dev bundle (macOS) --------------------------------
#
# The Dock label, the bold menu-bar name and the Cmd-Tab icon all come from
# the launching bundle's Info.plist — "Electron", com.github.Electron — and
# nothing main.ts does at runtime changes them. Until real packaging (W7),
# app.sh launches a copy of Electron.app branded as mads instead. It lives in
# dist/ next to the original, so the `npm ci` that wipes one wipes both, and
# it is rebuilt whenever the stamp beside it (outside the bundle: anything
# written inside after signing breaks the seal) no longer matches.
MADS_ICNS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/ui/assets/icon/mads.icns"
# Bump when mads_brand_app changes what it writes, so existing copies rebuild.
MADS_BRAND_RECIPE=1

# The executable app.sh launches: mads.app's on macOS (the executable keeps
# its name — Electron finds its "Electron Helper" apps by it), the stock
# binary elsewhere.
mads_app_bin() {
  case "$(uname -s)" in
    Darwin) echo "$MADS_ELECTRON_PKG/dist/mads.app/Contents/MacOS/Electron" ;;
    *)      mads_electron_bin ;;
  esac
}

mads_brand_stamp() {
  printf 'recipe %s electron %s icon %s' "$MADS_BRAND_RECIPE" \
    "$(cat "$MADS_ELECTRON_PKG/dist/version")" "$(cksum < "$MADS_ICNS")"
}

# Build dist/mads.app from dist/Electron.app if it is missing or stale; a
# no-op off macOS. Run after mads_ensure_electron. On failure nothing usable
# is left behind, so the caller can fall back to the stock bundle.
mads_brand_app() {
  [ "$(uname -s)" = Darwin ] || return 0
  local src="$MADS_ELECTRON_PKG/dist/Electron.app"
  local app="$MADS_ELECTRON_PKG/dist/mads.app"
  local stamp="$MADS_ELECTRON_PKG/dist/mads.app.stamp"
  local want helper
  want="$(mads_brand_stamp)" || return 1
  if [ -x "$(mads_app_bin)" ] && [ "$(cat "$stamp" 2>/dev/null)" = "$want" ]; then
    return 0
  fi
  echo "ensure-electron: branding the dev bundle as mads.app"
  rm -rf "$app" "$stamp"
  # ditto, not cp -R: it keeps the frameworks' symlinks and extended attributes.
  if ! { ditto "$src" "$app" &&
    plutil -replace CFBundleName -string mads "$app/Contents/Info.plist" &&
    plutil -replace CFBundleDisplayName -string mads "$app/Contents/Info.plist" &&
    plutil -replace CFBundleIdentifier -string dev.mads.app "$app/Contents/Info.plist" &&
    cp "$MADS_ICNS" "$app/Contents/Resources/electron.icns"; }; then
    rm -rf "$app"
    echo "ensure-electron: could not build mads.app" >&2
    return 1
  fi
  # Helpers carry the main bundle's id as a prefix, as a packaged app's would.
  for helper in "$app"/Contents/Frameworks/*Helper*.app; do
    [ -d "$helper" ] || continue
    plutil -replace CFBundleIdentifier -string dev.mads.app.helper "$helper/Contents/Info.plist" \
      || { rm -rf "$app"; echo "ensure-electron: could not build mads.app" >&2; return 1; }
  done
  # The edits broke Electron's signature, and Apple Silicon won't run an
  # unsigned bundle: re-sign the whole tree ad hoc (local use only).
  if ! codesign --force --deep --sign - "$app" >/dev/null 2>&1; then
    rm -rf "$app"
    echo "ensure-electron: could not sign mads.app" >&2
    return 1
  fi
  printf '%s' "$want" > "$stamp"
}

# 0 when the renderer sandbox cannot work on this host and the caller must set
# ELECTRON_DISABLE_SANDBOX=1. Prefers a real sandbox; only the functional test
# (electron --version, which aborts hard when the SUID helper is misconfigured)
# is authoritative.
mads_electron_needs_no_sandbox() {
  # macOS sandboxes renderers with seatbelt, which always works; the userns /
  # SUID probing below is Linux-only.
  [ "$(uname -s)" = Darwin ] && return 1
  if command -v unshare >/dev/null 2>&1 && unshare -Urm true 2>/dev/null; then
    return 1  # userns sandbox works — nothing to disable
  fi
  local helper="$MADS_ELECTRON_PKG/dist/chrome-sandbox"
  if [ -f "$helper" ] && command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    sudo -n chown root:root "$helper" && sudo -n chmod 4755 "$helper" || true
  fi
  ! "$(mads_electron_bin)" --version >/dev/null 2>&1
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
