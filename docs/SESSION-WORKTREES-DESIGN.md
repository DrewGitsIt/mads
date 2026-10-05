# Session worktrees: isolation, Commit, Land

**Status:** Implemented on `feat/session-worktrees` (2026-10-05). Revised after
critical review; see Review log and Implementation notes.
**Date:** 2026-10-05. **Driver:** Drew wants "agent, do your work in an isolated
space because I have other stuff rolling" to actually happen, without making
worktrees one more thing users have to manage.
**Mockup:** [`docs/mockups/session-worktrees.html`](mockups/session-worktrees.html).

## Context

Interactive sessions have **never** been isolated.

- W4.5 (11bb527) built per-session worktrees, branches and merge-back
  (`services/session/internal/worktree`, `Runner.Merge`, the Files tab's
  `MergeBackFlow`). But the session service only isolates when it gets
  `-worktree-root`, and the Router has never passed it. `git log -S worktree-root`
  finds only the `mads run` CLI flag.
- Live data on 2026-10-02: 4 of 20 sessions say `isolation: worktree`, and 0 have a
  `worktree_dir`. "New worktree (default)" has always run in place.
- Pipelines do isolate. `services/pipeline/main.go` builds
  `$MADS_HOME/worktrees/<session>/<task>` itself whenever isolation isn't
  `current_dir`. Task branches merge into the primary checkout under a per-repo flock
  at `<git-common-dir>/mads-land.lock` (`engine/workspacelock.go`).

The session-side code is dormant. Turning it on exposes edges no production session
has hit:

1. **Location clash.** `Manager.Create` uses `<root>/<sessionID>`. A root of
   `$MADS_HOME/worktrees` is the very directory that session's pipeline puts its
   task worktrees in.
2. **Resume is folder-bound.** claude-code finds a conversation to resume by its
   starting folder. Isolating a session that already has a `ProviderRef` would
   strand it.
3. **Merge deletes the folder under a live agent.** `Runner.Merge`'s own comment says
   so.
4. **Merge runs `git checkout <target>` in the user's own checkout.**
5. **Nothing cleans up on delete.**
6. **Pipelines ignore the session's folder.** They always use the workspace's
   primary repo, even for a session started somewhere else via the folder pill.
7. **The post-commit hook can't attribute commits the way the old plan assumed.** It
   links a commit through the per-worktree git config `mads.sessionId`, and reads
   only a `Task:` trailer, never `Session:` (`pkg/githook`). It also doesn't fire on
   `git merge`. For sessions working in place, `installHook` stamps the primary's
   config, so the most recent session to start in a repo claims every hook-reported
   commit there.
8. **No process owns the git work.** `POST /merge` lives on the session child. An
   inactive session has no child. Delete terminates the child before anything else
   runs (`handleDeleteSession`).

## Principle

**Users never manage worktrees. They decide what happens to the session's work.**
There are two click-only actions, **Commit** and **Land**. Everything else is
automatic, and stays invisible until something needs a decision.

| Layer | What | Lifetime |
|---|---|---|
| Record | conversation, tasks, tool diffs (shadow store), commit cards | the session's |
| Branch | `mads/session/<id>` | the session's |
| Folder | the worktree checkout at `$MADS_HOME/session-worktrees/<id>` | the session's; recreated on demand if lost |

Commit cards stay valid after a Land. Commit records carry the primary repo (the hook
resolves `--git-common-dir`; Commit and Land record it the same way), and `--no-ff`
keeps the commits reachable from the base.

## Decisions

### D1. Where and when a session is isolated

- **The root is `$MADS_HOME/session-worktrees/<id>`.** It is derived from
  `-mads-home` when `-worktree-root` is empty, the same way the pipeline derives its
  own. There's no Router plumbing, and e2e (temporary `-mads-home`) behaves like
  production. This fixes problem 1.
- **When a session is isolated:** when `isolation != current_dir` and `dir` is a git
  repo, **on first start only**, meaning no `ProviderRef` yet. Sessions that predate
  this, and have already run, stay where they are (fixes 2).
- **New field `Session.worktree_base`:** the branch the primary had checked out when
  the worktree was created. It is the Land target. If the primary is on a detached
  HEAD, the session isn't isolated, and its first card says why.
