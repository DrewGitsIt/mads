# Mesh auth: secure across devices, with no login

**Status:** Built (A1–A7, 2026-11-17 to 2026-11-20) and merged with mesh
mode (`docs/MESH-MODE-DESIGN.md`). Deferred: QR codes and phone pairing
(D6), C1 "held" (D4), LAN listening, and P2's host shells. The
implementation notes below record each step as it landed, and the cutover
runbook how it was deployed.
**Date:** 2026-11-15. **Driver:** Drew. Mesh mode works and is valuable, especially
with a planned lightweight mobile app. But today, once mads is installed, any device
that can reach it can effectively run code on it. People will mesh mads in many
shapes: a home LAN, some devices on a tailnet, a phone plus a laptop, n devices on a
company VPN. mads should need **no user login** and still be **secure inside the
mesh**.

## Glossary

- **Device:** one mads install, identified by its device key (D1). A
  computer or a phone.
- **Host:** the device a session runs on. It decides what other devices
  may do with its sessions (its grants, D3).
- **Viewer:** a device showing another device's sessions (All devices).
  Every computer can be both a host and a viewer.
- **Peer:** another device in this device's list (the Data Service's
  `mesh_peers` rows, the Router's `/peers`). A peer may be viewed (its
  sessions shown here), paired (its key pinned, with a grant here), or
  both.
- **Paired device:** a peer whose key is pinned here (D6); what it may do
  here is its grant, a level plus ceilings (D3, D4).

## Context: what decides trust today

**On one machine: network location is the credential.**
- `pkg/localguard` admits any request whose Host is loopback and which carries no
  foreign browser Origin. That keeps web pages out, but not other programs.
- Any local process, including another OS user's, can drive the Router (send to a
  full-trust session, create sessions), and can reach the Data Service, which has no
  auth at all.
- `X-Mads-Actor` is whatever the caller says it is (`services/data/internal/api/tasks.go:15`).

**Across machines: the tailnet is the credential** (mesh mode D2/D3).
- The Mesh Service admits callers by Tailscale WhoIs (same user, untagged) plus an
  app capability in the tailnet policy (`view` / `control`).
- That works for one person's tailnet. On a LAN with no Tailscale there is no
  identity at all. On a company VPN, whoever runs the network decides who can run
  code on your machine.
- Inbound access lives in Tailscale's policy, not in mads.

**What auth cannot change.** A device allowed to message a full-trust session can
make the agent run code. Any useful mesh has that. Auth can only make *which devices
are in the mesh* explicit, scoped, capped, revocable and audited, independent of the
network.

## Threat model (ranked by Drew, 2026-11-15)

1. **A compromised device already in the mesh.** Malware, a stolen-and-unlocked
   laptop, an insider. Enterprise controls may help, but a mesh grant is a powerful
   backdoor. The blast radius is every host that granted that device something.
2. **A stolen phone.** The most likely case. The mobile app (not yet specified) can
   do exactly two things: read any session on the mesh, and send a message to any
   session.
3. **Other programs and users on the same machine.** Today they can drive mads and
   read its Data Service. The local front door has no lock.
4. **A hostile machine on the same network** (café, LAN, VPN). It can reach ports, so
   transport must authenticate and encrypt on its own, without relying on the network.

**Constraint:** hosts must work **unattended**. A server may have no display or
input, so no design may rely on approving an action on the host at the time it
happens. All protection is decided in advance: grants, ceilings, audit, revocation.

**Tailscale's role:** an **optional transport**. Useful as an extra layer and as a
free "these are all my devices" list, never the source of trust.

## Principle

**Every client is a paired device, and every host decides for itself.** A device's
identity is a key pair. Pairing pins that key on a host, together with what it may do
there. Every connection is mutually authenticated with pinned keys, whatever the
network. Trust is never transitive: a host trusts only devices it paired directly, or
devices someone with authority over that host explicitly introduced (D5). The local
app is no exception. It is a device too, paired automatically at install.

## Decisions

### D1. Device identity: one key pair per install

- On first run, mads creates an **Ed25519 key pair**. Its public-key fingerprint is
  the device ID, shown as a short, groupable code for humans.
- **Storage:**
  - Desktop and headless alike: a `0600` file in `$MADS_HOME`, for this
    increment (Q5). Keyring wrapping and TPM sealing are deferred.
  - Phones: the key lives in the **hardware key store** (Secure Enclave / Android
    Keystore), non-exportable, and **requires user presence for each signature**. A
    stolen, locked phone can't sign anything (threat 2).
- Key rotation: re-pair. Rotating a key is an unpair plus a pair, logged as both.

### D2. Transport: mutual TLS with pinned keys, over any network

- Every cross-device connection is **TLS 1.3 with client certificates**. Both sides
  present self-signed certificates for their device key and check the other's against
  the pinned fingerprint. There's no certificate authority, no DNS trust and no
  login.
- It runs the same way over LAN, VPN or tailnet. On a tailnet it simply sits inside
  WireGuard.
- Optionally a host can **also require tailnet identity**: Settings → "Only accept
  devices on my tailnet" re-uses today's WhoIs check as a second factor. That's
  today's model, kept as a hardening option rather than the foundation.
- The Mesh Service (`services/mesh`) becomes the TLS endpoint. It verifies the
  pinned key, looks up that device's grant (D3), then forwards to the loopback
  Router as today, with the **authenticated** device ID as the actor (D7).

### D3. Grants: four levels, decided by the host

Inbound access lives **on the host**, per paired device. This reverses mesh-mode
D3's "one list, devices I can view; inbound is the tailnet's call". The host is the
one at risk, so the host decides.

| Level | May do | Typical device |
|---|---|---|
| **view** | Read sessions, transcripts, live output, the session list | Default for every new pairing; a wall display |
| **chat** | view, plus send to, interrupt and answer permission prompts for **existing** sessions at their **existing** trust | The phone |
| **control** | Everything a user does with sessions: start sessions (any folder, any trust), raise or lower trust, shell, commit/land/push, archive, restart | Your other computers |
| **admin** | control, plus pairing, grants and introductions **on this host** (D5) | One or two devices you deliberately manage this host from |

**Why chat is separate from control** (Drew challenged this, 2026-11-15):
- The app's interface is not the security boundary. A compromised phone or app (or
  anything that can get the app to sign) can send any request the key's grant allows.
- With `chat`, a misused key is confined to **your existing sessions, at the trust
  levels you chose, with every message in their transcripts**.
- With `control` it can also start full-trust sessions anywhere in `$HOME`, raise
  limited sessions to full, open a shell that bypasses the transcript, and push,
  land or delete.
- The split costs one dropdown entry.

**Honest limit:** chat to a full-trust session is still code execution in that
workspace. See C1.

The levels are **named presets** over the Mesh Service's route classes, which are
extended from today's `view`/`control` table in `services/mesh/internal/authz`. The
guard test that classifies every Router route stays. Each route gets the lowest level
that may use it.

### D4. Ceilings: limits that hold whatever the level

Each grant may carry ceilings. Install-wide defaults apply to new pairings.
- **C1 Full-trust chat.** Messages from a `chat` device to a full-trust session are
  **allowed** (default) or **refused**.
  - This is the real stolen-phone exposure. A thief with the phone and its passcode
    can still message your strongest sessions.
  - It's a product trade-off: it makes the phone less useful for driving full-trust
    work.
  - Default: allowed, with C1 offered in the device's row in Settings (Q1).
  - **Held** (wait until a `control` device approves) is **deferred** (2026-11-19),
    possibly later for compliance. Settings offers only allowed and refused. The
    stored type keeps `held`: a row that has it (written by `mads pair` or an
    older build) is refused with `held_unsupported`, as since A5.
- **C2 No shell** for a `control` device.
- **C3 Trust ceiling:** a device can't start or raise a session above a given trust
  level.

### D5. No transitive trust; explicit introductions

What other systems teach (researched 2026-11-15):
- Implicit transitive trust fails: PGP's web of trust; Kerberos unconstrained
  delegation, where one compromised service impersonates users everywhere.
- Systems that extend trust safely make each extension **explicit, recorded, bounded
  and revocable**: Syncthing's opt-in *introducer*, Keybase's signed device
  provisioning, constrained delegation. Signal/WhatsApp linked devices go further:
  one primary device links others, and linked devices can't link more.

For mads:
- **No relaying.** If C is paired with A and A with B, C never reaches B through A.
  Peers can't reach each other's `/peers` (already pinned by `authz` tests).
- **No inherited trust.** B trusts C only if B paired C, or C was introduced to B.
- **Introductions:** a device holding **admin on B** may introduce C to B.
  - That pins C's key on B at a level **no higher than the introducer's own**,
    and **never admin** (Q2): an admin introducer introduces at most control.
  - It's logged on B as `device.introduced` (who, whom, level).
  - It's revocable like any pairing.
- **No standing introduction rules** (dropped 2026-11-19). An earlier draft let B
  say "accept introductions from A, capped at chat" without A holding admin on B.
  The introducer must hold admin on the host, and that's the only way in besides
  pairing on the host itself. A headless host grows by an admin introduction or by
  `mads pair` over SSH.
- Consequence for threat 1: a compromised device can only add devices where it holds
  admin, and only up to its own level, never admin. Keeping admin rare keeps that
  small.

### D6. Pairing: a copy/paste code, approved on the host

Updated 2026-11-19: one ceremony, a **single-use pairing code** copied from the
host and pasted on the joiner, then **approved on the host**.
1. The host makes a code (Settings → Pair a device → Copy this device's pairing
   code, or `mads pair code`). It holds the host's name, its Mesh Service address,
   its fingerprint and a one-time secret. It works once and expires after 5
   minutes.
