# Commit, Land, Push: multi-repo sessions and Commit from Changes

**Status:** Slices 1–5 built on `commit-multirepo` (2026-11-05), not merged; see Implementation notes.
**Date:** 2026-11-05. **Driver:** Drew started a session in `~/repo` by accident.
Its agent did real work in `~/repo/mads`. Changes showed most of it, but Commit
and Land refused it, and there was no way to recover without re-rooting.
**Mockup:** [`docs/mockups/commit-land-push.html`](mockups/commit-land-push.html) (v2).
**Builds on:** [`SESSION-WORKTREES-DESIGN.md`](SESSION-WORKTREES-DESIGN.md) (D3–D6),
[`CHANGES-DESIGN.md`](CHANGES-DESIGN.md).
**Test case:** session `ses_6ea9f23b1559f1f6` ("Mads UX Issues Investigation",
`dir = cwd = ~/repo`). Its 10 uncommitted files in `~/repo/mads` stay uncommitted
until this ships; Drew will commit them through the new flow.

## Context

What went wrong in the test session, and why:

1. **Commit and Land have no target.** `sessionwt.TargetFor`
   (`pkg/sessionwt/target.go`) resolves one repo from `sess.Dir` with
   `git rev-parse --show-toplevel`. `~/repo` isn't a repo, so it returns
   `ErrNoFolder`. `Ops.Status` then reports "no git folder", and `Ops.Commit`
   refuses with `CommitNoFolder`.
2. **Bash edits went unrecorded.** The shellwatch `Tracker` follows only the
   working tree that contains the session's folder (`shellwatch.New(wt.dir, …)`).
   With no working tree, it never ran. 7 of the 10 files were edited with
   `python3`/`sed` and have no ledger rows. Changes lists them as unattributed
   with no turns, and Commit (which picks files from the ledger, `SessionFiles`)
   would call them outside.
3. **Changes lost an edit to a race.** A repo other than the session folder's
   gets its shadow baseline on first touch, in the background
   (`shadow.Store.Capture` → `baselineRoot`). The first Edit in mads ran at
   14:28:54. The baseline `add -A` finished at 14:28:58 and so already contained
   that edit. `useRunway.test.tsx` vanished from Changes, though the session's
   own Edit tool changed it.
4. **No warning at launch.** The Launchpad knows the folder isn't a repo
   (`dir_status: "plain"`). It only warns when worktree isolation is staged
   (`Launchpad.tsx`, `dirWarning`).

The general form: a session's folder is a non-repo parent (or another repo),
and its agent edits files in one or more repos. Changes already handles that:
`pkg/changes` diffs every repo root the session touched. Commit and Land assume
one repo, taken from `Dir`.

Separately, Commit and Land are only reachable from the conversation dock, not
from the Changes tab where the code is reviewed. And there's no Push: the work
stops at your local branch.

## Principle

The session works in a **set of repos**. Every git action (Commit, Land, Push)
runs per repo, and every surface shows the same set. The set comes from what the
session touched, the same roots Changes already uses, not from `Dir` alone. No
action ever needs the session re-rooted.

## Decisions

### D1. A session's repo set

A session's repos are:

- the repo containing its folder (worktree, else `Dir`), if any;
- its workspace's declared repos (`contracts.Workspace.Repos`), if it has one;
- every repo it touched: a file tool's capture, a shellwatch finding (D7), or a
  recorded commit.

Each repo carries `root`, `isolated`, `branch`, `base` (isolated only),
`remote` (D5), and counts: `uncommitted`, `unlanded` (isolated only),
`unpushed`. The session record gets a per-repo summary next to the existing
`uncommitted_count` / `unlanded_count`, which become sums. Counts stay
computed off the hot path, as today (SESSION-WORKTREES D4).

`TargetFor` stops being the only way to resolve a target. `Ops.Status`,
`Commit`, `Land` and `Push` take a repo root, which must be in the session's set.

