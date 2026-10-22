# Changes — design in progress (2026-10-14)

Drew + Claude design session. **Status: draft.** Agreed points are marked
*(agreed)*; everything else is a proposal still under review. Mockups:
`docs/mockups/changes-*.html` (next step).

## Why

The Files tab and the Context Rail's Changes list were built as an MVP: a
flat list of files touched. Real sessions break it:

- Agents make **far more changes than a person normally reviews** — hundreds
  of files, including throwaway test and scratch scripts.
- The changes may be **outside the user's expertise**.
- Agents commit unpredictably: many commits, one at the end, none at all, or
  in a non-git directory. They work in place, on branches, or in worktrees.

Goal: show what the agent changed in a way that is easy to scan, so the user
can pick files, turns or commits and understand what the agent did. The
primary job is **understanding the work** *(agreed)* — not approval, not
file-finding. Those are secondary.

### What's wrong today

`ui/src/files/FilesTab.tsx` stacks a commit list over a "working changes"
list (the touch ledger collapsed by `collapseChanges.ts`) beside a Monaco
diff. The rail (`RailChangesList.tsx`) is the same list, filename-first.

1. **Double listing.** Committed files also appear under Working changes,
   because the ledger is every file touched, not what's uncommitted. There's
   no join key between `FileChange` and `Commit`.
2. **No magnitude.** No +/− line counts; a one-line config tweak looks like a
   900-line rewrite.
3. **No grouping, filtering or search.** 200 files is a scroll.
4. **Scratch is indistinguishable from real work.**
5. **One silent baseline.** Working diffs are against session start; nothing
   shows "what Land would apply" for a worktree session.
6. **No link to the "why".** Nothing connects a file to the turns or tool
   calls that changed it, though the shadow store records both
   (`docs/SHADOW-GIT-DIFFS-ADR.md`).
