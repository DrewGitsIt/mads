#!/usr/bin/env bash
# ensure-electron.sh — make ui/node_modules/electron actually runnable.
#
# Sourced by scripts/app.sh (every launch) and scripts/install.sh (install
# time). Also runnable standalone:  bash scripts/ensure-electron.sh
#
# Two host problems live here, both of which come back on their own:
#
#  1. electron's postinstall (install.js -> extract-zip@2.0.1 -> yauzl 2.10)
#     stalls mid-extraction and exits 0 on Node >= 24.16 / >= 26.1, leaving a
#     partial dist/ and no path.txt. See scripts/install.sh's
#     node_electron_compat_check for the upstream links and the criterion for
#     deleting all of this. `make build` runs `npm ci`, and npm re-runs the
#     broken postinstall, so EVERY build re-breaks the binary — a one-time
#     install-day repair is not enough, which is why app.sh repairs on each
#     launch. The check below is a cheap stat when things are already fine.
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

# Rebuild dist/ + path.txt from the checksum-verified zip @electron/get keeps
# in ~/.cache/electron (downloading it only if the cache is cold). No-op and
# silent when mads_electron_ok already passes.
mads_ensure_electron() {
  mads_electron_ok && return 0

  local version zip
  version="$(node -p "require('$MADS_ELECTRON_PKG/package.json').version")" \
    || { echo "ensure-electron: no electron package — run 'npm --prefix ui ci'" >&2; return 1; }
  echo "ensure-electron: electron $version binary missing/partial (postinstall no-op) — repairing"

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
      });')" \
    || { echo "ensure-electron: could not obtain electron $version zip (network?)" >&2; return 1; }

  rm -rf "$MADS_ELECTRON_PKG/dist"
  mkdir -p "$MADS_ELECTRON_PKG/dist"
  if command -v unzip >/dev/null 2>&1; then
    unzip -o -q "$zip" -d "$MADS_ELECTRON_PKG/dist"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$zip" "$MADS_ELECTRON_PKG/dist/"
  else
    echo "ensure-electron: need 'unzip' or 'python3' to extract $zip" >&2; return 1
  fi
  # The zip carries no exec bits; install.js chmods these two.
  chmod +x "$MADS_ELECTRON_PKG/dist/electron" 2>/dev/null || true
  chmod +x "$MADS_ELECTRON_PKG/dist/chrome_crashpad_handler" 2>/dev/null || true
  # path.txt has NO trailing newline — index.js path.join()s it verbatim.
  case "$(uname -s)" in
    Linux)  printf 'electron' > "$MADS_ELECTRON_PKG/path.txt" ;;
    Darwin) printf 'Electron.app/Contents/MacOS/Electron' > "$MADS_ELECTRON_PKG/path.txt" ;;
    *) echo "ensure-electron: unsupported platform $(uname -s)" >&2; return 1 ;;
  esac

  mads_electron_ok || { echo "ensure-electron: still broken after extraction" >&2; return 1; }
  echo "ensure-electron: repaired electron $version"
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

# Standalone: repair, then report the sandbox verdict.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  set -euo pipefail
  mads_ensure_electron
  if mads_electron_needs_no_sandbox; then
    echo "ensure-electron: no usable sandbox — launch with ELECTRON_DISABLE_SANDBOX=1"
  else
    echo "ensure-electron: renderer sandbox OK"
  fi
fi
