#!/usr/bin/env sh
# mads installer (spec §14.7): curl-to-shell script that places the mads
# binary on PATH.
#
#   curl -fsSL https://raw.githubusercontent.com/drewkidwell/mads/main/scripts/install.sh | sh
#
# Releases don't exist yet (no work item has cut one), so the release-URL
# path below is wired but untested — for local builds and CI, point
# MADS_INSTALL_SOURCE at a locally built binary or a directory containing
# one instead:
#
#   MADS_INSTALL_SOURCE=./bin/mads sh scripts/install.sh
#   MADS_INSTALL_SOURCE=./bin      sh scripts/install.sh   # dir containing "mads"
#
# Env overrides:
#   MADS_INSTALL_SOURCE   local binary or directory (skips the download)
#   MADS_INSTALL_PREFIX   install directory (default: see choose_prefix)
#   MADS_VERSION          release tag to fetch when there's no local source
#
# Platform matrix: linux and darwin, amd64 and arm64 (spec §14.10; Windows is
# a post-launch goal). This script only detects the platform — the
# install/uninstall smoke test that exercises it (scripts/smoketest) runs the
# linux row locally and in CI; the darwin row has no runner here yet and is
# pending CI infrastructure (see scripts/README.md).
set -eu

BIN_NAME="mads"
REPO="drewkidwell/mads"

log() { printf '%s\n' "$*" >&2; }
die() { log "install.sh: $*"; exit 1; }

detect_platform() {
    os=""
    arch=""
    case "$(uname -s)" in
        Linux) os=linux ;;
        Darwin) os=darwin ;;
        *) die "unsupported OS: $(uname -s) (linux and darwin only)" ;;
    esac
    case "$(uname -m)" in
        x86_64 | amd64) arch=amd64 ;;
        arm64 | aarch64) arch=arm64 ;;
        *) die "unsupported architecture: $(uname -m) (amd64 and arm64 only)" ;;
    esac
    printf '%s_%s\n' "$os" "$arch"
}

# ~/.local/bin for a normal user; /usr/local/bin when run as root (e.g. under
# sudo), matching common installer convention. MADS_INSTALL_PREFIX overrides
# either.
choose_prefix() {
    if [ -n "${MADS_INSTALL_PREFIX:-}" ]; then
        printf '%s\n' "$MADS_INSTALL_PREFIX"
    elif [ "$(id -u)" = "0" ]; then
        printf '%s\n' "/usr/local/bin"
    else
        printf '%s\n' "${HOME:?HOME must be set}/.local/bin"
    fi
}

# Prints the path to a mads binary ready to copy: MADS_INSTALL_SOURCE (a
# file, or a directory containing "mads") for local/CI installs, else a
# release download for the detected platform.
resolve_source() {
    platform="$1"
    if [ -n "${MADS_INSTALL_SOURCE:-}" ]; then
        if [ -f "$MADS_INSTALL_SOURCE" ]; then
            printf '%s\n' "$MADS_INSTALL_SOURCE"
        elif [ -f "$MADS_INSTALL_SOURCE/$BIN_NAME" ]; then
            printf '%s\n' "$MADS_INSTALL_SOURCE/$BIN_NAME"
        else
            die "MADS_INSTALL_SOURCE=$MADS_INSTALL_SOURCE: no $BIN_NAME binary found"
        fi
        return 0
    fi

    version="${MADS_VERSION:-latest}"
    url="https://github.com/$REPO/releases/download/$version/${BIN_NAME}_${platform}.tar.gz"
    tmp="$(mktemp -d)"
    log "install.sh: downloading $url"
    if ! curl -fsSL "$url" -o "$tmp/${BIN_NAME}.tar.gz"; then
        die "download failed: $url
No releases have been published yet. For local or CI installs, build the
binary and point this script at it instead: MADS_INSTALL_SOURCE=./bin/$BIN_NAME"
    fi
    tar -xzf "$tmp/${BIN_NAME}.tar.gz" -C "$tmp"
    printf '%s\n' "$tmp/$BIN_NAME"
}

main() {
    platform="$(detect_platform)"
    prefix="$(choose_prefix)"
    source_bin="$(resolve_source "$platform")"

    mkdir -p "$prefix"
    dest="$prefix/$BIN_NAME"
    cp "$source_bin" "$dest"
    chmod +x "$dest"
    log "install.sh: installed $BIN_NAME to $dest"

    case ":${PATH:-}:" in
        *":$prefix:"*) ;;
        *)
            log ""
            log "install.sh: $prefix is not on your PATH."
            log "  Add this to your shell profile:"
            log "    export PATH=\"$prefix:\$PATH\""
            ;;
    esac
}

main "$@"
