# Shell dock: shells that outlive the view

**Status:** Proposed (2026-10-05). Not started.
**Date:** 2026-10-05. **Driver:** Drew wants the Ctrl+` shell to stay open,
with its contents, while he moves between sessions. He also wants to hide it
without killing it while a sudo'd service runs in it.
**Already landed (this branch):** a0ba437 (clean shell env, absolute cwd,
copy/paste), a551016 (no LF-to-CRLF translation on real pty streams).

## Context

The dock (`ui/src/conversation/ShellDock/ShellDock.tsx`) renders `TerminalView`
over Electron's `PtyBridge`. `TerminalView` calls `bridge.spawn()` when it
mounts and `bridge.kill()` when it unmounts. For the dock's bridge
(`ui/electron/pty/ptyHost.ts`, node-pty in the main process) that means
**a new shell on every mount and a dead shell on every unmount.**

- `shellOpen` is `useState` in `ConversationTab.tsx`. `SessionsSurface` keys
  `SessionWorkspace` on the selected session id, so switching sessions
  unmounts the dock. That kills the shell and forgets that it was open.
- Opening the activity dock (tool or subagent detail) also closes the shell
  dock, because two bottom surfaces never stack. Today that kills the shell too.
- The agent's Terminal display mode uses the same `TerminalView` over
  `SessionTerminalBridge`. Its `spawn` returns the session id and its `kill`
  is a no-op, so the agent's process already survives a remount. The dock
  should get the same "the view is not the process" behavior.
- `registerPtyHandlers` (and the `badge`/`dialog` handlers) still register
  inside `createWindow`. On macOS, `activate` re-runs `createWindow` after the
  last window closes, and `ipcMain.handle` throws on a duplicate channel.
  a0ba437 registered its new handlers once at startup but left these.

## Workflows

**W1, the quick shell.** Ctrl+` → run some code or git commands → drag the
dock taller to read a `git log` → open a new session and prompt it → come back
→ the shell is still there as he left it → close it and never think of it again.

**W2, the long-lived shell.** Run several `sudo` commands, some of which start
a service that lives only as long as the shell. Hide the dock to read more of
the conversation, knowing the shell isn't dead until he says so.

Surviving a mads restart is **not** a goal. That's rare, mostly a mads
developer's problem, and a real terminal covers it.

## Decisions

### D1. Shells live in a helper process, keyed by session

A `utilityProcess` (the "shell host") owns every dock shell. It loads
node-pty, which the main process no longer does. VS Code's terminal works the
same way.

- **One shell per session**, keyed by session id. Ctrl+` attaches to the
  session's shell if there is one, otherwise it starts one.
- **The shell host dies with the app.** Its shells get SIGHUP when the app
  quits or crashes, so nothing is left behind (W1).
- **Why not the main process:** a hidden shell running `yes` or a chatty build
  keeps the D3 mirror busy parsing. In the main process that would stall
  menus, dialogs and IPC for every window. In the helper, only the helper is
  busy. A node-pty crash also takes down only the helper.
- **Why not tmux:** tmux owns the screen, so the dock's own scrollback stays
  empty. Scrolling then needs tmux's mouse mode or copy mode, which fights the
  copy/paste that just landed. tmux sessions also outlive a crash, which breaks
  W1. **Why not a Go pty service:** it only buys surviving a restart (a
  non-goal), and it needs an authenticated socket that hands out a shell.
- **Transport:** at window load, main brokers a `MessageChannelMain` between
  the shell host and that window's preload. Shell data then flows straight
  from the helper to the window, and main handles only lifecycle. Shell
  traffic stays off HTTP, so it never counts toward the ~6-per-origin
  sockets (`ui/src/router/events.ts`) or `concurrency_limit`.
- **If the shell host crashes:** main sees `exit`. Every attached dock prints
  `[shell host exited]` and offers to restart. The next Ctrl+` starts a fresh
  host. The shells that were running are gone, and the message says so.
- **Registration moves out of `createWindow`:** every `ipcMain.handle` (pty,
  badge, dialog) registers once at `whenReady`. Per-window state hangs off
  `webContents`.

### D2. Attach, detach, end

The shell host's protocol, per shell:

| Message | Meaning |
|---|---|
| `open(sessionId, {cwd, cols, rows})` | Attach to the session's shell, or start one. Replies `{shellId, created}`. |
| `detach(shellId)` | The view went away. The shell keeps running and keeps feeding its mirror. |
| `end(shellId)` | Kill the shell (D6). |
| `write` / `resize` | As today. |
| `list()` | Every live shell with its D5 details. Used by Settings, quit and the rail. |
| `data` / `exit` (events) | As today. `data` goes only to the attached window. |