### D2. The Commit dialog (mockup §1–2, option B)

One dialog, always over **everything uncommitted** in the session's repos. The
Changes tab's tier, turn, search and baseline only affect what you see. mads
never commits a partial or transient state of a repo.

- **Two or more repos:** a repo list on the left, then "Not in a repo" last. The
  selected repo's files and message are on the right. Each repo shows a dot:
  ready (green), needs a message (amber), nothing selected (grey), failed (red).
- **One repo, or none:** a single pane. The repo and branch move into the
  subtitle and the button counts files (today's `CommitDialog`, plus the
  groups below). With no repo at all, the dialog only offers moving files (D4).
- **Each repo has two groups:**
  - *Changed by this session*: ledger-attributed files. They start checked.
  - *Changed by someone else*: files that differ in the repo but weren't
    changed by this session's tools. They start unchecked and can be
    committed. When the Data Service's ledger shows another session touched the
    path, the row names it ("session “Launchpad Pills UX Polish”"). Otherwise it
    reads "not by a mads session".
- **One message and one commit per repo, always.** Each repo has its own Fast
  Model draft, plus "Draft all messages". The button reads "Commit N repos" and
  commits them in turn.
- **Partial failure:** committed repos show their hash. A failed repo is
  selected, shows git's output and keeps its selection. The button becomes
  "Commit 1 repo". Nothing is rolled back.

### D3. Where Commit, Land and Push live

- **Dock (F2):** one button, the next step (Commit N, Land into base ↑N,
  Push ↑N), as today's `WorkButton`. The ⎇ popover ("Where this session's work
  goes") shows the whole path, each step with its own button.
- **Changes tab (G3):** a status chip in the Compare to row replaces the bare
  totals. It shows the totals, the state in words ("9 uncommitted", "3 commits
  to land", "committed · 1 to push", "committed and pushed"), the next-step
  button, and ▾ for the same path popover. Its counts come from the session's
  work state, not the Compare to choice. At narrow widths the state text drops
  first.
- **Several repos:** both surfaces sum ("Commit 11 · 2 repos") and open the D2
  dialog.

### D4. Moving a file into a repo (mockup §1)

Files outside every repo can't be committed where they are. In the "Not in a
repo" pane, drag a file onto a repo in the left list, or use "Move to…" (a repo
menu, for keyboard use). Either way, a folder picker opens at the top of that
repo's pane:

- The field is `PathField`, limited to folders inside the repo. It suggests the
  folder that already holds files of the same type first. An empty field means
  the repo root.
- **It's a move, not a copy.** The Router checks that the source is outside
  every repo and the destination doesn't exist and isn't ignored by git, then
  renames the file (copy and delete across devices).
- The moved file counts as the session's change (a ledger row) and appears
  checked in that repo's "Changed by this session" group.
- The agent is told the new path through the runner's message queue
  (`runner/queue.go`), so its next turn doesn't reuse the old one.
- The move happens when you press Move here, not on Commit. Cancelling the
  dialog afterwards doesn't undo it, and the picker says so.

### D5. Commit → Land → Push

Land appears only for an isolated repo. Any step can be skipped. Push has two
forms, and v1 ships both:

| State | Push sends | From |
|---|---|---|
| Isolated, before Land | the session branch (`-u`, ready for a PR) | the session's worktree |
| Isolated, after Land | the base branch | your checkout (`Primary`) |
| In place | the checked-out branch | your checkout |

- **Remote:** the branch's upstream if it has one; else `origin`; else the only
  remote. With no remote, Push is hidden.
- **Every push asks for confirmation.** The dialog lists the commits and says
  "fetched just now · fast-forward". Push fetches first. **Never a force push.**
