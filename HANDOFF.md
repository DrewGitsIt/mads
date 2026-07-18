# Engine reap-on-launch: reclaim leases whose holder is provably dead

## What changed this pass

Prior attempt (commit 099465c) already implemented the full feature
(migration, `store.Reclaim`, `pkg/procid` proof logic, engine launch
wiring, and unit tests for every proof branch except one). Code review
flagged that `store.Reclaim`'s holder guard at `leases.go:328` — the
`curHolder != holder` check — had no direct test.

Added `TestReclaimForeignHolder` in
`services/data/internal/store/leases_reap_test.go`: claims a lease as
actor `"other"` with a fully-matching identity, then calls
`Reclaim(ctx, taskID, "pipeline", identity, ...)`. Asserts `ok == false`,
the lease row is untouched, and the task stays `in_progress`. This
exercises the guard on its own (matching identity, mismatched actor),
distinct from `TestReclaimMismatchedIdentity` (matching actor, mismatched
identity) already in the file.

## What I did not do

No production code changes — the guard at `leases.go:328` already
implements the foreign-holder check correctly; only test coverage was
missing. Did not touch the migration, `pkg/procid`, or the engine launch
sequence.

## For the reviewer

- New test: `services/data/internal/store/leases_reap_test.go:171`
  (`TestReclaimForeignHolder`).
- Ran `go build ./...` and `go test ./...` in both `services/data` and
  `services/pipeline`; all pass.
- All other acceptance criteria (migration backward-compat, every other
  proof branch, evidence event, stale worktree/branch discard on claim,
  unchanged TTL reaper, live-sibling-no-reclaim) were verified present in
  commit 099465c and unmodified here.
