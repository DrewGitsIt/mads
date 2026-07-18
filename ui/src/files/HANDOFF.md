# HANDOFF: Committed files open their diffs

Task: tsk_1b1c78ee5d438a91
Stage: architect → executor

## Root cause

`CommitList`'s file rows (`ui/src/files/ChangeList/CommitList.tsx`) are plain
`<span>`s — no `onSelect`, no button semantics. `ChangeList`'s rows are
buttons wired to `getFileDiff -> DiffViewer`. There is also no Router
endpoint that can produce a diff for a file inside a commit.

**Correction to the task brief**: the brief says working-change diffs already
work via `GET /sessions/{id}/changes`. That route does not exist. It is
registered as a documented, test-enforced gap in
`services/router/internal/api/route_manifest_test.go`'s `knownGapRoutes`
table (`"Files tab / merge-back (getFileDiff), not built"`), and both
`ui/src/router/client.ts` and `ui/src/router/types.ts` carry comments saying
the same. **Do not build `/changes` as part of this task** — it's a
pre-existing, separately-tracked gap. "Working-change selection still works"
in the acceptance criteria means: the `ChangeList` click/selection mechanic
keeps functioning exactly as it does today (driven by `FakeRouterClient`
fixtures in tests; against a real server it already fails the same way it
does on `main`). This task only adds the new commit-diff endpoint and wires
it up — it does not fix the working-change gap.

## 1. Router: `GET /sessions/{id}/commits/{hash}/diff?path=...`

New file `services/router/internal/api/commit_diff.go`, following the shape
of `commits.go`'s `handleSessionCommits`/`commitFiles`.

```go
type fileDiff struct {
    Path     string `json:"path"`
    Original string `json:"original"`
    Modified string `json:"modified"`
}

func (s *Server) handleSessionCommitDiff(w http.ResponseWriter, r *http.Request) {
    sessionID := r.PathValue("id")
    hash := r.PathValue("hash")
    path := r.URL.Query().Get("path")
    if path == "" {
        writeErrMsg(w, http.StatusBadRequest, "path is required")
        return
    }
    if !commitHashRe.MatchString(hash) {
        writeErrMsg(w, http.StatusBadRequest, "invalid commit hash")
        return
    }

    commits, err := s.data.Commits(r.Context(), sessionID)
    if err != nil {
        writeError(w, err) // existing helper, session lookup failure
        return
    }
    var commit *contracts.Commit
    for i := range commits {
        if commits[i].Hash == hash {
            commit = &commits[i]
            break
        }
    }
    if commit == nil {
        writeErrMsg(w, http.StatusNotFound, "commit not found for session")
        return
    }
    if commit.Repo == "" {
        writeErrMsg(w, http.StatusBadGateway, "no repo recorded for commit")
        return
    }
    if !commitReachable(r.Context(), commit.Repo, hash) {
        writeErrMsg(w, http.StatusBadGateway, "commit unreachable in repo")
        return
    }

    original, _ := gitShowFile(r.Context(), commit.Repo, hash+"^", path) // "" if file didn't exist pre-commit (added file) — not an error
    modified, err := gitShowFile(r.Context(), commit.Repo, hash, path)
    if err != nil {
        writeErrMsg(w, http.StatusBadGateway, "file not found in commit")
        return
    }

    writeJSON(w, http.StatusOK, fileDiff{Path: path, Original: original, Modified: modified})
}

func commitReachable(ctx context.Context, repo, hash string) bool {
    cmd := exec.CommandContext(ctx, "git", "cat-file", "-e", hash+"^{commit}")
    cmd.Dir = repo
    return cmd.Run() == nil
}

func gitShowFile(ctx context.Context, repo, rev, path string) (string, error) {
    cmd := exec.CommandContext(ctx, "git", "show", rev+":"+path)
    cmd.Dir = repo
    out, err := cmd.Output()
    if err != nil {
        return "", err
    }
    return string(out), nil
}
```

Notes / deviations from the literal brief wording:

- The brief says "`git show <hash> -- <path>` parsed to the FileDiff shape."
  That command emits a unified-diff patch, which is not trivially
  reconstructable into full original/modified text (context lines, no
  content outside the hunks). Use two full-content fetches instead:
  `git show <hash>^:<path>` for the pre-commit content and
  `git show <hash>:<path>` for the post-commit content. This is the standard
  way to get `DiffViewer`'s `{original, modified}` shape and matches how
  `FileDiff` is already used for working-change diffs.