A dock-only `ShellBridge implements PtyBridge` maps the existing interface onto
this: `spawn` means `open`, and `kill` means `detach`. `TerminalView` doesn't
change, and the dock's × calls a new `end`. Only one window attaches to a shell
at a time. The latest `open` wins, and the previous window sees `detached`.

**Window reload or crash:** main watches `did-start-navigation`,
`render-process-gone` and `destroyed`, and detaches every shell that window had.
After a reload, the dock reattaches from D4's stored state.

### D3. A mirror for coming back

Each shell gets an `@xterm/headless` terminal (5.5, matching `@xterm/xterm`) in
the shell host, fed every byte whether or not a view is attached.

- **On `open` of an existing shell**, the host serializes the mirror with
  `@xterm/addon-serialize` and sends the result as the first `data` chunk,
  then resumes live output. The host is single-threaded, so nothing is dropped
  or doubled between the snapshot and the live stream.
- **Then it resizes the pty to the new view's size.** The SIGWINCH makes
  vim, htop and `less` repaint, which covers anything the serializer misses.
- **While attached**, the host batches `data` per ~16 ms frame.
- **Scrollback:** 1000 lines in the mirror and the view (xterm's default),
  roughly 1.5 MB per shell at 120 columns. 5000 lines costs about 7 MB. The
  setting is a constant, not a user option, until someone asks for one.

A mirror rather than raw replay: a bounded raw-byte ring starts mid-sequence
and replays at the old size, which smears full-screen programs. The agent's
Terminal mode does exactly that today (see Follow-ups).

### D4. Dock state per session: hide is not end

- **Per-session view state** `{open, height}` lives in a small store persisted
  to `localStorage` under the session id, so it survives session switches and
  a window reload. Whether a shell exists comes from the shell host, not from
  this store.
- **Ctrl+` and the dock's chevron hide or show.** They never kill and never
  prompt. Ctrl+` with no shell starts one.
- **× ends the shell.** If the shell is idle at its prompt (D5), it ends
  without a prompt, which is W1's "close it and forget it". If something is
  running, a dialog lists it:

  > **2 processes are still running in this shell**
  > `sudo python -m http.server 80` · `tail -f /var/log/syslog`
  > Ending the shell stops them. Anything started with `nohup`, `disown` or
  > `setsid` may keep running.
  > **[Keep running, hide]** · [End shell]

- **Opening the activity dock hides the shell dock** (no longer kills it).
- **If the shell exits on its own** (`exit`, or a crash), the dock shows
  `[process exited]` with a **Start new shell** button. Hiding and reopening
  also starts a fresh one.
- **A live, hidden shell is visible in two places:** a small terminal glyph on
  the session's rail card and a dot on the dock toggle. "Hidden" is the only
  state that needs flagging. A visible dock speaks for itself.
  The rail glyph is a terminal mark (`>_`), never a dot: the card already
  carries the worktree work marker (a hollow accent dot for uncommitted
  changes, filled for commits to land), and a second circle would read as
  the same thing.

### D5. What's running

The shell host answers "is this shell busy, and with what" **on demand**:
for `list()`, × and quit. It never polls, except every 5 s while the Settings
inventory is open.

- **Foreground:** node-pty's `process` (Linux, macOS). If it differs from the
  shell's own name, something is in the foreground.
- **Background jobs:** walk the shell's descendants. On Linux, read
  `/proc/<pid>/task/<pid>/children` recursively. On macOS, take one
  `ps -A -o pid=,ppid=,comm=` snapshot. Root-owned descendants (sudo) still
  appear, which is all the warning needs.
- **Report:** command lines (truncated), the count, and summed RSS for
  Settings.

### D6. What End kills

`end` calls `pty.kill()`. Closing the pty sends SIGHUP to the shell's session.
`sudo` relays the signal to its command, so `sudo some-service` stops too,
which is what W2 expects. Processes started with `nohup`, `disown` or `setsid`,
or real daemons, survive by design. We can't SIGKILL root processes without
sudo, so we don't try. The End dialog says this up front (D4).

### D7. Lifecycle

- **Session deleted** (`session.deleted`): its shell ends. The delete dialog
  gains a line when the shell is busy: "Its shell is running `…`; deleting
  ends it."
- **App quit** (`before-quit`): `list()`. If any shell is busy, one native
  confirmation lists them by session, with buttons **[Quit and end them]** and
  [Cancel]. Idle shells end quietly.
- **Crash:** the shells die with the shell host (D1). Nothing is left behind.

### D8. Shell inventory (Settings → Shells)

The escape hatch. A new **Shells** section in Settings. It isn't a Config
section, so it has no profile diff or restart badge.

- **Columns:** session (a link, or "session gone"), folder, started, hidden or
  visible, what's running (D5, or "idle"), memory.
- **Actions:** End on each row (same D4 dialog when busy), and **End all idle**.
- Because the shells die with the app, this list is complete. Only
  `nohup`/`setsid` escapees can be missing, and those are out of scope.
- **Soft limit:** no hard cap and never an automatic kill, because silently
  killing a W2 shell is the worst outcome here. Opening a ninth shell shows a
  one-line notice in the dock: "9 shells are running · Manage". Manage links
  to the inventory.

## Non-goals (v1)

- Surviving a mads restart or crash (tmux or a real terminal does that).
- More than one shell per session, or tabs in the dock.
- Terminal mode's redraw problems (see Follow-ups).
- A shell in the browser-only UI. The dock still needs Electron.
- Tracking processes after End (`nohup` escapees).

## Risks accepted for v1

- **node-pty in a `utilityProcess` on the macOS build.** It's the same native
  binary and ABI as today, but packaging (asar unpack, signing) has to include
  the shell host's entry file. Check it in the macOS port's on-device list.
- **The serializer isn't perfect.** Some terminal modes may not round-trip
  (mouse tracking, bracketed paste). D3's resize-to-repaint covers most of
  it. Anything left is cosmetic until the program redraws.
- **The cwd is fixed at start.** If the session's folder changes later
  (isolation turning on at first start), an existing shell stays where it was.

## Decomposition (dependency order)

1. **Shell host.** `ui/electron/pty/shellHost.ts` (the utilityProcess entry),
   the D2 protocol, and the MessagePort broker in `main.ts`. Move node-pty out
   of main. Register every handler once (fixes macOS `activate`). Unit tests
   with a fake pty, and a `*.realtest.ts` round trip through the real helper.
2. **Mirror and snapshot.** D3: headless, serialize on attach, resize nudge,
   batching. Tests: attach, write, detach, write more, re-attach; the snapshot
   holds both writes and nothing doubles.
3. **Dock wiring.** `ShellBridge`, D4 store, chevron and ×, Ctrl+` semantics,
   activity dock hides instead of killing, `[process exited]` and restart, and
   window-reload reattach.
4. **Busy and End.** D5 on Linux and macOS, the D4 End dialog, D7 quit
   confirmation, `session.deleted` and the delete-dialog line.
5. **Visibility.** The rail glyph and the dock-toggle dot.
6. **Inventory.** D8 Settings section and the soft-limit notice.
7. **Manual pass in the real app** (Playwright runs in a browser and has no
   pty). Drive the running Electron app with the CDP probe:
   - **W1:** open, `git log`, resize, switch sessions, come back (same
     contents and size), ×. No prompt; the inventory is empty.
   - **W2:** `sudo python3 -m http.server 8099`, hide, switch sessions, come
     back (still serving), × → dialog → **Keep running**, then End → the port
     is free.
   - Reload the window (Ctrl+R) and confirm the dock reattaches.
   - `kill` the shell host and confirm the message and restart.
   - Quit with a busy shell and confirm the prompt.

## Follow-ups (not this design)

- **Terminal mode redraws.** a551016 removed one cause (`convertEol` on a real
  pty). What's left, from reading the code and not yet reproduced:
  - The Session service replays its raw PTY ring as one leading `pty_output`
    on stream subscribe (`services/session/internal/api/api.go:219`). It can
    start mid-escape-sequence and replays at the old size.
  - The UI shares one `SessionEventSource` per tab, so a chat → terminal
    toggle without a reconnect may never see that replay at all.
  - A Go vt emulator in the hub (the D3 idea on the server side) would fix
    both. Verify before designing.
- **`HANDOFF.md`** is cited in several comments under `ui/src/conversation/`
  and `ui/electron/`, but the file doesn't exist. Restore it, or point those
  comments here.

## Decisions log

- 2026-10-05: shells live in the app (B), not tmux (D) or a Go service (E).
  Surviving a restart isn't worth it. Drew's two workflows set the semantics:
  hide never kills, × ends and asks only when something is running.
- 2026-10-05: the shell host is a `utilityProcess`, not the main process
  (CPU isolation and crash isolation).
- 2026-10-05: a Settings shell inventory replaces any automatic cap.
- 2026-10-05: no "Shells…" palette command. Finding a hidden shell is the rail
  glyph's job, and cleanup is rare enough for Settings.