2. The joiner pastes it (Paste a pairing code, or `mads pair join`). Its Router
   connects to the host's Mesh Service over mutual TLS with its own device key,
   checks the host's certificate against the fingerprint in the code, and presents
   the secret, its name and its platform.
3. The host shows an approval prompt: the joiner's name, platform and fingerprint.
   The host's user approves with a level (never admin) or declines. The code is
   used up either way.
4. The joiner learns the outcome and what it was granted, pins the host (granting
   it nothing until its user chooses), and sets what the host may do on it and
   whether it shows the host's sessions.

- **Headless host:** `mads pair code` on the host (over SSH or a console) prints the
  same code, and `mads pair requests` / `approve` / `decline` answer it. Or an
  admin device introduces the joiner (D5).
- A pairing secret is single-use and rate-limited. A wrong, expired or used
  secret gets one generic answer, is logged (debounced), and doesn't reveal
  whether a code exists.
- **Deferred:** QR codes and phone pairing (the phone's key in its hardware key
  store, D1), and a "discoverable" flow with number comparison instead of a
  pasted secret.

### D7. The local front door is a paired device too

- **At install, the local app pairs itself.** The local key is readable only by the
  owner. The Electron main process, the CLI and the session/pipeline children prove
  possession of it to the Router.
- **Browsers can't send custom headers on EventSource.** So the main process
  exchanges the key for an `HttpOnly`, `SameSite=Strict`, loopback-scoped session
  cookie, and the renderer's fetches and streams carry it.
- **Between services:** the Router↔Data Service and Router↔child hops get a per-install
  service secret, extending the per-child secret the shell spike introduced. The Data
  Service stops serving unauthenticated loopback callers.
- **Result for threat 3:** other OS users and random local programs can no longer
  drive mads or read its data. A program running *as you* can still read your key
  file; that's the ceiling of any local, no-login design, and the same as SSH keys.
- **Development:** `make` targets and the e2e harness get the key the same way. A
  `-insecure-local` flag exists only for tests and is refused outside them.

### D8. Audit and revocation

- **The actor is authenticated, never asserted.** The Mesh Service and the local
  front door overwrite `X-Mads-Actor` with the authenticated device ID; a
  caller-supplied value is ignored. Every event a remote device causes carries it, so
  a session's history shows which device did what.
- **New events:** `device.paired`, `device.unpaired`, `device.grant_changed`,
  `device.introduced` and `device.refused` (wrong key or secret, or above its level).
  Refusals are debounced like `peer.reachability_changed`.
- **Revocation:**
  - Unpairing on a host takes effect on the next request. Live streams for that
    device are closed.
  - **Remove device…** (one action, 2026-11-19) removes the device here, then
    tries every other host where this device administers, and reports each
    (removed, offline with a retry, failed). It lists the known hosts it can't
    manage, and says that hosts it doesn't know can't be checked.
- When each device last used this host, and its recent actions, are shown in
  Settings (the Device activity log).

### D9. Settings → Paired devices

One section per install, coupling grants and ceilings (agreed 2026-11-15), laid
out as `docs/mockups/paired-devices-v3.html` (updated 2026-11-19 after Drew's
review; see "Settings → Paired devices (A7) UI" for the full list):
- **This device** heads the section: its name, kind and fingerprint, with **Pair a
  device** and **Introduce a device…**.
- **One table, both directions per row:**
  - "*Device* may [not use · view · chat with · control · administer] mads on
    this *host*": the level, as a verb, edited in place.
  - "Lets this *host* …": what that device grants this one, from its
    `/mesh/hello`; the last known value with its time while it's offline; "—"
    for a device that doesn't host mads, or one never reached yet.
  - Show sessions (today's peer list, "whether I view it"), and a status.
- **Row details**, inline: C1 (allowed or refused) and C3, the fingerprint, and
  **Remove device…**.
- **One Device activity log** for the section, filterable by device.
- **Advanced options:** "only accept devices on my tailnet".
- Saving is instant; raising a device to control or admin asks first.

## What changes in the mesh-mode branch

| Today (mesh-mode) | With this design |
|---|---|
| Tailscale WhoIs + app capability decide access | Pinned device keys over mTLS decide. WhoIs becomes an optional extra check. |
| Two levels, `view` / `control` | Four: view / chat / control / admin. The `authz` table is re-split. |
| Peers table = "devices I view" | A **paired devices** table: key, inbound grant and ceilings, plus "I view it". *As built (A3):* the same `mesh_peers` table, migration `0035` unchanged; the new fields live in the row's JSON. |
| Mesh Service listens on the tailnet IP only | It listens wherever reachable devices are (LAN or tailnet, configurable), TLS-only. *As built (A4):* TLS-only, still on the tailnet IP only; LAN listening is later. |
| Router trusts any loopback caller | The local app, CLI and children authenticate (D7) |
| `X-Mads-Actor` is caller-asserted | Set from the authenticated identity (D8) |

Everything else stays: routing by session, `scope=all`, the device menu, the per-host
reads, and selection following the list.

## Risks accepted

- **A program running as you can read your local key** (D7). It's the same ceiling
  as SSH keys. The mitigation is OS-level, such as keyring-protected storage where
  available.
- **C1 defaults to allowed:** a stolen, unlocked phone can message full-trust
  sessions until it's revoked.
- **Pairing UX on headless hosts** needs SSH or an admin introduction. That's a
  deliberate cost of not trusting the network.

## Non-goals

- User accounts, logins, SSO.
- A central coordination server.
- Relaying across devices (C through A to B).
- Per-session ACLs. Grants are per device and host; trust levels stay per session.
- The mobile app itself (separate spec). It's assumed to read and send only.

## Decomposition (dependency order)

1. **A1** Device identity and key storage (D1); `mads device` CLI to show the
   fingerprint.
2. **A2** Local front door (D7). **Shipped** (docs/LOCAL-FRONT-DOOR-DESIGN.md),
   not as first drafted here: one install token (`$MADS_HOME/local.token`) as a
   bearer for the Router and the Data Service alike, by every caller, plus
   session-scoped orchestrate tokens; children on owner-only Unix sockets.
   There's no cookie exchange (Electron and Playwright add the bearer below the
   page, H7) and no separate Data Service service secret. This alone closes
   threat 3.
3. **A3** Paired-devices store and events (D3/D8). *As built:* the peers table
   gains the paired side in its JSON; migration `0035` is kept as-is.
4. **A4** mTLS in the Mesh Service, plus pinned-key verification on the viewer's
   outbound side; optional tailnet check (D2).
5. **A5** Four levels and ceilings in `authz` and the Router (D3/D4); authenticated
   actor (D8).
6. **A6** Pairing ceremonies (D6), introductions (D5), revoke everywhere (D8).
7. **A7** Settings → Paired devices (D9).
8. **A8** Rebase mesh-mode onto A1–A7, then squash and merge.

**Batch 3** (2026-11-19) is A6 without QR codes or phone pairing, plus A7: the
pairing code and host approval, introductions, remove everywhere, device activity,
and the Settings section. A8, the merge, comes after batch 3.

## Settings → Paired devices (A7) UI

The approved spec is the mockup `docs/mockups/paired-devices-v3.html` (open it from
disk; its header comment maps each block to a TSX component and lists the data
it needs). **This UI is subject to change in the future, but it contains the key
information and touchpoints.** Decided in Drew's review of it (2026-11-19):

- **Levels read as verbs:** "*Device* may [not use · view · chat with · control ·
  administer] mads on this *host*", with the host's own short name ("this
  MacBook-Air"). Grant none reads "not use".
- **A device's kind is detected, never chosen.** It comes from the platform the
  device reports (`/mesh/hello`, or the pairing request): computer (macOS,
  Linux, Windows), phone (iOS, Android), or unknown. The kind picks the icon and
  the suggested level: control for a computer, chat for a phone, view for
  unknown.
- **Pairing never offers admin.** Approving a pairing request and introducing a
  device offer not use through control. Administer is set only on the device's
  row, with a confirmation, and only on the host itself (Q2); the row's picker
  marks it "Set here only".
- **The joiner's reverse level starts at "not use"**, with a "Suggested: control"
  hint for a computer. Nothing is granted back until the joiner's user picks it.
- **Remove device…** is one merged action (D8): it removes the device here, then
  tries every other host where this device administers, with a per-host result:
  Removed, Offline (with Retry) or Failed (with Retry). It lists the known hosts
  this device can't manage ("Not yours to manage"), and says that devices this
  one has never seen can't be checked.
- **One Device activity log** for the whole section, filterable by device, and
  the same list inside a row's details. "Introduced by" and the pairing method
  appear only in the log, not on the row.
- **Saving is instant.** There's no Save button; raising a device to control or
  administer asks first.
- **"Only accept devices on my tailnet"** is a switch under the section's
  Advanced options, tagged "Less safe" when off.
- **Wording:** a level of none reads "Doesn't let this *host* use mads" in the
  status. A host never reached shows "—" (Not reached yet) under "Lets this
  *host* …", as does a device that doesn't host mads sessions.

What the backend gives the UI: see "Implementation notes (batch 3 backend)".

## Implementation notes (A1, A3, `mads pair`; 2026-11-17)

Nothing here changes who may reach a host: the Mesh Service still admits by
WhoIs plus the tailnet capability. A1/A3 record identities and grants, and the
Mesh Service reads them, so A4/A5 have something to enforce.

**A1, device identity (`pkg/devicekey`).**
- The key pair is `$MADS_HOME/device.key`: an Ed25519 private key as PKCS#8 PEM,
  mode `0600`, inside the `0700` `$MADS_HOME` that A2 enforces (Q5).
- It's created on first need: Router start, Mesh Service start, or
  `mads device`. Creation takes a flock on `device.key.lock` and links the
  file into place exclusively (the same create-once as `local.token`, through
  `localauth.EnsureHome` / `Flock` / `OwnerUID`), so concurrent first starts
  agree on one key. An existing key is never regenerated.