- **Remote ahead: rebase, but only if you choose it in the confirmation.** The
  confirmation lists the incoming commits, and its main button becomes "Rebase
  onto origin and push". mads never rebases without that click. The rebase:
  - uses `git rebase --rebase-merges` with Land's mergiraf setup, so a Land
    merge commit (`merge --no-ff`) and the session commits under it are kept,
    not flattened into copies;
  - only runs when no session working in that folder is mid-turn, no merge or
    rebase is in progress, and none of the folder's uncommitted files are touched
    by the incoming commits. Otherwise the button is off and the confirmation
    says which check failed;
  - on any conflict, runs `git rebase --abort`, checks the branch is back on its
    old commit, and offers "Ask the agent to rebase" with the conflicting files
    named. Nothing is left half-done.
  - Rebasing re-creates commits, so commit signing falls under the same
    no-prompt rule below.
- **No credential prompts:** git runs with `GIT_TERMINAL_PROMPT=0`, stdin
  closed and a timeout, so a sign-in failure shows git's message rather than
  hanging. (How this behaves with credential helpers and SSH agents still has
  to be checked.)
- Push runs under the same locks as Land (session lock, plus the repo's
  `mads-land.lock` when it pushes from your checkout).
- **Several repos:** Land and Push use the D2 dialog's repo list. One action
  per repo, with failures reported per repo.

### D6. Land refusals open Commit

When Land refuses because the worktree has uncommitted files (`LandDirty`), it
opens the D2 dialog with those files listed. The session's own files are under
"Changed by this session"; the refusal's `Outside` files are under "Changed by
someone else". Today the refusal names them and offers "Ask the agent".

### D7. Shell edits across the repo set

No scanning folders, and no file-system watching (rejected for scale, latency,
nesting depth, network drives, and sessions started in `/`). Instead:

- **Repos named by commands.** For each command that could write
  (`shellwatch.ReadOnly` false), the parser `ReadOnly` already uses extracts
  `cd X`, `git -C X` and absolute paths. Each is resolved to its repo root on the
  tracker goroutine, and cached. This is string work on the pump, nothing more.
- **One `Tracker` per repo in the set.** A repo stays tracked for the rest of
  the session once it has been named. Each tracker keeps today's behaviour:
  looks are coalesced, read-only commands are skipped, a slow repo (over 1.5 s)
  is looked at once per turn, and only new findings are recorded, as one batched
  ledger write and one coalesced recount.
- **Cap:** about 8 trackers per session. Past that, extra repos are looked at
  once per turn.
- **Isolated sessions:** the session's own worktree keeps today's whole-worktree
  status (`startShellWatch` skips it). Other repos in its set get trackers.
- **Known gaps:**
  - A command that writes into a repo it doesn't name (a script, a build).
    Those files land under "Changed by someone else".
  - The first command in a newly named repo. Its edits are still credited by
    modification-time window. But with no earlier snapshot, a deletion by that
    command is missed, and a file already dirty before the session is compared
    against HEAD. Changes notes that the root was compared against HEAD.

### D8. Fix the first-touch baseline race

When a capture triggers a root's baseline, the baseline tree takes that path's
captured pre-image instead of the file on disk. This applies to any capture that
lands before the baseline finishes, not only the first. A root first found by
shellwatch (D7) is baselined from HEAD plus whatever `git status` shows dirty,
excluding the paths that command changed.

### D9. Launchpad guardrail

Show `dirWarning` whenever `dir_status` is `plain`, whatever the isolation
setting: "~/repo isn't a git repository. You can work here, but changes can only
be committed in the repos the agent touches." When
the folder contains repos one level down, offer them as shortcuts. Never scan
deeper.

## Risks accepted for v1

- D7 misses writes into repos a command doesn't name. Mitigation: those files
  still appear under "Changed by someone else".
- Naming the other session that changed a file relies on that session's ledger,
  which has the same Bash-only gaps.
- Commits across several repos aren't atomic; per-repo results are reported.
- Push credential behaviour across helpers and SSH agents is unverified (D5).

## Non-goals (v1)