- `hash^:<path>` failing is the **expected, benign** shape for an added file
  (no parent had that path) — treat only the *modified*-side fetch failure,
  or an unreachable commit/repo, as an error. Do not error on a failed
  original-side fetch.
- Reuses `s.data.Commits` + linear scan (no single-commit-by-hash lookup
  exists in `dataclient`; this mirrors the existing `commitFiles` enrichment
  pattern in `commits.go`).
- Unlike `commitFiles`'s silent-degrade-to-nil best-effort pattern (fine for
  list enrichment), this endpoint must return an **honest error status**
  (400/404/502) per the acceptance criteria — never a 500, never a silent
  empty 200.
- `commitHashRe` is already package-level in `commits.go` — reuse it, don't
  redefine.

Register in `services/router/internal/api/api.go` near the existing
`GET /sessions/{id}/commits` registration (~line 155):

```go
mux.HandleFunc("GET /sessions/{id}/commits/{hash}/diff", s.handleSessionCommitDiff)
```

### Route manifest test

Add a row to `routeManifest` in
`services/router/internal/api/route_manifest_test.go`:

```go
{"GET", "/sessions/abc/commits/def/diff", "GET /sessions/{id}/commits/{hash}/diff"},
```

Do **not** touch `knownGapRoutes` — `/changes` stays there, unrelated to this
task.

### New test file `commit_diff_test.go`

Mirror `commits_test.go`'s style (`newTestServerWithData`, real temp git
repos, `gitCommitTestRun`, `data.RecordCommit`). Cover:

- Added file in a commit: empty `original`, populated `modified`, 200.
- Modified file in a commit (exists in parent, changed): both sides
  populated and differ, 200.
- Commit hash not linked to the session: 404, not 500.
- Commit recorded with no `Repo`: 502 with an error body, not a silent 200.
- Commit recorded but repo path unreachable (e.g. deleted temp dir): 502,
  not 500.
- Missing `path` query param: 400.

## 2. Frontend

### `CommitList` — selectable file rows

`ui/src/files/ChangeList/CommitList.tsx`: convert each file row from a
`<span>` to a `<button type="button">`, matching `ChangeList.tsx`'s row
markup (`aria-current`, `data-selected`, `onClick`). New props:

```ts
selectedFile?: { hash: string; path: string };
onSelectFile: (hash: string, path: string) => void;
```

A row is selected when `selectedFile?.hash === commit.hash && selectedFile.path === file.path`.

`ui/src/files/ChangeList/CommitList.module.css`: share `ChangeList`'s
interactive-row idiom via `composes` instead of duplicating the button
reset/hover/`data-selected` styling:

```css
.fileRow {
  composes: row from "./ChangeList.module.css";
  font: var(--fs-sm) var(--mono); /* keep CommitList's existing secondary-prominence sizing */
}
```

Drop the CSS declarations that duplicate what `.row` in `ChangeList.module.css`
already provides (`display: flex; align-items: center; gap: var(--space-2);`);
keep `.filePath`'s text-overflow/mono styling as-is since `ChangeList.module.css`'s
`.row` doesn't style text color/size — that stays CommitList-specific.

### `DiffViewer` — commit label

`ui/src/files/DiffViewer/DiffViewer.tsx`: add optional prop
`commitHash?: string`. When present, render a passive `Chip` (no `onClick`,
confirmed `Chip` renders a plain `<span>` in that mode) in the toolbar:

```tsx
{commitHash && <Chip tone="neutral">Diff from commit {commitHash.slice(0, 7)}</Chip>}
```

Wording is deliberately distinct from `CommitList`'s bare short-hash text so
it's unambiguous as a test/e2e selector.

### `FilesTab` — mutually exclusive selection

`ui/src/files/FilesTab.tsx`: replace the current `selectedPath` state with a
discriminated union so mutual exclusivity is structural, not managed by hand:

```ts
type Selection =
  | { source: "working"; path: string }
  | { source: "commit"; hash: string; path: string };

const [selection, setSelection] = useState<Selection | null>(null);
```