- **Persist first, then start the provider.** `worktree_dir`, `worktree_branch` and
  `worktree_base` are written **before** the provider starts. Today `Start` creates
  the worktree, starts the provider, and only then persists. So a provider failure
  leaves a folder and branch that the next start trips over ("a branch named …
  already exists").

### D2. Ensure, don't assume

Every step that needs the folder calls **ensure**, under the repo's
`mads-land.lock`. That covers start, resume, Commit, Land and pipeline launch. Ensure
works from the **deterministic** path and branch name, never from what the record
says:

| Found | Action |
|---|---|
| Folder is a valid worktree on `mads/session/<id>` | use it |
| Folder gone, branch present | `git worktree prune`, then `worktree add <path> mads/session/<id>` at the **same path** so resume works. A **locked** stale entry (prune skips those) is unlocked first. |
| Path exists but isn't that worktree | move it aside to `<path>.stale-<ts>`, recreate, emit a session event |
| Both gone | `worktree add -b mads/session/<id> <path> <worktree_base>`, with the start point **passed explicitly** (today `Create` branches from primary HEAD). If the base branch itself is gone, use primary HEAD and say so. |

Every non-trivial case appends a session event, which renders as a card, so nothing
heals silently. Deleting the folder by hand loses only uncommitted edits.

### D3. Who runs the git work (new)

All worktree mechanics move into one shared package, used by every caller. Each
caller holds `mads-land.lock`, the same flock the pipeline engine already takes for
its task merges, so session, pipeline and Router serialize against each other per
repo.

| Operation | Live session (child running) | Inactive session |
|---|---|---|
| ensure at start/resume | the session child (`Runner.Start`) | n/a |
| Commit, Land | forwarded by the Router to the **session child**, which can see `busy` and hold back queued messages | the **Router**, directly. No agent can be mid-turn. |
| Pipeline launch ensure, task merges | the pipeline child | the pipeline child |
| Delete cleanup | the **Router**, after `TerminateSessionForDelete` | the Router |
| Header state (D4) | whoever just changed it publishes it on the record | n/a (stale is fine; recomputed on wake) |

Terminate-then-remove is the right order for delete, but git won't protect a live
process: `worktree remove --force` succeeds even with a process working inside the
folder. Cleanup does nothing for sessions with no `dir`/`worktree_dir` (everything
that predates this).

### D4. One header control, driven by state, computed off the hot path

| State | Control |
|---|---|
| uncommitted session files | **Commit…** (count of files) |
| clean, branch ahead of base (isolated only) | **Land into `<worktree_base>`** (count of commits) |
| both | **Commit…**, with "then land N" noted |
| neither | hidden |

**"Uncommitted session files"** means files in the session's change list that git
also reports as changed. Otherwise a file the agent touched and then reverted would
keep counting.

**This is never computed per render or per event stream message.** `git status` on a
large checkout takes seconds. The owner in D3 recomputes it only after a file change,
a Commit, a Land, a pipeline merge, and at start. It runs:

- `git status --porcelain --untracked-files=no -- <session files>`
- `git rev-list --count <base>..mads/session/<id>`

It publishes the result as session fields (`uncommitted_count`, `unlanded_count`) and
the header just reads them. The rail marker reads the same fields.

### D5. Commit (click-to-commit, decided 2026-10-05)

- **The dialog lists only files this session changed,** from the session's change
  list. Not `git status`, not other sessions (Drew, 2026-10-05). Rows produced by
  pipeline tasks (rows with a `TaskID`) are excluded, because they live in task
  worktrees.
- **Paths are validated on the server, not trusted.**
  - Each path is resolved against its row's `Repo` and symlinks are followed.
  - The result must stay inside the session's folder (or `dir` when working in
    place).
  - Paths are passed to git after `--`.
  - A deleted file stages as a deletion.
  - A path that is already committed (for example by a pipeline merge, D8) is
    reported per path ("already committed") rather than failing the whole commit.
- **The message box offers a Fast Model draft.** The draft button is hidden without a
  Fast Model.
- **A Settings toggle, "Write commit messages for me",** makes Commit skip the
  dialog: the Fast Model writes the message and commits all session files. The
  result card carries **Undo** (`reset --soft`) while that commit is still the branch
  tip. Without a Fast Model it falls back to the dialog.
- **Never automatic.** No commit at turn end (rejected as churn, 2026-10-05).
- **Identity is the user's own git config.**
  - If there's no `user.name`, or signing fails (e.g. `commit.gpgsign` with no way to
    ask for a passphrase from a background process), the card shows git's error
    word for word.
  - It never falls back to the `mads-session` safety identity.
  - The message carries a `Session: <id>` trailer, for people reading the log.
