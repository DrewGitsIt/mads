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
#   1. electron's npm postinstall (extract-zip@2.0.1) silently exits 0 on
#      Node >= 24.16 / >= 26.1 without extracting the binary -> broken
#      node_modules/electron. We detect it (node_electron_compat_check)
#      and self-heal from the verified .zip (ensure_electron).
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
command -v unzip >/dev/null 2>&1 || command -v python3 >/dev/null 2>&1 \
  || die "need 'unzip' or 'python3' to extract the electron binary"
log "node $(node --version), npm $(npm --version), go $(go version | awk '{print $3}')"

# ---- 1b. node <-> electron install compatibility (KNOWN UPSTREAM BUG) ------
# electron's postinstall (install.js -> extract-zip@2.0.1 -> yauzl 2.10)
# stalls mid-extraction and exits 0 on Node >= 24.16 and >= 26.1, leaving a
# broken node_modules/electron (partial dist/, no path.txt). The well-known
# "Electron failed to install correctly" error is a SYMPTOM of this, not the
# cause. This is not a mads bug; it is upstream and reproducible by anyone:
#
#   https://github.com/electron/electron/issues/51619
#       top-level report: "Installing Electron fails under Node.js 26.1.0+
#       and 24.16.0+" (Electron 40-42, Debian/Arch/RPi, x64)
#   https://github.com/thejoshwolfe/yauzl/issues/177
#       root-cause repro: "Read stream stalls mid-entry ... process exits 0
#       (works on Node 22)"
#   https://github.com/thejoshwolfe/yauzl/pull/168
#       the fix: "fix: early-exit in node v26"
#   https://github.com/max-mapper/extract-zip/issues/154
#       same failure reported on the extract-zip side
#
# The fix exists in yauzl 3.x, but extract-zip is still at 2.0.1 (2020) and
# pins yauzl ^2.10.0, so the electron install path cannot reach it yet.
#
# SAFE TO REMOVE -- both this check and ensure_electron's repair branch --
# when: extract-zip (or electron's install path) has shipped a release that
# extracts cleanly on Node >= 24.16 / 26+, AND mads' locked deps pull it in.
# How to verify at that time: on a clean checkout with Node >= 26, run
#   npm --prefix ui ci
# and confirm ui/node_modules/electron/path.txt and dist/electron exist with
# NO repair step. Re-check https://github.com/electron/electron/issues/51619
# status first.
#
# This check only WARNS: ensure_electron() below self-heals, so installs
# keep succeeding on affected nodes either way.
node_electron_compat_check() {
  local full major minor
  full="$(node -p "process.versions.node")"   # e.g. 26.7.0
  major="${full%%.*}"
  minor="${full#*.}"
  minor="${minor%%.*}"
  case "$major" in
    ''|*[!0-9]*) return 0 ;;
  esac
  case "$minor" in
    ''|*[!0-9]*) return 0 ;;
  esac
  # Everything at or above 24.16 is affected. Note there is deliberately no
  # minor-version condition on later majors: the earlier form ANDed
  # "minor >= 1" onto every major above 24, so any X.0.z release (27.0.0,
  # 28.0.0, ...) escaped the check and the user got no warning at all.
  # Over-warning on a hypothetical fixed release is the safe direction here.
  if [ "$major" -gt 24 ] || { [ "$major" -eq 24 ] && [ "$minor" -ge 16 ]; }; then
    warn "node $full is affected by the electron install bug (electron/electron#51619)."
    warn "  electron's postinstall will no-op; ensure_electron() below repairs the binary."
    warn "  Cleaner path: use the pinned Node (.nvmrc) — 'nvm install && nvm use'."
  else
    log "node $full: no known electron install incompatibility"
  fi
}
node_electron_compat_check

# ---- 2. build (Go services + UI; runs 'npm ci' internally) ------------------
log "building (make build)..."
make -C "$REPO_ROOT" build >/dev/null
log "build OK"

# ---- 3+4. ensure the electron binary actually exists, and detect a working
#           sandbox mode ------------------------------------------------------
# The repair logic lives in scripts/ensure-electron.sh because scripts/app.sh
# needs it too: `npm ci` above re-runs the broken postinstall, so the binary
# can rot again long after install day. Keeping one copy means the launch path
# and the install path can never drift apart.
# (Upstream: electron/electron#51619; see node_electron_compat_check above for
# the full link list and the criterion for deleting all of this.)
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
# right interpreter, and its standalone block (repair, then report the
# sandbox verdict) reports the verdict as an exit status: 0 = sandbox OK,
# 3 = caller must disable the sandbox, anything else = repair failed.
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
      die "could not repair the electron binary"
      ;;
  esac
else
  ELECTRON_STEP_SKIPPED=1
  warn "bash not found in PATH; skipping electron repair and sandbox check"
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
  log "smoke test: skipped (no bash in PATH, electron repair was skipped)"
elif [ "$SANDBOX_FALLBACK" -eq 1 ]; then
  ELECTRON_DISABLE_SANDBOX=1 "$ELECTRON_PKG/dist/electron" --version
else
  "$ELECTRON_PKG/dist/electron" --version
fi
log "DONE. Launch the app with: $LAUNCHER"