7. **The list comes from the ledger, not the disk.** Prior art's worst
   complaint (Cursor: `git status` showed 177 files, the agent's review 21)
   is this exact shape.

## Prior art (research, 2026-10-14)

Full report in the session transcript; the transferable patterns:

- **Baseline switcher** — Codex (uncommitted / branch / last turn),
  Conductor and GitHub (one commit at a time), Gerrit (any two patchsets).
- **Importance ordering, noise muted at the bottom** — Amp's Intelligently
  Ordered Diffs (core first; tests, fixtures, generated muted; toggle back to
  alphabetical). Research: alphabetical order is suboptimal for ~54% of
  reviewers; later files get less attention; no single order wins, so let it
  be switched.
- **Intent groups with a reading order** — CodeRabbit cohorts/layers, Devin
  Review, Amp tours.
- **Step timeline beside files** — Devin Progress, VS Code per-request
  changes, Cline per-step Compare.
- **"Since you last looked"** — GitHub/Gerrit viewed state, Bugbot
  incremental review.
- **Batched line comments back to the agent** — Claude Desktop, Warp,
  Conductor, Codex.
- **Moved-code detection** — Devin, Amp, SemanticDiff.

Anti-patterns: a change list that disagrees with disk; review only as an
end-of-session modal; an AI summary *instead of* the diff ("the summary is a
story, the diff is what happened"); review affordances lost after commit;
user edits mixed in unattributed.

## Decisions

- **Primary job: understanding the work** *(agreed)*.
- **Layout: option A** *(agreed 2026-10-14)* — tier-sorted list + turn
  strip, Group by: Tier | Folder | Turn | Commit | Intent. It must be useful
  with no LLM; plug-ins add on top. B (story first) and C (timeline first)
  rejected: both lean on an agent telling an accurate story.
  Mockups: `docs/mockups/changes-options.html` (A/B/C), `changes-v2.html`
  (A refined, current).
- **The Files tab is renamed "Changes"** *(agreed)*.
- **Turn strip** *(agreed)*: whole column is the hit target; ‹ › arrows; a
  context line shows the first ~120 chars of the turn's **prompt** (the
  deterministic context — the LLM digest is optional). It filters in Tier /
  Folder / Commit grouping and navigates (scroll-spy) in Turn grouping.
- **Only user prompts open a turn** *(agreed)*. Work the harness does on its
  own after a background task finishes is credited to the user turn that was
  running. No synthetic empty-prompt turns: presenting harness-started work
  as its own turn would misrepresent who asked for it. A future refinement
  could credit it to a turn labelled by the background task's notification;
  never to an anonymous one. (The mockup's hatched turn 10 is superseded.)
- **Rows carry no risk pills** *(agreed)*: dot, A/M/D, name, +/−, size bar.
  Commit state comes from the Commit grouping and the diff header link, not a
  per-row hash pill.
- **Folder grouping is a standard tree** *(proposed in v2)*: compact
  folders, alphabetical, per-folder rollups, all-generated/scratch folders
  collapsed.
- **Tier is the secondary sort in every grouping** *(proposed in v2)*.
- **Rail** *(Drew's sketch, v2)*: Changes (n files) → tier dropdown →
  ‹ turn › selector (arrows skip turns with nothing in the tier; prompt
  below) → files by size → "Open in Changes". Optional "Latest" follow stop.
- **Diff real estate** *(agreed 2026-10-14, after the first live pass on a
  13" laptop; mockup `docs/mockups/changes-diff-space.html`)*. The diff
  pane was ~640px: long lines scrolled sideways, and Monaco silently forced
  inline below 900px, so the view toggle did nothing. Option B + Full-width:
  - The diff wraps long lines, uses a slim gutter (no minimap, overview ruler
    or folding), folds unchanged stretches, and uses soft palette tints
    (`utils/createMonacoTheme.ts`). New and deleted files show as plain code
    with an edge bar and a "New file · N lines" banner.
  - Inline is the default; the Inline / Side by side choice is always
    honoured and remembered.
  - The list column is 340px by default, draggable and remembered; ⇤ or `\`
    hides it, leaving a slim strip to bring it back. `j` / `k` step through
    files, and ‹ n/N › appears in the diff header while the list is hidden.
  - **Full-width** / **Half-width** (rightmost in the tab's toolbar) slides
    the tab over the sessions sidebar; the icon rail stays. It's on by
    default below 1600px, and the button's choice sticks while the tab
    lives. Esc, leaving the tab, or switching session gives the sidebar
    back. No attention dot while it's covered: the user is reading code.
    It applies on entering the tab, not on picking a file, so rows don't
    shift under the cursor.
  - The turn strip stays full size; its label, file count and prompt (two
    lines) sit to the right of the bars, not on a line below.
  - Stacked (list above the diff) was rejected: the Folder tree is
    unintelligible in a short, wide list.
- **Subagent worktrees are left out** *(agreed 2026-10-21, stopgap)*. A
  harness gives each subagent its own worktree inside the session folder
  (Claude Code: `.claude/worktrees/<name>/`; Hermes with
  `delegation.worktree_isolation`: `.worktrees/subagent-<id>/`) and removes
  it when the subagent is done. Codex and goose subagents share the
  parent's folder, so their edits are real and stay listed. Their edits reach the ledger as the session's own, so
  live ones double-counted the work, and removed ones read as mass deletions:
  the only "before" was a mid-session pre-image, because the worktree didn't
  exist at session start. `pkg/changes/worktrees.go` drops any file under the
  session folder that's in a known harness worktree location (matched by
  path, so removed ones count) or a live linked worktree. Nested repos and
  submodules aren't affected. A note gives the count. The work shows once it
  lands in the folder. Delegates may later get their own changes view.
- **Search** *(agreed)*: whole-token matches win (`.ts` matches `foo.ts`, not
  `foo.tsx`; a camelCase hump counts as a word edge). Any partial matches
  hidden that way are counted, with a "show N partial matches" link. When
  nothing matches a whole token, plain substring matching applies.
- **Baselines** *(agreed)*: Session start (default — the only one that
  covers non-git directories), Base branch (what the worktree was created
  from, what Land targets), main (the repo's default branch). Each hides when
  it equals another or the folder isn't git.
- **Scratch files** *(agreed)*: identified by fast deterministic rules (never
  an LLM), so they also shrink the input to ordering/summarization. Deletable
  in one action. **Every delete asks for confirmation; no undo in the MVP.**
- **"Since you last looked"** *(agreed)*: a light per-file marker, no
  Reviewed checkboxes.
- **Scope** *(agreed)*: the same component later replaces the pipeline
  drill-in's `TaskFiles.tsx`, as part of the separate pipeline UX overhaul.
- `docs/review-flow-mockup.html` is **superseded** *(agreed)* — criteria
  mapping and per-file reviewed baselines are out.

### Plug-ins: grouping, summarization, review *(agreed in principle)*

Token cost and personal preference both argue for swappable methods. mads
ships an opt-in default that works well; users can replace it.

Proposal (contract still to be designed): **command plug-ins** — a
descriptor (`id`, `slots: [group, summarize, review]`, `trigger`, `command`)
naming a program that reads a JSON change manifest (files, tiers, line
counts, diffs, turn digests) on stdin and writes groups / summaries /
findings as JSON on stdout. The shipped default is a built-in engine
implementing the same contract on the Fast Model. **Agent plug-ins** (a
prompt or skill run as a delegate session) come later as a second engine
type, mirroring `docs/DECLARATIVE-PROVIDERS-DESIGN.md`'s descriptor+engine
split.

Triggers: **`manual`** and **`settled`** *(agreed)*. No `turn_end`:
summarizing at every turn end spends tokens on half-finished work.

## Architecture *(agreed)*

Split the **writer** from the **reader**; no new service yet.

- **Writer: the session service.** Pre-image captures and turn snapshots
  must run in-process with the agent's event stream (the ADR depends on
  catching a file before the tool runs). Unchanged, plus `session.settled`
  below.
- **Reader: `pkg/changes`**, a domain package, served by the router behind
  one endpoint plus a `changes.updated` event. It owns the `ChangeSet`
  contract the Files tab and rail consume: list-from-disk against the chosen
  baseline, line counts, tiers, scratch and risk rules, turn-range filtering,
  and "since you last looked". This also pulls the diff logic now spread
  across `working_diff.go`, `commit_diff.go` and `tool_diff.go` out of the
  router, whose README says it holds no business logic.
- **Plug-in runs** use the router's existing spawn-run-exit job pattern
  (`internal/job`).
- **Cross-session conflicts** need no resident process:
  - *Same checkout* (two in-place sessions on one file) — a query over the
    data service's ledger, which already does a narrow version
    (`claimedByOthers`, `services/data/internal/store/sessions.go`).
  - *Separate worktrees* that will clash at Land — path overlap from the
    same query; a true conflict check is an on-demand `git merge-tree`.

Why not a Changes service now: it would be a fifth supervised process type
whose job is reading data owned by others (the session's shadow store, the
data service's ledger, the user's repos) while owning nothing but a cache.
The session service can't host the reader either: it runs only while a
session is active, and never talks to other sessions.

**Extraction triggers** — promote `pkg/changes` to a service if either
appears: proactive cross-session conflict alerts needed while no UI is open
and no session is active (even then, the always-running router is a
candidate host), or diff caching heavy enough to want its own process. Keep
`pkg/changes` service-shaped so the move is mechanical.

## `session.settled` *(agreed; build with its first consumer)*

"Turn ended" is not "quiet": backgrounded subagents and background processes
keep writing after turn end. Only the runner sees every signal, so it decides
and announces.

**Settled** = all of:
- main agent idle (`markAgentIdle`);
- `AgentState.Background == 0` (`setAgentBackground`);
- no delegate in `running` (`publishDelegates`);
- shellwatch has no look running or pending (new `Tracker.Busy()`);
- none of the above changed, and no file change was recorded
  (`recordFileChange`, `recordShellChanges`), for a **5 s quiet window** —
  long enough to cover claude-code starting a turn by itself when a
  background task finishes.

A session whose process has exited is settled by definition.

**Event:** a new `session.settled` type (not a field on `AgentState` — that
would touch every `agent_state` consumer and the rail's grouping). Appended
once per quiet period, **only after the settle-time snapshot finishes**;
payload lists the roots snapshotted and their commits. So a reader hearing
`settled` can read a fresh snapshot immediately — that ordering is the
contract. Cold readers derive it: inactive ⇒ settled; else settled iff the
latest `session.settled` is newer than the latest `working` agent state.

**Settle-time snapshot:** a synchronous variant of `shadow.commitTurn`.
Fixes a data gap independent of plug-ins: today a backgrounded subagent's
post-turn writes land in the *next* turn's commit, or nowhere if there isn't
one. Cost is the ADR's ~0 ms no-op in the common case.

**Regression guards:**
1. *A turn starting during the settle commit.* Its pre-image captures could
   precede the settle commit's `turn_commit` record, and `tool_diff` would
   take that commit as the tool's "after" side — taken before the tool ran.
   The commit re-checks that no turn started since settling; if one did, it
   appends no `turn_commit` record and no event (the shadow commit itself is
   harmless; the next turn commit builds on it).
2. *Timer outliving `Shutdown`* (the agent-state retry had this bug): stop it
   in `Shutdown`, with a test like the retry-leak test.
3. `EventTypes` golden test updated; the type mirrored in
   `ui/src/router/types.ts`.
4. PTY sessions without structured events never settle — documented, not
   faked.

Tests: settles after a turn; held by a background process, by a running
delegate, by a pending shell look; quiet window resets on a write; a turn
starting mid-settle suppresses the record and event; no timer after
`Shutdown`. Full session/shadow/router/contracts suites, `make check` and UI
vitest compared before/after. UI subscriptions are always type-filtered
(`ui/src/router/events.ts`), so the new type is invisible to existing
surfaces.

## Open questions

- `ChangeSet` schema and the plug-in JSON contract (manifest in, groups /
  summaries / findings out; size limits and paging for large diffs).
- Scratch rules: the exact deterministic set (created in session *and* one
  of: outside the session root, under a temp dir, gitignored, matches
  scratch name patterns at repo root, created by a shell command and never
  referenced by another changed file).
- Risk-flag rules (deletions, dependency manifests, CI, auth, migrations,
  permissions, outside-repo).
- Where "since you last looked" is stored (per user, per session, per file).
- Line counts and moved-code detection: `git diff --numstat` against the
  shadow baseline covers repo roots; non-repo roots need blob-to-disk diffs.
- Task scope for pipeline runs. The Tasks-tab pipeline lens
  (`docs/mockups/tasks-pipeline.html`) lists each task's touched files, and
  clicking one should open its diff in Changes scoped to that task. Today
  `ChangesFocus` can only name a path, tier, or turn. Open: what baseline a
  task's diff is taken against (its worktree's fork point while the task is in
  flight, its merge commit once it has landed), whether "task" becomes another
  Group by option or a filter like turn, and how a blocked task's
  never-merged worktree is shown. This is the same component that replaces
  `TaskFiles.tsx` (see Scope, above).