- **Recording: Commit posts the new hash to the Data Service itself**
  (`POST /sessions/{id}/commits`, with the repo resolved as in Principle). It doesn't
  rely on the hook, which attributes in-place commits to whichever session started
  last (problem 7).
- **A failing pre-commit hook** shows its output on the card, and the selection is
  kept so the user can fix it and retry.
- **Works for in-place sessions too.** They have no Land, so this is their "done".

### D6. Land (isolated sessions only)

**When Land is allowed.** The owner (D3) refuses Land, with the reason on the card,
while any of these is true:

- the agent is mid-turn (`busy`);
- a pipeline run is active for the session (`Supervisor.HasPipeline`);
- the folder has `MERGE_HEAD` or `REBASE_HEAD`, the state "Ask the agent to resolve"
  leaves if the agent stops half-way;
- `git status --porcelain` in the folder isn't empty. That's the full status, not
  just session files: untracked files would ride along otherwise.

While Land runs, user messages queue behind it, using the runner's existing queue.

**Steps,** all under `mads-land.lock`:

1. **Bring the base into the session branch, in the session folder.** Record
   `B = rev-parse <worktree_base>`, then `git merge B` in the folder.
   - On a conflict, abort. The card lists the files and offers **Ask the agent to
     resolve**: an ordinary, editable user message telling the agent to do this merge
     itself and fix the conflicts.
   - The merge commit uses the user's identity, as in D5, because it ends up on the
     base branch.
2. **Re-check, then merge in the primary.**
   - If `<worktree_base>` no longer equals `B`, go back to step 1. The base moved, so
     "can't conflict" no longer holds.
   - Otherwise run `git merge --no-ff mads/session/<id>` in the primary, with a
     `Session:` trailer and the user's identity.
   - **Any** non-zero exit runs `merge --abort`, so the user's checkout is never left
     mid-merge. Possible causes: overlapping uncommitted edits, untracked files the
     branch would add, a `pre-merge-commit` hook, an in-progress rebase or merge,
     detached HEAD, or `<worktree_base>` not checked out in the primary.
   - The card names the reason, verbatim from git where that's clearest.
3. **Record and tidy.**
   - The merge commit doesn't fire `post-commit`, so Land posts its hash to the Data
     Service itself. That's how the "Landed" card and the commits list get it.
   - It then tries `git merge --ff-only <worktree_base>` in the folder, so the session
     continues on top of the landed work. This step is **best-effort**: after a
     `--no-ff` land, the branch is already 0 commits ahead, so the header is correct
     either way. A refusal isn't an error.

The folder **stays**. A running agent keeps a valid working folder, resume keeps
working, and the Files tab's paths stay real (fixes 3 and 4).

**Results are cards in the conversation:** landed, needs the agent, your checkout
refused, or Land unavailable right now. They are not chips on the Files tab.
`MergeBackFlow`, its target-branch box and the merge-editor stub are retired.