- A key file that isn't a regular file, is readable or writable by group or
  others, or is owned by another user is **refused**, not repaired: the error
  says to unpair the device, remove the file and restart. The Router and the
  Mesh Service log a refusal and carry on, since nothing depends on the key yet.
- The file is opened without following a symlink and without blocking
  (`localauth.OpenPrivate`, `O_NOFOLLOW|O_NONBLOCK`), then checked on the open
  descriptor. A symlink, even a dangling one, is refused rather than read as
  "no key yet", and a FIFO can't hang startup. `local.token` shares the same
  loader, which fixes the same FIFO hang there.
- The private key is never in argv, env, logs or events. `Identity` prints
  only its fingerprint under every `fmt` verb, and parse errors never echo the
  file.
- **Fingerprint:** the SHA-256 of the raw 32-byte public key, in unpadded
  RFC 4648 base32: 52 characters, `A–Z2–7`. That canonical form is what's
  stored and pinned.
  - People read it grouped in fours (`JOYG-7DSO-HJ3R-…`, 13 groups). The
    **short form** is the first four groups (80 bits), for labels and logs only.
  - Input accepts any case, dashes or spaces. Anything that isn't the whole
    fingerprint is refused, the short form included.
- **`mads device [--json]`** prints the name (short hostname, as mesh mode
  labels this node), the grouped fingerprint, the short form and the
  `mads pair --accept` line to run on a host. `--json` adds the public key
  (base64).
- The Mesh Service reports the fingerprint on `/mesh/hello` as `device`. It
  isn't a secret: it's what other hosts pin, and A6's ceremony will compare it.
  Viewers ignore the field for now.

**A3, the paired-devices store.**
- **The table stays `mesh_peers`; migration `0035` is unchanged.** The paired
  side lives in the row's JSON (`contracts.Peer`):
  - `viewed`: whether this node views the device. Absent reads as `true`, so
    every row from before pairing (the live pop-os row included) means exactly
    what it did: viewed, unpaired, no grant.
  - `fingerprint`, plus an optional `public_key`. When the key is set it must
    match the fingerprint.
  - `grant`: `{level, ceilings}`. The level is view, chat, control or admin;
    no `grant` on a paired device is D9's "none". Ceilings are
    `full_trust_chat` (C1: allowed, refused or held; empty means allowed, Q1),
    `no_shell` (C2) and `max_trust` (C3, a session trust; empty means no
    ceiling). The zero value is the default for each; install-wide defaults
    wait for A7.
  - `pairing`: `{paired_at, paired_by, how}`, with `how` one of accept,
    ceremony or introduction.
- **Store rules** (checked in the write transaction):
  - A fingerprint is on at most one row. There's no column to index, so it's a
    `json_extract` check.
  - A row is viewed, paired or both. `mesh_url` is required only when it's
    viewed, and a grant only exists on a paired row.
  - The store owns `pairing`: it's stamped (now, the actor) when a fingerprint
    is first set or changes, and kept when the fingerprint doesn't change. A
    caller's `how` is the only part taken. Until A5 authenticates the actor,
    `how` and `paired_by` are whatever the writer asserts.
  - Names can't be `candidates` (a Router route) or start with `per_` (ids do,
    and `/peers/{key}` resolves ids first). The store enforces this, so the CLI
    can't bypass the Router's check.
  - **Conditional writes.** `PUT` and `DELETE /peers/{id}` with `If-Match:
    <updated_at as read>` fail with 409 `stale` if the row changed since.
    The Router and `mads pair` write back through them
    (`dataclient.UpdatePeerIfUnchanged` / `DeletePeerIfUnchanged`). The Router
    edits the Data Service's current row, never its in-memory copy, and retries
    once on a fresh read. So a hide toggle can't re-pin a device revoked a
    moment ago, and a remove can't delete a fresh pairing. `updated_at` only
    moves forward per row (at least 1 ns past the stored value), so a clock
    stepping back can't let a stale `If-Match` match.
  - **Create never replaces a row.** For every registry, a `POST` naming an id
    already in use is 422 (`id … already exists`), like a duplicate name. The
    write is an upsert, and replacing a row through it would skip the update
    path's checks, hook and events. A free caller-chosen id still works.
- **Events (D8).** `peer.created/updated/deleted` stay the registry's change
  signal, payload unchanged. Beside them, in the same transaction:
  - `device.paired` when a fingerprint is pinned;
  - `device.unpaired` when it's removed or the row is deleted;
  - `device.grant_changed` when a paired device's level or ceilings change.
  - Payload `contracts.DeviceChange`: `{peer_id, name, fingerprint, level,
    previous_level, how}`.
  - A changed key is `unpaired` plus `paired`, as D1 says rotation is.
  - D8 doesn't say whether unpairing deletes the row or marks it revoked.
    Unpairing removes the paired side; the event log keeps the history.
  - `device.introduced` and `device.refused` come with A6 and A4/A5.
- **Admin (Q2).**
  - **What protects peers today is the mesh route table.** `authz` is
    default-deny, and `/peers` is Deny, so no mesh caller reaches peer
    management at all.
  - **What the marker adds**, so a future route (A5/A6) can't grant admin by
    accident:
    - The Mesh Service stamps `X-Mads-Mesh-Peer` on everything it forwards.
    - The Router's mesh layer carries that mark in the request's context
      (`dataclient.WithMeshRelay`). Every Data Service write made with that
      context then carries it.
    - Work a handler detaches from the request (a background goroutine on a
      fresh context) doesn't carry it, and none of that work writes peers. The
      mark isn't also stamped in the front door: the mesh layer is always
      installed (`meshLayer` is nil only without a manager,
      which `main` never builds), and the door doesn't know about the mesh.
    - Presence counts, even with an empty value, so a blank node name doesn't
      bypass it.
  - **The rule, in the store's write transaction against the stored row:**
    granting admin needs host authority, meaning the install token presented
    directly with no mesh mark; otherwise 403. Granting means any of:
    - a device newly at admin;
    - a new key at admin;
    - an admin's ceilings loosened (C1 refused → held → allowed, C2 off, C3
      raised or removed).
  - Keeping, tightening, lowering or removing an admin is a normal write, so
    "any admin may remove admin remotely" stays possible.
- **The Router** lists, probes and routes only to viewed rows.
  - Its `DELETE /peers/{peer}` (the app's "remove") stops viewing a paired
    device and keeps its pairing; an unpaired row is deleted as before.
  - Its `POST /peers` for a name that's paired but not viewed gives that row
    its viewed side, but only when the device's `/mesh/hello` reports the
    fingerprint that row pins (`device`). The name can come from the remote's
    own hello, so it proves nothing. Otherwise it's the name clash it always
    was (422), and the row keeps its key and grant.
    - The `device` fingerprint is no proof either until A4: it's
      self-reported over unauthenticated HTTP. The check guards against
      accidental merges, not a spoofer.
    - A spoofer could get a paired-only row viewed at its own URL. That gains
      it no access, and "remove" undoes it. After A4, the pinned-key check
      makes such a URL unreachable.
- **The follow loops** (the Router's `watchPeers`, the Mesh Service's
  directory):
  - They start from `dataclient.LatestSeq`, which pages past `GET /events`'
    500-event cap.
  - They reset their backoff on a delivered event, or when a subscription
    held at least 5 s before ending (a token rotation, a Data Service
    restart), so endings spread over hours don't add up. A stream that opens
    and closes at once keeps doubling the backoff and can't spin.

**The Mesh Service reads the Data Service** (`services/mesh/internal/paired`).
- It loads the paired rows at start and follows `peer.*`, the Router mesh
  manager's loop: cursor, then list, then stream. One change from that loop:
  a failed list read also backs off and retries, instead of waiting for the
  next event.
- It reads with the install token through `localauth.FileSource` (new flag
  `-data-url`, default `http://localhost:17374`) and keeps the list in memory by
  fingerprint.
- It serves normally with the Data Service down: the listener never waits on
  the list, which retries with backoff (1 s up to 5 s since A4) and is empty until the
  first load.
- It's used only for logging so far: each change logs the count and each
  device's name, short fingerprint and level. A4 looks callers up here by
  certificate key; A5 reads their level.
- This keeps §15: the Data Service stays the sole owner, and the Mesh Service
  is a reader of its registry and event log, as the Router is.

**`mads pair`** (on the host, as the user mads runs as; it goes through the
Data API with the install token):
- `--accept FINGERPRINT [--name NAME] [--grant view|chat|control|admin|none]`
  pins a device, like an `authorized_keys` line.
  - A new device gets `view` and isn't viewed. Its default name is
    `device-<first 8 of the fingerprint>`.
  - Accepting a fingerprint that's already paired updates that row: `--name`
    renames it, `--grant` changes its level.
  - `--name` naming a viewed, unpaired row pairs that row, so a device can be
    both. A name already paired to another key is refused.
  - `--view [--mesh-url URL]` also views the device.
  - **Only the fingerprint is needed.** D2 pins fingerprints and checks the
    peer's certificate key against them, so the public key adds nothing at
    accept time. A6's ceremony can record it.
- `--list [--json]` shows every row: name, short fingerprint, grant, viewed,
  and when and how it was paired.
- `--revoke FINGERPRINT|NAME` unpairs. A viewed row keeps its viewed side; a
  paired-only row is deleted.
- Output and help say plainly that grants are recorded, not yet enforced.

- `mads pair --accept` refuses this host's own fingerprint. The Data Service
  doesn't know its host's key, so the CLI checks this.
  - A device key that exists but can't be read (loose mode, another owner)
    fails the accept with that error rather than skipping the check.
  - No key at all can't match, so the accept goes ahead.

**Rollout: don't roll back carelessly.** Once batch-1 binaries have written
paired rows, an older mesh-mode build misreads them:
- It reads a paired-only row (no `mesh_url`, `viewed: false`) as a viewed device
  and probes an empty URL.
- Its full-replace writes (rename, hide) silently drop `viewed`,
  `fingerprint`, `public_key`, `grant` and `pairing`, since it doesn't know
  those fields.

Before running an older build against the same Data Service:
- `mads pair --revoke` the paired-only devices;
- expect to re-pair the rest.

**Left for later:**
- ~~`X-Mads-Actor` is still caller-asserted (D8, A5)~~: for mesh callers it is
  now the authenticated device (A5). Locally, `paired_by` and `how` are still
  whatever the install token's holder says; the CLI says `user`.
- ~~**A5 decision item: `GET /events` is View on the mesh.**~~ Decided in A5:
  a device below admin doesn't see `peer.*` or `device.*` (see the A4/A5
  notes).

