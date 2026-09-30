#!/usr/bin/env sh
# install.sh — one-command installer for mads.
#
# MOCK: today you run it straight from a checkout:
#     sh scripts/install.sh
# Goal (later):  curl -fsSL https://mads.example/install.sh | bash
#   where the served script first fetches the source (or a prebuilt release
#   tarball), then executes the same steps as below.
#
# Why this script exists:
#   1. electron (>= 42) has no npm postinstall; its binary is downloaded
#      lazily on first use. We fetch it up front (ensure-electron.sh) so the
#      sandbox probe below has a binary to run.
#   2. Modern Chromium sandboxes renderers via unprivileged user namespaces.
#      Some distros (Ubuntu 24.04 + AppArmor userns restriction, DGX Spark)
#      block those, and npm cannot ship the setuid fallback (no setuid bits
#      in tarballs). We detect, prefer a proper SUID fix when sudo is
#      available, else fall back to ELECTRON_DISABLE_SANDBOX=1.
#
# Idempotent: every step checks for its result before doing work, so
# re-running is cheap and safe.
set -eu

log()  { printf '[mads-install] %s\n' "$*"; }
warn() { printf '[mads-install] WARN: %s\n' "$*" >&2; }
die()  { printf '[mads-install] ERROR: %s\n' "$*" >&2; exit 1; }

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
UI_DIR="$REPO_ROOT/ui"
ELECTRON_PKG="$UI_DIR/node_modules/electron"
# POSIX twin of ensure-electron.sh's mads_electron_bin (that file is bash).
case "$(uname -s)" in
  Darwin) ELECTRON_BIN="$ELECTRON_PKG/dist/Electron.app/Contents/MacOS/Electron" ;;
  *)      ELECTRON_BIN="$ELECTRON_PKG/dist/electron" ;;
esac

# ---- 0. hermetic binary install (CI / smoketest path) -----------------------
# scripts/smoketest (spec §14.7) drives this script with MADS_INSTALL_SOURCE
# pointing at a prebuilt mads binary (a file, or a directory containing one)
# and MADS_INSTALL_PREFIX at an isolated temp prefix. That contract predates
# the electron work above and must keep working: place the binary and stop —
# no build, no electron, nothing written outside the prefix. (Running the
# build here would also write a read-only Go module cache under the test's
# temp $HOME, which breaks t.TempDir cleanup.)
if [ -n "${MADS_INSTALL_SOURCE:-}" ]; then
  if [ -f "$MADS_INSTALL_SOURCE" ]; then
    src="$MADS_INSTALL_SOURCE"
  elif [ -f "$MADS_INSTALL_SOURCE/mads" ]; then
    src="$MADS_INSTALL_SOURCE/mads"
  else
    die "MADS_INSTALL_SOURCE=$MADS_INSTALL_SOURCE: no mads binary found"
  fi
  if [ -n "${MADS_INSTALL_PREFIX:-}" ]; then
    prefix="$MADS_INSTALL_PREFIX"
  elif [ "$(id -u)" = "0" ]; then
    prefix="/usr/local/bin"
  else
    prefix="${HOME:?HOME must be set}/.local/bin"
  fi
  mkdir -p "$prefix"
  cp "$src" "$prefix/mads"
  chmod +x "$prefix/mads"
  log "installed mads to $prefix/mads"
  case ":${PATH:-}:" in
    *":$prefix:"*) ;;
    *) warn "$prefix is not on your PATH" ;;
  esac
  exit 0
fi

# ---- 1. preflight ----------------------------------------------------------
for t in node npm go make; do
  command -v "$t" >/dev/null 2>&1 || die "required tool '$t' not found in PATH"
done
log "node $(node --version), npm $(npm --version), go $(go version | awk '{print $3}')"

# ---- 2. build (Go services + UI; runs 'npm ci' internally) ------------------
log "building (make build)..."
make -C "$REPO_ROOT" build >/dev/null
log "build OK"

# ---- 3+4. ensure the electron binary actually exists, and detect a working
#           sandbox mode ------------------------------------------------------
# The install logic lives in scripts/ensure-electron.sh because scripts/app.sh
# needs it too: every `npm ci` wipes the lazily-downloaded binary again.
# Keeping one copy means the launch path and the install path can never drift
# apart.
#
# Chromium's modern Linux sandbox = unprivileged user namespaces (unshare -Urm)
# + seccomp-bpf. If the OS blocks userns (AppArmor restriction on Ubuntu 24.04),
# ensure-electron.sh's mads_electron_needs_no_sandbox tries the setuid
# chrome-sandbox fix (needs sudo) and only then reports that the sandbox has
# to be turned off.
#
# ensure-electron.sh is bash (BASH_SOURCE, set -euo pipefail) but this script
# is POSIX sh so that `curl | sh` works on hosts with no bash — sourcing it
# the way app.sh does would run those bashisms under dash/ash and blow up
# ("Bad substitution", then set -u aborting on an unset var). So execute it
# as a subprocess instead: its own #!/usr/bin/env bash shebang selects the
# right interpreter, and its standalone block (install, then report the
# sandbox verdict) reports the verdict as an exit status: 0 = sandbox OK,
# 3 = caller must disable the sandbox, anything else = install failed.
SANDBOX_FALLBACK=0
ELECTRON_STEP_SKIPPED=0
if command -v bash >/dev/null 2>&1; then
  status=0
  bash "$REPO_ROOT/scripts/ensure-electron.sh" || status=$?
  case "$status" in
    0)
      log "sandbox: renderer sandbox OK"
      ;;
    3)
      SANDBOX_FALLBACK=1
      warn "sandbox: no usable sandbox; app will run with ELECTRON_DISABLE_SANDBOX=1"
      warn "         (renderer sandbox OFF — acceptable for local dev, not for shipping)"
      ;;
    *)
      die "could not install the electron binary"
      ;;
  esac
else
  ELECTRON_STEP_SKIPPED=1
  warn "bash not found in PATH; skipping electron install and sandbox check"
  warn "         (the mads Go binary will still be installed; re-run once bash"
  warn "         is available to get a working Electron UI)"
fi

# ---- 5. launcher ------------------------------------------------------------
# NOTE: bin/mads is the Go CLI (see Makefile); the app launcher is bin/mads-app.
LAUNCHER="$REPO_ROOT/bin/mads-app"
{
  echo '#!/usr/bin/env bash'
  echo '# Generated by scripts/install.sh — app entry point (services + Electron).'
  echo 'set -euo pipefail'
  [ "$SANDBOX_FALLBACK" -eq 1 ] && echo 'export ELECTRON_DISABLE_SANDBOX=1'
  echo "exec \"$REPO_ROOT/scripts/app.sh\" \"\$@\""
} > "$LAUNCHER"
chmod +x "$LAUNCHER"
log "launcher: $LAUNCHER"

# ---- 6. smoke test -----------------------------------------------------------
if [ "$ELECTRON_STEP_SKIPPED" -eq 1 ]; then
  log "smoke test: skipped (no bash in PATH, electron install was skipped)"
elif [ "$SANDBOX_FALLBACK" -eq 1 ]; then
  ELECTRON_DISABLE_SANDBOX=1 "$ELECTRON_BIN" --version
else
  "$ELECTRON_BIN" --version
fi
log "DONE. Launch the app with: $LAUNCHER"
