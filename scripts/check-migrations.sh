#!/usr/bin/env sh
# Migration ordering check: fails when this tree's data-store migrations
# can't land cleanly on top of main's.
#
#   sh scripts/check-migrations.sh
#
# Two rules, both against the latest main (fetched first):
#   1. No version number is used by two different names (the data service
#      refuses to start: "version NNNN has conflicting names").
#   2. Every migration this tree adds over main is numbered above main's
#      highest. store.Migrate skips versions <= the applied schema version,
#      so a lower-numbered one would silently never run on an existing
#      database — or collide with main's on rebase, as 0028 did.
#
# Env overrides:
#   MADS_MIGRATIONS_BASE      ref to compare against (default origin/main)
#   MADS_MIGRATIONS_NO_FETCH  set to skip `git fetch` (offline / tests)
set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT"

DIR=services/data/internal/store/migrations
BASE="${MADS_MIGRATIONS_BASE:-origin/main}"

log() { printf 'check-migrations: %s\n' "$*" >&2; }

if [ -z "${MADS_MIGRATIONS_NO_FETCH:-}" ]; then
    git fetch --quiet --depth=1 origin main
fi

# "NNNN name" per migration, one line each (up/down collapsed).
versions() { sed -nE 's/^([0-9]{4})_(.*)\.(up|down)\.sql$/\1 \2/p' | sort -u; }
num() { expr "$1" + 0; } # "0028" -> 28, without octal surprises

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
git ls-tree --name-only "$BASE" "$DIR/" | sed 's|.*/||' | versions >"$tmp/base"
ls "$DIR" | versions >"$tmp/head"

base_max="$(tail -n1 "$tmp/base" | cut -d' ' -f1)"
base_max="$(num "${base_max:-0}")"
next="$(printf '%04d' $((base_max + 1)))"

fail=0

for v in $(cut -d' ' -f1 "$tmp/head" | uniq -d); do
    log "version $v is used by more than one migration:"
    grep "^$v " "$tmp/head" | sed 's/^/    /' >&2
    fail=1
done

comm -13 "$tmp/base" "$tmp/head" >"$tmp/added"
while read -r v name; do
    if [ "$(num "$v")" -le "$base_max" ]; then
        log "${v}_$name is new on this branch but $BASE is already at $(printf '%04d' "$base_max") — renumber it to $next or later"
        fail=1
    fi
done <"$tmp/added"

[ "$fail" -eq 0 ] || exit 1
log "ok ($BASE at $(printf '%04d' "$base_max"))"