- `ChangeList`'s `onSelect` sets `{ source: "working", path }`.
- `CommitList`'s new `onSelectFile` sets `{ source: "commit", hash, path }`.
- Pass `selectedFile={selection?.source === "commit" ? { hash: selection.hash, path: selection.path } : undefined}` to `CommitList`.
- Pass `selectedPath={selection?.source === "working" ? selection.path : undefined}` to `ChangeList` (or equivalent existing prop name — check current usage).
- The diff-loading effect branches on `selection.source`: `"working"` calls
  the existing `getFileDiff`; `"commit"` calls the new `getCommitFileDiff(sessionId, hash, path)`.
- Pass `commitHash={selection?.source === "commit" ? selection.hash : undefined}` to `DiffViewer`.

Setting one selection variant naturally clears the other since there's a
single state slot — no explicit reset code needed.

### `client.ts` / `types.ts`

`ui/src/router/client.ts`: add to the `RouterClient` interface near
`getFileDiff`:

```ts
getCommitFileDiff(sessionId: string, hash: string, path: string): Promise<FileDiff>;
```

Add to `FetchRouterClient`, near its `getFileDiff` implementation:

```ts
async getCommitFileDiff(sessionId: string, hash: string, path: string): Promise<FileDiff> {
  return routerGet(this.baseUrl, `/sessions/${sessionId}/commits/${hash}/diff`, { path });
}
```

Do not touch `getFileDiff` or anything related to `/changes` — out of scope.

`ui/src/router/types.ts`: no changes needed. `FileDiff { path, language?, original, modified }`
already fits the new endpoint's response (Go omits `language`; the client
already guesses it via `guessLanguage`).

### `fixtures.ts` / `FakeRouterClient`

Add a fixture map keyed by composite `hash:path`:

```ts
export const fixtureCommitFileDiffs: Record<string, FileDiff> = {
  "a1b2c3d:services/data/internal/backoff.go": { path: "...", original: "...", modified: "..." },
};
```

Add a constructor param and method to `FakeRouterClient` mirroring
`getFileDiff`'s throw-on-missing-fixture style:

```ts
async getCommitFileDiff(sessionId: string, hash: string, path: string): Promise<FileDiff> {
  const diff = this.commitFileDiffs[`${hash}:${path}`];
  if (!diff) throw new Error(`no fixture diff for commit ${hash} path ${path}`);
  return diff;
}
```

### Test updates

- `CommitList.test.tsx`: add tests for clicking a file row (calls `onSelectFile`
  with the right hash/path), `data-selected` reflecting `selectedFile`, and
  keyboard operability (rows are real `<button>`s, so this is mostly just
  confirming `role="button"`/tab-reachability — no bespoke key handling
  needed since native buttons already do this).
- `DiffViewer.test.tsx`: assert the commit-label `Chip` renders when
  `commitHash` is passed and is absent otherwise.
- `FilesTab.test.tsx`: add a test clicking a commit file (use
  `fixtureCommits["session-files-clean"]`'s `services/data/internal/backoff.go`,
  paired with a new `fixtureCommitFileDiffs` entry) and asserting the diff
  viewer shows content plus the commit label; add a mutual-exclusivity test
  that selects a working-change file, then a commit file, and asserts the
  working-change row's `data-selected`/`aria-current` clears (and vice versa).
  Confirmed no path collisions between `session-files-clean`'s existing
  working-change fixture paths and its commit file list, so the existing
  "shows the diff for a selected file" test needs no changes.

## 3. E2E

`ui/e2e/session-work.spec.ts`: extend the "files tab shows the pipeline's
changed files" test (or add a new one in the same describe block) to click
the seeded commit's file row (`src/commit-file.ts`, seeded via
`recordRealCommit` with a real parent that does not contain that path — so
the endpoint's added-file/empty-`original` path is exercised for real, no
special-casing needed) and assert:

- `.monaco-editor` becomes visible (Monaco's own stable internal class,
  already used as an e2e selector elsewhere).
- The commit-label text (short hash prefix of the seeded `commitHash`) is
  visible.

## Out of scope (do not touch)

- `GET /sessions/{id}/changes` (working-change diff content) — pre-existing
  `knownGapRoutes` entry, unrelated to this task.
- `GET /mentionables`, `POST /system/open-editor`, `POST /sessions/{id}/merge` —
  other pre-existing `knownGapRoutes` entries, untouched by this change.

## Checks

`make check` and `make ui-smoke` must pass before handoff back.