- One isolated worktree per repo in a multi-repo workspace. Isolation stays
  with the primary repo, as today.
- Creating PRs. Push leaves the session branch ready for one.
- Force push, pull, rebase or merge from the remote.
- Moving folders, or copying files, into a repo (D4 moves single files).
- Scanning or watching folders for repos or edits (D7).

## Decomposition (dependency order)

1. **Fixes.** D8 (baseline race) and D9 (Launchpad guardrail). Independent and
   small.
2. **Repo set and multi-repo Commit.** D1 repo set and per-repo counts; repo-taking
   `Ops`; D2 dialog (B and single-pane); D3 dock popover and G3 chip;
   "changed by someone else" attribution lookup; D7 trackers per repo. The test
   session becomes committable here.
   - **Before D7 ships:** replay a Bash-heavy transcript (the test session has
     about 85 Bash calls) through the trackers in a test. Count read-only calls,
     looks, ledger writes and events.
3. **Move into repo.** D4: Router endpoint, ledger row, queued note to the agent.
4. **Push.** D5: a new `sessionwt` op, remote resolution, fetch and ahead/behind,
   confirmation, locks, per-state target.
5. **Land and Push across repos; Land refusal opens Commit.** D5 and D6.

## Decisions log

- 2026-11-05 — Launchpad guardrail for non-repo folders (D9). *Drew.*
- 2026-11-05 — Commit is also reachable from Changes, as one dialog over
  everything uncommitted; filters are for viewing only. *Drew.*
- 2026-11-05 — Sections per repo; always one message per repo. *Drew.*
- 2026-11-05 — Files outside every repo can't be committed. Drag to a repo plus
  a path picker; a move, not a copy. *Drew (copy rejected after review).*
- 2026-11-05 — No up-front repo scan and no fsnotify/FSEvents watching; repos
  come from what commands name (D7). *Drew.*
- 2026-11-05 — Mockup picks: dialog B, single pane for one repo or none,
  "Changed by someone else", dock F2, Changes G3. *Drew.*
- 2026-11-05 — Commit → Land → Push; both kinds of push; confirm every push;
  Land and Push across repos use the D2 dialog; a dirty Land refusal opens Commit.
  *Drew.*
- 2026-11-05 — When the remote is ahead, the push confirmation offers "Rebase
  onto origin and push" (opt-in, never automatic), with D5's gates; on conflict,
  abort and fall back to asking the agent. No "untested" warning: the user knows
  they're rebasing. *Drew.*
- 2026-11-05 — The "details I filled in" (per-repo counts, remote resolution,
  Push locks, prompt-free git, D6 grouping, ~8 tracker cap, D9 wording and
  one-level-down repo shortcuts) are accepted. *Drew.*

## Implementation notes

### Slices 1–2 (2026-11-05, branch `commit-multirepo`)

- **D8:** `shadow.Store.Capture` registers each path's first pre-image before
  reading it; the baseline writes those into its index after `add -A`
  (`applyPreImages`). Repos found by shell tracking get
  `Store.BaselineBefore` with the changed files as HEAD had them.