**Closing a session** (the rail's existing close action, which makes it inactive)
asks once if there are unlanded commits: **Land into `<base>` and close / Keep for
later.** Close never discards anything.

### D7. Delete

- The Router terminates the child (as today), then, under the lock, runs
  `git worktree remove --force <folder>` and deletes the branch. This applies only
  when the record has `worktree_dir`.
- If the session has unlanded commits or uncommitted files (D4's counts, recomputed
  at delete time), the confirmation says so in words ("3 unlanded commits and 2
  uncommitted files will be discarded"). This extends the state panel in
  `docs/mockups/session-delete-confirm.html`.
- There's no "keep the branch" option. Keeping work is what Land and Keep for later
  are for.
- The shadow store keeps its own retention sweep. It never reads the worktree, so
  removing the worktree doesn't affect tool diffs.

### D8. Pipelines run inside the session's isolation

- A pipeline launched from an isolated session calls ensure, uses the **session
  folder** as its `Cwd`, branches its tasks from the session branch, and merges task
  results into it. Pipeline work then lands through the same button.
- A pipeline from an in-place session uses `Session.dir`, falling back to the
  workspace's primary repo as today (fixes 6).
- **Refused merges** (decided 2026-10-05: auto-commit and retry):
  - A refusal is detected **before** merging, not read from the error. Today a
    refusal and a failure look identical, and `merge --abort` then fails too.
  - The check: under the lock, intersect `git status --porcelain` in the session
    folder with `git diff --name-only HEAD..<task branch>`.
  - Overlapping files are committed with the safety identity and a `Session:`
    trailer, and that commit is posted to the Data Service like D5's.
  - Then the merge runs.
- **Waiting for the agent.** A task merge rewrites files in a folder the agent may be
  editing, so the engine's merge step **waits until the agent isn't busy**. If it
  waits longer than the stage timeout, it merges anyway. It then posts a card, and
  sends the agent a message naming the files that changed under it.
- **Effect on an open Commit dialog:** auto-committed files drop out of it, and the
  Commit endpoint reports them as already committed (D5).

## Risks accepted for v1

- **A fresh worktree has no ignored files.** No `node_modules`, `.env` or build
  caches. In a JS repo the agent's first act becomes an install. Concurrent installs
  across worktrees already cause phantom build failures (seen in this repo). Possible
  follow-up: a per-workspace "worktree setup command". Until then it's documented,
  not solved.
- **Disk.** Each isolated session is a full checkout (git objects are shared). There's
  no parking or reclaiming. Revisit if it bites.
- **In-place hook attribution** (problem 7) stays wrong for commits the *agent* makes
  itself in place. Commit and Land record their own commits, so the button paths are
  correct. Fixing the hook (prefer a `Session:` trailer over `mads.sessionId`) is a
  separate change.
- **A user whose own checkout is a linked worktree.** `--git-common-dir` then points
  at the main worktree, so records name that repo. This is consistent with the
  pipeline's existing note in `engine/worktree.go`.

## Non-goals (v1)

- Hunk-level staging. Whole files only. If the user also edited a session file,
  committing it takes their edits too.
- Showing other sessions' changes, or the repo's `git status`, in the Commit dialog.
- Landing into a branch that isn't checked out in the primary.
- Changing the built-in isolation default. It stays `worktree` (spec §17). In-place
  users save "Current directory" with "Save as my defaults".

## Decomposition (dependency order)

1. **Shared worktree package + ensure.** Move `services/session/internal/worktree`
   mechanics somewhere the Router, the session service and the pipeline service can
   all use. Add D2's table, `mads-land.lock`, explicit start points, and the
   stale/locked cases. Scratch-repo unit tests for every row of D2.
2. **Isolation on.** D1 root, first-start-only, `worktree_base`, persist-before-start.
3. **Header state.** D4 counts on the record, recomputed on the listed events.
4. **Commit.** D3 routing, D5 path validation, identity, self-recording, Fast Model
   draft.
5. **Land.** D6 gates, the three steps, the base re-check, abort-on-any-failure,
   self-recording, best-effort fast-forward.
6. **Delete cleanup.** D7, Router side, plus the dialog's state panel.
7. **Pipelines.** D8: `Cwd`, task branch base, pre-merge refusal detection,
   auto-commit, waiting for the agent.
8. **UI.** Header control, Commit dialog, conversation cards, close prompt, rail
   marker, Settings toggle. Retire `MergeBackFlow`.
9. **e2e.**
   - One journey: start isolated, commit, conflict, ask the agent, land, keep
     chatting, delete.
   - `rm -rf` the folder and confirm the session recovers.
   - Land refused while the agent is busy.

## Decisions log

- 2026-10-02: build session isolation, copying the pipeline's self-derived root.
- 2026-10-05: the verb is **Land**. Pipelines share the session's isolation, with
  auto-commit and retry. **Click-to-commit only.** The Commit dialog shows only this
  session's files.
- 2026-10-05 (for review): Land keeps the folder and fast-forwards the branch, instead
  of removing the folder. Land needs a clean folder instead of auto-committing.

## Review log

**2026-10-05, critical review** (Fable 5.1 subagent; safety, cohesion, performance).
Findings and how they were handled:

1. **Blocker: commit linkage.** The hook links by `mads.sessionId`, not `Session:`,
   and doesn't fire on merge. Verified in a scratch repo. **Adopted:** Commit and
   Land record their own hashes (D5, D6 step 3). Problem 7 added. The hook fix
   deferred as a known risk.
2. **Major: Land rewrites files under a live agent, with no gate.** **Adopted:** D6
   gates (busy, pipeline, `MERGE_HEAD`/`REBASE_HEAD`, full porcelain) and queueing.
   Step 1's identity specified.
3. **Major: "cannot conflict" only if the base didn't move; step 2 must abort on any
   failure.** **Adopted:** base re-check loop, `merge --abort` on any non-zero exit,
   fast-forward made best-effort. The mockup's "only your uncommitted edits" claim
   corrected.
4. **Major: first-start-only and ensure vs `Start` ordering and git failure modes.**
   **Adopted:** persist before provider start (D1), deterministic ensure table with
   locked/stale/base-gone cases, shared flock (D2).
5. **Major: D7 refusal detection and races.** **Adopted:** pre-merge overlap check,
   wait-for-idle with timeout plus card plus agent message, per-path "already
   committed" (D5, D8).
6. **Major: no owner per verb.** **Adopted:** new D3. The shared package (step 1) is
   new work this created.
7. **Minor: Commit path handling and identity failures.** **Adopted** in D5.
8. **Minor: don't compute header state per render.** **Adopted** in D4.
9. **Minor: fresh worktrees lack ignored files.** **Adopted** as an accepted risk,
   with a follow-up named.
10. **Nit:** hard-coded "main", undefined "close", the counting rule. **Adopted**
    (D4, D6; mockup uses `<base>` wording in its notes).

Kept as the reviewer recommended: two-step Land with conflicts confined to the
worktree, the folder kept, no `checkout` in the user's tree, `--no-ff`, click-only
commits, path checks against the change list, the separate root, first-start-only
isolation, and retiring the free-text target box.

## Implementation notes (2026-10-05)

Where the build differs from the decisions above, and why:

- **Two locks, not one.** Using `mads-land.lock` for everything made a first
  session start wait behind a pipeline's whole landing step. Work inside a
  session's own folder now takes a per-session lock,
  `<git-common-dir>/mads-session-<id>.lock`. The repo lock is only for writes to
  the user's checkout: the engine's lands, and step 2 of Land. Order is session
  lock, then repo lock.
- **Linked sessions share one folder.** Spec §1.12 says linked sessions must
  never work in different worktrees. So a session started with a link to an
  isolated session in the same repo **joins its folder**: `dir` is the
  counterpart's worktree, isolation is `current_dir`, and the counterpart lands
  everything. Linking two existing sessions that each have their own folder is
  refused, with a plain sentence. Delete keeps a folder another session is
  working in, and keeps one a running pipeline is merging into. The dialog says
  so.
- **The uncommitted count includes untracked files** the session created, not
  only modified tracked files.
- **A folder on the wrong branch is reused, not moved aside.** If it's still a
  worktree of the repo, moving a live folder away would be the worse surprise.
  Land then refuses it as `off_branch`.
- **Sessions with no folder (`.`) are never isolated.** That avoids branching
  from whatever repo the Router happened to start in.
- **Commit and Land record their own commits,** with `source`, subject, files and
  `drafted` on `commit.created`, which is how cards are drawn. The mads hook is
  silenced for those commits (`-c mads.sessionId=`), so nothing is recorded
  twice. The user's own hooks still run.
- **Router-direct operations wait at most 5s for a lock,** then refuse with
  `locked`. The status read never takes the wake lock.
- **Land's writes to your checkout run in their own process group,** and session
  shutdown waits for a Land in flight. Killing the session mid-Land can't leave
  your checkout half-merged.
- **Pipelines:**
  - They take the session lock only around their write.
  - Mid-merge folders and the agent being busy are both waited out, never
    aborted.
  - Any staged change counts as overlap, because git refuses to merge over any
    staged change.
  - When the wait times out, the Router (a merge watcher plus a
    `folder_notice` job) sends the agent a message naming the files that
    changed under it.
- **Undo refuses a commit already on the branch's upstream** (`already_pushed`).
- **A pre-existing bug** in `githook.EnsureInstalled` planted files inside linked
  worktrees. It's fixed, because otherwise every isolated folder would read as
  dirty.

**Deploying** needs the Data Service, Router, session and pipeline binaries
rebuilt, and the Data Service and Router restarted, because of new routes:
`PATCH /sessions/{id}/worktree` and `DELETE /sessions/{id}/commits/{hash}`. The
built-in isolation default is still `worktree`. So after deploy, every new
session started in a git repo is isolated. Existing sessions keep running where
they always have.

