# Mesh mode: see and drive sessions on every device you own

**Status:** Built. P0 (viewing sessions on other devices) and P1 (starting and
driving them) shipped with mesh auth (`docs/MESH-AUTH-DESIGN.md`, A1–A7),
which replaced this doc's trust model: D2's WhoIs-only authentication and
D3's "one list" are superseded there. Not built: P2 (shells on the host,
D7), P3's discovery UI, and D10 (files across devices). The implementation
notes below record how each phase landed.
**Date:** 2026-11-12. **Driver:** Drew is ~300 miles from pop-os for the weekend
with live sessions on it, and wants to chat with them from the MacBook's mads UI.
The work keeps running on pop-os. The longer goal is any-node-sees-all-nodes,
behind a toggle.

## Follow-ups

- **The rail's live state for remote sessions.** `useSessionAgentState`
  (the rail's working, sleeping and approval state, mounted in
  `SessionsSurface`) subscribes on this device's bus with no session id,
  so a remote session's state on the rail never goes live: it shows what
  this window last read. Its open session's
  pane is live (its subscriptions are on the host's bus). Fixing it means
  per-remote-session subscriptions for the rail's visible remote rows, or
  a peer-wide stream; neither is built.

## Context

mads is single-machine by construction.

- **The Router is the UI's only backend.** It listens on `localhost:17373`, serves
  `ui/dist`, and the renderer's address is baked in at build time
  (`ui/src/router/client.ts:560`, `ROUTER_BASE`). The renderer never talks to the
  Data Service (17374).
- **Network location is the credential** (`pkg/localguard`). Host must name
  loopback. Origin must be the Router's own `http://localhost:<port>`. Its doc
  comment: "A mads reachable from other machines would need real authentication in
  front of the same routes; this package is where that would plug in."
- **`app.sh` owns the services.** Closing the window stops the Data Service and
  Router it started. Restarting the Router cuts off every session mid-turn; sessions
  resume on their next message.
- **Electron bridges are local to the viewing machine.** The shell dock (node-pty in
  a utilityProcess, `ui/electron/main.ts:205`), the directory picker, link opening
  and the clipboard all act on the machine running the window. The renderer treats
  them as optional (`window.mads?.shell`, `?.dialog`, `?.links`).
- **Session ids are random** (`ses_` + 16 hex), so they're unique across machines
  in practice. That's what makes implicit per-session routing (D4) possible.

**Live state, 2026-11-12:**
- pop-os: Pop!_OS 24.04 on Wayland. `app.sh --restart` has been up 3 days, with 4
  active sessions. Suspend is masked.
- MacBook: its own mads instance on the same ports.
- Both checkouts now sit at `8c3a25b0`.
- The Mac reaches pop-os over Tailscale through a DERP relay (no direct path),
  at 85–180 ms.

## Principle

**A session lives where it runs.** Every other node only gets a view of it, and that
view is authorised by the tailnet. Nothing is copied or synced. Each node's Data
Service stays the sole owner of its own sessions (spec §15).

**The default UI is unchanged.** It shows this device only. Mesh is opt-in twice:
once to add a device to your mesh, and again to flip the session list from **This
device** to **All devices**.

## Decisions

### D1. Topology: symmetric nodes, opt-in visibility

- **Every node can serve and view.** A node *serves* by running the mesh listener
  (D2). It *views* by registering peers (D3) and flipping the scope toggle (D5).
  Serving and viewing are independent: a node can serve without viewing anything.
- **Weekend prototype:** pop-os serves and the Mac views. Making it symmetric means
  deploying the same binaries to pop-os later, which needs a pop-os Router restart.
  Do that at a convenient moment.

### D2. Transport and identity: tailnet listener with WhoIs and app capabilities

**The loopback Router API keeps "local mode" trust.** Mesh traffic arrives on a
separate listener that:

1. **Binds only to the node's Tailscale address** (`100.x.y.z:17380`), never
   `0.0.0.0`. It's unreachable off-tailnet and never put on Funnel.
2. **Authenticates every request** with Tailscale LocalAPI `WhoIs(remoteAddr)`:
   - The caller must be a node owned by the same tailnet user. Tagged callers are
     refused in v1.
   - The caller must carry the app capability `github.com/drewgitsit/mads/cap/mesh`,
     granted in the tailnet policy (see Tailnet audit). No capability means 403.
3. **Authorises by route class.** *(Superseded 2026-11-14: two levels,
   `view` and `control`; see "Implementation notes (git and access levels)".)*
   The capability's `access` list maps to an allowlist:
   - `view`: GETs on session-scoped routes, `/sessions`, `/events`.
   - `chat`: `messages`, `input`, `interrupt`, `queue`, `permission-requests/*/resolve`,
     `compact`, `rename`, `attachments`.
   - `create`: `POST /sessions`, plus GETs on `/agents`, `/workspaces`,
     `/sessions/defaults`, `/providers`.
   - `shell`: P2.
   - **Never proxied, whatever the capability:** `/credentials*`, `/accounts*`,
     `/profiles*`, `PUT /config`, `/registry/*`, `/update/*`, `/imports/*`.
4. **Refuses anything a browser sent.** Any `Origin` or `Sec-Fetch-*` header means
   403. Only Routers talk to the mesh listener, so the browser attack surface that
   localguard exists for is closed by construction.
5. **Forwards to the local Router over loopback** with `Host: localhost`, no Origin,
   and `X-Mads-Mesh-Peer: <caller node name>` for logging and audit. It also streams
   SSE, flushing per event. Since the local front door, it also sends **this node's
   own install token** and drops any credential the caller sent; see "Implementation
   notes (local front door)".

WhoIs needs `tailscaled`'s LocalAPI. On pop-os the socket
(`/var/run/tailscale/tailscaled.sock`) is mode 0666, and `tailscale whois` works as
`drew` (verified 2026-11-12). A plain `GET /localapi/v0/whois?addr=` over the socket
is enough, with no `tailscale.com` Go dependency. **macOS LocalAPI access differs**
(GUI-app daemons use a same-user token file), so it's only needed once the Mac
serves (P3).

**Where the listener lives:**

| Option | Pros | Cons |
|---|---|---|
| **(a) A new Mesh Service, `services/mesh` (recommended)** | Deploys to pop-os **without restarting its Router or sessions**. Keeps Tailscale code and the authorisation surface out of the Router, testable alone with a fake WhoIs. Fits "small independently runnable services". | One more process to supervise. Until the Router supervises it, start it via `systemd --user` or `app.sh`. One extra loopback hop. |
| (b) A second listener inside the Router | One process, no extra hop | Needs a pop-os Router restart for P0. Puts remote-trust code next to local-trust code. |

(a) is recommended. It can fold into the Router later if supervision gets annoying.

### D3. Peers: one list, "devices I can view"

*(Superseded by `docs/MESH-AUTH-DESIGN.md` D3 and D8: inbound access is now
decided on the host, per paired device, and the peer list holds paired
devices as well as viewed ones. Kept as the original reasoning.)*

**Audience.** This is a consumer connecting their own homelab boxes, laptops and
phones, not an enterprise device mesh. So there is **one list per node: the devices
this node views.** Nothing in mads keeps an inbound allowlist. Who may view *me* is
decided by the tailnet policy alone (the D2 grant: `autogroup:self` plus the app
capability), and revoking a device happens there.

**How existing settings behave** (audited 2026-11-12). These shape the choice:
- **Scalar settings** (`contracts.Config`) are one Data Service row. `PUT /config`
  replaces the whole row and appends a payload-less `config.updated` in the same
  transaction. Go readers re-read per request. UI hooks subscribe through the shared
  `/events` stream and refetch.
- **Providers aren't stored at all.** They're a compiled registry
  (`services/router/main.go:138`). "Installed?" is preflight, computed on demand.
  Model catalogs are a time-limited cache. No event fires when a provider is
  installed or upgraded; only `account.auth_changed` exists.
- **Runtime-mutable collections** (agents, workspaces, capabilities, profiles,
  templates, links) are Data Service tables with `*.created`, `*.updated` and
  `*.deleted` events. UI hooks subscribe and refetch. Health has a precedent:
  `capability.health_changed` lives in the event log.

Peers are a runtime-mutable collection, so they follow the third pattern.
`Config` was rejected for three reasons:
- Its whole-row writes race with other edits.
- `config.updated` carries no detail about what changed.
- Profiles snapshot config, so switching profiles would swap your mesh.

**Model:**
- **Membership is durable.** It's a Data Service table, `mesh_peers`:
  `{id, name, dns_name, mesh_url, added_at, hidden}`. Events `peer.created`,
  `peer.updated` and `peer.deleted` are written in the same transaction as the row
  change (transactional outbox).
- **Subscribers:**
  - The UI's `usePeers` hook. The toggle and host chips update immediately.
  - The Router's event watcher. It opens and closes peer connections and session
    index entries as peers come and go.
- **The tailnet is the upper limit on membership.** A peer whose WhoIs or capability
  check fails stays listed but shows `denied`. A peer that has left the tailnet,
  or is offline, shows `unreachable`.
  - *As built (P0b):* the Router learns this from the peer's own answers. The
    peer's Mesh Service runs the WhoIs check; a 403, or `/mesh/hello` without
    `view`, means `denied`. No `watch-ipn-bus`: macOS daemons don't expose a
    fixed LocalAPI socket. Policy changes therefore show up within one probe
    plus the debounce (~15 s). The separate `gone` state waits for P3.
  - "Add device" reads the tailnet's devices with the `tailscale` CLI
    (`status --json`) for the same reason.
- **Reachability is computed by the Router and logged.** Each transition
  (`connected`, `unreachable`, `denied`, `version_mismatch`) appends a
  `peer.reachability_changed` event, `{peer_id, from, to, reason, rtt_ms}`.
  This follows the `capability.health_changed` precedent, so connects and disconnects
  appear in the event log for troubleshooting. To keep a flapping relayed link from
  flooding the log, a transition must hold for 10 s before it's logged, and
  `connected` is logged on first success.

**Add device:**
- The form lists tailnet devices (LocalAPI status) that answer `GET /mesh/hello`.
  `hello` returns `{node, mads_commit, mesh_api, access}`. It's the only mesh route
  that needs no capability beyond tailnet membership.
- Choosing one runs `POST /peers` on the local Router, which writes to the Data
  Service.

**Version skew:** a peer whose `mesh_api` differs is listed but greyed out.
`mads_commit` is shown so you know which side to pull.

### D3b. Per-host reads: providers aren't the same everywhere

**Running sessions don't care.** The provider runs on the host, so a Mac with no
goose installed can chat with a pop-os goose session.

**The conversation view does care.** It reads per-host data that has no session id
in the path, so implicit routing (D4) can't place it:

| Read | Used by | What breaks for a remote session if read locally |
|---|---|---|
| `useProviderModels` | `StatusLine.tsx:36`, `DelegateDock.tsx:32` | The model list and switcher offer the viewer's models. |
| `useProviderInfos` | trust picker annotations | The viewer's provider capability table (version may differ). |
| `useAgents` / `useAgentIndex` | agent name and colour | Host agent ids are unknown on the viewer. |
| `useWorkspaces`, `listAllTasks`, `listDelegates` | dock and rail | Wrong host's data. |

**Fix: a `HostScope` React context.**
- The conversation view provides it from `session.host`.
- The hooks above read it and call `/peers/{host}/...` when the host isn't self.
- Their in-flight caches are keyed per host.
- Default is self, so local behaviour is unchanged.

**Create on a peer (D6)** reads that peer's `/providers` *and* `/preflight`. The
launchpad then offers only providers installed and healthy **on that host**, and
per-host defaults come from that peer's `/sessions/defaults`.

**Out of scope:**
- Making provider availability change-notified; it stays fetch-on-mount, as today.
- Cross-host model or agent pickers in a single view.

### D4. Routing in the viewing node's Router

The UI keeps talking only to its own Router. The Router adds three kinds of routing.

**Explicit:**
- `/peers/{peer}/<path>` is forwarded to that peer's mesh listener. The create flow
  (D6) uses this for per-host resources: agents, workspaces, defaults.

**Implicit, by session id:**
- `/sessions/{id}/...` for an id that isn't local but is in the **peer session
  index** goes to the owning peer.
- This means the conversation view's call sites need no changes: the 43 importers of
  the `router` singleton, raw `fetch`es, EventSources and `/output` SSE.
- The index is filled from aggregate list calls and kept current from peer event
  streams.
- An id that's neither local nor indexed returns 404, as it does today.

**Aggregate, only with `scope=all`:**
- `GET /sessions?scope=all` merges local sessions with every peer's. Each row gains
  `host: {name, self}`.
- **Events, as built:** the UI opens one stream per host. That's its own
  `/events`, plus `/peers/{peer}/events` for each peer. Each stream keeps its own
  cursor and the UI knows the host from the stream it came on. This replaces a
  multiplexed `GET /events?scope=all`, which would have needed a per-host
  `Last-Event-ID` map. `/sessions/{id}/output` needs nothing extra: it's
  session-scoped, so implicit routing covers it.
- **A peer that's down** doesn't fail the call. Its last-known sessions come back
  with `host.reachable: false` and the UI shows them as stale.
- Without `scope=all`, `GET /sessions` behaves exactly as today.
- **Links, read in bulk** (`GET /links?session_ids=`, the rail's link pills):
  not session-scoped, so implicit routing can't place it. The Router splits
  the ids by the session index and merges each owning host's answer (links
  never span hosts). Peers' `link.*` events stay on the peer's bus, as their
  `session.*` events do: the UI re-reads the visible remote sessions' links on
  the same 10 s poll as the All devices list (one clock per window, paused
  while it's hidden). The Router answers each peer's part from its last
  read and refreshes it in the background, one read per peer at a time.

**Everything else stays local:** agents, tasks, pipelines, metrics, config,
credentials and the rest. Linked sessions across hosts are a non-goal for v1.

### D5. UI

- **Scope toggle:** a **This device / All devices** toggle in the session list
  header. It's only visible once at least one peer is registered, and is stored as a
  UI preference.
- **Merged list:** in All mode the shared session list store fetches with
  `scope=all`. Rows show a host chip. Unreachable hosts grey out their rows.
- **Conversation view:** unchanged, because of implicit routing.
- **Machine-local bridges are gated for remote sessions:**
  - The shell dock is hidden until P2.
  - File links render as "on pop-os: `<path>`", copy-only.
  - Reveal-in-folder is hidden.
  - Attachments still work, since they go through the Router.
- **Notifications and badges** count every host in All mode.

### D6. Create a session on a peer (P1)

- **Host pill:** the launchpad gets a host pill, defaulting to this device. Choosing
  a peer loads agents, workspaces and defaults through `/peers/{peer}/...`.
- **Folder picking, two tiers:**
  - Picking one of the peer's registered **workspaces** needs no folder browsing at
    all.
  - For an arbitrary folder, the native picker is replaced by a folder browser
    backed by `GET /mesh/fs/list?path=` on the **Mesh Service itself**. The Router
    has no general folder listing (`/sessions/{id}/move/folders` only suggests
    folders inside a session's repo), and serving it from the Mesh Service avoids a
    pop-os Router restart. It's gated on `create`, directories only, under `$HOME`.
- **Create:** `POST /peers/{peer}/sessions`. The new session joins the index, and
  the UI navigates to it as if it were local.

### D7. Shell dock on the host (P2: spec only, not built this weekend)

The pty host moves from the viewer's Electron to the session's host.

- **Host side:** a host-side shell service (a node-pty or Go pty process supervised
  by the Router) exposes `GET /sessions/{id}/shells` and a WebSocket attach.
- **Reuse:** it keeps the headless-xterm mirror and snapshot-on-attach design from
  `SHELL-DOCK-DESIGN.md` D3, so reconnects over a relayed path stay cheap.
- **Viewer side:** `shellBridge.ts` gets a WebSocket `PtyBridge` alongside the IPC
  one, chosen by `host.self`.
- **Cost:** a pop-os Router restart, plus the WebSocket going through the mesh proxy.

### D10. Files across devices: read-only, opened locally (planned 2026-11-16)

**Use case (Drew):** open a file that lives on another device in a local tool (an
IDE, a browser, an image viewer) to review it. Writing back is rare; ask an agent on
that device instead. So **no write path in this increment**.

**Design sketch** (built after mesh auth, `docs/MESH-AUTH-DESIGN.md`):
- **The host serves a session's files read-only.** `GET /sessions/{id}/files/raw?path=`,
  limited to that session's folder or worktree after resolving symlinks, the same
  way `/mesh/fs/list` is limited to `$HOME`. Each read is audited with the
  authenticated device.
- **Open locally.** A file link in a remote session, the Changes tab and a Files
  view gain **"Open a copy"**: Electron downloads it to a per-host cache
  (`<userData>/remote-files/<host>/<session>/<path>`) and opens it with the default
  app, or a chosen one (IDE, browser). The copy is labelled read-only (file mode
  `0444`), and re-opening refreshes it.
- **HTML mockups with relative assets** (CSS, images) break as a single file.
  "Open folder copy" fetches the file's directory, size-capped and with confirmation
  above the cap, so the local browser renders it intact.
- **In-app preview** (images, PDFs, Markdown, text, and HTML from an **isolated
  preview origin**, never the Router's own) is a later nice-to-have, mainly for the
  phone. Agent-written HTML must never run with mads' sign-in.
- **Access level:** reading raw files is **chat**, so the phone can review. View-only
  devices see transcripts but not raw files. Optionally, a host setting hides
  dotfiles and secret-shaped files (`.env`, keys) from remote reads.
- **Out of scope:** edit-and-sync-back, mounts, remote IDE handoff. Ask the agent on
  that device, or use git.

## Tailnet audit (2026-11-12, from both nodes' netmaps)

| # | Finding | Mesh impact | Recommendation |
|---|---|---|---|
| T1 | The ACL is the default **allow-all**: every tailnet IP can reach every port on every node. | spark (or any future device) can reach `:17380`. | Authorisation must come from app capabilities (D2), not reachability. Optionally replace allow-all with explicit grants later. |
| T2 | No grants and no custom app capabilities exist. | The mesh has nothing to check yet. | Add the grant below. |
| T3 | SSH runs in **check** mode for all nodes, including `root`. The current approval expires **2026-11-13 13:14 UTC (8:14 AM EST)**. The Mac has `RunSSH: false`. | It doesn't affect the mesh itself (not SSH), but deploying or debugging on pop-os needs a fresh approval each ~12h. | Fine as-is. Consider a longer `checkPeriod` for your own devices, and dropping `root` from `sshUsers`. |
| T4 | The **Funnel** node attribute is granted to all devices. | A misconfigured `tailscale funnel` would publish mads to the internet. | Remove the `funnel` nodeAttr unless something uses it. The mesh never binds a Funnel. |
| T5 | Key expiry: Mac 2027-02-22, pop-os 2027-04-30, spark disabled. | None for now. | — |
| T6 | No direct path Mac↔pop-os. Neither side has port mapping (UPnP, NAT-PMP or PCP). pop-os and spark share one public IP. | +85–180 ms per round trip. SSE is fine. P2 shells will feel it. | Optional: enable UPnP, or forward UDP 41641 to pop-os, on the home router. |
| T7 | The Mac's `tailscale` CLI is 1.102.3 but its daemon is 1.104.1. pop-os runs 1.104.6. | Cosmetic, until the Mac serves (P3, macOS LocalAPI). | Update the Homebrew CLI, or use the app's bundled one. |

**Proposed policy addition** (the admin console's *Access controls*; confirm with
its preview tool before saving):

```jsonc
"grants": [
  {
    // My own devices may use mads mesh on each other.
    "src": ["autogroup:member"],
    "dst": ["autogroup:self"],
    "ip":  ["tcp:17380"],
    "app": {
      "github.com/drewgitsit/mads/cap/mesh": [{ "access": ["view", "chat", "create"] }]
    }
  }
]
```

`autogroup:self` limits the destinations to devices you own, so a shared node or a
future family member's device would get nothing. Add `"shell"` to `access` when P2
lands.

## Risks accepted for v1

- **The Mesh Service is fully trusted by its local Router** (it's a loopback
  caller). Its allowlist is the whole authorisation boundary, so it gets
  table-driven tests over every route in the Router's mux, and a guard test that
  fails when a new route is added without being classified.
- **Implicit routing trusts that ids are unique.** A collision would mis-route. The
  odds are negligible at 64 bits, and a peer that claims a local id is ignored
  (local wins).
- ~~**No end-to-end encryption beyond WireGuard.**~~ Superseded by mesh auth A4
  (2026-11-18): the mesh listener is mutual TLS with pinned device keys,
  inside WireGuard. See docs/MESH-AUTH-DESIGN.md.

## Non-goals (v1)

- Linked sessions or orchestration across hosts.
- Moving a session between hosts.
- Syncing agents, personas or credentials.
- Tagged or shared-node callers.
- Funnel or any off-tailnet access.
- A Windows mesh.

## Decomposition (dependency order)

| Phase | Work item | Restarts |
|---|---|---|
| **P0a** (**done 2026-11-12**) | `services/mesh`: tailnet-only listener, LocalAPI WhoIs client (with a fake for tests), capability → route-class allowlist with a guard test against the Router's mux, browser-header refusal, SSE-safe forward to the loopback Router, `/mesh/hello`. | None on pop-os (new process). |
| **P0b** | Data Service: `mesh_peers` migration, peer CRUD and `peer.*` / `peer.reachability_changed` event types in `pkg/contracts`. Router on the viewer (`services/router/internal/mesh`): `GET/POST/PUT/DELETE /peers`, `GET /peers/candidates`, a peer event watcher and reachability tracker, `/peers/{peer}/*`, the peer session index, implicit `/sessions/{id}/*` routing, `scope=all` on `/sessions`. **Done 2026-11-12.** | Mac Router. |
| **P0c** (**done 2026-11-12**) | UI: scope toggle, host chips, stale-host state, bridge gating for remote sessions. vitest coverage plus a Playwright spec against a stub peer. | None (`make ui`). |
| **P1** (**done 2026-11-13**) | Create on a peer: host pill, per-host agents and workspaces, a folder browser backed by `/mesh/fs/list`. | Mesh Service only. |
| **P2** (not built) | Shell dock on the host (D7). | pop-os Router. |
| **P3** (not built as a phase; mesh auth made every node both host and viewer) | Symmetric: the viewer side on pop-os, the Mac serving (macOS LocalAPI), the "Add device" discovery UI. | Both. |

**Weekend logistics:**
- I'm running as a session in the Mac's mads, so restarting the Mac Router to load
  P0b ends that turn. Build and test in a worktree and restart last.
- On pop-os the Mesh Service starts via `systemd --user`, so it outlives the SSH
  session.

## Implementation notes (P0c, 2026-11-12)

**Store and toggle.**
- `ui/src/router/mesh.ts` holds the peer list and the This device / All
  devices scope, saved in `localStorage` as `mads.mesh.scope`.
- `App` keeps it live with `useMeshLive()`. Everything else reads it
  passively with `useMesh()`, so components rendered on their own (and their
  tests) never fetch `/peers`.
- The toggle (`DeviceScopeBar`) shows only once a visible peer exists. It
  also appears in the rail's empty state: with no sessions on this device,
  that's the only place to switch.

**Session list.**
- In All mode `sessionsStore` asks for `GET /sessions?scope=all` and records
  each row's host (`sessionHosts.ts`).
- It re-reads every 10 s rather than streaming peers' `session.*` events.
  A peer's full `session.*` history is at least 400 KB per 500-event page,
  and replaying it over a relayed link on every load costs more than a
  periodic merged GET.

**Events.**
- `events.ts` is now one `Bus` per stream: this device's `/events`, plus one
  per remote session that has subscribers,
  `/peers/{peer}/events?session_id=…`.
- A session-scoped subscription goes to its session's host automatically, and
  moves when the host becomes known.
- Bus health (the "updates paused" banner) is this device's stream only.

**Per-host reads (D3b).**
- `HostScopeProvider` wraps the open session's workspace, using the host from
  its list row.
- `useProviderModels`, `useProviderInfos` and `ConversationTab`'s agent index
  read from that host. Their in-flight caches are keyed per host.

**Machine-local features.**
- For a remote session there is no shell dock: the menu item is hidden, and
  the dock explains why.
- Its file links open only as web links (`getLinkOpener({ remote })`).

**Mesh Service.** `POST /sessions/{id}/worktree/refresh` moved to `view`:
it answers `202` at once (an optional body names the parts to recompute), the
counts arrive on `session.updated`, and the conversation view sends it on open.

**Verified.**
- vitest: 360 files, 3,891 tests.
- Playwright: full suite, 74 tests. That includes `e2e/mesh-mode.spec.ts`,
  which runs two real stacks with a stand-in Mesh Service and covers viewing,
  chatting, the hidden shell, the scope persisting across reload, and
  Settings → Devices.
- A read-only headless pass against the live Mac Router with pop-os: 28
  pop-os sessions shown, a conversation opened, no failed requests.

**Known gaps.**
- **Rail labels.** Fixed in P1 (see below).
- **Rail freshness.** Live updates for remote cards (status, activity) come
  from the 10 s poll, not events. An open remote conversation is live: its
  output stream and session-scoped events are routed.
- **Test stand-in.** The e2e stand-in Mesh Service doesn't enforce the route
  table. That's pinned by `services/mesh`'s Go tests.

## Implementation notes (P1, 2026-11-13)

**Host pill.**
- The Launchpad's first pill is "on: This device / {peer}". It only appears
  when a peer is connected and grants `create`.
- Choosing a device does three things:
  - Resets the device-specific pills: folder, workspace, agent, model.
  - Points the Launchpad's own reads at that device: defaults, the provider
    capability table, workspaces, agents and models. The pills read through a
    `HostScopeProvider`.
  - Creates with `POST /peers/{peer}/sessions`.
- The new session is tagged with its host and recorded before anything reads
  it. The list switches to All devices and refreshes, so the new session
  opens and stays in the rail.

**Folders.**
- For a remote start the native dialog is hidden (`PathField noNativePicker`).
- The folder pill embeds `RemoteFolderBrowser`, which is backed by the peer
  Mesh Service's `/mesh/fs/list`.
- Typed paths and the peer's workspaces still work. "Recent" lists only that
  device's sessions' folders.

**Off for a remote start:**
- "Save as defaults": those are this device's defaults, and `/config` isn't
  reachable over the mesh.
- Session links: they stay on one device.
- ~~Attachments~~: done, see "Implementation notes (attachments)".

**Rail labels.** `usePeerLibrary` merges each visible, connected peer's
workspaces and agents into the rail's lookups, matched by id. Ids are random
per install, and this device wins a tie.

**Verified.**
- vitest: 3,894 tests, including `Launchpad.mesh.test.tsx`.
- Playwright: 75 tests. `mesh-mode.spec.ts` now starts a session on the peer
  from the Launchpad. It browses to the peer's folder, checks the session runs
  there (`dir`), and checks the reply streams back. Its rail card shows no raw
  id.
- Read-only live pass against pop-os:
  - 28 cards, 0 raw ids.
  - The Launchpad on pop-os shows pop-os's defaults, and its folder browser
    lists `/home/drew`.
  - No failed requests.

## Implementation notes (attachments, 2026-11-14)

**Staged files follow the device they're started on.**
- The Launchpad's `StagedAttachments` list keeps the picked or pasted files
  (`retainFiles`).
- When the "on" pill changes, `retarget(client)` deletes the uploaded copies
  where they were and uploads each kept file again, under a fresh draft, to the
  new device.
- A per-item attempt counter makes an upload still in flight to the old
  device get deleted there when it lands, never adopted.

**Memory.**
- A picked or dropped `File` is a handle to the file on disk.
- A pasted image is held in memory until Start, Remove or clear. Sending
  (`rehome`) and clearing release it.
- Only the Launchpad keeps files: an open session can't change device.

**Thumbnails** come from the list itself (`stagedFiles.attachmentUrl`), that
is, from the Router the file was uploaded through. During a switch, the
Launchpad's new client would otherwise point at the old draft and 404.

**In an open remote conversation** the composer needed no change: its uploads
and thumbnails are session-scoped, so routing by session reaches the peer.

**Verified.**
- vitest: 3,899 tests, including `stagedAttachments.test.ts` and the
  Launchpad follow-the-device test.
- Playwright: `mesh-mode.spec.ts` pastes a screenshot, switches to the peer
  (the thumbnail reloads from the peer's draft), starts there, and sends a file
  from the open remote conversation.
- Full suite: 75/76. The failure is in `session-worktrees.spec.ts` (g/h, Land
  timing), which is flaky the same way before mesh mode (1 failure in 4 runs
  on the pre-mesh checkout).

**Found, not fixed (predates mesh mode).** The Launchpad's "↵ to start" key
hint takes up space in the layout and disappears on blur. Clicking a pill
straight after typing moves the pills between mouse-down and mouse-up, so the
first click is lost. main's ux-jumps merge ("a steady launchpad hint") may
already fix this; check on rebase.

## Implementation notes (git and access levels, 2026-11-14)

**Two access levels: `view` and `control`** (decided 2026-11-14). A level
exists only where a device might plausibly be held back from it.
- A display that should only watch gets `view`.
- A device you work from gets `control`, which implies `view`.

**`control` covers** chat, starting sessions, the shell (P2), and every git
operation: commit, draft message, undo, land, push, move into a repo, delete
scratch. It also covers session management: archive, trust, restart the
agent, chat/terminal transport.

**Still never forwarded:** the host's settings, credentials, accounts,
profiles, registry, metrics, imports, library and pipeline writes, links,
and orchestration.

**Policy.** The grant becomes `"access": ["control"]`.
- `authz.Effective` read the 2026-11-12 names `chat` and `create` as
  `control` at first. Those aliases are gone (mesh auth A5): the policy's
  `access` names are levels.
- `/mesh/hello` reports only the effective levels.

**UI.**
- The host pill now requires `control`.
- Archiving a remote session refreshes the list at once. A peer's
  `session.deleted` doesn't reach this window's event stream.

**Verified.**
- Go: authz and server tests for both levels.
- Playwright: `mesh-mode.spec.ts` commits and lands a peer session's change
  from this device (the peer's base branch gets the file) and archives it
  from the rail (gone on the peer).
- Full Playwright run: 77/77.

**Found, not fixed (predates mesh mode).** Archiving the session that's open
leaves its conversation re-reading a deleted session: four `GET
/sessions/{id}` 404s and one `/changeset` 404, locally too.

## Implementation notes (session list, 2026-11-14)

**Selection follows the list** (decided 2026-11-14).
- When the open session goes away, its neighbour in the rail opens: the next
  card in its own section (active or Done), else the previous one, else the
  Launchpad.
- "Next" respects the current filter chip and search.
- This applies to Archive (rail), Delete (workspace menu), ✓ Done, and a
  session deleted elsewhere: another window, the CLI, or a peer.
- Starting a new session is always a choice, never a side effect.

How it's built:
- `useSelectionFollowsList` (`features/sessions/state/selectionFollowsList.ts`)
  runs on every settled list, so it doesn't depend on any one event. A peer's
  `session.deleted` never reaches this window.
- It only acts on ids the list has held, so a just-created session or a jump
  target is safe.
- The rail reports its visible order (`onVisibleOrder`). A short history keeps
  the vanished card's neighbours: the rail's effects run first.

Explicit actions don't wait for the list:
- **Archive/Delete of the open session** moves first, then sends the DELETE.
  Unmounting stops every fetch. Moving after the DELETE left the session's own
  `session.deleted` to re-read it (the 404s found 2026-11-14).
- **✓ Done** moves on `confirmed`, so a refused Done or a dismissed question
  stays put.
- **A 404 on the workspace's own `getSession`** (deleted from outside) is the
  "gone" signal.

**A scope change never swaps the pane.** A remote session the new scope hides
stays open, off-list, until the user picks something else.

**The Delete dialog waits for its recount.** An isolated session's work is
recounted when the dialog opens. Delete is enabled once that answers (or
fails, or after 5 s).
- Confirming earlier confirmed against stale counts.
- Over the mesh it also killed the in-flight `git status` (a 500).

**Device menu (4b).** The scope control is a menu: This device, All devices,
or one device (`peer:<name>`).
- One device is the merged list filtered by host.
- A device since removed or hidden falls back to This device.
- The open card carries `aria-current`.

**Verified.**
- vitest: 3,918 tests (`selectionFollowsList.test.ts`, dialog recount,
  device menu, peer scope).
- Playwright: `session-selection.spec.ts` covers archive → next → previous →
  Launchpad, menu Delete → next, ✓ → next, and an outside delete. It runs
  under the 4xx tripwire, with no exemptions except the outside delete.
- `mesh-mode.spec.ts` covers the device menu, the off-list pane, and remote
  archive with no exemptions.
- Full suite: 81/82. h is the known Land/branch-cleanup timing flake.

## Implementation notes (local front door, 2026-11-17)

The local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md) put an install token in
front of every Router route. Mesh mode works with it this way:

- **The Mesh Service authenticates to its own Router** with that node's install
  token. It reads `$MADS_HOME/local.token` (`-mads-home`, or `-token-file`)
  through `localauth.FileSource` and re-reads it when the file changes, so
  `mads token rotate` needs no restart. `localauth.Transport` adds the bearer to
  every forwarded request. Before that, the forwarder drops the caller's own
  `Authorization` and `Proxy-Authorization`, along with the browser headers it
  already stripped. A peer can't present a credential to this Router; the bearer
  the Router sees is always this node's own.
- **The local install token never crosses the mesh.** The token is a loopback
  credential for one machine's user. Peers authenticate by tailnet identity
  instead: WhoIs plus the app capability, checked by the peer's Mesh Service. On
  the viewer, the stack is localguard → front door → CORS → mesh → API
  (`services/router/main.go` `meshLayer`). The door has already checked and
  removed the bearer before mesh routing sees a request. `forward` deletes
  `Authorization` again anyway. The Manager's own client (probes, the session
  index, `scope=all`) is a plain `http.Client`, never the Data Service client's
  token-bearing transport. If one machine's token leaked to another, a
  compromised peer could replay it against the first machine's loopback API. The
  design never gives a peer anything to replay.
- **The mesh layer is reachable only through the door.** The door sorts requests
  by the mux's pattern, and `/peers…` or a peer-only session route would
  otherwise fall to `/`, the exempt static UI. So `mesh.Claims` marks those paths,
  and `api.Classify` reports them as `layer`, which needs the token. A
  session-scoped token (an agent's orchestrate MCP server) is refused by the mesh
  layer even on its scoped routes, because a session on a peer is never its own.
- **Tests:**
  - `services/router/mesh_frontdoor_test.go`:
    - `TestMeshRoutesAreBehindTheFrontDoor`: no token or a wrong one gets 401
      and a scoped token gets 403. Nothing reaches the peer.
    - `TestInstallTokenNeverReachesAPeer`: covers every request the viewer
      Router sends a peer, including background probes and indexing. None
      carries `Authorization` or the token anywhere.
  - `services/mesh/internal/server/token_test.go`: forwarding sends the node's
    token to a door-protected Router, drops the caller's credential, and follows
    rotation. Without a token it gets the Router's 401.
  - `ui/e2e/mesh-mode.spec.ts`: each topology has its own token. The stand-in
    Mesh Service sends the peer's token and asserts the viewer Router sent it no
    credential at all.
- **New Router routes in `authz.Routes`:**
  - View:
    - `GET /links`
    - `GET /sessions/{id}/repos`
    - `GET /sessions/{id}/land` (the Land preview)
    - `GET /sessions/{id}/tools/{toolUseId}/output`
  - Control: `POST /sessions/{id}/land/draft`, a git write.
  - Deny:
    - `GET /children`: host-local child sockets.
    - `GET /repos/state?path=`: it reads the git state of any path on the host,
      so a peer could probe the disk for repos. The UI reads a session's repos
      through `/sessions/{id}/repos`.
- **Deploying:** a node's Router and Mesh Service must be updated together. A
  front-door Router answers 401 to an older Mesh Service that sends no token.
  Peers then show `forbidden` or unreachable.

## Implementation notes (mesh auth A4/A5, 2026-11-18)

Access over the mesh is now docs/MESH-AUTH-DESIGN.md's, not D2's and D3's:
- **The transport is mutual TLS with pinned device keys.** A viewer dials only
  rows with a fingerprint (`unpaired` otherwise), over one reused HTTP/2
  connection per peer. `mesh_url` keeps its `http://` form and is dialled
  with TLS.
- **A host admits a paired device at its grant** (view, chat, control or
  admin) and ceilings. WhoIs and the capability stay as a second factor, on
  by default, and the capability's level caps the grant.
- **`/mesh/hello` is no longer open to every tailnet member:** it answers
  paired devices only. "Add device" lists tailnet devices without probing
  them, and adding one needs its fingerprint.
- **A request a peer relayed is answered by the node it reached:** never
  forwarded again (`/peers`, implicit session routing, `scope=all`,
  `/links`).
- **A viewing device's `/events` leaves out `peer.*` and `device.*`.**

## Open questions

- **Q1.** ~~Peer registry storage?~~ Decided: D3.
- **Q2.** Is the app-capability domain `github.com/drewgitsit/mads` right, or do you
  own a domain you'd rather namespace it under?
- **Q3.** ~~Is there a folder-listing route?~~ No. D6 serves it from the Mesh
  Service instead. Is "directories under `$HOME` only" the right bound?
- **Q4.** ~~Notifications?~~ Decided: badge only.

**Related, separate change:** `useConfig`, `App.tsx` and `Launchpad` read config
without subscribing to `config.updated`, so they stay stale until they remount.
This is pre-existing, so it gets its own small change outside mesh mode.

## Decisions log

- 2026-11-12: Any-node-sees-all behind a toggle; default is this device (Drew).
- 2026-11-12: Merged list preferred over a host switcher (Drew).
- 2026-11-12: Tailnet identity (WhoIs plus app capability) instead of an SSH tunnel,
  as a trial of a production deployment (Drew).
- 2026-11-12: P0 + P1 this weekend; P2 spec only (Drew).
- 2026-11-12: Mesh Service as a new service, not a Router listener (Drew).
- 2026-11-12: Capability name `github.com/drewgitsit/mads/cap/mesh` (Drew).
- 2026-11-12: Peer folder browsing limited to directories under `$HOME` (Drew).
- 2026-11-12: Peer attention shows as a badge only, no desktop notifications (Drew).
- 2026-11-12: One list, "devices I can view". Inbound access is the tailnet policy
  alone; consumer audience, not enterprise (Drew).
- 2026-11-12: Peers are a Data Service table with `peer.*` events. The tailnet is
  the upper limit (Drew).
- 2026-11-12: Reachability transitions go in the event log, for troubleshooting
  (Drew).
- 2026-11-16: Cross-device files are read-only and opened locally (D10); write-back
  is done by asking an agent on that device (Drew).
- 2026-11-17: On the local front door: the Mesh Service sends its own node's install
  token to its Router; no install token ever crosses the mesh. Peers are
  identified by tailnet identity only.