## Implementation notes (A4, A5; 2026-11-18)

Who may reach a host is now decided by the host: a pinned device key over
mutual TLS, at that device's grant and ceilings. The tailnet is a second
factor, on by default.

**A4, mutual TLS (`pkg/devicekey/tls.go`, `services/mesh`).**
- **The Mesh Service is TLS 1.3 only and requires a client certificate.**
  - Its server certificate is self-signed, made in memory at start from
    `device.key`. No certificate files: the certificate only carries the
    key, peers pin the key, so a new certificate per start changes nothing
    for them.
  - The handshake accepts any client that proves an Ed25519 key (TLS 1.3's
    CertificateVerify). Whether that key is paired is decided per request,
    by its fingerprint (the A1 function) in the paired directory:
    `403 unpaired_device`, with the `mads pair --accept` line to run.
  - *Why per request, not in the handshake:* the viewer gets a code it can
    show in `/peers` instead of a bare TLS alert, revocation needs no
    reconnect, and the refusal can be audited with its route. The cost is
    that an unpaired key reaches the HTTP parser, and nothing else.
  - Until the directory's first load every device is refused (`503
    devices_unavailable`): fail closed.
- **Revocation is immediate, streams included.** Each forwarded request is
  tied to the grant it was admitted under (level and ceilings). After every
  directory reload (`paired.Directory.OnReload`), requests whose device was
  unpaired or whose grant changed are cancelled, as A2's door ends streams
  when a credential dies. Any change ends them, an upgrade too; the viewer
  reconnects and is admitted afresh. Kept-alive connections need nothing:
  the next request is checked. A tailnet policy change still applies to new
  requests only, within `-whois-ttl`, as before.
- **`device.refused` (D8)** is appended for an unpaired key, a level too low
  and each ceiling. Not for Deny routes (the same for every device) or the
  tailnet refusals (the tailnet's own business). Debounced per device, code
  and route (10 minutes, repeats counted on the next one) and capped at 30 a
  minute overall (the rest counted as `dropped`), written off the request
  path. A client minting a key per connection can't flood the event log.
  Handshake failures (no certificate, a non-Ed25519 key) only reach the
  service's log.
- **The tailnet stays required by default** (`-require-tailnet`, default
  `true`; the Settings toggle is A7). WhoIs, same user, untagged, holding the
  capability: the `browser_request`, `other_user`, `tagged_node` and
  `no_capability` refusals are unchanged. The listener still binds only the
  tailnet IP.
- **Viewer side (`services/router/internal/mesh/dial.go`).**
  - Every request to a peer presents this device's certificate and checks
    the peer's key against the row's `fingerprint`
    (`devicekey.ClientConfig`). `InsecureSkipVerify` only turns off CA chain
    checking; the pin is checked on every handshake.
  - A viewed row with no fingerprint is never dialled: reachability
    `unpaired` (new), with the command to fix it. A peer proving another
    key is `denied`, naming both keys. A Mesh Service on plain HTTP is
    `unreachable` (A8 dropped the "update it" `version_mismatch` reading
    once every device ran TLS). There is no plaintext fallback.
  - **Connection reuse:** one keep-alive transport per pinned key and
    address, HTTP/2 (the Mesh Service offers it). Probes, the session index,
    links, forwarded requests and long-lived streams share one connection.
    `POST /peers`' vetting connection is the one the new row then uses.
- **`mesh_url` stays `http://host:port` and is always dialled with TLS**
  (read-time migration). The Data Service validates that form and older
  builds read it, so nothing is rewritten and a rollback still finds rows it
  understands.
- **`POST /peers` takes the device's `fingerprint`.** A new row is stored
  pinned (`pairing.how` accept) with **no grant**: viewing a device pins its
  key and lets it do nothing here. Without a fingerprint, the name of a row
  already paired here supplies it; otherwise `422 peer_unpaired`. The
  paired-only merge (batch 1) is now real proof: the device proved the key
  the row pins in the handshake.
- **No unauthenticated endpoint.** `/mesh/hello` is behind TLS and answers
  paired devices only (a device granted nothing gets `access: []`, so the
  viewer reads `denied`). `GET /peers/candidates` comes from the tailnet's
  status only; devices not yet added aren't probed and read `unpaired`. A
  fingerprint comes from `mads device` on that device; A6's ceremony brings
  the pre-pairing path.
- **The loop guard covers every relayed route.** The Router answers a
  request carrying `X-Mads-Mesh-Peer` (presence, even empty) by itself:
  `/peers…` is 403 `relayed`, a session that lives on a peer isn't
  forwarded, `scope=all` is dropped, `/links` isn't fanned out. Two nodes
  that view each other never bounce a request, and a device paired here
  never reaches this node's peers through it (D5).

**A5, levels and ceilings (`services/mesh/internal/authz`, `server`).**
- **The route table is re-split** into view, chat, control and admin, each
  route at the lowest level that may use it. The guard test still
  classifies every Router route. `TestLevelsReach` pins each level's reach.
  - **chat** adds exactly: `POST /sessions/{id}/messages`, `/interrupt`,
    `/permission-requests/{reqId}/resolve`, `/compact`, `/attachments`, and
    `DELETE /sessions/{id}/queue/{qid}`, `/attachments/{aid}`.
  - **control** is the rest of what was control. The agent's terminal
    (`POST /sessions/{id}/transport`, `/input`, `/resize`) is control, not
    chat: raw keystrokes into the TUI can run shell commands or change its
    permission mode, outside the transcript. Rename, set aside, model, rail
    order, resume, terminate and delivering held updates are management,
    not talking to a session, so control too.
  - **admin** has no routes. The Router's `/peers…` falls to `/`, Deny,
    until A6 brings admin over the mesh; until then admin reaches exactly
    what control does.
  - The legacy aliases are gone: the policy's `access` names are levels.
    `chat` means chat, `create` counts for nothing. The live policy is
    `["control"]`.
- **The effective level is the paired device's grant, capped by the
  capability.** The capability is the second factor's gate. Its `access`
  list names a level too, and the lower of the two holds, so neither the
  tailnet admin nor a grant here can raise what the other allows.
  `/mesh/hello` reports the effective level and every level below it
  (`["view","chat","control"]`), so a viewer that only checks for `view`
  and `control` reads every level right.