- **D9:** `GET /sessions/defaults` returns `dir_repos` (one directory read,
  hidden folders skipped, a worktree's `.git` file counted, at most 8).
- **D1, as built:** the repo set is the folder's repo plus every repo the
  session's own ledger rows fall in (`sessionwt.RepoTargets`). Repos nested
  inside the folder's repo are left out. **Workspace repos aren't added up
  front:** they join once a tool or a named command touches them, so a
  workspace session doesn't pay a `git status` per repo for nothing.
  `Session.Repos` carries the per-repo counts; the old counts are sums.
- **D2, as built:** `GET /sessions/{id}/commit-plan` (Router, read-only).
  A file not changed by the session reads `session “Name”` (another
  session's ledger, `POST /file-changes/touchers` on the Data Service), else
  "changed while this session ran" or "uncommitted before this session
  started" (session-start snapshot), else "not by this session". Commit's
  path check no longer rejects files the session didn't change. Undo finds
  the repo whose tip the commit is when the card doesn't name one.
- **D3:** dock popover path is `WorkPath`, shared with the Changes tab's
  status chip (`WorkChip`); the session record reaches the Changes tab
  through `WorkspaceTabContext`.
- **D7, as built:** `shellwatch.Named` scans each write-capable command
  (heredocs included) for `cd`, `pushd`, `git -C` and absolute or `~/`
  paths; the runner keeps one tracker per repo (`shellSet`), resolving
  new repos off the pump. Past 8 repos, trackers look once per turn
  (`NewPerTurn`). An isolated session's own worktree still has no tracker.
- **Replay** (`shellwatch.TestReplayTranscriptLoad`, run with
  `MADS_REPLAY_TRANSCRIPT=<.jsonl>`), on five real transcripts of 96 to
  1,489 Bash calls: 32–55% of calls skipped as read-only; at most 3 repos
  followed; scan cost 19–37 µs per call on the pump; one look about 15 ms
  in mads. Looks (an upper bound on ledger writes and events) were at most
  about 1.6 per write-capable call before coalescing.

### Slice 3 (2026-11-05)

- **D4, as built:** `POST /sessions/{id}/move` (Router) and
  `GET /sessions/{id}/move/folders` (from `git ls-files --cached --others
  --exclude-standard`, same-extension folders first). The plan carries
  `move_targets`: its repos, the repos directly inside a non-repo session
  folder, and the workspace's repos. Outside files that are gone (moved,
  deleted) leave the plan.
- **The note to the agent** generalizes the re-root note:
  `Session.PendingNotes` (scoped `PATCH /sessions/{id}/notes`) ride the
  next turn sent, after any re-root note, and only the delivered ones are
  removed. The Router tells a live child through `POST /notes`. As with the
  re-root note, terminal (PTY) input doesn't carry it; it stays owed.

### Slice 4 (2026-11-05)

- **D5, as built:** `GET /sessions/{id}/push` (fetches, then the preview)
  and `POST /sessions/{id}/push`, both run by the Router. Targets: `branch`
  (an isolated session's own branch, setting its upstream; in place, the
  checked-out branch) and `base` (isolated only, from the user's checkout).
  `expect` refuses a push whose branch moved since the confirmation.
- **Counts:** `unpushed_count` (and per repo) is what the next push would
  publish, against the remote branch as last fetched (or, for a branch the
  remote lacks, against all of the remote's branches): the base for an
  isolated session, else the checked-out branch. 0 with no remote.
- **Rebase gate:** "busy" is any session with a turn in flight whose
  folder is inside one of this session's repos (paths compared resolved),
  plus a pipeline run for this session.
- **Credentials:** `GIT_TERMINAL_PROMPT=0`, `GCM_INTERACTIVE=never`, ssh
  in `BatchMode` unless the user set their own ssh command; fetch 60 s,
  push 2 min. Still unverified against real credential helpers.
- **UI:** the rail doesn't mark "to push" (every session in a shared
  checkout would light up). "pushed ✓" shows only after a push from that
  view, since 0 unpushed can also mean no remote.

### Slice 5 (2026-11-05)

- **Push across repos:** the push confirmation takes the Commit dialog's
  repo-list layout when more than one repo has commits to push; each
  repo's preview is fetched at once, and a repo the remote has moved past
  needs its own "Rebase onto … first" tick. One push per repo, one at a
  time.
- **Land across repos isn't built, deliberately:** a session isolates only
  its folder's repo (one worktree per repo is a non-goal), so at most one
  repo ever has anything to land.
- **D6:** a Land refused as dirty opens the Commit dialog, as do the dock's
  and the Changes chip's "changes outside" controls; the popover offers
  "Commit them…". In practice the popover already holds Land back while
  such files exist, so the refusal mostly matters when counts are stale.
