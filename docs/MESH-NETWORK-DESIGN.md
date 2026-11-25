# Mesh networks: reach your devices on any network they share

**Status:** Part 1 built (backend, CLI, macOS Tailscale LocalAPI: branch
`mesh-net-backend`; see "As built: Part 1" at the end, which the UI builder
follows). Part 2 (the UI) built on branch `mesh-net-ui` ("As built: Part 2"). Mockup: `docs/mockups/network-reachability-v4.html`
(the approved one; v1–v3 are in the branch's history).
**Date:** 2026-11-22, revised the same day after Drew's review of v1.
**Driver:** Drew.
**Builds on:** `docs/MESH-AUTH-DESIGN.md` (pinned-key mutual TLS, grants,
pairing codes) and `docs/MESH-MODE-DESIGN.md`. Research:
`~/mads-research/reports/Mads mesh transport options.md`.

## The problem

Mesh mode only works over Tailscale today:
- The Mesh Service asks tailscaled for this machine's tailnet IP when it
  starts, and **exits if tailscaled isn't there**. It listens on that one
  address.
- A pairing code carries **one** address (`u`, the MagicDNS name).
- Addresses that another device gives this host (a joiner, an
  introduction) must be **tailnet addresses** (`paircode.CheckTarget`).
- The tailnet check can **lower** a device's level: a device granted admin
  acts as control unless the tailnet policy also says `"admin"`
  (`authz.Min`).
- There's **no setup flow.** A new user has to know to install Tailscale,
  run a pairing ceremony, and hope.

None of the Tailscale coupling is needed for security any more. Since mesh
auth (A4/A5), a device gets in only by proving the key this host pinned,
over TLS 1.3 mutual TLS, at the level this host granted. The network only
has to carry bytes.

## Framing: grouped by how devices reach each other

mads doesn't create connectivity. It uses the user's. Work is grouped by
how two devices reach each other:

| Class | Example | When |
|---|---|---|
| **1. Your devices already share a network** | Tailscale, ZeroTier, NetBird, WireGuard, a home LAN, a cloud VPC, a server with a public address | **This increment** |
| 2. Finding devices on that network | mDNS on a LAN, Tailscale's peer list | **This increment, only while you're adding devices** (U4). NetBird's lookup and others later |
| 3. Gates on a network | Accept only from a subnet or VPC range | Later |
| 4. Public exposure | Hardening for a public port, port mapping (UPnP/PCP), learning a public IP | Later |
| 5. Reach without a shared network | A mads relay, tunnel adapters, a "run this command" adapter, hole punching | Later |
| 6. Not transport | Phone push, teams | Separate designs |

For class 1, every network looks the same to mads: **the viewer opens a TCP
connection to an address where the host listens, and pinned-key mutual TLS
does the rest.** A VPN of any brand is just an interface with an address,
so one generic "direct" adapter covers all of them. Tailscale becomes an
optional helper: it names its network, lists its peers for discovery, and
offers an optional identity check. Nothing depends on it.

**Across networks, plainly:** two devices mesh only if they share at least
one network. mads doesn't bridge networks. A VPN such as Tailscale is how
devices in different places share one: it's one network wherever they are.
So "use mads from anywhere" means "pick a VPN as your mesh network"; "use
mads at home" means "pick your home network". (A mads relay, class 5, is the
later answer for devices that share nothing.)

## What changes, in one table

| Today | With this design |
|---|---|
| No setup flow | **Set up mesh** (U1): pick how your devices reach each other, then add devices, each approved on the device itself |
| The Mesh Service needs tailscaled to start | It starts without it. It finds this machine's networks itself and asks tailscaled only for names and peers (best effort) |
| Listens on the tailnet IP only | Listens on the **mesh network**: one setting for this device, picked first (U2). Usually one network; more than one is an advanced option |
| Pairing needs a pasted code | Devices with setup open **find each other** on the mesh network, and both screens confirm a check code (U4). A pasted code stays the fallback that always works |
| A pairing code carries one address (`mads-pair-1`) | It carries a **list** of addresses (`mads-pair-2`) |
| A peer row has one `mesh_url` | It has a list of **addresses** and the **last one that worked** |
| The viewer dials that one address | It tries addresses in order, keeps the one that works, retries better ones later, and says clearly when none work |
| WhoIs is on by default and can cap a level | WhoIs is an **optional check on the Tailscale network only**. It can turn a device away. It never lowers a level |
| "Only accept devices on my tailnet" is the newest `device.settings_changed` event | Settings are a real Data Service row, `mesh_settings` |
| Another device's address must be on the tailnet | It must be an address this host could reach on a network it's on, never loopback, link-local or cloud metadata |

## What Drew's review of v1 changed

| Drew's note | Answer in this revision |
|---|---|
| **#1** What's the onboarding flow? A new user has no paired devices and hasn't decided how devices talk | **U1, Set up mesh:** the empty state leads into it; network first, then add devices, then approve on each device |
| **#2** Why show network details for every device? Nobody managing devices cares about IP addresses | The default view shows **no addresses** (U3). Status is Connected, Offline or Not reachable from this network; "via Tailscale" is only in its tooltip. Addresses live under **Troubleshoot connection**, which opens by itself only when something is wrong |
| **#3** Is this for meshing across networks? Why per-device network toggles; is that safe? Shouldn't the network be one global setting picked first? | v1's switches were **this device's** networks (where it accepts connections), never one per paired device, but they read that way. Now it's **one "Mesh network" setting, picked first** (U2). The safety question is answered directly in U2, and "across networks" in the Framing |
| **#4** Devices that can't run mads (cameras) mustn't show up as targets | **Only mads devices appear** (U4): a device is findable only while its own mads setup is open, through mDNS `_mads._tcp` on a LAN or a probe of tailnet peers. A camera never answers either |

## UX

### U1. First run: Set up mesh

**Entry points.** In Settings → Paired devices, a device with no mesh
network shows only This device and an empty state: "Use mads across your
devices. See and drive sessions on your other computers, and let them use
this one. First, choose how your devices reach each other." It has **Set up
mesh** and "I have a pairing code". Once set up, **Add devices** in the
This device bar reopens step 3. A device without a screen runs
`mads mesh setup` (step 1 as flags, then step 3 as a waiting prompt that
approves on the console).

**Step 1: How will your devices reach each other?** One question, with the
networks this device is on now as radio cards. Each card says what it means
in one line, plus who can knock:

| Choice | What it says | Who can try to connect |
|---|---|---|
| **Tailscale** (or "ZeroTier", "NetBird", "VPN (wg0)": any VPN on the machine). Tagged Recommended | "VPN · Works from anywhere your devices have internet." | "Only devices on your tailnet (VPN) can try to connect. Only devices you add get in." |
| **Wi-Fi "Kidwell-5G"** / **Wired network** (a private address on Wi-Fi or Ethernet; the Wi-Fi name where the OS gives it) | "Local network · Only while both devices are on this network." | "Anyone on this network can try to connect. Only devices you add get in." |
| A Wi-Fi that looks shared (many devices; campus or enterprise networks): tagged **Shared network** | the same | in warning colour: "A shared network: anyone on campus can try to connect. Only devices you add get in, but a VPN such as Tailscale is safer here." |
| **None of these** (always last) | "My devices aren't on the same network, or I'm not sure." | |

- A public address is never offered here. It's under Advanced options (U2).
- Tailscale is preselected when present. The MVP picks exactly **one**
  mesh network (see "One network, not several" below).
- The footnote answers "across networks": "Devices only connect over a
  network they share. To use mads away from home, pick a VPN such as
  Tailscale: it's one network wherever your devices are."