- **C1, full-trust chat:** applies to an effective chat-level device's
  messages (`POST /sessions/{id}/messages`), and since round 1 to compact's
  focus text. Interrupting or answering a prompt carries no free text. With `refused` or `held`, the
  Mesh Service reads the session's trust from its Router (a relayed GET) and
  refuses a message to a `full` session: `full_trust_chat_refused`, or
  `held_unsupported` for `held`. Held needs the approval flow and its UI
  (`TODO(mesh A7)`). If the trust can't be read it fails closed (`503
  ceiling_check_failed`). Control devices aren't subject to C1 (D4).
- **C2, no shell:** refuses the terminal routes above (`no_shell`), as does
  any C3 ceiling below full (round-1 fixes, above). P2's
  host shells aren't on this branch; they join `authz.ShellRoutes` when
  they land.
- **C3, trust ceiling:** the body is read (1 MiB cap, `413` past it) and put
  back for the forwarder. It is decoded the way the Router decodes it
  (encoding/json, the first value, case-insensitive keys), so the trust
  checked is the trust applied. The target must be at or below the
  ceiling: lowering a session to a level still above it is refused too,
  and an unknown value is refused.
  - **Every route that can set a session's trust:**
    - Forwarded, checked:
      - `POST /sessions` (`trust`; absent, `null` or `""`: the host's
        default for that provider, workspace and folder, read from the
        Router's `GET /sessions/defaults` (the same resolution create
        makes, including full for a full-trust-only provider) and written
        into the body, every case variant of the key replaced);
      - `POST /sessions/{id}/trust` (`trust`).
    - Deny over the mesh, so never reached:
      - `PUT /config` (`default_trust`);
      - `POST /profiles/{id}/activate` (a profile's config);
      - `POST /retros/{id}/follow-up-session`;
      - `POST /pipelines`;
      - `POST /imports/conversations`;
      - the orchestrate surface (`/sessions/{id}/orchestrate/*`,
        `/links/{id}/orchestrate/message`), and with it delegates, which
        agents start there.
    - Don't set trust:
      - `POST /sessions/{id}/resume` and `/bounce` restart at the
        recorded trust;
      - `POST /sessions/{id}/transport` switches transport;
      - a permission answer is allow or deny, never a mode change;
      - `POST /sessions/validate` only validates.
  - `TestCeilingRoutes` pins this list.
- **Authenticated actor (D8).** The Mesh Service sets `X-Mads-Actor:
  device:<name>/<short fingerprint>` (the name reads well; the short
  fingerprint survives a rename), `X-Mads-Mesh-Peer: <name>` and
  `X-Mads-Mesh-Level: <effective level>`, overwriting whatever the caller
  sent. The Router carries the actor into the request's context
  (`dataclient.WithMeshActor`, only with the relay mark). Every Data
  Service write for it then sends that actor, and every event appended for
  it records it (`paired_by` included).
  - Not covered: events a session child writes for the turn a device
    started (the transcript), and work detached onto a fresh context.
- **`GET /events` (the batch-1 decision item):** for a relayed request below
  admin, the Router leaves out `peer.*` and `device.*`, both batch and
  stream. Those are the only events that carry the registry: names,
  fingerprints, grants and refusals. Filtering happens in the Router: it
  already knows relayed requests by the mark and re-encodes every event it
  streams, where the Mesh Service would have to parse SSE. The level header
  counts only with the relay mark, and the Mesh Service always overwrites
  it. Admin devices see them, ahead of A6's admin routes.

**Round-1 review fixes (2026-11-18).**
- **A trust ceiling below full implies C2.** The agent's terminal would
  bypass C3: a device could start a `read_only` session and then type a
  `!` command, switch the TUI's permission mode, or answer its prompts.
  So with `max_trust` set to anything but `full`, the terminal routes are
  refused (`no_shell`) even with C2 off.
- **C1 covers compact too:** its focus text is free text the agent reads.
- **Paths are refused unless clean.** The route table matches the
  cleaned path, but the Router serves the path as sent. So the Mesh
  Service refuses (`400 bad_path`) any path that isn't in clean form (a
  trailing slash is kept as sent), or that carries an encoded slash. C1
  reads the session id from the escaped path, unescaped, as ServeMux does.
- **The tailnet gate runs before the key lookup** when it's required: a
  caller off this user's tailnet never reaches `unpaired_device`.
- **Audit budgets.**
  - Refusals of unpaired keys and of paired devices each have their own
    30-a-minute budget, so throwaway keys can't blind the record of what
    paired devices try.
  - An unpaired key is debounced by key alone, whatever paths it asks
    for.
  - Caller-chosen strings (paths, methods) are clipped to 256 bytes in
    events and log lines.
- **Connections.**
  - The listener closes idle connections after 2 minutes, accepts at
    most 64 at once, and allows 128 HTTP/2 streams per connection.
  - An `unpaired_device` answer closes its connection (`Connection:
    close`; GOAWAY on HTTP/2).
- **The effective level is part of what a request was admitted under.**
  A capability lowered in the tailnet policy ends that device's streams,
  as a grant change does. The tailnet sends no events, so open requests
  are also re-checked on the WhoIs cache's clock (`-whois-ttl`).
- **A failed reload is retried.** It used to be only logged, and the
  event that triggered it was consumed, so a revocation could be lost
  until the next change. Now the directory starts over (cursor, list,
  stream) until the list reads.
- **The directory's backoff is capped at 5 s, so a revocation never waits
  out a long backoff.** Before, a Data Service restart soon after a
  reconnect could leave the cached list unrefreshed for up to 30 s. The
  Data Service is on loopback and a retry is one list read, so a short
  cap is cheap. A periodic poll beside the stream was the alternative, but
  it adds steady load and still needs the retry above.
- **The viewer never follows a peer's redirect.** Every request to a peer,
  forwarded ones included, goes through a transport that sends only https
  to that peer's own address.
- **Router, pre-existing (reachable by a chat device):**
  - A message's `attached` field (held link updates) is stripped in every
    letter case, since the child decodes field names case-insensitively.
  - A message body that is a JSON object followed by more is refused: the
    child reads only the first value, which the Router never inspected.
  - `attachments` is the only other field the Router strips. The child
    has no field by that name, so another casing reaches nothing.
- **Accepted, noted:**
  - The device actor on `session.*` events is visible to view-level
    devices reading the event log. That's the same host's history, and it
    names devices only by their name here and short fingerprint.
- **Left for batch 3:** message sends and their turns are recorded by the
  session child, not the Router, so they carry no device actor. Adding it
  needs the child to take the actor from the forwarded message.

**Round-2 review fixes (2026-11-18).**
- **A peer's answer can't redirect or run on this node's origin**
  (tester-confirmed). The viewer passed a peer's `3xx` and its `Location`
  back to the UI. The browser followed it on the local origin with the
  install bearer that Electron (or the page's headers) adds, to any local
  route: a peer granted nothing here raised a local session to full trust,
  renamed and deleted local sessions, and read `/credentials`. Now every
  relayed answer goes through one `ModifyResponse`, which covers implicit
  session forwarding, `/peers/{peer}/…`, streams and downloads:
  - a redirect (or `101`) becomes `502 peer_redirect` with no `Location`;
  - only body-describing headers are kept (`Content-Type`, length,
    encoding, disposition, range, caching, `Retry-After`). `Set-Cookie`,
    `Refresh`, `Link`, `Location`, CORS, `Clear-Site-Data`, HSTS and frame
    options are dropped;
  - the body can't run here, which closes an XSS route through
    attachments. Attachments open in a new tab on the Router's origin, so
    a peer answering `text/html` would have run script with the app's
    credentials. Every relayed answer is `nosniff` with `Content-Security-
    Policy: sandbox; default-src 'none'` (an opaque origin, no scripts, if
    it is ever opened as a page). Any type but JSON, an event stream, plain
    text or a raster image (HTML, SVG, scripts, PDF, or no type at all) is
    served as `application/octet-stream`, `Content-Disposition:
    attachment`, keeping the file name.
  - `scope=all` and `/links` already re-encode peers' JSON and pass no
    peer headers or statuses; the client calls never follow a redirect
    (round 1).
  - The same hole existed on the host's own Router, reachable over the
    mesh. A chat-level device may attach files to the host's sessions, and
    the host served them with `http.ServeFile` (type by extension or
    sniffed, `inline`). So an attached `.html`, `.svg` or `.xhtml` opened
    as a page on the host's origin when the host's user clicked the tile,
    running script with the host app's bearer. Fixed (round 2, after the
    peer sanitizer): the policy is now one package,
    `services/router/internal/safeserve`, applied wherever the Router
    serves bytes it doesn't control:
    - session and draft attachments, originals and thumbnails;
    - session exports (the transcript's words, already downloads);
    - the orchestrate turns text;
    - every relayed peer answer.
  - Everything else the Router serves is JSON or SSE it encodes itself, or
    the session child's JSON and SSE (transcripts, files, commits, diffs,
    tool and pipeline output), whose types the code sets, never the
    agent. The static UI is the build's own.
  - Raster images and plain text still show inline (thumbnails, text
    tiles); a download that's already an attachment keeps its
    `Content-Disposition` exactly (the export's file name).
- **Connections can't be held to lock paired devices out.** The listener
  (`server.Listen`, `DefaultListenLimits`):
  - holds at most 64 connections, and at most 8 from one address (past
    that a new one is closed at once);
  - closes a connection unless a paired device is admitted on it within
    10 s. That covers bare TCP, a handshake that stalls, a throwaway key
    holding an HTTP/2 preface open, and a refused caller lingering;
  - with the tailnet required, runs the WhoIs gate on a connection's first
    read, before the handshake, in that connection's goroutine, so it
    never stalls the accept loop. Only a definite refusal (not a tailnet
    node, another user's, tagged, no capability) closes it; on a transient
    failure the request gate answers `503`;
  - after any refusal on a connection with no admitted device, closes it
    (`Connection: close`; GOAWAY on HTTP/2), tailnet refusals included.
  - The tester's three attacks are re-run in `TestListenerResistsLockOut`:
    each is closed within the deadline, and a paired device then gets in.
- **Re-checking open requests.**
  - The paired directory's reload hook (`Revalidate`) reads only the
    directory. A revocation ends streams at once, whatever tailscaled is
    doing.
  - The tailnet re-check (`RevalidateTailnet`) runs only on its clock
    (`-whois-ttl`). It ends a stream only on a definite answer: not a
    tailnet node, another user's, tagged, no capability, or a lower level.
  - Each WhoIs call has its own 2 s timeout. On a transient failure the
    last good answer stands for three TTLs past its expiry (a tailscaled
    restart); past that the failure stands, failing closed. That's the one
    case where a stream ends without a definite answer.
  - Log lines now say which re-check ended a request and why.
- **C1 on compact** applies only when it carries focus text
  (`instructions`, any case, read from a bounded body as the child reads
  it). A plain compact isn't a message.
- **Log volume.**
  - Tailnet refusals are logged once a minute per address and code.
  - The paired directory logs a Data Service outage on its first failed
    attempt, every 20th after, and once when the list reads again.
- **`bad_path` refuses an encoded slash anywhere, ids included.** Session
  ids are `ses_` plus hex, so no real request is refused.

**Round-3 review fixes (2026-11-18).**
- **Informational answers.** ReverseProxy relays a peer's `1xx` straight
  from its client trace, before `ModifyResponse`. A `103 Early Hints`
  carried `Link` preloads and `Set-Cookie` to the browser, and the clear
  that follows erased this node's own frame protections. The proxy now
  writes into its own header map: a `1xx` is dropped with what it held,
  and only the final, sanitized headers are added to this node's.
- **Trailers** from a peer are dropped: they're headers set after the
  body, where no filter sees them.
- **`safeserve`:** more than one `Content-Type`, or a list in one value,
  is ambiguous, so it's served as an attachment. Every response ends with
  exactly one `Content-Type` value.
- **The Mesh Service admits a connection** (exempting it from the 10 s
  deadline) only for a device granted something here, not one granted
  nothing.
- **Admission and the deadline settle a connection once** (one
  compare-and-swap): an admitted connection is never closed by a deadline
  firing at the same moment, and an expired one is never admitted.

**Tests:**
- `pkg/devicekey/tls_test.go`: the certificate carries the key; pinned
  handshake, wrong key, no pin, no client certificate, a non-device key,
  TLS 1.2.
- `services/mesh/internal/authz`: the guard, each level's reach, the
  ceiling route sets, `AboveCeiling`.
- `services/mesh/internal/server`:
  - every refusal (none reaches the Router);
  - the capability capping the grant;
  - device stamping and spoof-stripping;
  - over real TLS (HTTP/2): a revoked stream ending and the next request
    refused on the same connection, and grant changes ending streams;
  - C1, C2 and C3 with bodies;
  - refusal debouncing and the rate cap.
- `services/router/internal/mesh`:
  - pinned probes (unpinned, wrong key, the client certificate presented);
  - no plaintext fallback;
  - one handshake for probes, the index, forwards and a stream together;
  - the loop guard on every relayed route, with the mark named and empty;
  - the device actor on writes and events.
- `services/router/internal/api`: the events filter, batch and stream.
- `ui/e2e/mesh-mode.spec.ts`: the stand-in Mesh Service speaks mTLS over
  HTTP/2 with its own Ed25519 certificate (`e2e/lib/meshTLS.ts`). It lets in
  only the viewer's device key, and the whole journey runs on at most two
  handshakes. The viewer refuses to add it without a fingerprint, or pinned
  to the wrong one.

**Left for batch 3 and later:**
- The Settings UI (A7): "only accept devices on my tailnet", grants and
  ceilings per device, adding a device with its fingerprint (the UI's Add
  stays disabled for a device not yet paired; `unpaired` only has a label).
- C1 `held` and its approval flow.
- Admin over the mesh, ceremonies, introductions and revoke everywhere (A6).
- `POST /sessions` with staged `links` reaches the links tables, which are
  Deny on their own routes. It needs a create by a control device, at a
  trust within C3. Decide in A6/A7 whether a mesh create may stage links.
- Child-written events don't carry the device actor (above).
- LAN listening.

## Implementation notes (batch 3 backend: A6 without QR; 2026-11-19)

The backend and CLI for pairing by code, introductions, remove everywhere and
device activity. The Settings UI (A7) is built on it separately.

**Pairing by code (D6).**
- **Where the state lives: the Data Service, in memory.** Codes and the
  requests they open last minutes, so they're never on disk; a Data Service
  restart voids them. Of a code only the SHA-256 of its secret is kept: the
  host's Router makes the secret and prints it, the Mesh Service hashes what a
  joiner presents. The secret is in no event and no log. (The Mesh Service
  can't hold it: the code is made on the Router, which has no route to the
  Mesh Service, §15. The Data Service is where both already go.)
- **The code** (`pkg/paircode`): `mads-pair-1:` then base64url (unpadded) of
  `{n: host name, u: mesh_url, f: fingerprint, e: expiry, s: 32-byte
  secret}`. Whitespace anywhere is ignored (wrapped pastes). Another version
  number is refused by name (newer, older); anything else is "not a mads
  pairing code". The UI can decode it to preview the host before sending.
- **Limits:** a code works once, for 5 minutes; at most 5 are outstanding (a
  sixth retires the oldest); a request waits 5 minutes for the host's answer,
  and an answered one stays readable 10 minutes.
- **The host's address in the code** is its Mesh Service's newest
  `mesh.listening` event (new): at start the Mesh Service logs where it
  listens (MagicDNS name and port, or `-addr` as given), its fingerprint,
  platform and whether the tailnet is required. A host whose Mesh Service has
  never run can't make a code (409 `no_mesh_service`).
- **The joiner** dials the code's address over mutual TLS with its own key,
  pinned to the code's fingerprint: a different key fails the handshake
  (`host_key_mismatch`), so the secret never reaches whoever answers. It
  presents the secret, its name, its platform and (if it has a Mesh Service)
  its own address. `/mesh/pair` is the only route an unpaired key reaches,
  still behind the tailnet factor when that's required. One generic 403
  `pairing_refused` for every bad secret, audited as `device.refused`
  (debounced per key, the unpaired budget). 5 attempts per address, then one
  per 12 s; 120 overall, then one per 500 ms (round 1: the per-address
  bucket is the defence, the overall one a backstop).
- **Refused:** a code from this device itself (`pair_self`, both sides); a
  host already paired here (409 `already_paired` on the joiner, before
  dialling); a joiner already paired on the host (409 there, before the code
  is consumed). Re-pairing means removing first.
- **Approval** (`POST /devices/pair-requests/{id}/approve {level, name?}`)
  needs host authority and is never admin (422). It pins the joiner
  paired-only, `pairing.how` `code`, with the platform and address it gave
  (`remote`); a name already taken gets `-2`, `-3`.
- **The joiner learns the outcome** by asking the host's Mesh Service
  (`GET /mesh/pair/{id}`, by the same key only) every 2 s. Approved, it pins
  the host with **no grant**, `pairing.how` `code`, recording what the host
  granted as `remote.level`; it views the host if that level is at least view
  and the host's hello answers (retried for 3 s while the host's Mesh Service
  catches up with the approval). The join is in the joiner's Router's memory:
  a restart while waiting loses it.

**What a device lets this one do (D9's "Lets this … ").**
- The Mesh Service's hello reports `platform` (`runtime.GOOS`, `-platform`
  overrides for a future phone app) and `hosts_sessions`
  (`-hosts-sessions`, default true), beside the caller's levels as before.
- The viewer probes every row it views, and every paired row it doesn't view
  but can dial (a mesh address, and not known to host no sessions). The
  hello's highest level, platform and hosts_sessions are kept in memory with
  the time they were read, and written to the row's new `remote` field
  (`contracts.PeerRemote`) only when they change, or the first time a Router
  process hears them. So an offline host shows its last answer, with an "as
  of", across restarts, and a steady device writes nothing. A 403 (it
  unpaired this device, or the tailnet check failed there) reads as none; no
  answer keeps the last one. `remote.as_of` absent is "not reached yet".
  On the stored row `as_of` is when the answer last changed (the row is
  written on change); `GET /peers` and `GET /devices` overlay when it was
  last read.

**Admin over the mesh (D5).** Four narrow routes are `admin` in `authz`
(`TestLevelsReach` pins exactly these); `/peers` stays Deny.
- `GET /devices`: the host's rows.
- `POST /devices/introduce {fingerprint, name, platform, hosts_sessions,
  mesh_url, level}`: pins a device paired-only, `pairing.how` introduction,
  `paired_by` the introducer; logged as `device.introduced` (in place of
  `device.paired`) by the introducer. At most the introducer's effective
  level, never admin; 409 if the device is already paired there.
- `PUT /devices/{fp}/grant {level}`: never to admin remotely, never above the
  caller's own level; lowering anything, admin included, is fine (Q2). No
  ceilings remotely (`local_only`).
- `DELETE /devices/{fp}`.
- **Checked twice.** The Router caps by the effective level the Mesh Service
  stamps. The Data Service's peer registry checks again in the write
  transaction against the stored rows: a relayed write now carries the
  device's full fingerprint (`X-Mads-Mesh-Device`, stamped by the Mesh
  Service like the level), the device must be stored at admin, and it may
  only delete a row, create a paired-only introduction with no ceilings, or
  change a level. Admin itself stays host-only (`checkAdminGrant`).
- The introducer's side is local: `POST /devices/{fp}/introduce {host,
  level}` sends the device's name, platform and address to the host.

**Remove everywhere (D8).** `POST /devices/{fp}/remove-everywhere` removes the
row here (both sides), then calls `DELETE /devices/{fp}` on every other host
whose last known `remote.level` is admin, in parallel, 6 s each and 8.5 s
in all (under the UI's 10 s): `removed` (also when it's not paired there),
`offline` (not reached in time), `failed` (it answered with an error),
`not_admin` (it no longer lets this device administer it). Known hosts
below admin are listed as `cannot_manage`, with their last level. `{hosts:
[...]}` retries just those, the device already gone here (`here: absent`).

**Device activity (D8).** `GET /devices/activity?device=&limit=&before=` is
`device.*` (paired, unpaired, grant_changed, introduced, refused,
code_created, pair_requested, pair_declined) plus every event whose actor is
a device, newest first, filtered by device (fingerprint in `device.*`
payloads, short fingerprint in the actor, which survives a rename). Two
indexed walks merged; migration `0036` adds a partial index of device
actors. `last_used_at` on `GET /devices` is each device's newest event,
grouped by actor on read: nothing is written per request, so a device that
only reads shows as never having used the host.

**A device's message is now on record as the device's.** The session child
records the turn under the session's name, so the Router appends
`session.device_message` (sent or queued) once the child takes a relayed
message; its actor is the device. The child is also told that actor
(`X-Mads-Actor`) and none of the mesh's other stamps. A caller's own
`X-Mads-Actor` without the relay mark is dropped before the child; a local
caller that adds the relay mark itself can still set it, but that caller
holds the install token (host authority), so it gains nothing. The child itself doesn't use the
actor yet: `session.turn_ended` and the agent-state events still carry the
session id. Changing that means threading the actor through the runner's
queue (a queued message is delivered later, from another turn), which is
broader than this batch.

**C1 (D4).** `PUT /devices/{fp}/grant` accepts `full_trust_chat` allowed or
refused only (422 for held). The stored type keeps `held`, and the Mesh
Service still refuses it as `held_unsupported`.

**Routes (Router).** Local only (Deny over the mesh, and refused by the
handler if relayed anyway):

| Route | What |
|---|---|
| `GET /devices/self` | this device: name, fingerprint, short, platform, mesh_url, hosts_sessions, require_tailnet, require_tailnet_locked (below) |
| `PUT /devices/settings` | `{require_tailnet}` → the self device: "Only accept devices on my tailnet" (below) |
| `GET /devices/activity?device=&limit=&before=` | the activity log, `[]Envelope` newest first (default 100) |
| `POST /devices/pairing-code` | `{id, code, expires_at, host:{name, fingerprint, mesh_url}}` |
| `DELETE /devices/pairing-code/{id}` | retire an unused code |
| `GET /devices/pair-requests` | `[]PairRequest`: id, fingerprint, name, platform, hosts_sessions, mesh_url, remote, tailnet_node, name_collides, state, level, peer_id, created_at, expires_at, decided_at |
| `POST /devices/pair-requests/{id}/approve` | `{level: none\|view\|chat\|control, name?}` → the `PairRequest` |
| `POST /devices/pair-requests/{id}/decline` | → the `PairRequest` |
| `POST /devices/pair` | `{code}` → 202 `JoinStatus` `{id, request_id, state, level, host:{name, fingerprint, mesh_url}, expires_at, peer_id, error, self}` |
| `GET /devices/pair/{id}` | the `JoinStatus` |
| `POST /devices/{fp}/introduce` | `{host, level}` → `{host, device, level, as}` |
| `POST /devices/{fp}/remove-everywhere` | `{hosts?, also_introduced?}` → `{device, here, hosts:[{name, fingerprint, result, error, introduced_by_this, also_removed}], cannot_manage:[{name, fingerprint, level}], introduced_by_this:[{name, fingerprint}], also_removed:[{name, fingerprint}]}`; answers within 8.5 s (6 s per host) |

Also over the mesh at admin: `GET /devices` (`[]DeviceStatus`: the peer row,
`reachability` (empty for a device this node never dials), `rtt_ms`,
`access`, `checked_at`, `remote`, `last_used_at`), `POST /devices/introduce`,
`PUT /devices/{fp}/grant {level, ceilings?}`, `DELETE /devices/{fp}[?also_introduced=1]`
(→ 200 `{introduced_by_this, also_removed, absent?}`; a device no longer
paired is 404 `device_not_found`, or with `also_introduced` 200 `absent:
true`). An unknown pairing request is 404 `pair_request_not_found`, an
unknown code 404 `pairing_code_not_found`. Events
for a live UI: `peer.*` for the rows, `device.*` for the prompt and the log.
"Show sessions" stays the existing `POST /peers` (with the row's name,
`mesh_url` and `fingerprint`) and `DELETE /peers/{name}`.

**CLI.** `mads pair code`, `join [CODE] [--wait]` (stdin when no code, which
keeps it out of the process list), `requests`, `approve ID --grant LEVEL
[--name]`, `decline ID`, `remove NAME|FP [--everywhere]`, all through the
Router. The flag forms are unchanged.

**Rollout.** A host's Data Service, Router and Mesh Service go together: a
relayed peer write now needs an admin device behind it, and an older Mesh
Service sends no `X-Mads-Mesh-Device`. A viewer on an older build still
views a batch-3 host as before. Migration `0036` is an index only.

**Tests.**
- `pkg/paircode`: round trip, wrapped pastes, every malformed and
  other-version code.
- Data Service (`internal/api/pairing_test.go`, `peers_test.go`,
  `store/devices_test.go`): single use, expiry, the cap, one answer for every
  bad code byte for byte, no secret or hash in any event, approve (never
  admin, host authority only, once, name suffixes, already paired), decline,
  request expiry; the relayed-write rules; the activity log and last use, and
  that the query uses the index.
- Mesh Service (`server/pair_test.go`): hello's platform; the device header
  stamped over a spoof; join, no oracle, audit debounce; refusals before the
  store (off the tailnet, no capability, own key, already paired); rate
  limits; status by the same key only.
- Router (`mesh/pairing_e2e_test.go`): **two stacks in-process**, each a real
  Data Service, the host a real Mesh Service on a fake tailnet
  (`services/mesh/meshtest`, a fake LocalAPI socket): the whole handshake,
  reverse levels in `/peers` and `/devices`, re-pair refused both sides, the
  activity log; a code with the wrong fingerprint refused in the handshake
  without consuming it; decline; admin over the mesh (introduce capped and
  logged, raising to admin and ceilings refused remotely, lowering an admin
  allowed, `/peers` shut, control refused) and remove everywhere.
  `mesh/devices_test.go`: reverse level from hello (written once, offline
  keeps it, a refusal is none), each remove-everywhere result, local-only
  routes, C1 held refused.
- `api/device_message_test.go`: the device actor on `session.device_message`
  and to the child; none for a local send or a refused one.
- `authz`: the guard still classifies every route; `TestLevelsReach` pins
  admin's four.

**Left for later:**
- Child-written events (`session.turn_ended`, agent state) still carry the
  session, not the device (above).
- A join waiting through a Router restart is lost.
- `last_used_at` counts writes only (no per-request writes, by design).
- QR codes, phone pairing, number comparison, C1 held (D4, D6).
- Install-wide default ceilings (D9): introductions and remote raises
  tighten with them, but none exist yet.
- A row from before round 1 at none has no grant (`grant` absent), so an
  admin device can't manage it remotely; the host can.

**Round-2 review fixes (batch 3, 2026-11-19).**
- **Registry events can't be hand-written.** `POST /events` refuses
  `device.*`, `peer.*` and `mesh.*` but a service's own records under its
  own actor (`device.refused` and `mesh.listening` by `mesh`,
  `peer.reachability_changed` by `router`), and refuses all of them for a
  relayed request. The rest is the store's, written with the change.
- **Any mesh port; CGNAT.** A device's own mesh address may use any port.
  `100.64.0.0/10` is also carrier-grade NAT space: with the tailnet
  required, the Mesh Service confirms a joiner's IP is this user's tailnet
  node (WhoIs), and an introduced device must be given by MagicDNS name
  (the Router can't ask tailscaled). `TailnetSuffixOf` never answers a
  bare `ts.net`.
- **Pre-round-1 none.** A paired, unviewed row with no grant reads as a
  grant at none (`Peer.PairedForAccess`, read-time, no migration): managed
  remotely, given a grant on its first change. `view_only` is only a
  device this host views and never paired to let in.
- **Remove everywhere** carries each host's `introduced_by_this`;
  `also_introduced` removes a gone device's introductions too; it answers
  within 8.5 s.
- **Hosts nothing, rechecked.** A device that said it hosts no sessions is
  asked again every 10 minutes (if granted something here).
- **The debounce and restarts.** Answers it held back are written when
  the Router stops; the first answer after a start is written at once.
- **Pairing.** Unknown requests: `pair_request_not_found`; the approve
  answer names the device as pinned; `mads pair join --json` is complete.
- **Migration 0037** holds the last-use index (`IF NOT EXISTS`); 0036 is
  back as first shipped.
- **Rollback hazard (round 1 on):** a grant at level none (`level: ""`)
  is invalid to builds before round 1. They read it as none (no access),
  but refuse any write to that row (422) until it's changed here or
  `mads pair --revoke`d. Before rolling back, give such devices a level or
  revoke them.

**Round-1 review fixes (batch 3, 2026-11-19).**
- **M1, limits never loosen remotely.** A grant may be at level none
  (`grant.level` ""): none keeps the grant and its ceilings, locally and
  remotely, and approvals at none store one. An introduction takes the
  introducer's ceilings; a device raised remotely takes at least the
  raiser's. The peer registry enforces it on the stored rows: a relayed
  write never drops a grant or loosens ceilings, creates or raises only
  with at least the caller's, and touches only rows with a grant (a row
  this host only views is the host's: `view_only`).
- **S1, another device's address.** A joiner's or introducer's mesh
  address must be on this host's tailnet (a Tailscale address, or a plain
  MagicDNS name under its suffix) at the mesh port: 422 `bad_mesh_url`.
  A row this node doesn't view and granted nothing is never dialled.
  `GET /devices` over the mesh carries no dial errors.
- **S2, the prompt.** Requests carry the WhoIs-verified `tailnet_node` and
  `name_collides`; the joiner shows its own short fingerprint (`self`).
- **S3, introductions outlive their introducer.** Removing a device
  reports `introduced_by_this`, and `also_introduced` removes them, here
  and on each host.
- **S4.** Activity and last use read off indexes (`device.*` as a type
  range; a partial `(actor, seq)` index, also in migration 0036).
- **S5.** A device's row is written at most once a minute for a change
  that grants this node more; less is written at once.
- **S6.** Below admin, a device actor reads `device` in `/events`, and
  `mesh.*` is left out.
- **S7.** Names are cleaned wherever they come in; the store refuses
  control or formatting characters and names over 64 characters.
- **S8.** Errors carry their own code (`already_answered`, `expired`,
  `already_paired`, ...), never the internal request line or the Data
  Service's address.
- **S9.** Hello reports `grant` and `tailnet_capped`; the viewer keeps
  them as `remote.granted`, `remote.tailnet_capped`. Admin over the mesh
  needs `"admin"` in the tailnet policy (runbook).
- **Nits.** Text from another device is cleaned and capped before it's
  shown; joins are keyed by host and request (`id`, `request_id`) and
  pruned; `GET /devices` rechecks admin; `/devices/activity` answers 400 to
  a bad query; `mesh.listening` counts only from the Mesh Service; the
  platform is the hello's everywhere; local writes are actor `user`;
  remove everywhere reports hosts with no sessions as `skipped` and keeps
  the name on a retry; the Mesh Service's pairing answers use its mads
  name (`-mesh-name`); a paired device's polls aren't limited with
  strangers'; the CLI's JSON is the Router's.

## Implementation notes (A7 Settings UI; 2026-11-19)

**"Only accept devices on my tailnet" is live.**
- Stored in the Data Service as the newest `device.settings_changed`
  (`contracts.MeshSettings{require_tailnet}`), the way `mesh.listening` is
  this host's address: no table, and every change is in the device
  activity log with its actor. `GET /devices/settings` reads it, `PUT`
  writes it with host authority only (the install token, never relayed).
  The Router's `PUT /devices/settings` is local-only (Deny over the mesh).
- The Mesh Service's paired-device list follows `device.settings_changed`
  beside `peer.*` and re-reads the setting on each reload; a change calls
  `SetRequireTailnet`, which applies from the next connection and request,
  and when turned on ends forwarded requests that fail the tailnet check
  (as a lowered capability does).
- **Precedence:** an explicit `-require-tailnet` on the Mesh Service's
  command line wins (reported in `mesh.listening` as
  `require_tailnet_flag`; `GET /devices/self` says
  `require_tailnet_locked`, and the Router's PUT answers 409
  `require_tailnet_locked`). Unset, the stored setting decides; never set,
  it's on (the flag's default), and the Mesh Service starts on until it
  has read it.

## Cutover runbook (A4/A5)

This is a deliberate cutover with no plaintext fallback. Once a node runs
these binaries it can't talk to a node running an older mesh build, either
way. Do both nodes in one sitting, from where you can reach both, with SSH
to pop-os.

**What each node needs.**
- **The Mac** (the viewer): the Router from this build. A Mac Router
  restart ends the turn of any session it runs, including the one doing
  this.
- **pop-os** (the host; its checkout is old, `8c3a25b0`, from before A2):
  everything at once, all from this build: Data Service, Router, session,
  pipeline, `mads`, the Mesh Service, and `ui/dist`. A2's front door, the
  install token and the paired-devices fields come with them.
  - The Router and the Mesh Service must match: an A2 Router refuses a Mesh
    Service that sends no token.
  - Restarting pop-os's Router ends its sessions' turns. They resume on
    their next message.

**Deploying to a Linux peer.** pop-os has no Go, so build on the Mac (and
`ui/dist` with it, so both nodes serve the same UI). Everything is pure Go (the SQLite driver
is `modernc.org/sqlite`), so cross-compiling needs nothing else. The
repo's only Linux target is `make -C services/mesh build-linux`; the rest,
as before, is built by hand:

```sh
for s in data router session pipeline mesh; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/linux-amd64/$s ./services/$s
done
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bin/linux-amd64/mads ./cmd/mads
npm --prefix ui run build        # ui/dist, the app's normal build
rsync -a bin/linux-amd64/ pop-os:repo/mads/bin/
rsync -a --delete ui/dist/ pop-os:repo/mads/ui/dist/
```

`scripts/app.sh` rebuilds with `go build` and `npm` on every launch, so on a
node without Go, restart the copied binaries the way pop-os runs them
today (the root `Makefile`'s `router` target shows the Router's command
line). The Mesh Service runs under `systemd --user`
(`services/mesh/README.md`).

**Steps.**
1. **pop-os, services:** stop the old Mesh Service (`systemctl --user stop
   mads-mesh`). Deploy the binaries and `ui/dist` above. Restart the Data
   Service, then the Router. The new Data Service creates
   `~/.mads/local.token` on first start; the new Router creates
   `~/.mads/device.key`.
2. **Fingerprints:** `mads device` on each node. Note pop-os's (`POP_FP`)
   and the Mac's (`MAC_FP`).
3. **pop-os pins the Mac at control:**
   `mads pair --accept MAC_FP --name mac --grant control`
   (as the user mads runs as; it goes through pop-os's Data Service).
4. **The Mac pins pop-os on its existing viewed row**, granting it nothing
   here: `mads pair --accept POP_FP --name pop-os --grant none`. `--name`
   names the viewed, unpaired `pop-os` row, so that row gains the key.
   Without `--grant none`, pop-os would get `view` on the Mac.
5. **pop-os, Mesh Service:** start the new one (`systemd-run --user --unit
   mads-mesh …`). It logs `listening on 100.x:17380 (TLS; admits by paired
   device key and tailnet identity)` and the paired list (`mac … control`).
6. **The Mac:** restart its Router on this build.

Steps 3 and 4 need only each node's Data Service and the new `mads`, so they
can come before or after the restarts. Until step 4, the Mac shows pop-os as
`unpaired`. Until step 5, it shows pop-os as `version_mismatch`, because the
old Mesh Service speaks plain HTTP.

**Verify.**
- On the Mac: `mads api GET /peers`. Expect pop-os `"reachability":
  "connected"` and `access` `["view","chat","control"]`. If the tailnet
  policy still lists `["view","chat","create"]`, it caps the Mac at chat:
  set it to `["control"]`.
- **Admin over the mesh needs `"admin"` in the tailnet policy.** The
  capability caps the grant (the lower of the two holds), so under
  `["control"]` a device granted admin here acts as control: it can't
  introduce, change levels or remove devices remotely. For a device meant
  to administer, the policy's `access` must include `"admin"`. Its
  `/mesh/hello` says when this happens (`grant`, `tailnet_capped`), and
  the viewer's `GET /peers` and `GET /devices` carry it as
  `remote.granted` and `remote.tailnet_capped`.
- In the app, All devices: open a pop-os session, send a message, see the
  reply. On pop-os, `journalctl --user -u mads-mesh` shows no refusals.
- **An unpaired device is refused:**
  - From any machine, `curl -k https://pop-os:17380/mesh/hello` fails the
    TLS handshake (no device certificate).
  - On pop-os, `mads pair --revoke mac`. Within a probe or two the Mac
    shows pop-os `denied` ("this host hasn't paired device …"). Its open
    pop-os conversation's streams end. pop-os's log and event log show
    `device.refused` (`unpaired_device`, debounced).
  - Re-run step 3; the Mac reconnects on its next probe.

**Rollback.**
- Restore the previous binaries on both nodes together: the old Mesh
  Service (WhoIs only) with the old Mac Router. A mixed pair can't talk.
- Rows written by these builds are what batch 1 wrote, and batch-1 builds
  read them fine. Rolling the Mac back to the batch-1 build keeps its pinned
  pop-os row.
- Rolling pop-os back to `8c3a25b0` (before pairing) needs batch 1's
  warning ("Rollout: don't roll back carelessly"):
  - First, `mads pair --revoke mac` on pop-os: its `mac` row is paired-only,
    and an older build reads it as a viewed device with no URL.
  - That build also has no A2 front door. Its Data Service ignores the token.

## Open questions

- ~~**Q1.**~~ C1 defaults to **allowed** (decided 2026-11-16).
- ~~**Q2.**~~ **Admin is granted only on the host itself** (decided 2026-11-16):
  `mads pair --grant admin` from an SSH or console session as the mads user, or
  scripted (`--accept <fingerprint>`, like `authorized_keys`), or the host's own
  UI. It's never granted by introduction or remotely. Any admin may *remove* admin
  remotely, the safe direction, so revoke-everywhere still works. A compromised
  admin device can add devices up to control, all logged, but can't mint admins.
- ~~**Q3.**~~ **Yes:** A2 (the local front door) ships first, on its own, ahead of
  the mesh work (decided 2026-11-16).
- ~~**Q4.**~~ **Yes:** `chat` includes answering permission prompts (decided
  2026-11-16). So a chat-level device can approve a session's tool call; C1-held
  messages don't change that.
- ~~**Q5.**~~ **An owner-only (`0600`) key file in `$MADS_HOME` on every platform,
  for this increment** (decided 2026-11-16). This is the SSH host-key precedent:
  unattended boot works everywhere, including after a power cut, and over SSH with
  no desktop session.
  - Accepted: a leaked disk image or backup leaks the key, until revoked.
  - Deferred: keyring wrapping, TPM sealing (`systemd-creds`), and opt-in
    passphrase-at-boot.

## Decisions log

- 2026-11-15: Threat model ranked: compromised mesh device, stolen phone; hosts must
  run unattended; Tailscale is an optional transport (Drew).
- 2026-11-15: Device pairing with no login; the local app is self-paired at install;
  grants and ceilings live together in one Settings section (Drew).
- 2026-11-15: view / chat / control kept separate, after challenge: the app's
  interface is not a security boundary (Drew + Claude).
- 2026-11-16: C1 defaults to allowed; A2 ships first and alone; chat includes
  answering permission prompts (Drew).
- 2026-11-16: Admin is granted only on the host itself; device keys are owner-only
  files for now (Drew).
- 2026-11-17: A3 keeps migration `0035` and the `mesh_peers` table; the paired
  fields live in its JSON. The Mesh Service becomes a reader of paired devices
  from the Data Service (Drew).
- 2026-11-18: A4/A5 stay tailnet-only, with tailnet identity required as a
  second factor by default; the grant decides the level. The mTLS cutover is
  deliberate: no plaintext fallback. Connections to a peer are reused
  (Drew).
- 2026-11-19: Mockup review (paired-devices v3), Drew:
  - C1 is allowed or refused only; held is deferred, possibly later for
    compliance (D4).
  - Standing introduction rules are dropped: an introducer must hold admin on
    the host, introduces at most its own level, and never admin (D5).
  - Pairing is a copy/paste single-use pairing code with approval on the host.
    QR codes, phone pairing and a discoverable flow with number comparison are
    deferred (D6).
  - Settings → Paired devices is the v3 mockup (D9, "Settings → Paired devices
    (A7) UI"): levels as verbs; a device's kind detected from its platform and
    never chosen, suggesting control, chat or view; the joiner's reverse level
    starting at not use with a hint; admin never offered when pairing or
    introducing, only on the row with a confirmation, on the host itself (Q2);
    one merged Remove device…; one section-wide Device activity log, the only
    place "introduced by" appears; instant save; the tailnet-only switch under
    Advanced options; "Doesn't let this *host* use mads"; "—" for a host not
    reached yet.
  - Batch 3 is A6 without QR, plus A7; A8 (the merge) follows it.