- **None of these** opens "Give your devices a network in common". It
  first says what mads does ("mads connects devices over a network they
  both share. It doesn't make one."), then tips by situation:
  - *Devices in the same place:* join the same Wi-Fi network name on both
    (wired + Wi-Fi on the same router usually works); avoid the guest
    network, which usually isolates devices; if the router shows separate
    2.4 GHz and 5 GHz network names, join the same one on both (on most
    routers both bands are one network and this doesn't matter); some
    routers have client/AP isolation turned on.
  - *Devices in different places:* install the same VPN on each device and
    sign in to the same account, with links to the install guides for
    Tailscale (tailscale.com/kb/1017/install), ZeroTier
    (docs.zerotier.com/start) and NetBird (docs.netbird.io/how-to/getting-started);
    then **Check again**.
  - Later: a mads relay.
  The same block is reused wherever "no shared network" is the answer
  (pairing by code, F2; Troubleshoot connection).

**Step 2: This device is ready.** "Your other devices can reach mads on
this MacBook-Air over Tailscale. Only devices you add get in. Anyone else on
that network can knock, and is turned away." The Mesh Service starts
listening there now. For a local network on macOS, a note says the system
may ask whether mads may accept incoming connections: choose Allow. "While
setup is open, other mads devices on Tailscale can find this MacBook-Air
(for up to 10 minutes)." Then **Add devices**, or **Finish later**.

**Step 3: Add your devices.** "Looking for mads devices on Tailscale…"
lists **only mads devices whose own setup is open** on the same mesh
network (U4). Each row shows name, platform, which network, short
fingerprint, and **Add**.
- Below the list: "Don't see it? On that device, open mads → Settings →
  Paired devices → Set up mesh (or Add devices), choosing the same network.
  A device without a screen: `mads mesh setup`."
- Then: "Only devices running mads with setup open are listed. 3 other
  devices on your tailnet (a camera, a printer, a phone without mads)
  aren't shown." The count is shown only where it's known: the tailnet
  peer list.
- "Not on the same network right now? **Use a pairing code instead**":
  today's copy/paste ceremony (D6), now carrying an address list.

**Step 4: Approve, on both screens.**
1. **Add** sends a pairing request to the found device. The adding device
   shows a **check code** (`482 913`): "Check that pop-os shows the same
   number, then approve there."
2. The found device shows "Alexs-MacBook-Air wants to join your mesh":
   name, platform, short fingerprint, "found this pop-os on Tailscale", the
   same check code ("Is this the number on Alexs-MacBook-Air? If not,
   decline: another device is asking"), and the usual level choice
   (suggested control for a computer, never admin). **Add** or **Decline**.
3. Back on the adding device: "pop-os approved. Did it show this number?"
   **Yes, the same number** pins pop-os. "No, it was different" pins
   nothing. Then the existing join result: what pop-os lets this device
   do, Show sessions, and what pop-os may do here (starting at not use).

Both screens confirm because either side could be fooled alone (U4,
"Trust").

**Order doesn't matter.** Both devices run setup. Whichever reaches step 3
second sees the first, because a device stays findable while its setup is
open (steps 2 and 3).

### U2. The mesh network: one setting for this device

**One network, not several (Drew's v2 review).** The MVP chooses exactly one
mesh network per device; the wizard and Advanced options offer no
multi-network option. Why:
- *Does several work?* Mechanically yes: Syncthing and libp2p dial a device
  at several addresses, and sessions don't care, because a device is one
  device whichever path carries it.
- *Is it safe?* Each extra network it listens on is another set of people
  who can reach the port before the key check.
- *Is it needed?* Rarely. With a VPN, both devices at home already connect
  directly over the LAN through the VPN (Tailscale goes direct when it
  can), so "LAN at home, VPN away" needs no second mesh network.
- The real case is a mix of devices where some can't run the VPN (say a
  display only on the home LAN, and a phone only on Tailscale). That's a
  later scope unit. The data model keeps a list so adding it later needs no
  migration.

The mesh network is **where this device accepts connections from your
other devices.** It's one setting for the device, picked first, never a
choice per paired device. (v1's per-network switches were the same thing,
but a list of switches next to a list of devices read as one per device.)

Settings → Paired devices shows it as one card under This device:
**Mesh network · Tailscale**, the one-line meaning and who can knock, a
state (Reachable / Not on it now / Partly reachable) and **Change…** (step
1 again). When this device isn't on its mesh network ("This MacBook-Air
isn't on Tailscale right now"), the card says what that means: your devices
can't reach it, and it can only reach devices on networks it's on.

**Advanced options** (a disclosure in the card):
- **Check Tailscale identity** (only with Tailscale chosen): "Only accept
  devices signed in to your tailnet as you." Refuse-only (S4).
- **Also reachable from the internet** (off; "Less safe" when on; a
  confirmation explains scanners and the fingerprint).
- **Port:** 17380, the same on every network.

**Remembered per network, never automatic.** Choosing "Wi-Fi Kidwell-5G"
means that Wi-Fi, recognised again after its address changes (Q3). A
network the user didn't choose is never listened on: a laptop opening
campus Wi-Fi doesn't start listening there.

**Is listening on a network safe? (Drew's question.)** Listening lets
anyone who can reach that network open a connection to mads's port. What
they get before proving a paired key:
- **That the port is open and it's mads,** and **this device's
  fingerprint**: the TLS server certificate carries the device key, and
  TLS 1.3 hides it from eavesdroppers, not from whoever connects. So
  someone who connects can tell the same laptop is on this network again
  later.
- **A bounded pre-auth surface:** a TLS 1.3 handshake that requires a
  client key, then one HTTP request that's refused unless the key is
  paired here, with connection caps, a 10 s admission deadline and audit
  budgets (S1).
- **The pairing door,** only behind a single-use 256-bit code, or while
  this device's setup is open and someone approves the request on its
  screen (U4).

**Only paired keys get in. Nothing about the network changes that.** The
difference between networks is **who can knock**:
- **A tailnet:** only your own devices (and devices someone shared into
  your tailnet) can reach the port at all. Nothing else sees it.
- **Home Wi-Fi:** your household and whatever is on it (TVs, cameras).
- **Campus Wi-Fi:** thousands of strangers and their scanners, any of whom
  can fingerprint the laptop and hammer the pre-auth surface.

That's why Tailscale is recommended, shared Wi-Fi is flagged, and the
internet is behind Advanced options.

### U3. Paired devices, day to day

The section, top to bottom:
1. **This device:** name, fingerprint chip, **Pair with a code**, **Add
   devices** (and Introduce a device…, as in v3).
2. **Mesh network** card (U2).
3. **The device table** from v3, unchanged in its columns: Device · "May …
   mads on this <host>" (the verb picker) · "Lets this <host> …" · Show
   sessions · Status.
4. **Device activity**, as v3.

**Status is one plain word, no addresses** (Drew's #2):

| Status | When | Tooltip |
|---|---|---|
| **Connected** | It answers on some network | "Connected via Tailscale · 38 ms. It last used this MacBook-Air 2 min ago." |
| **Offline** | It's on a network you share, and nothing answers | "pop-os isn't answering. It may be asleep, or mads isn't running there." |
| **Not reachable from this network** | None of its networks is one this device is on now | "pop-os uses a network this MacBook-Air isn't on right now." |
| Doesn't let this <host> use mads | As v3 | As v3 |
| Last used this <host> … | Devices that don't host mads (phones), as v3 | As v3 |

**Row details** (click a row, as v3):
- **Troubleshoot connection**: a disclosure, **closed unless there's a
  problem**, and open by itself (with the row expanded) when Status is
  Offline or Not reachable from this network.
  - It holds the reason and next steps (F4).
  - It also holds the address list: "Where this MacBook-Air looks for
    pop-os", each address with its state and last success.
  - Actions: Try again now, and Paste a pairing code… (updates addresses).
  - This is the only place addresses appear.
- Advanced options (C1, C3, fingerprint, Remove device…) and Activity, as
  v3.

### U4. Discovery: only mads devices appear

Finding devices needs **some signal before pairing**, and that reopens two
earlier decisions:
- batch 2 removed every unauthenticated endpoint (`/mesh/hello` answers
  paired devices only);
- D6 chose a pasted code over a "discoverable" flow with number comparison,
  which it deferred.

Two ways to do it:

| | **A. Always discoverable** | **B. Discoverable while you're adding devices** (recommended) |
|---|---|---|
| When a device can be found | Whenever it's on its mesh network | Only while its own Set up mesh / Add devices window is open (steps 2–3), at most 10 minutes, renewed while open; or `mads mesh discoverable` (10 min) on a headless host |
| LAN | mDNS/DNS-SD `_mads._tcp` advertised all the time | The same advert, only during the window |
| Tailscale | List tailnet peers, probe each for `GET /mesh/discover` | The same, and only devices in their window answer |
| What strangers learn | Name, platform and fingerprint of every mads device, on every network it listens on, all the time (on campus Wi-Fi: "Alexs-MacBook-Air is here") | The same, but only for minutes, and only while you're at the device setting it up |
| Pairing requests from strangers | Possible at any time: a stream of prompts on the host, including unattended ones | Only during the window: while someone is looking at the screen, and up to 10 minutes after |
| Friction | Open Add devices on one device | Open setup on both (the onboarding does this anyway) |

**Recommendation: B.** Discovery only fills in the name and address of a
device you're actively adding. The trust step stays a person's decision on
the host, confirmed on both screens. Outside the window, mads is as quiet
as today. This revisits D6's deferred discoverable flow: Drew should
confirm it. The pasted code remains, unchanged, as the fallback that always
works (headless hosts, devices not on the same network now, or a check
code that doesn't match).

**How B works:**
- **Finding (LAN):** while the window is open, the Mesh Service advertises
  `_mads._tcp` on each mesh network that's a LAN. TXT: `v=1`, `n=<name>`,
  `p=<platform>`, `fp=<short fingerprint>`; the port is the SRV record's.
  The adding device browses `_mads._tcp`. Cameras, printers and TVs don't
  advertise `_mads._tcp`, so they never appear. On iOS later, the browse
  needs `NSBonjourServices` and the Local Network prompt.
- **Finding (Tailscale):**
  - the adding device lists tailnet peers (`tailscale status --json`, as
    the Router's `discover.go` already does);
  - it probes each online peer at the mesh port with `GET /mesh/discover`,
    with a 1.5 s timeout and at most 16 at once;
  - only devices in their window answer `{mads: 1, name, platform,
    fingerprint, mesh_api}`. Anything else (no listener, not mads, mads
    outside its window) is dropped silently. "N other devices on your
    tailnet aren't shown" is the count of the rest.
- **Other VPNs:** ZeroTier carries mDNS (it bridges multicast), so it works
  like a LAN. NetBird and plain WireGuard don't: there, devices pair with a
  code for now (their peer-list helpers are later, class 2).
- **`/mesh/discover`** is the only unauthenticated route, and only during
  the window:
  - outside the window it doesn't exist: same answer as any unpaired key,
    `403 unpaired_device`, connection closed;
  - the TLS handshake is unchanged, and the finder presents its own device
    key;
  - it's rate-limited like `/mesh/pair` and audited as `device.discovered`
    (debounced).
- **Requesting:** **Add** dials the found device, pinned to the
  fingerprint it just announced, and `POST /mesh/pair {mode: "discovered",
  name, platform, addresses, nonce}`. This is accepted only while the
  target's window is open: at most 3 pending at once, the same per-address
  limits as codes. No secret is presented.
- **The check code:** 6 digits from SHA-256(host fingerprint ‖ joiner
  fingerprint ‖ both nonces), shown on both screens.
- **Trust** (why both screens confirm):
  - Someone else on the network during the window could send a request
    too. The host's user sees a different check code, and declines.
  - Someone could answer discovery pretending to be pop-os and approve
    automatically. The joiner's user sees no prompt on the real pop-os, or
    a different code, and answers "No, it was different". Nothing is
    pinned until the joiner confirms.
  - This is Bluetooth-style numeric comparison. The pasted code's
    guarantee (the fingerprint came from the host's own screen) is
    stronger, which is why the code stays for anyone who prefers it.
- **Approval** is the existing pair-request prompt, plus the check code.
  `pairing.how` is `discovered`, and Device activity shows "found on
  Tailscale and approved".

## Fewer clicks, and Tailscale sign-in (Drew's v3 review)

**Setup is four clicks between two of your Tailscale devices:** on pop-os
**Set up with Tailscale**; on the MacBook **Set up with Tailscale**, then
**Add** next to pop-os; on pop-os **Approve**. How:
- **The network step collapses when the answer is obvious.** With a VPN
  detected, the empty state's primary button is "Set up with Tailscale"
  (secondary: "Other networks…", "I have a pairing code"). In the wizard,
  a detected VPN shows one "Use Tailscale?" card with **Set up** and "Other
  options".
- **No "This device is ready" screen.** Choosing the network goes straight
  to "Add your devices", led by one line: "This MacBook-Air is reachable on
  Tailscale. Only devices you add get in." The wizard has two steps.
- **Tailscale vouches for both ends, so there's no number to compare.** The
  number check exists because a stranger on the same network could pretend
  to be pop-os. On a tailnet, each side asks its own tailscaled (WhoIs) about
  the other's address; when both answer "same tailnet user, not tagged", the
  device is one of yours. The host's prompt says "Tailscale confirms
  MacBook-Air is signed in to your tailnet as you", and the joiner pins the
  host as soon as it's approved (no "Did it show this number?").
  - It's a Tailscale **helper**: optional, only when the pairing actually ran
    over Tailscale and both WhoIs answers agree. On a LAN, campus Wi-Fi or
    any VPN without an identity API, the number check stays.
  - Accepted risk: anything signed in to your tailnet as you can ask to
    pair without a number check. The host user still approves each request
    and its level, and a device that isn't yours shouldn't be signed in as
    you; if it is, the tailnet is already compromised.
- **Better defaults:** the approval preselects the suggested level;
  "Show sessions" starts on when the other device is a computer that grants
  this one something; the joiner's reverse level stays "not use", with the
  suggestion beside it.

**Tailscale sign-in (key expiry) stops being a silent failure.** When a
machine's Tailscale key expires (180 days by default) or it's logged out, it
drops off the tailnet and every other device just saw "Not reachable". mads
can't sign a remote machine back in (the tailnet was the path to it), so the
remedy is prevention plus a clear diagnosis, all as Tailscale helpers:
1. **Prevent, at setup and on the Mesh network card:** the device reads its
   own key expiry from tailscaled's LocalAPI and says "Tailscale signs this
   pop-os out on Mar 3. For a machine you reach remotely, turn off key
   expiry", with a link to the Tailscale admin console (Tailscale's own
   advice for servers).
2. **Warn ahead, on every device:** `/mesh/hello` reports
   `tailscale_key_expiry` (additive field). Within 7 days, the viewer's row
   reads "Connected · Tailscale sign-in expires in 5 days" and Troubleshoot
   opens with how to turn off key expiry, while the machine is still
   reachable.
3. **Sign-in prompt on the device itself:** when tailscaled reports
   `NeedsLogin`, the Mesh network card shows "Tailscale needs you to sign
   in again" with **Sign in to Tailscale**, which opens the login URL the
   LocalAPI gives.
4. **Diagnosis on the viewer:** a device that stopped answering over
   Tailscale after its last reported expiry reads "Probably signed out of
   Tailscale" (not a generic "Offline"), with the fix: sign in on that
   machine (`sudo tailscale up` prints a link you can open from any device),
   and turn off key expiry so it doesn't recur.
5. **A real fallback path** (staying reachable through a Tailscale outage)
   needs a second path: the multi-network or relay scope units, not the MVP.

## The command line (headless hosts)

Fresh installs listen nowhere until setup, so a machine without a screen (a
server, the DGX Spark) sets up from a terminal, usually over SSH. The CLI is
the same flow as the UI, under one verb: `mads mesh`. (The batch-3
`mads pair …` commands stay as aliases.)

**Interactive setup, then a GUI device adds it** (the usual case):

```
$ mads mesh setup
How will your devices reach each other?
  1) Tailscale  (recommended)  works from anywhere your devices have internet
  2) Wired network (eth0)      only while both devices are on this network
  3) None of these
Choose [1]: 
✓ This spark is reachable on Tailscale. Only devices you add get in.
✓ Tailscale key expiry is off for this spark.        (or, if it's on:)
! Tailscale signs this spark out on Mar 3. For a machine you reach
  remotely, turn off key expiry: https://login.tailscale.com/admin/machines

Waiting for your other devices (up to 10 minutes; Ctrl-C to stop).
On a device with mads open: Settings → Paired devices → Add devices.

Alexs-MacBook-Air (macOS) wants to join your mesh.
✓ Tailscale confirms it's signed in to your tailnet as you.
What may it do with mads on this spark?
  [n]ot use  [v]iew  [c]hat with  c[o]ntrol (suggested)
Choose [o]: 
Control lets it start Full access sessions, run a shell, and commit, land
and push on this spark. Add it? [y/N] y
✓ Added Alexs-MacBook-Air. It may control mads on this spark.

Waiting for more devices… (Ctrl-C when done)
^C
Done. `mads mesh status` shows your devices.
```

On a network without vouching (a LAN), the prompt also shows the check
number ("Is 482 913 on Alexs-MacBook-Air too? [y/N]") before the level.

**Recommended for a headless machine: pair one introducer, then introduce.**
Run `mads mesh code` on the server once, paste it on your main device (the
Mac) and approve it in the server's terminal at *administer* (admin is only
ever granted on the host itself). From then on the Mac adds your other
devices to the server with **Introduce a device…**, without logging in to the
server again. Codes stay single-use with a 5-minute expiry (Drew,
2026-11-22: no multi-use or pre-approved codes).

**From the headless side, adding a device that's already open:**
`mads mesh add` lists mads devices found on the mesh network and adds the
chosen one (that device then approves on its own screen).

**Scripted / non-interactive:**

| Command | Does |
|---|---|
| `mads mesh setup --network tailscale [--yes]` | Choose the mesh network without prompts (`--network lan:eth0`, `--network none` to stop listening) |
| `mads mesh status` | This device's mesh network and its state (incl. Tailscale sign-in/expiry), and each paired device: what it may do here, what it lets this device do, status ("Connected", "Probably signed out of Tailscale"…) |
| `mads mesh add [name]` | Discover and add a device (interactive pick without a name) |
| `mads mesh approve [--level control] [--yes]` | Wait for and approve one request (for scripts; refuses `admin`) |
| `mads mesh code` / `mads mesh join <code>` | Pairing code, both directions (join is joiner-first: it adopts the network it reached the host over) |
| `mads mesh set <device> --level view` / `mads mesh remove <device> [--everywhere]` | Same as the row actions |
| `mads mesh address add <device> <host:port>` | Manual address (was `mads mesh address add`) |

Every prompt has a flag, and every interactive command exits non-zero with a
one-line reason when it can't proceed (e.g. "This spark isn't on any network
it can share. See: mads mesh setup --help").

## Under the hood: pairing and connecting

### F2. Pairing by code (the fallback)

**A pairing code doesn't create connectivity.** It carries the host's
addresses and proves the host's key; the joiner still has to share a network
with one of those addresses. So "I have a pairing code" on a device with no
mesh network yet is a **joiner-first** flow:

- paste the code; mads checks which of the code's networks this device is
  on right now;
- if one is: "This MacBook-Air can reach pop-os over Tailscale. Pairing
  makes that this MacBook-Air's mesh network." **Ask to pair** pairs over
  it and sets this device's mesh network to that network (toast: "Your mesh
  network is now Tailscale");
- if none is: "pop-os is reachable over Tailscale (and its home network).
  This MacBook-Air isn't on it right now", followed by the U1 "network in
  common" tips, and the button becomes **Check again**. The code isn't
  used up.

A joiner that only dials doesn't need to listen at all. It listens on its
mesh network only so the other device can reach it back (for example to
show its sessions there).

The steps, for both directions:

1. **Copy code** makes a `mads-pair-2` code carrying every address this
   device listens on now (its mesh networks), in its preferred order. The
   dialog says which network ("how to find this MacBook-Air (on
   Tailscale)"), not the addresses. With no mesh network yet, Copy starts
   Set up mesh instead.
2. **Paste code** previews the host and the networks it's reachable on.
3. The joiner tries the addresses as in F3. The first one that completes
   the pinned TLS handshake carries the pairing request. When none works,
   it shows the same failure as F4 ("isn't reachable from this network"),
   and the code isn't used up.
4. Both kinds of request, code and discovered, carry the joiner's **own**
   address list (was one `mesh_url`). The host checks each one (S3).
5. Approval is unchanged. Both rows store the addresses they were given and
   the one that worked.
6. **Pasting a code from a host that's already paired here** no longer
   fails with `already_paired`. It offers **Update addresses**, because the
   code's fingerprint matches the pinned key. That's the manual fix after
   a host's address changes (F5), and Troubleshoot connection links to it.
   It doesn't use up the code.

### F3. The viewer connects

Each peer row keeps a short list of candidate addresses. To connect (on
start, after a failure, or when this device's own networks change):
1. **Filter.** Skip any address that isn't on a network this device is on
   right now (S2). Under Troubleshoot it reads "This MacBook-Air isn't on it
   now". Nothing is dialled for it, so failure is fast.
2. **Order.** The last address that worked goes first. The rest go by
   network: same LAN (10), then VPN or tailnet (20), then public (30), then
   in the order the host listed them. A relay will be 50 later.
3. **Race, staggered** (happy eyeballs, RFC 8305). Start the first. Start
   the next 250 ms later without cancelling the first, and so on. **The
   first one to finish the TLS handshake with the pinned key wins.** A
   plain TCP connect isn't success: on a different network the same
   private address may belong to someone else's machine. The others are
   cancelled.
4. **Timeouts:** 3 s per attempt (TCP plus TLS), 6 s for the whole race.
   The probe keeps its 4 s budget for the hello itself on a working
   connection.
5. **Keep it.** The winner becomes the peer's one HTTP/2 connection, as
   today (`connKey` = pin@address). The row's `last_good` is updated, at
   most once a minute unless the address changed.
6. **Upgrade later.** While connected on an address that isn't the best
   candidate, try the better ones every 60 s in the background. If one
   answers twice in a row, new requests move to it. The old connection
   stays until its streams end or 10 minutes pass. The UI already reconnects
   its event streams when they end.
7. **Re-race at once** when the current address fails a probe, or when
   this device's own networks change (the Mesh Service's detector, F5). No
   waiting out a backoff.

The row's Status says only **Connected**. The path in use is in its
tooltip ("Connected via Tailscale · 38 ms"). Troubleshoot connection lists
every address with its state (in use, works, not on a network this device
is on, not answering, another device answered) and its last success (U3).

### F4. When nothing works

Two different failures, worded differently. Both open the row's
Troubleshoot connection by themselves:

| What happened | State | Status | Message and next steps |
|---|---|---|---|
| No address is on a network this device is on | `no_shared_network` (new) | **Not reachable from this network** | "**pop-os isn't reachable from this network.** pop-os uses Tailscale. This MacBook-Air is on Wi-Fi eduroam right now. Devices only connect over a network they share." Then: turn on Tailscale here (if pop-os uses it); or, if pop-os uses only its home network, put the same VPN on both and add it under Mesh network on pop-os; or go back to a network pop-os uses; later, a mads relay. |
| Addresses were tried, none answered | `unreachable` (as today) | **Offline** | "pop-os isn't answering." Then: is it awake with mads running; is its firewall letting mads in (port 17380); does this Wi-Fi block devices from reaching each other (common on campus and hotel Wi-Fi); did its address change (paste a new code from it, F5)? |

A candidate where **another device answered** (the pin didn't match) counts
as "not answering" for that address, not as `denied` for the peer. It reads
"A different device answered at 192.168.1.20". `denied` stays for a device
that answered as itself and refused.

### F5. Networks change

| What changed | What happens |
|---|---|
| **The host's DHCP address changes** (pop-os .20 → .42 on the same Wi-Fi) | The Mesh Service sees it on its next scan (every 10 s), keeps the choice (same network), moves the listener to .42 and reports it. Viewers that still reach pop-os some other way (Tailscale) **learn .42 from pop-os's hello** within a probe, and switch to it if it's better. A viewer that only knew .20 can't reach it: "Offline", and Troubleshoot suggests pasting a fresh pairing code from pop-os (F2.6) or `mads mesh address add`. A DHCP reservation on the router avoids it. (Q13: whether hosts should also announce themselves to their paired devices on the LAN outside setup.) |
| **The laptop moves networks** (home → campus) | As a viewer: its own networks changed, so it re-races at once. The home address is now filtered out; Tailscale wins if both have it; otherwise "Not reachable from this network". As a host: it listens only on its chosen mesh networks, so campus Wi-Fi is never listened on. If its only mesh network is home Wi-Fi, the Mesh network card says it isn't on it now (U2). |
| **Tailscale stops or signs out** | The Mesh network card says "This MacBook-Air isn't on Tailscale right now". Listeners on other chosen networks are untouched. The Mesh Service keeps running. |
| **The host changes its mesh network** (Mesh network → Change…) | Its listening set changes, and viewers learn it from the next hello, if they can still reach it; otherwise they need a fresh pairing code from it. |

**How viewers learn new addresses:** the host's `/mesh/hello`, already
served only over pinned TLS to paired devices, gains `addresses`: where
this host listens now, with each network's kind and label. The viewer
merges them into the row (S2's rules still apply):
- addresses the host reported are replaced by the newest report;
- addresses this device's user added are kept until removed;
- `last_good` is kept even if the host stops reporting it, but ranks after
  the reported ones.

The row is written only when the list changes.

### F6. Half-paired devices

A pairing is two rows, one on each device: "I trust this key, I let it do
X, and maybe I view it." Removing on one side, a reply lost mid-pairing,
or a wiped Data Service can leave only one of them. Before this section,
that broke silently:

- **This device's row only** (the Mac has pop-os; pop-os has nothing): the
  Mac shows pop-os as paired while pop-os refuses every request
  (`unpaired_device`). A code from pop-os only offers "update its
  addresses", which never asks pop-os.
- **The other device's row only** (pop-os has the Mac; the Mac has
  nothing): the Mac can't pair again (`already_paired`, `pair.go`) and
  shows nothing to say why. pop-os's row still lets the Mac's key in.

The rules:

**R1. Removal is mutual when it can be.** Remove device (and `mads mesh
remove`) first asks the device being removed to forget this one, then
deletes the row here whatever the answer:
- `POST /mesh/forget` (Mesh Service, any paired caller, any level, none
  included): the host's Data Service deletes the caller's row (`POST
  /pairing/forget {fingerprint}`, Mesh Service only), recorded as
  `device.unpaired` with `by: "itself"`, and the caller's live streams
  end. Answers `204`; `unpaired_device` if it wasn't paired there (also
  success for the caller).
- The Router calls it before its own delete, within 5 s. The remove result
  gains `there: "removed" | "not_paired" | "offline" | "failed"`, and the
  dialog says it: "Removed on pop-os too", or "pop-os didn't answer: it
  may still list this MacBook-Air until it hears from it".
- Forgetting needs nothing from the host's user: it only takes away the
  caller's own access.

**R2. A pairing request from a key already paired here re-pairs it.**
`already_paired` is gone from `/mesh/pair`. The request goes through the
same approval as a new one (code or discovered, check code included); the
prompt says "Alexs-MacBook-Air wants to pair again. It's paired here now
(control); approving replaces that." Approval updates the existing row in
place: same id and name, the level just chosen, the request's addresses,
`pairing.how` from this request. Declining leaves the old row as it was.
Nothing weakens: that key could already connect, and a code or the host
user's approval is still required.

**R3. Checking a pairing.** The Router asks a paired device whether it has
this device paired, over pinned TLS, and classifies the answer:

| Answer | Means |
|---|---|
| `/mesh/hello` 200 | `paired` |
| `403 unpaired_device` | `not_paired` (that device loaded its list and this key isn't on it) |
| `503 devices_unavailable`, tailnet refusals, timeouts, no address | `unknown`, with the reason |
| A pin mismatch | `other_key`: a different key answered (a reinstall?), never re-paired without a code or check code |

A `not_paired` for a row paired less than 30 s ago is read as `unknown`
(the other device's Mesh Service follows new pairings from its event log
after a short lag). The result is kept in memory with `checked_at`, shown
on `GET /devices` rows as `pairing_there`. It's checked:
- by the existing probe loop, as now;
- at once, for every paired row, when Paired devices opens (`POST
  /devices/check`, local), when Add devices opens, and before a code is
  used.

**R4. What `not_paired` does depends on the row:**
- **A row this device views** (it uses the other device's sessions): kept.
  Status "No longer paired on pop-os"; the row offers **Re-pair**, which
  re-pairs it in place: same row, name, viewed flag and the level this
  device gives it; only the other side is new. Re-pair opens Add devices
  if the device is found there, else Pair with a code.
- **A grant-only row** (the other device used this one; this one doesn't
  view it): removed automatically, with an activity line ("Removed
  Alexs-MacBook-Air: it no longer has this pop-os paired") and
  `device.unpaired` with `by: "reconcile"`. It only takes away access
  that the other device already gave up. Only a pairing the other device
  was once seen to have is undone (review round 1): the row's
  `remote.paired_there_at` holds the `paired_at` a hello was answered
  with, and must equal the row's current one. An introduction (the
  introduced device has no row for this one until it adds it), a
  discovered pairing whose joiner hasn't confirmed its check code yet,
  and a re-pairing since are never removed this way.

**R5. Discovery says who's half-paired.** `/mesh/discover`'s answer gains
`paired_with_you` (the asking key only; the TLS handshake says which). The
found list's `paired: bool` becomes `pairing: "none" | "both" | "here_only"
| "there_only"`, and Add devices shows:

| `pairing` | Shown |
|---|---|
| `none` | **Pair** (as now) |
| `both` | **Paired ✓**, no button |
| `here_only` | **Re-pair**, "pop-os no longer has this MacBook-Air paired" |
| `there_only` | **Re-pair**, "pop-os still has this MacBook-Air from an earlier pairing" |

Re-pair is the same button style as Pair, with the reason in the warning
color (green stays "healthy" everywhere in this UI).

**R6. A code from a device already paired here** is checked first (R3):
`paired` offers "Update its addresses" as now; `not_paired` offers
**Re-pair** (R2 on the other side, R4's in-place update here).

**R7. Join retries.** A code is single-use *per key*: the Data Service
keeps a redeemed code's hash, the joiner's fingerprint and its request id
for the request's lifetime (5 min), and the same key presenting it again
gets that request back (`200`, its current state) instead of
`pairing_refused`. So a reply lost after the host took the request can be
retried without leaving a half-pair. The joiner sorts failures:

| Failure | Shown | Retry |
|---|---|---|
| Couldn't connect, timed out, connection reset | "Couldn't reach pop-os" | **Try again** (same code) |
| `devices_unavailable`, `pairing_unavailable` | "pop-os's mads isn't ready" | **Try again** |
| `rate_limited` | "Too many tries" | **Try again** after `Retry-After` |
| `pairing_refused` (expired, another key used it), declined | as now | No: a new code |
| Pin mismatch | "A different device answered" | No |
| Tailnet identity refusal | as now, with why | No |

**Also:** a row with no address reads "pop-os has no address for this
device: pair again, or `mads mesh address add`", not "doesn't host mads
sessions" (`hostsSessions()` in `model.ts`).

**Wire shapes (F6):**

| Where | Change |
|---|---|
| Mesh `POST /mesh/forget` | Any paired caller, any level. `204`; `403 unpaired_device` if not paired. Calls the Data Service's `POST /pairing/forget`. |
| Data `POST /pairing/forget {fingerprint}` | Mesh Service only (as `/pairing/redeem`). Deletes that fingerprint's row (paired and viewed sides); `device.unpaired` with `by: "itself"`. `204`, or `404 device_not_found`. |
| Mesh `/mesh/pair` | No `already_paired`. The request (code or discovered) is taken as usual. |
| `contracts.PairRequest` | `+ repair: {peer_id, level}` when the joiner's key is already paired here (its row and current level). Approval updates that row in place (same id and name; the chosen level; the request's addresses; `pairing.how`). |
| Data `/pairing/redeem` | The same secret and fingerprint again, within the request's lifetime: `200` with that request (whatever its state). Another fingerprint: `pairing_refused` as now. |
| Mesh `GET /mesh/discover` | `+ paired_with_you: bool` (the asking key). |
| Router `GET /devices` rows | `+ pairing_there: "paired" \| "not_paired" \| "unknown" \| "other_key"`, `+ pairing_there_reason`, `+ pairing_checked_at`. Absent until checked. |
| Router `POST /devices/check` | Local. Checks every paired row now (at most ~6 s overall). `200 {devices: DeviceRow[]}`. Grant-only rows found `not_paired` are removed (R4) before answering. |
| Router `DELETE /devices/{fp}`, `POST /devices/{fp}/remove-everywhere` | Forget on the device first (5 s). The answer gains `there: "removed" \| "not_paired" \| "offline" \| "failed"`, `+ there_error`. |
| Router `GET /devices/discovered` | Devices `+ pairing: "none" \| "both" \| "here_only" \| "there_only"` (`paired` stays: a row exists here). |
| Router `POST /devices/discovered/{fp}/request` | Allowed for a device already paired here: approval updates the row in place. |
| Router `POST /devices/pair {code}` | A code from a device already paired here is checked first: `paired` gives `409 already_paired_update` (as now); otherwise `409 repair_offered {peer_id, name}`. `{code, repair: true}` joins, and approval updates the row here in place (same id, name, viewed flag, the level this device gives it). |
| Router join errors | `extra.retryable: true` (plus `retry_after` seconds, when rate-limited) for: couldn't connect, timeout, `devices_unavailable`, `pairing_unavailable`, `rate_limited`. |
| Router `GET /devices/pair-requests` | Items carry `repair`. The prompt says "wants to pair again … approving replaces that". |
| CLI | `mads mesh remove` prints `there`. `mads mesh status` shows "no longer paired there". `mads mesh approve` says it's a re-pair. `mads mesh join` answers `repair_offered` with `[r]e-pair / [c]ancel`. |

**Not covered:** a device that can't reach the other in either direction
stays `unknown`; nothing reconciles until one reaches the other or the
user re-pairs. A host can't announce new addresses to a viewer that can't
reach it (F5).

**As built (F6, backend):** the wire table holds as written; where the
code had to choose, it chose this:
- `POST /pairing/forget` is restricted as `/pairing/redeem`'s neighbours
  are: a relayed request (`X-Mads-Mesh-Peer`) is `403 forbidden`. (The Data
  Service has no caller identity finer than the token; `/pairing/redeem`
  itself checks nothing more.)
- A code presented again by the key that used it gets `200` from the Data
  Service; the Mesh Service still answers `/mesh/pair` with `201` (its one
  success shape), and the Router accepts either.
- Forget-before-delete (R1) is for this device's own user only. A
  `DELETE /devices/{fp}` relayed by a device administering this host (and
  so each host of remove-everywhere) deletes without asking, and has no
  `there`. In remove-everywhere the forget runs alongside the hosts'
  removals, inside the same budget; the row here goes once it has answered.
  A pin mismatch during forget is `failed`; no key pinned is `offline`.
- A re-pairing approved on the host (R2) keeps the row's name (an approver's
  typed name is ignored), its ceilings and the addresses its user added;
  `device.paired` carries `repair: true` and `previous_level`, and the
  pairing record is stamped afresh. The joiner's in-place update (R6, and
  discovered `here_only`) keeps user-added addresses too. Undoing a
  re-pairing for a check-code mismatch still deletes the row, as for a new
  one.
- R3's 30 s guard counts from the row's `pairing.paired_at` or this
  Router's own pinning/approval, whichever is later. `pairing_there_reason`
  is blank on `GET /devices` relayed to an admin device.
- R4 removals run from `POST /devices/check` (every paired row) and from the
  probe loop (the rows it already probes: grant-only, level above none,
  with an address). The check before a code is used records the answer but
  removes nothing. `repair_offered` is given for every answer but `paired`
  (`unknown` and `other_key` included), as the table says "otherwise".
- R5: a found device that doesn't send `paired_with_you` (an older build)
  reads as `both` when paired here, unless this device's last check said
  `not_paired`.
- Right after a pairing (R3's guard, applied twice more): a found device
  paired here less than 30 s ago reads `both`, not `here_only`; and a host
  doesn't dial a device whose request it approved (probe loop, `POST
  /devices/check`, which reads it `unknown`) for 30 s, so the joiner, not
  yet pinned, records no `device.refused` for a pairing that worked.
- CLI: `mads mesh join --repair` answers `repair_offered` without a
  terminal; a `retryable` failure ends "try the same code again (in Ns)".
  Stopped waiting, a code join says the request is still with the host
  (approved there, it completes here); there's no route to cancel it.
- R7: a code presented again whose request is declined or expired answers
  that state at once (`POST /devices/pair`, still `202`); an approved one
  still goes through the poll that pins. The host logs it as presented
  again, not "waiting for approval".
- R7's "Couldn't reach {name}" is also Pair with a code's `host_unreachable`
  answer (its tips underneath, **Try again**); no shared network keeps
  **Check again**.
- R2: the host's prompt asks to confirm control only when it raises the
  re-paired row's current level (`repair.level`).

## The adapter interface

The goal: a small, constrained interface, like the Provider adapters, so
later units add adapters without touching TLS, grants or the UI. **mads owns
the TLS handshake, the candidate lists, the ranking and the address
policy.** An adapter only supplies byte streams and facts about networks.

New package `pkg/meshnet`, used by the Mesh Service (detect, listen) and the
Router (dial):

```go
// Kind is what sort of network an address is on. It decides the risk
// label and the dial priority.
type Kind string // "overlay" | "lan" | "public"

// Network is one place this device can be reached, as detected now.
type Network struct {
	ID       string         // stable across restarts and address changes: "tailscale", "lan:<subnet>/<gateway>"
	Kind     Kind
	Label    string         // "Tailscale", "Wi-Fi", "VPN (ZeroTier)", "Public IPv6"
	Iface    string         // "en0", "tailscale0"
	Addrs    []netip.Addr   // what a listener binds
	Prefixes []netip.Prefix // what counts as "on this network", for S2
	Adapter  string         // which adapter offers it: "direct"
}

// Candidate is one address another device may try for this one. Codes,
// hellos and peer rows carry lists of these. An unknown Via is skipped,
// never fatal, so a newer device's relay candidate doesn't break an
// older one.
type Candidate struct {
	Via   string // adapter id; "direct" is the only one in this increment
	Addr  string // "192.168.1.20:17380", "pop-os.tail4c2e.ts.net:17380"
	Kind  Kind
	Label string
}

// Adapter carries bytes. It never sees TLS, keys' meaning or grants.
type Adapter interface {
	ID() string
	Detect(ctx context.Context) ([]Network, error)            // cheap; polled every 10 s
	Listen(ctx context.Context, n Network, port int) ([]net.Listener, error)
	Advertise(n Network, ls []net.Listener) []Candidate
	Dial(ctx context.Context, peer devicekey.Fingerprint, c Candidate) (net.Conn, error)
}

// Helper adds facts about networks another adapter found. Optional.
type Helper interface {
	Describe(ctx context.Context, n *Network) // e.g. confirm Tailscale, add its name
}

// Finder lists other mads devices that are discoverable on a network
// right now (U4). Optional. It only suggests: a found device still has to
// complete the pinned handshake, the check code and the host's approval.
type Finder interface {
	Find(ctx context.Context, n Network) (<-chan Found, error)
}
type Found struct {
	Name, Platform string
	Fingerprint    devicekey.Fingerprint // as announced; pinned for the request only
	Candidates     []Candidate
}

// Gate is an optional check on one network's connections. It can only
// refuse. There is no "admit" and no level in its answer.
type Gate interface {
	Check(ctx context.Context, n Network, remote netip.AddrPort) (refuse string, err error)
}
```

**What ships in this increment:**
- `direct`: interface detection (`net.Interfaces`), plain TCP listen and
  dial. It covers LAN, VPC, public addresses and every VPN installed on the
  OS.
- `tailscale` as a **Helper** (confirms a 100.64.0.0/10 interface is
  tailscaled's, adds its MagicDNS name), a **Finder** (lists tailnet peers
  and probes `/mesh/discover`) and a **Gate** (the WhoIs check).
- `mdns` as a **Finder** for LAN networks (browses `_mads._tcp`), plus the
  advertiser the Mesh Service runs during a discoverable window. Proposed
  library: `github.com/grandcat/zeroconf` or `hashicorp/mdns`, pure Go.
  NetBird also uses 100.64.0.0/10, so the range alone never says "Tailscale".
  Today's `services/mesh/internal/tailnet` moves behind these two.

**Seams left for later units, with nothing built:**
- `Dial` takes the peer's key, so a key-routed relay can route (class 5).
- `Listen` returning listeners lets a relay adapter "listen" by registering
  outbound, as Syncthing's relays do. A `Reach` capability (inbound,
  outbound-registered, dial-only) gets added then, for the UI's wording.
- `Candidate.Via` with unknown values skipped lets `relay` and `cmd`
  candidates ride in codes and hellos before every device understands them.
- `Finder` is where NetBird's lookup and other peer lists join later
  (class 2). Discovery is never trusted: the pin, the check code and the
  host's approval decide.
- `Gate` is where "only this subnet" goes (class 3).
- A `Passthrough` capability must be true for any adapter: mads refuses
  adapters that end TLS in the middle (some tunnels do).

## Data model

### The peer row (`contracts.Peer`, JSON in `mesh_peers`, no SQL migration)

```go
Addresses []PeerAddress `json:"addresses,omitempty"`
LastGood  *PeerPath     `json:"last_good,omitempty"`

type PeerAddress struct {
	Candidate                   // via, addr, kind, label
	Source   string     `json:"source"` // code | hello | user | joiner | introduced | legacy
	AddedAt  time.Time  `json:"added_at"`
	LastOKAt *time.Time `json:"last_ok_at,omitempty"`
}
type PeerPath struct {
	Addr string    `json:"addr"`
	At   time.Time `json:"at"`
	RTT  int64     `json:"rtt_ms,omitempty"`
}
```

- **Read-time migration**, the way `viewed` absent reads as true: a row
  with no `addresses` reads as one `legacy` candidate made from `mesh_url`.
  Nothing is rewritten until the row next changes.
- **`mesh_url` stays**, kept equal to `last_good` (or the first address) in
  its `http://host:port` form. The Data Service still validates it, and an
  older build reading the row still finds a working address.
- **Store rules:** at most 8 addresses; each is `host:port` and passes the
  address policy's shape checks (S2); a viewed row needs at least one
  address (it needed a `mesh_url`).
- **Live state isn't stored.** Which address is in use, and each address's
  state, are the Router's memory, as reachability is. `GET /peers` and
  `GET /devices` add `path {addr, kind, label, rtt_ms, since}` and
  `addresses[].state`.

### The host's network choices: a real settings row

The architecture review called "newest event as setting" debt. This design
fixes it for mesh settings:

- **New table `mesh_settings`** (migration `0038`): one row (`id = 1`),
  JSON `data`, `updated_at`, the shape of `config`. Not inside `config`,
  because profiles snapshot config (MESH-MODE-DESIGN D3) and switching
  profiles mustn't change where the machine listens.
- `contracts.MeshSettings` becomes:

  ```go
  type MeshSettings struct {
  	Networks []NetworkChoice `json:"networks"`
  	Port     int             `json:"port,omitempty"` // default 17380
  	RequireTailnet *bool `json:"require_tailnet,omitempty"` // read once by the migration, then unused
  }
  type NetworkChoice struct {
  	ID           string    `json:"id"`              // Network.ID
  	Name         string    `json:"name,omitempty"`  // the Wi-Fi name, or one the user gives
  	Listen       bool      `json:"listen"`          // chosen as a mesh network
  	TailnetCheck bool      `json:"tailnet_check,omitempty"` // only on the Tailscale network
  	DecidedAt    time.Time `json:"decided_at"`
  }
  ```

- `PUT /devices/settings` writes the row with host authority only, as
  today, and appends **`device.settings_changed` in the same transaction**
  (the outbox pattern the registries use). The activity log keeps its
  history. The Mesh Service follows the event and re-reads the row.
- **Migration `0038`** creates the row from the newest
  `device.settings_changed`: `require_tailnet` true, or never set, becomes
  `tailscale: {listen: true, tailnet_check: true}`; false becomes
  `{listen: true, tailnet_check: false}`. No other network is on. So an
  upgraded machine is already "set up" with Tailscale as its mesh network,
  listens exactly where it did, and checks what it checked.
- **A fresh install has no mesh network** (`networks` empty): the Mesh
  Service runs but listens nowhere, and Settings shows the Set up mesh
  empty state (U1). That's a change from today, where a fresh install
  listened on the tailnet as soon as it started.
- **A network not in the row is never listened on.** Only the user's
  choice (Set up mesh, Change…, Advanced options) writes it. The Mesh
  Service never writes this row. It only reads it.
- **The discoverable window isn't a setting.** It's in-memory state on the
  Mesh Service. The Router opens it (`POST /devices/discoverable`, renewed
  every minute while the setup dialog is open, at most 10 minutes without
  renewal) through a `device.discoverable` event: the same Data Service
  path the Mesh Service already follows. It ends when the dialog closes or
  times out.

### What the Mesh Service reports

What it found and where it actually listens is status, not a setting, so it
stays an event, like `capability.health_changed`:
- **`mesh.listening`** is appended at start **and whenever the set
  changes** (an address moved, a network came or went, a port failed),
  debounced to one per 5 s. Its payload gains `networks: [{id, kind, label,
  iface, addrs, listening, error, changed_from}]`. The old fields stay.
- The Router's `GET /devices/self` merges the newest report with the
  settings row into what Set up mesh and the Mesh network card show.
  Pairing codes are made from it.

### Events

| Event | Change |
|---|---|
| `device.settings_changed` | Payload is the new `MeshSettings`. Written with the row |
| `mesh.listening` | Also written on change. Payload gains `networks` |
| `peer.reachability_changed` | Payload gains `via {addr, kind, label}`, and its `to` can be `no_shared_network`. Still debounced 10 s. A path change while connected (LAN → Tailscale) is logged too: "Now reached via Tailscale (was home network)" |
| `peer.updated` | Already fires when a hello changes a row's addresses |
| `device.discoverable` (new) | The window opened, renewed or closed: `{until}`. The Mesh Service follows it |
| `device.discovered` (new) | This device answered a discovery probe, or got a discovered pairing request (debounced per address). Audit only |

### Router routes (all local only, Deny over the mesh)

| Route | Change |
|---|---|
| `GET /devices/self` | Adds `mesh: {networks: [{id, kind, title, meaning, who_can_knock, shared, chosen, available, listening, error}], port, tailnet_check, public, discoverable_until}`. `require_tailnet` stays for one release |
| `PUT /devices/settings` | Takes `{networks: [ids], tailnet_check?, public?}`. Turning on the internet needs `confirm_public: true` |
| `POST /devices/discoverable` | `{on}`: opens (or renews) or closes this device's window (U4) |
| `GET /devices/discovered` | Found mads devices on this device's mesh networks: `[{name, platform, fingerprint, network, paired}]`, plus `hidden_count` where known (tailnet peers that didn't answer). Starts a search, which runs while the dialog polls |
| `POST /devices/discovered/{fp}/request` | Sends the discovered pairing request: `{id, check_code}`. Its status is `GET /devices/pair/{id}` as for codes, plus `POST /devices/pair/{id}/confirm {match}`: the joiner's "Yes, the same number" |
| `GET /devices`, `GET /peers` | Each row adds `path` (shown only in the Status tooltip) and `addresses[]` with `state` and `last_ok_at` (shown only under Troubleshoot connection); reachability may be `no_shared_network` |
| `POST /devices/{fp}/addresses` | `{addr}`: add an address (source `user`); used by `mads mesh address add`, with no UI in this increment |
| `DELETE /devices/{fp}/addresses/{addr}` | Remove an address this device's user added |
| `POST /devices/{fp}/redial` | Try again now: re-race at once |
| `POST /devices/pair` | Answers `{update_addresses: true, peer_id}` (200) for an already-paired host's code, when the body says `update_addresses: true`; otherwise 409 `already_paired_update` with the host's name, so the UI can offer it |

## Pairing code v2

```
mads-pair-2:<base64url, unpadded, of {n, f, e, s, a}>
a: [{"v":"direct","h":"100.101.4.7:17380","k":"overlay","l":"Tailscale"},
    {"v":"direct","h":"192.168.1.20:17380","k":"lan","l":"Home"}]
```

- `u` is gone. `a` holds 1 to 8 candidates, in the host's preferred order.
  `l` is shown cleaned and capped (`paircode.CleanText`).
- **Parse accepts 1 and 2.** A v1 code reads as one `legacy` candidate from
  `u`. A version above 2 is refused by name, as now.
- An unknown `v` is skipped. A code with no usable candidate is "not a mads
  pairing code (it has no address this mads can use)".
- `maxLen` stays 4096. Eight addresses with labels fit with room to spare.
- An older mads given a v2 code says "from a newer mads: update mads here".
  That message exists today, so the failure is clear.
- Addresses in a code are visible to anyone who sees it, until it expires.
  The dialog lists them, so nothing is hidden. Hiding addresses (a
  relay-only code) comes with the relay.

## Security

### S1. What listening on a network exposes (U2 says it in plain words)

Anyone on that network can reach the port. What they get, before proving a
paired key:

| They can | Already limited by |
|---|---|
| Open TCP connections | 64 at once, 8 per address, closed unless a paired device is admitted within 10 s (`server.Listen`) |
| Start a TLS handshake | TLS 1.3 only, an Ed25519 client certificate required, 10 s header timeout |
| Send HTTP with a throwaway key | Refused per request (`unpaired_device`), the connection closed, audit debounced and budgeted |
| Try a pairing secret | 256-bit single-use secret, 5 minutes; 5 tries per address then 1 per 12 s, 120 overall; one generic answer |
| Probe `/mesh/discover`, or send a discovered pairing request | Only while this device's setup window is open (S6) |
| See that mads runs here, and this device's public key (the server certificate) | **Not covered.** See below |

**Added in this increment:**
- **Limits per listener.** Each network gets its own 64-connection budget,
  so a flood on the café Wi-Fi can't use up the slots your tailnet devices
  need. Today's single budget would let it.
- **A cool-down for failed handshakes.** An address with 10 failed
  handshakes (no certificate, wrong key type) in a minute is closed at
  accept for 60 s. It's generous on purpose: many devices share one address
  behind CGNAT.
- **Never a wildcard bind.** Each chosen network binds its own addresses.
  `refuseWildcard` stays.
- **Choosing the mesh network first** (U2) is the main defence. A device
  listens only on networks its user picked, with a VPN recommended and
  shared Wi-Fi flagged, so most networks never see the port.

**Known gap (post-merge, with network switching):** deselecting a network
closes its listener, not the connections already accepted on it
(`netloop.Scan`): a paired device connected over it keeps its connection
and streams until they end. Only paired keys are involved. It belongs to
the network-switching design work (see "After the merge").

**Accepted:** anyone who can reach the port learns this device's key
fingerprint from its certificate. TLS 1.3 encrypts the certificate from
eavesdroppers, but not from whoever connects. That makes a laptop
trackable across networks where it listens. It's why a network is never
listened on unless chosen, and why shared Wi-Fi is flagged. Revisit with
public exposure (class 4).

### S2. Which addresses this device will dial

There are more ways to give a device an address now: codes, hellos,
joiners, introductions, and the user. A malicious peer or code mustn't be
able to make this device open connections into places it shouldn't
(SSRF). The policy lives in one place, `meshnet.DialPolicy`, and is checked
**at dial time, against the IP actually connected** (a `net.Dialer.Control`
hook). A DNS name can't sneak past it by resolving to something else.

**Always refused:** loopback, unspecified, link-local (169.254.0.0/16,
fe80::/10, which includes cloud metadata at 169.254.169.254), multicast,
broadcast, cloud metadata over IPv6 (fd00:ec2::254), IPv4-mapped forms of
those, and ports below 1024 other than 443.

**Allowed only on a network this device is on:** private ranges
(10/8, 172.16/12, 192.168/16, fc00::/7) and 100.64.0.0/10 must fall inside
a prefix of one of this device's own interfaces. A VPN satisfies this,
because the device has an interface on the VPN's range. A home address
seen from campus doesn't.

**Public addresses** are allowed: that's the internet, reachable by
definition.

**One exception:** an address this device's own user adds
(`mads mesh address add`) may be private and outside its prefixes. That
covers a VPC peer in another subnet reached through a route. It's the
user's own choice, not another device's.

The filter also does ranking work (F3 step 1). Tests and the e2e harness
allow loopback with a flag that refuses to start outside tests, like
`-insecure-local`.

### S3. Addresses another device chooses for this host

This replaces `CheckTarget`'s "must be on my tailnet":
- A joiner's and an introducer's addresses are checked against S2's shape
  rules when they arrive (422 `bad_address`), and against all of S2 at
  every dial.
- A row this host doesn't view and granted nothing is still never dialled
  (S1 of batch 3).
- `GET /devices` over the mesh still carries no dial errors, so a remote
  admin can't use this host to probe ports and read the results.
- The CGNAT caveat (100.64.0.0/10 is also carrier space) is handled by the
  prefix rule. The Router's "give the MagicDNS name, not an address" rule
  goes.

### S4. "Only accept devices on my tailnet" becomes a per-network check

- It's the `tailscale` Gate, **applied only to connections that arrive on
  the Tailscale listener.** A connection on the home Wi-Fi isn't a tailnet
  connection, so a tailnet check there would only ever refuse. If you want
  "tailnet only", make Tailscale the only mesh network.
- **Refuse only.** It passes for a node owned by this host's tailnet user
  that isn't tagged. It no longer reads the app capability at all.
- **On by default**, new installs too (decision 2026-11-22): choosing
  Tailscale as the mesh network turns it on. Upgrades keep what they had
  (Data model, migration `0038`).
- It runs where it runs today: at the connection's first read, before the
  handshake, and re-checked on the WhoIs cache's clock. A transient
  tailscaled failure keeps the last good answer for three TTLs, then fails
  closed, as now.
- `-require-tailnet` given on the command line still wins and locks the
  switch. It now means "the Tailscale network's check is on". It no longer
  limits where the service listens.

### S5. Unchanged

`/mesh/hello` is still behind pinned TLS and answers paired devices only. The pairing secret still never reaches
a device that fails the pin. There's no plaintext fallback. Trust is never
transitive.

### S6. Discovery (U4)

- **One unauthenticated route, and only for minutes.** `/mesh/discover`
  and discovered pair requests exist only while the window is open. It's
  opened by a person at the device (the setup dialog, or `mads mesh
  discoverable` at a console), never remotely, and lasts at most 10 minutes
  without renewal. Outside it, a probe gets the same `unpaired_device` as
  any stranger.
- **What a stranger learns during the window:** name, platform and
  fingerprint, from the mDNS advert or the probe. That's the fingerprint
  they could already get by connecting (S1), plus the name.
- **Spoofed requests:** another device on the network can send a request
  during the window. The host shows its check code, which won't match the
  one on your device. At most 3 are pending at once, and the per-address
  limits of `/mesh/pair` apply.
- **Spoofed hosts:** a device that answers discovery as "pop-os" with its
  own key, and approves automatically. The joiner pins nothing until its
  user confirms the code matched the real pop-os's screen. The real
  pop-os shows no prompt, or a different code.
- **Unattended hosts** can't be discovered unless someone opens the window
  at the console, so a server never prompts for strangers.
- **Comparison with the code:** the code's fingerprint comes from the
  host's own screen, so it doesn't depend on a person comparing six
  digits. It stays available everywhere, and the "No, it was different"
  path points to it.

## The WhoIs change and what follows

Dropping `authz.Min(grant, capability)`:
- **A device's level is its grant.** The admin-over-the-mesh runbook step,
  "the tailnet policy's `access` must include `admin`", goes away. So does
  the confusing case where Settings says administer and the device acts as
  control.
- `Hello.TailnetCapped` and `remote.tailnet_capped` stay in the contract,
  always false, for one release. The UI's `cappedLine` and Introduce's
  "held below by the tailnet policy" text are removed.
- **The tailnet policy needs nothing from mads.** The app capability
  `github.com/drewgitsit/mads/cap/mesh` can stay or go. Removing it
  changes nothing. (MESH-MODE Q2, the capability's domain, is moot.)
- **What's lost:** a tailnet admin can no longer lower a mads device's
  level from the Tailscale policy. Mesh auth already moved that decision to
  the host, and a tailnet admin can still cut a device off with the
  network itself.
- **Revalidation** of open requests by WhoIs runs only for requests that
  came in on the Tailscale listener. A level change from the policy no
  longer exists to watch for.
- **Pair requests** still show `tailnet_node` when the request came in on
  the Tailscale listener and tailscaled answered. It's labelled as what
  Tailscale says, not a check.

## Protocol and compatibility

- **`MeshAPI` stays 2.** Every wire change is additive: `addresses` in
  hello; `addresses` beside `mesh_url` in `/mesh/pair` and
  `POST /devices/introduce`; `mode: "discovered"` in `/mesh/pair`, and the
  new `/mesh/discover`. Older readers ignore unknown JSON fields.
- **Discovery needs both ends on this build.** An older device neither
  advertises nor answers `/mesh/discover`, so it isn't found. Pair it with
  a code, which works across versions.
- **Old viewer, new host:** works on whatever address the old viewer
  stored, as long as the host still listens there. Migration `0038` keeps
  the Tailscale listener on, so Drew's stored MagicDNS URLs keep working.
- **New viewer, old host:** one `legacy` candidate, dialled as today. The
  old hello has no `addresses`, so nothing changes.
- **Codes:** new reads v1 and v2. Old refuses v2 by name.
- **Introductions:** a new introducer sends `addresses` and `mesh_url`. An
  old host reads `mesh_url` and applies its tailnet rule, as today.
- **Deploy each host's Data Service, Router and Mesh Service together**, as
  for batch 3. Migration `0038` is a new table only.
- **Rollback:** older builds ignore `mesh_settings` and read the newest
  `device.settings_changed` (whose payload keeps `require_tailnet` for
  that). Their full-replace peer writes drop `addresses`, `last_good` and
  `remote.paired_there_at` (which only turns F6's reconcile off), but
  `mesh_url` survives, so they keep working over the tailnet. **Pairs made
  over a LAN stop working on an older build:** their `mesh_url` is a LAN
  address, and an older Mesh Service listens on the tailnet address only
  (and exits without tailscaled). Re-upgrading after `0038.down`
  re-derives Tailscale as the mesh network even if a LAN was chosen.

## Migrating Drew's two machines

1. **Build and deploy** as in the A4/A5 runbook: cross-compile for pop-os,
   rsync `bin/` and `ui/dist`, restart its Data Service, Router and Mesh
   Service (`systemctl --user restart mads-mesh`). Then the Mac. Either
   order works. Every device runs a Mesh Service, the Mac included: it's
   how other devices (a phone, too) reach it. `scripts/app.sh` builds and
   starts it with the Data Service and Router, and stops it with them.
   There's one per mads home: it holds `$MADS_HOME/run/mesh.lock` with its
   pid, a second one is refused, and `app.sh` finds a running one there
   (reused if it runs from the same checkout, stopped by `--restart`).
2. **Expect no setup screen and no visible change.** Migration `0038`
   makes Tailscale each machine's mesh network, with pop-os's check on,
   since that's what it ran with. Settings shows the Mesh network card, not
   the Set up mesh empty state. The Mac's pop-os row reads its MagicDNS
   `mesh_url` as a legacy candidate, connects, and the first hello adds
   pop-os's reported addresses. Status reads "Connected" (tooltip "via
   Tailscale"). The Mac now runs a Mesh Service too (`app.sh` starts it)
   and listens on its tailnet addresses, so pop-os can reach it back.
3. **Verify the tailnet** (the acceptance's first half): `mads api GET
   /peers` on the Mac shows pop-os `connected`, `path.kind` `overlay`;
   open a pop-os session and send a message; `journalctl --user -u
   mads-mesh` on pop-os shows no refusals.
4. **Try the LAN** (the second half), at home. A device listens on one
   mesh network, and a viewer learns a host's new addresses only from a
   hello it can still reach (F5), so give the Mac pop-os's LAN address
   first: on the Mac, `mads mesh address add pop-os <pop-os LAN IP>:17380`.
   Then on pop-os, Settings → Paired devices → Mesh network → Change… →
   its wired network. If `ufw` is on, allow 17380/tcp from that subnet.
   Within a probe, the Mac's Status tooltip reads "via home network".
   Run `tailscale down` on the Mac: it stays connected via the home
   network, which shows the Mac's dial path needs no tailscaled. Bring it back up, and switch pop-os back to Tailscale (the
   Mac still has pop-os's Tailscale addresses from earlier hellos). macOS
   may ask to allow Local Network access for the Mac's mads binaries.
5. **Optional:** remove `"admin"` (or the whole mads capability) from the
   tailnet policy. Turn off pop-os's Tailscale check if you don't want it.
6. **Try discovery** (new for both): on each machine, Paired devices → Add
   devices. Each lists the other as Paired, and nothing else on the
   tailnet.

## Tests and acceptance

**Unit:**
- `pkg/meshnet`:
  - detection from fake interface tables: Tailscale vs NetBird on
    100.64/10, VPN names, bridges hidden, IPv6 public vs ULA vs link-local;
  - network identity stable across a DHCP change;
  - `DialPolicy`: every refused range, IPv4-mapped forms, a DNS name
    resolving to loopback refused at dial, the prefix rule, the user-added
    exception;
  - ranking order;
  - the staggered race with fake dialers: the winner is the first pinned
    handshake, not the first TCP connect; a pin mismatch at one address
    goes on to the next; total timeout.
- `pkg/paircode`: v2 round trip, v1 still read, unknown `v` skipped, zero
  usable candidates refused, the 8-candidate cap, a v3 refused by name.
- Data Service: the `mesh_settings` row and `device.settings_changed` in
  one transaction; migration `0038` from true, false and never set (and a
  fresh install with no networks); host authority only; the address store
  rules.
- The check code: the same six digits on both sides from the same inputs,
  different for any change in either fingerprint or nonce.

**Mesh Service:**
- starts with no tailscaled;
- listens only on chosen networks, never a wildcard;
- moves its listener on an address change (fake detector) and reports it;
- a flood on one listener leaves another's budget free;
- the handshake cool-down;
- the Tailscale gate runs only on its own listener and never changes a
  level (a device granted admin under a policy of `["control"]` acts as
  admin);
- hello's `addresses`;
- a fresh install listens nowhere until a mesh network is chosen;
- `/mesh/discover` and discovered pair requests: refused outside the
  window (the same answer as any unpaired key), answered inside it, closed
  at expiry, rate-limited, at most 3 pending;
- the mDNS advert appears and disappears with the window, only on LAN
  mesh networks.

**Router:**
- merging hello addresses (reported addresses replaced, user ones kept,
  last-good kept);
- `last_good` written once per change;
- `no_shared_network` vs `unreachable`;
- the upgrade loop moves new requests and leaves streams alone;
- re-racing when this device's networks change;
- an already-paired host's code updating addresses;
- S3's `bad_address`;
- discovery over Tailscale with a fake peer list: only peers that answer
  as mads are listed, and the rest are counted;
- a discovered pairing pins nothing until the joiner confirms the code,
  and "No" pins nothing.

**Two stacks in-process** (`pairing_e2e_test.go`): two loopback addresses
(127.0.0.2 and .3) as two "networks", with the test-only loopback flag.
Set up both, find each other with a fake Finder, approve with the check
code, then drop one network and watch the viewer move to the other.

**UI** (vitest and a Playwright journey, from the v2 mockup):
- the empty state leads into Set up mesh;
- step 1 shows only networks this device is on, plus None of these;
- the found list shows only devices the API returns;
- Status shows no addresses, and the tooltip names the path;
- Troubleshoot connection opens by itself only on a problem.

**Acceptance (Drew):**
- The Mac and pop-os work over the tailnet exactly as today, and the same
  build works with pop-os's mesh network switched to its LAN instead
  (one network per device), the Mac reaching it at an address it was
  given (`mads mesh address add`, or a fresh code) with Tailscale down on
  the Mac (migration steps 3 and 4).
- On a fresh install pair (two users' accounts or a VM), Set up mesh →
  Add devices → approve gets two devices meshed without typing or pasting
  anything.

## After the merge (decided at the architecture review, 2026-11-25)

The review is at `/tmp/mesh-net-arch-review.md` (not committed). Drew
chose to merge with the small items done and these three areas next:

1. **Network switching** (a design area, not urgent). Switching the mesh
   network between VPNs and LANs when several are available, and the LAN
   fallback when the VPN is down. Design the UX in Settings, and what
   happens to live sessions working over a network the user switches
   away from (guardrails, warnings, a grace period, or a second network
   kept during the move). It includes the known gap in S1 (deselecting a
   network doesn't close its accepted connections), how a viewer follows
   a host to a new network (F5 today: only through a hello it can still
   reach), and identifying static-IP devices on a LAN.
2. **HTTP/2 safety** (mid priority). Assert the negotiated protocol
   end to end (the two-stack test; the e2e stand-in Mesh Service refusing
   HTTP/1.1), and either `go 1.27` in `go.mod` or a Go 1.26 CI job.
3. **Responsibilities and failure modes** (soon after the merge, before
   more is built on top). Who owns what between the Router and the Mesh
   Service, and what each failure does:
   - the Router's call to its own Mesh Service (`alive.go`), against the
     same-node rule;
   - the two network detectors (Router and Mesh) that must agree on IDs,
     and the settings rules enforced in Data, Router and Mesh;
   - the adapter interface plumbed through the Router's dial side (today
     a concrete `*meshnet.Direct`, `Via: direct` hard-coded on redial,
     the Finders in the Router package);
   - the Router `Manager` (31 fields) split into viewer, pairing and self;
   - the pairing state machine spread over Data, Mesh, Router and CLI;
   - the Mesh Service writing to Data, told apart only by "not relayed".

Smaller follow-ups from the review: one `GrantLevel` rank helper (five
copies), one address cap, Router reachability consistent with address
states (then drop the UI's `effectiveReachability`), `/peers/candidates`
on the LocalAPI client, OS-independent fixture tests for the detectors and
the LocalAPI owner check, and the flaky `TestReverseLevelFromHello` (about
1 in 50, older than F6).

## Out of scope, and where it goes

| Not in this increment | Later unit | Seam left |
|---|---|---|
| Finding devices outside setup: hosts announcing themselves to their paired devices on a LAN (Q13), NetBird's and other VPNs' peer lists | 2. Network helpers | `Finder`; `PeerAddress.Source` |
| Accept only from a subnet or VPC range | 3. Network gates | `Gate` |
| A hardened public listener (bans tuned for scanners), UPnP/PCP/NAT-PMP port mapping, learning a public IP behind NAT (STUN) | 4. Public exposure | `Kind` public, the per-listener limits, "Anyone on the internet" off by default |
| A mads relay, tunnel adapters (ngrok TLS, Funnel `--tcp`, frp), a `cmd://` adapter (`ssh -W`, `tailscale nc`) | 5. Reach without a shared network | `Candidate.Via`, `Dial(peer key)`, `Listen` as registration, `Passthrough` |
| Hole punching | 5, inside the relay adapter | Not in the interface, on purpose |
| Phone push; teams | Separate designs | None needed here |
| An Add address… UI (the CLI has it), IPv6 privacy-address choice, QUIC and moving a live connection between paths | Later polish | The addresses route, the upgrade loop |
| QR codes and phone pairing (D6) | The phone app | The pairing request already carries a mode |

## Open questions (with recommendations)

- **Q1. Should the Tailscale check stay on for upgraded installs?**
  (Superseded: Drew chose on by default for every install; see the
  decisions log.) *Recommend yes*: migration `0038` keeps what each
  machine ran with (on for Drew), and new installs start off. Turning it off is one switch.
- **Q2. Should the check still require the app capability?** *Recommend
  no*: same user and untagged is enough for a refuse-only check, and it
  means the policy needs nothing from mads. The cost: an enterprise
  tailnet admin loses an "allow mads" switch. The capability could come
  back later as an opt-in Gate.
- **Q3. How is a LAN network recognised again?** *Recommend* its subnet
  plus the gateway's IP and, where the OS shows it (Linux
  `/proc/net/arp`, macOS `route`/`arp`), the gateway's hardware address.
  Two networks with the same subnet and gateway IP (both `192.168.1.1`)
  read as the same network where the hardware address can't be read. That
  risk is accepted: the pin still decides who gets in.
- **Q4. Discovery: A (always discoverable) or B (only while adding
  devices)?** *Recommend B* (U4). It reopens D6's deferred discoverable
  flow and adds the one time-limited unauthenticated route. Drew to
  confirm. If he declines both, Add devices becomes "copy the code on one,
  paste on the other" again, and only #1–#3 of his review change.
- **Q5. Should another device's private address outside this device's
  prefixes ever be dialled?** *Recommend no* (S2). Only the user's own
  `mads mesh address add` may do that. Revisit with network gates for VPCs.
- **Q6. Should a LAN listener accept only callers from its own subnet?**
  *Recommend not in this increment*: that's a network gate (class 3). The
  pin already decides who gets in.
- **Q7. Should hello report addresses to a device granted nothing?**
  *Recommend yes*: it's a paired device, and it needs the addresses to
  show "Lets this host …" and to work once it's granted something.
- **Q8. LAN before Tailscale when both work?** *Recommend yes* (Syncthing's
  order), switching only after two good probes so a flaky Wi-Fi doesn't
  flap.
- **Q9. Should pasting an already-paired host's code update its
  addresses?** *Recommend yes* (F2.6). It's the simplest manual fix for a
  changed address. The alternative is a separate "address code".
- **Q10. One port for every network?** *Recommend yes*: 17380, set once in
  `mesh_settings.port`. Different ports per network add UI for no benefit
  in this class.
- **Q11. How often to scan for network changes?** *Recommend every 10 s*,
  plus at once on a listener error. Go has no portable change
  notification. Netlink (Linux) and the route socket (macOS) can replace
  polling later.
- **Q12. What happens to open streams when the path changes?** *Recommend*:
  new requests go to the new path; the old connection closes when idle or
  after 10 minutes; streams that end are reopened by the UI, as they are
  already.
- **Q13. Should a host announce itself on the LAN to its paired devices
  outside setup,** so a DHCP change heals itself? For example, an mDNS
  advert whose name is a keyed hash only paired devices can match.
  *Recommend later* (class 2). For now, a DHCP reservation or a pasted
  code. Tailscale addresses don't change, so Drew's setup doesn't need it.
- **Q14. How does mads tell a shared Wi-Fi from a home one, for the
  warning?** *Recommend*: well-known names (`eduroam`, and ones containing
  "guest", "public", "hotel" or "airport"), a subnet larger than /22, or
  more than 32 hosts seen in the ARP table. It's only a warning: a wrong
  guess changes the wording, not what's allowed.
- **Q15. The Wi-Fi name (SSID) for labels:** Linux gives it freely; macOS
  needs Location permission since 14. *Recommend*: ask for it only when
  Set up mesh opens, and fall back to "Wi-Fi network" plus a name the user
  can give.
- **Q16. More than one mesh network per device:** *decided, not in the
  MVP* (see "One network, not several").

## Decisions log

- 2026-11-23 (build, Part 1): backend, CLI and the macOS Tailscale LocalAPI
  built; the decisions the doc left open are under "As built: Part 1"
  (who migration 0038 treats as upgraded, the settings API refusing a
  second network, vouching from both ends, every found device checked over
  TLS, the loopback switch for tests, and others).

- 2026-11-23 (Drew): `mads mesh setup` waits for devices until Ctrl-C or
  10 minutes (the discovery window), never indefinitely; `mads mesh approve`
  handles exactly one request (idempotent for scripts); single-letter
  prompts with Enter accepting the suggestion. Build approved, in two parts:
  (1) backend + CLI + macOS Tailscale LocalAPI, (2) UI from mockup v4.

- 2026-11-22 (Drew): the Tailscale identity check is ON by default (new
  installs too); mads never reads or requires anything from the tailnet
  policy (no app capability), only "same tailnet user, not tagged". For
  always-on machines recommend Disable key expiry over tagging (tagged nodes
  have no user and are refused by the check; "also accept tagged devices"
  is a later option). The macOS Tailscale app's LocalAPI must be supported
  in the MVP (the Mac becomes a host); without it the check fails closed.
  Headless setup via `mads mesh …` is in scope and gets its own walkthrough.
  Discovery (the setup window) gets the same adversarial review as pairing
  codes. Pasting a paired device's code updates its addresses (key must
  match). One port, 17380. Old questions on new-network defaults and LAN vs
  VPN preference are obsolete with one mesh network.

- 2026-11-22 (Drew, v3 review): setup is 4 clicks between Tailscale devices
  (Set up with Tailscale, no ready screen, Tailscale vouches instead of the
  number check); Tailscale key expiry gets prevention (turn off key expiry),
  early warnings via hello, an on-device sign-in prompt, and a "Probably
  signed out of Tailscale" diagnosis. Mockup v4.

- 2026-11-22 (Drew, v2 review): one mesh network per device in the MVP (no
  multi-network UI); one "Add devices" entry point; dialogs say "Close", not
  "Stop"; "None of these" gives situation tips with links to the VPN install
  guides; "I have a pairing code" is joiner-first and adopts the network it
  paired over. Mockup: v3 (in history; v4 supersedes it).

- 2026-11-22: Scope is organized by how devices reach each other. This
  increment is "your devices already share a network": a VPN of any brand,
  LAN, VPC and public IPs all look the same, a TCP connection to where the
  host listens. Tailscale is a helper, not load-bearing. Networking is
  pluggable adapters behind one small interface, like Providers (Drew).
- 2026-11-22: The MVP (Drew):
  - the host listens on networks the user chooses from a detected list,
    with plain risk labels;
  - the Mesh Service starts without tailscaled;
  - codes carry a list of addresses, as a new version;
  - the viewer races candidates, keeps what works, upgrades later, and
    fails clearly;
  - WhoIs is an optional per-network check, off by default (superseded
    2026-11-22: on by default), refuse only;
    the capability cap is dropped;
  - acceptance: Drew's Mac and pop-os keep working over the tailnet and
    also work over a LAN.
- 2026-11-22: Drew's review of the v1 mockup:
  - onboarding comes first (Set up mesh);
  - the network is one setting for the device, chosen first, not a
    per-device toggle;
  - the device list shows no addresses (status only; Troubleshoot
    connection when something's wrong);
  - only devices running mads may appear as targets.
  The v2 mockup and U1–U4 follow from these. Discovery option B is
  recommended and awaits Drew's decision (Q4).

## As built: Part 1 (backend, CLI, macOS LocalAPI)

Built on branch `mesh-net-backend` (2026-11-23). Part 2 (the UI from mockup
v4) builds on the routes below; the UI is otherwise unchanged (the old
"Only accept devices on my tailnet" switch still works through the legacy
`require_tailnet` form).

### Where the pieces are

| Piece | Code |
|---|---|
| Adapter interface, direct adapter, detection, S2 dial policy, F3 ranking and race | `pkg/meshnet` |
| Tailscale LocalAPI client (Linux + macOS discovery), Helper | `pkg/meshnet/tailscale`; fakes in `tailscaletest` |
| mDNS advert and browse (`_mads._tcp`) | `pkg/meshnet/mdns` (on `golang.org/x/net/dns/dnsmessage`, the only new dependency) |
| Pairing code v2, check code | `pkg/paircode` |
| `mesh_settings` row, migration 0038, discoverable window, discovered requests | `services/data` |
| Network loop (detect, listen on chosen networks, move, report, mDNS) | `services/mesh/internal/netloop` |
| Per-network gate, budgets, cool-down, `/mesh/discover`, discovered pairing, hello addresses | `services/mesh/internal/server` |
| Viewer paths, race, upgrade, hello merge, discovery, pairing v2, new routes | `services/router/internal/mesh` (`dial.go`, `paths.go`, `discovery.go`, `pairing.go`, `selfmesh.go`) |
| `mads mesh` | `cmd/mads/mesh.go` |

### How the macOS Tailscale app's LocalAPI is found

Without it the Mac can't run the identity check (it fails closed) and can't
host. `pkg/meshnet/tailscale.Locator` mirrors tailscale's own client (its
`safesocket` package: `LocalTCPPortAndToken`, then the default socket), in
the same order:

| Tailscale on this machine | Where the LocalAPI is | How mads finds it |
|---|---|---|
| Linux `tailscaled` | unix socket `/var/run/tailscale/tailscaled.sock` (else `/run/tailscale/…`), mode 0666 | stat the path |
| macOS app's bundled CLI | `$TS_MACOS_CLI_SHARED_DIR/sameuserproof-<port>-<token>` (set by the app for its CLI) | read the directory |
| macOS **standalone** app (from tailscale.com, "macsys"; Drew's Mac) | TCP `127.0.0.1:<port>`; `/Library/Tailscale/ipnport` is a **symlink whose target is the port number** (for example `ipnport -> 61234`); `/Library/Tailscale/sameuserproof-<port>` holds the token (root:admin 0640, so an admin user reads it) | `readlink`, read the token file |
| macOS **App Store** app (sandboxed network extension `IPNExtension`) | TCP `127.0.0.1:<port>`; token and port in a file **named** `sameuserproof-<port>-<token>` in its group container, `~/Library/Group Containers/<team>.io.tailscale.ipn.macos/` (team `W5364U7YZB`) | read the directory, newest file first (an app restart leaves the old one); else what tailscale's CLI does, `lsof -n -a -u <uid> -c IPNExtension -F n`, finding `.tailscale.ipn.macos/sameuserproof-` |
| macOS open-source `tailscaled` (Homebrew, `sudo tailscaled`) | unix socket `/var/run/tailscaled.socket` | stat the path |

Before a token is sent anywhere, the endpoint is checked as tailscaled's
(round-1 review S2): the standalone app's `ipnport` and `sameuserproof`
must be root's, and every process listening on the port (round 2, R8:
all of them, not the first) must be root, or this user's `IPNExtension`
for the App Store app. The listeners' pids come from `/usr/sbin/netstat
-anv -p tcp` (it names root's listeners without root, where an
unprivileged lsof doesn't see them: the standalone app's network extension
runs as root), their owners from `/bin/ps -o pid=,uid=,comm= -p ...`;
re-checked every 10 s and after any failed request; a default socket must
be root's. So a stale port another local user grabbed after Tailscale
restarted never gets the token, or lies in WhoIs. A 401/403 ends the
search: no other endpoint is tried with its token.

Every request has `Host: local-tailscaled.sock` (the LocalAPI checks it);
over TCP it carries the token as HTTP basic auth (empty user, token as
password), as tailscale's client sends it. A client that can't connect
(the app restarted on a new port) locates again. `-localapi-socket` on the Mesh Service or
the Router pins one socket. Tests use `tailscaletest`, which serves a fake
LocalAPI on a socket or on a localhost port behind a token, laid out as
each macOS variant is; nothing in the tests reads the real `/Library/
Tailscale` token or talks to a real tailscaled (the layout above was
checked by listing the directory only).

The Mesh Service reads `status` (self, key expiry, `BackendState`,
`AuthURL`) and `whois`; the Router also reads `status` with peers (the
tailnet finder), `whois` (vouching) and posts `login-interactive` (Sign in
to Tailscale).

### Routes and shapes (for Part 2)

All local only: Deny over the mesh (`authz.Routes`), and a relayed request
is refused `403 local_only`, except `GET /devices` (admin over the mesh, as
before, without path or address states). Errors are `{code, error}`, some
with more fields as listed.

| Route | Body | Answer |
|---|---|---|
| `GET /devices/self` | | `SelfDevice` below |
| `PUT /devices/settings` | `{networks?: [id] (0 or 1), tailnet_check?: bool, public?: bool, confirm_public?: bool}`; the earlier `{require_tailnet}` alone still sets the Tailscale check | `SelfDevice`. 422 `one_network`, `unknown_network`, `public_not_a_mesh_network`, `tailnet_check_needs_tailscale`, `confirm_public`; 409 `require_tailnet_locked` |
| `POST /devices/discoverable` | `{on, renew?}`: open, renew (`renew: true`, every minute while the dialog is open; at most 30 minutes from the open) or close | `{on, until?}`. 409 `window_closed` on a renewal of a closed or 30-minute-old window: open it again. Closing also ends the search, and the window's pending discovered requests within a minute |
| `GET /devices/discovered[?network=id]` | | `{searching, started_at, networks: [{id, label, kind}], devices: [{name, platform, fingerprint, short, network, network_label, via ("tailscale"\|"mdns"), paired, peer_name?, compatible, seen_at}], hidden_count, hidden_known}`. Starts a search (or continues it); poll every ~2 s; it stops 30 s after the last poll, or after 10 minutes |
| `POST /devices/discovered/{fp}/request` | `{}` | 202 `JoinStatus` (mode `discovered`, `check_code`, `vouched`). 404 `not_found` (not in the search), 409 `already_paired`, 422 `no_shared_network`, 502 `host_unreachable` / `host_key_mismatch`, 403 `not_discoverable` (its window closed), 429 `too_many_requests` (the message says when a slot frees) |
| `GET /devices/pair/{id}` | | `JoinStatus` |
| `POST /devices/pair/{id}/confirm` | `{match: bool}`: "Yes, the same number" / "No, it was different" | `JoinStatus` (`confirm` `matched`, pinned; or `mismatch`: nothing pinned, and the host is told, undoing its side). 409 `not_awaiting_confirm` |
| `POST /devices/pair` | `{code, update_addresses?}` | 202 `JoinStatus` (mode `code`); 200 `{update_addresses: true, peer_id, name}`. 409 `already_paired_update` `{peer_id, name}`; 422 `no_shared_network` `{host_networks: [label], tips: "no_shared_network"}`; 502 `host_unreachable` `{tips: "host_unreachable"}`, `host_key_mismatch`; 422 `bad_code`, `code_version`, `code_no_address`, `code_expired`, `pair_self`. A code is never used up by these |
| `POST /devices/pairing-code` | | 201 `{id, code ("mads-pair-2:…"), expires_at, host: {name, fingerprint, mesh_url, addresses}, networks: [{id, label, kind}]}` (name the networks, not the addresses). 409 `no_mesh_network`: start Set up mesh instead |
| `GET /devices/pair-requests` | | `[PairRequest]`, now with `mode` (`code`\|`discovered`), `check_code`, `vouched`, `vouch_withheld` (`another_request`\|`name_collides`: show the number anyway), `joiner_mismatch`, `addresses`, `network`, `network_label`, `tailnet_node` ("Tailscale says": a label, not a check) |
| `POST /devices/pair-requests/{id}/approve` | `{level, name?}` (unchanged) | `PairRequest` |
| `GET /devices` | | `[DeviceStatus]`; each adds `path: {addr, kind, label, rtt_ms, since}` (Status tooltip only), `addresses: [{via, addr, kind, label, network, source, added_at, last_ok_at, state, error}]` (Troubleshoot only), `diagnosis`, `remote.tailscale_key_expiry`; `reachability` may be `no_shared_network` |
| `POST /devices/{fp}/addresses` | `{addr}` | the row. 422 `bad_address`, `too_many_addresses` |
| `DELETE /devices/{fp}/addresses/{addr}` | | the row. 409 `not_user_added`, `last_address`; 404 `address_not_found` |
| `POST /devices/{fp}/redial` | `{}` (Try again now) | `{reachability, reason, path, addresses}` |
| `POST /devices/tailscale/login` | `{}` (Sign in to Tailscale) | `{auth_url, backend_state, needs_login}`. 409 `no_tailscale`, 409 `not_needed` (signed in: nothing is started), 502 `tailscale_unavailable` |

```
SelfDevice  {name, fingerprint, short, platform, mesh_url, hosts_sessions,
             require_tailnet, require_tailnet_locked,        // kept one release
             mesh: SelfMesh}
SelfMesh    {configured,                                    // false: the Set up mesh empty state
             networks: [NetworkView], port, public,
             tailnet_check, tailnet_check_status ("on"|"off"|"unavailable"), tailnet_check_locked,
             discoverable_until?, window_closed_reason?, pairing_started: [..],
             listening: [NetAddress], service_seen, service_answering?,
             tailscale?: {present, backend_state, needs_login, auth_url?, dns_name, tailnet,
                          key_expiry?, key_expiry_off, admin_url, endpoint, error?}}
NetworkView {id, kind ("overlay"|"lan"|"public"), title, iface, meaning, who_can_knock, risk,
             recommended, shared, wireless, ssid, chosen, available, listening, error?}
JoinStatus  {id, request_id, state, level?, host: {name, fingerprint, mesh_url?, addresses},
             expires_at, peer_id?, error?, self, mode, check_code?, vouched,
             confirm? ("needed"|"matched"|"mismatch"), network?,
             adopted_network?: {id, label, kind},   // set only once the host is pinned
             not_adopted? ("shared"|"gateway_unknown")}
NetAddress  {via, addr, kind, label, network}
```

Text keys the UI maps to its words (U1, U2):

| Field | Keys |
|---|---|
| `meaning` | `vpn_anywhere` ("VPN · Works from anywhere your devices have internet"), `local_only` ("Local network · Only while both devices are on this network"), `internet` |
| `who_can_knock` | `tailnet_only`, `vpn_members`, `anyone_on_network`, `anyone_on_shared_network` (the warning colour), `anyone_on_internet` |
| `risk` | `low`, `medium`, `high` |
| `tips` | `no_shared_network` (U1's "Give your devices a network in common"), `host_unreachable` (F4's "isn't answering" steps) |
| `addresses[].state` | `in_use`, `works`, `not_on_network`, `not_answering`, `other_device` ("A different device answered at …"), `refused` (the dial policy), `untried` |
| `diagnosis` | `tailscale_signed_out` ("Probably signed out of Tailscale"), `tailscale_key_expiring` (within 7 days) |

Events: `device.discoverable` `{until, by, opened_at, closed_at?}`;
`device.level_uncapped` `{fingerprint, peer_id, name, level, capability,
had_capability}` (a device acting above what the tailnet policy's
capability allowed before the cap was dropped; once per device per Mesh
Service run); `device.discovered` `{kind
("probe"|"request"), remote, fingerprint, name, network, repeats}` (audit);
`mesh.listening` adds `networks`, `addresses`, `port`,
`tailnet_check_status`, `tailscale`, `at`, and is written on every change
(debounced 5 s); `peer.reachability_changed` adds `via` on any change to
connected (the first connect, and back after an outage),
and a path change while connected is logged as one with `from` and `to`
both `connected`, `via` and `via_from` ("Now reached via Tailscale (was
home network)").

### Round-1 review changes

- **Listening is what answers now** (round 2, T5): the Mesh Service's
  last report outlives it, so for `GET /devices/self` the Router asks it,
  over the mesh TLS with this device's own key, `GET /mesh/self` (204, its
  own key only, before the tailnet gate; counted as a request, so never a
  failed handshake) on an address it reported, dialing only this
  machine's own addresses (loopback or an interface's), cached 3 s.
  `service_answering: false` and a network's `listening: false` with
  "the Mesh Service isn't answering there (is it running?)" when nothing
  answers; `mads mesh setup` says so, and `mads mesh status` gives a
  network's real reason ("not listened on: …", T7).

- **The check code is committed to** (C1): a discovered request is two
  steps on `POST /mesh/pair`. First `{mode: "discovered", commit, name,
  platform, addresses, tailscale_vouch}`, where `commit` is SHA-256
  (`mads-commit-1` ‖ joiner nonce ‖ joiner fingerprint ‖ host
  fingerprint); the host answers `{commit_id, host_nonce}`. Then `{mode:
  "discovered", commit_id, nonce}`; the host checks the nonce against the
  commitment (same key, once, within a minute) before any code exists.
  Neither side sees the other's nonce before its own is fixed, so a device
  in the middle can't grind the six digits. A first message with a nonce
  is refused `commit_required`.
- **One pending discovered request per source** (an IPv6 one by its /64),
  three sources at once; every Mesh Service limit counts IPv6 by /64. A
  source whose request the host declined gets no more in that window
  (`declined_source`). Accepted gap: devices behind one NAT share a
  source, so a stranger there can hold the slot (or be declined and
  block it) for the window; the pairing code remains the way round it.
- **Commitments per window** (round 2, R1): a window takes at most 8
  commitments in all, and closes itself (`window_closed_reason:
  abandoned_pairings`, `device.pairing_abandoned`) after 3 are dropped
  unrevealed; each is `device.pair_started`, listed in `GET
  /devices/self` as `pairing_started`. A middleman posing as a joiner
  learns this host's code for each before revealing: its chance per
  window is at most 8 in a million. The joiner waits at most 5 minutes
  plus grace whatever the host says.
- **Approvals** (R5) carry `check_code_confirmed` and `shown_vouched`;
  a discovered request is approved only on a confirmed code or a vouch
  that still holds and was shown (409 `vouch_withheld` /
  `check_code_required`).
- **Joiner-first adoption** happens only after the host is pinned, and
  never for a network that looks shared.
- **Look-alike networks**, **VPN identity** and **CGNAT widening**: see
  the decisions below.
- **macOS LocalAPI ownership**: see "How the macOS Tailscale app's
  LocalAPI is found".
- **The Tailscale listener** closes connections from outside Tailscale's
  ranges at accept.
- **Test hooks**: `-mesh-allow-loopback` on the Mesh Service (test and
  `mesh_loopback` builds only) binds loopback only; with `-fake-networks
  FILE` (Mesh Service and Router) the networks come from a JSON file of
  `meshnet.Network` with loopback addresses, `lo*` interfaces and
  loopback prefixes only (round 2, T8), re-read every scan, so a
  black-box tester can exercise the Tailscale listener (with a fake
  LocalAPI via `-localapi-socket`), vouching, budgets and mDNS on `lo0`
  without touching a real interface.
- **CLI**: `mads mesh add --check-code DIGITS` (compared here) replaces
  `--matched`.

### Decisions where this doc was silent

- **Who migration 0038 treats as upgraded.** Any `mesh.listening`, any
  `device.settings_changed`, or any `mesh_peers` row: Drew's Mac never ran a
  Mesh Service (it couldn't on macOS) but has pop-os paired, and must come
  up with Tailscale chosen. A machine with none of those is a fresh install.
- **`require_tailnet` in the settings payload** (for rollback) is the
  Tailscale network's check when Tailscale is chosen, else the value given,
  else on.
- **The settings API refuses a second network** (`one_network`) and a public
  network as the mesh network (that's `public`), so the MVP's "exactly one"
  holds whatever a client sends. Choosing Tailscale turns its check on unless
  `tailnet_check: false` comes with it.
- **The Tailscale network** is the interface named `tailscale*` or carrying
  an `fd7a:115c:a1e0::/48` address, or one tailscaled says is its own (the
  Helper); 100.64/10 alone is a generic VPN. Its "on this network"
  prefixes are Tailscale's ranges, since its interface has a /32. Another
  VPN's prefixes are its addresses' plus what the route table sends
  through its interface; only NetBird's is widened to 100.64/10 by name.
- **A VPN's id is its family and address prefix**
  (`vpn:zerotier:10.147.17.0/24`), not its interface (macOS renumbers
  `utunN`): another VPN on the same interface name isn't the one chosen,
  nor is a different kind of VPN on the same stock subnet (round 2, R9).
  Routes wider than /8 through it (a full tunnel's 0/1 + 128/1, a
  default) never count as "on" it (R3).
- **An address is on the most specific network holding it** (the longest
  prefix, R2): a LAN's /24 over a VPN that routes all of 192.168/16, for
  vouch checks and adopting a joiner's network.
- **A LAN chosen with its router's hardware address** isn't listened on
  while that address is unknown, nor when it differs; a stored Wi-Fi name
  (`ssid`) must match when both are known.
- **A LAN is only chosen with its router's hardware address** (round 2,
  R10): `PUT /devices/settings` answers 422 `gateway_unknown` while it
  isn't known (the ARP table hasn't the router yet, or the LAN has no
  gateway, an IPv6-only ULA one), joiner-first adoption skips it
  (`not_adopted: "gateway_unknown"`), and a stored LAN choice without one
  isn't listened on ("choose it again"). The Data Service doesn't enforce
  it (it stores what the Router decided). **macOS limitation:** macOS
  gives the Wi-Fi name only with Location permission (Q15), so there the
  router's hardware address is the only thing telling two look-alike
  Wi-Fis apart; two networks behind routers with the same hardware
  address (a cloned MAC) on the same subnet would pass as one.
- **The discovery window is enforced twice**: by the Mesh Service and by
  the Data Service, which also lets a discovered request outlive its window
  by one minute at most. Outside the window a discovered request gets the
  same `pairing_refused` as a bad code.
- **Vouching.** Each side trusts only its own WhoIs. The host's prompt
  skips the number only on its own WhoIs of the joiner (request on the
  Tailscale listener, same user, untagged, not a subnet router or exit
  node); the joiner's `tailscale_vouch` is only its claim, sent so both
  screens agree on whether a number is shown, and it protects only the
  joiner. The joiner skips "Did it show this number?" only on its own
  WhoIs of the host (reached over its own Tailscale network) and the
  host's vouch as it stood at approval. The host withholds the vouch
  (`vouch_withheld`) while another discovered request is waiting or the
  name collides, since WhoIs says "one of your nodes", not which one.
- **Every found device is checked over TLS**, mDNS ones too: the
  `/mesh/discover` answer's fingerprint must be the key the handshake
  proved, and an mDNS advert's `fp` must match it. An advert's address is
  dialled only when it's the address the advert came from, so a
  stranger's advert can't point the browser at another machine.
- **The Tailscale check stays in the Mesh Service's gate** (its WhoIs
  cache, the three-TTL grace and the revalidation of open streams), not
  behind `meshnet.Gate`, which is defined for later gates (class 3).
- **mDNS** is a minimal responder and browser: IPv4 multicast only, no
  probing or conflict resolution, announced twice and withdrawn (TTL 0) at
  the window's end; browse queries at 0, 1, 3, 5 s then every 10 s. Bounded
  (64 entries, 9000-byte packets) and dropped on any parse error.
- **`last_good`** is written when the address changes, else at most hourly
  (the doc said "at most once a minute"; less often is quieter for
  `peer.updated`, and the live path is in memory anyway).
- **Another device answering** at every address now reads `unreachable`
  with "a different device answered at …" (F4), not `denied` as before.
- **The failed-handshake cool-down** counts a connection that closes
  without carrying a request (no certificate, a scanner, a stalled
  handshake), per network budget.
- **`/mesh/discover` limits**: 10 per address then one per 3 s; 120 overall
  then one per 250 ms.
- **Loopback** is dialled only with `-mesh-allow-loopback`, which a
  production build refuses: allowed in go test binaries and builds with
  `-tags mesh_loopback` (the Playwright harness and the Router boot tests).
- **`mads mesh approve` opens the setup window while it waits** (a
  discovered request can't arrive otherwise) unless one is already open,
  which it then leaves alone; a discovered request that Tailscale didn't
  vouch for needs `--check-code` with `--yes`, and a wrong code declines
  it and exits non-zero. `mads mesh add --check-code` is compared with
  this device's code, never taken as "it matched". The
  suggested level is control for a computer and view for a phone.
- **A user-added address** (`mads mesh address add`) into a full list
  replaces the oldest host-reported one, never another user one or
  `last_good`.
- **The Router keeps its own view of this device's networks** (the same
  detection, every 10 s) for S2 and F3, independent of the Mesh Service, and
  reads tailscaled itself for discovery, vouching and sign-in.
- `GET /peers/candidates` (the older tailnet list from `tailscale status
  --json`) is unchanged.

### Open items

- ~~Verify on Drew's Mac: the standalone app's LocalAPI with its real
  token~~ Done 2026-11-23: `GET /devices/self` on Drew's Mac reports
  endpoint `macos-standalone`, `Running`. Still unverified: the App Store
  layout (no App Store install was available).
- **macOS Local Network privacy** (macOS 15): the Mesh Service's mDNS and
  LAN listener, and the Router's mDNS browse and LAN dials, may need the
  user to allow Local Network access for those binaries; not yet tried on a
  real Mac.
- mDNS on IPv6 (`ff02::fb`); real-network mDNS is tested only against an
  in-memory network.
- The Wi-Fi name (SSID) on macOS (needs Location permission, Q15); Linux
  reads it with `iwgetid` when installed.
- ~~The UI's `cappedLine` and Introduce's "held below by the tailnet
  policy" text~~ Removed in Part 2.

## As built: Part 2 (the UI)

Built on branch `mesh-net-ui` (2026-11-23) from mockup v4, on Part 1's
routes unchanged (no backend change).

| Piece | Code (`ui/src/features/settings/sections/paired/`) |
|---|---|
| Wording and state rules (meaning, who can knock, card state, Tailscale banners, row problems, address states, refusals, adoption) | `network.ts` |
| Empty state | `MeshEmptyState.tsx` |
| Set up mesh (quick card, list, None of these, Add devices) | `setup/MeshSetup.tsx`, `NetworkChoice.tsx`, `NoSharedNetwork.tsx`, `AddDevices.tsx`, `FoundDevice.tsx`; the window and polling in `setup/useSetup.ts` |
| Mesh network card | `MeshNetworkCard.tsx` |
| Troubleshoot connection | `TroubleshootConnection.tsx` (in `DeviceDetails`) |
| The check code | `ui/src/components/CheckCode` |

Decisions where the mockup was silent, and deviations:
- **The steps bar sits under the dialog title**, not above it: the shared
  `Dialog` puts its title first, and changing it was out of scope. It shows
  only when step 1 is in the dialog (UX polish).
- **Introduce a device…** shows in the This device bar only once another
  device lets this one administer it (UX polish; v3 drew it always).
- **Paste a pairing code** names the code's networks before asking. A code
  doesn't carry network ids, so "This MacBook-Air can reach pop-os over
  Tailscale" is said only when a network of this device has the code's
  label; otherwise "pop-os is reachable over Tailscale". The Router decides
  on Ask to pair (`no_shared_network` → the tips and Check again).
- **The quick "Use Tailscale?" card** is reached where Set up mesh starts
  without a choice (Copy a code with no mesh network); the empty state's
  Set up with Tailscale skips it (one click, then Add devices), and Other
  networks… opens the full list.
- **The Mesh network card** has two more states than U2's: *Not answering*
  (the Mesh Service isn't running or answering) and *Not listening* (with
  the network's error). It re-reads `GET /devices/self` every 5 s while not
  Reachable (every second while Getting ready…), since whether the service
  answers is checked live with no event.
- **Activity:** the setup window's minute renewals and `device.discovered`
  (audit) are hidden; a settings change is worded against the one before
  it ("Check Tailscale identity turned off").
- **A joiner's "No, it was different"** shows on the host as a toast and an
  activity line ("…said the number on its screen didn't match, so it was
  removed here").

UX polish (2026-11-25, from the add-devices UX walk; UI only):
- **The host's prompt is the control confirmation.** Its Add names the level
  ("Add with control") and approves at once, still held back 1 s with
  Decline focused; the warning is already on screen, so no second "Let it
  control?" dialog. Raising a level from the table, and now the joiner's
  joined answer, still asks.
- **Codes in one click:** Pair with a code opens straight to this device's
  code, already copied (the chooser is gone; Paste is a link in it); a
  `mads-pair-` code pasted anywhere on Paired devices opens Paste a pairing
  code with it.
- **Add devices holds still:** pinned near the top; its first line is the
  card's state (no ✓ before Reachable); the help folds once a device is
  found; the spinner turns only while nothing is; a device approved here
  says so and the dialog closes itself after 5 s untouched (and on Paired
  devices the answer shows under the bar, not as a toast over it).
- **One fingerprint format** (hyphenated, 7 groups to a line, the short form
  in the accent); a grant-only host reads *Reachable* with a neutral dot;
  the section keeps to Settings' 760 px column with sentence-case labels.

Review round 1 (Part 2) also changed the backend, minimally:
- **A pairing request right after setup** (discovered or by code) waits up
  to 15 s (`Options.ReadyWait`) for this device's Mesh Service to report
  where it listens, then gives the chosen network's own addresses at the
  mesh port, so the host always has an address to reach the joiner back.
- **Tailscale sign-in**: `GET /devices/self`'s `tailscale.control_url` (only
  while a sign-in is needed) and the login answer's `control_url` are
  tailscaled's ControlURL (its prefs); the UI opens a sign-in link only over
  https to login.tailscale.com or that host. The Router hands on no
  sign-in link that isn't https (`unexpected_auth_url`, and none in
  `/devices/self`); the UI says "Tailscale gave an unexpected sign-in link"
  whichever refused it.
- The setup window is one per page, opened, renewed and closed in order.
  A hidden page stops renewing but doesn't close it, so the window lasts
  as long as an unrenewed one (10 minutes) and a pairing under way
  survives mads being covered; shown again, it renews at once (or shows
  it closed, with Open again). It's closed with a keepalive request when
  the page goes; polls pause while hidden and back off.

Open items:
- **Device activity** (`GET /devices/activity`, Data Service): leaves out
  `device.discovered` and the setup window's renewals in the query, so they
  never push shown entries off a page, and includes a connected device's
  path changing (`peer.reachability_changed` from and to `connected`;
  per device by its peer id): "pop-os now reached via Tailscale (was
  Home)".
- **e2e**: the real Mesh Service runs for the device under test on fake
  networks; the other device is the stand-in (two real Mesh Services can't
  share 127.0.0.1's one mesh port, and macOS has no 127.0.0.2 without an
  alias).

