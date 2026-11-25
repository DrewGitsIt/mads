# Mesh Service

The gate for mesh mode ([docs/MESH-MODE-DESIGN.md](../../docs/MESH-MODE-DESIGN.md),
[docs/MESH-AUTH-DESIGN.md](../../docs/MESH-AUTH-DESIGN.md),
[docs/MESH-NETWORK-DESIGN.md](../../docs/MESH-NETWORK-DESIGN.md)). It lets the Routers
on the user's other devices reach **this** node's Router:
- It listens only on this device's **mesh network**: the network its user chose
  (Tailscale, another VPN, a LAN), on its own addresses there (`:17380`), never a
  wildcard. It finds this machine's networks itself every 10 s (`internal/netloop`,
  `pkg/meshnet`) and moves a listener when its network's address changes. A fresh
  install has no mesh network and listens nowhere until set up (`mads mesh setup`,
  or Settings → Paired devices). It starts without tailscaled, which it asks only
  for names, peers and the identity check (`pkg/meshnet/tailscale`, which finds the
  LocalAPI on Linux and on macOS).
- It speaks **TLS 1.3 only, with client certificates** (mutual TLS, D2). Its
  server certificate is made in memory at start from this device's key
  (`$MADS_HOME/device.key`, `pkg/devicekey`); there are no certificate files.
- A caller is the **paired device** whose key it proves: the fingerprint of its
  certificate key, looked up in this host's paired devices. An unpaired key is
  refused (`unpaired_device`).
- On the **Tailscale** network only, an identity check (on by default; WhoIs: same
  tailnet user, untagged) can refuse a caller. It never lowers a level and reads
  nothing from the tailnet policy. With the check on and tailscaled unreachable,
  the Tailscale network refuses everyone (fails closed).
- It authorises the device by its **grant** (view, chat, control or admin) and
  ceilings (C1–C3), over a per-route class table, and forwards allowed requests
  to the loopback Router with the device as the actor.

It also serves `/mesh/hello` (who this node is, what the caller may do here, where
it listens now and its Tailscale key expiry) and `/mesh/fs/list` (the folder browser
for starting a session here from another device). While this device's setup window
is open (at most 10 minutes, opened at the device: `device.discoverable`), it also
answers `GET /mesh/discover` (name, platform, fingerprint) to anyone completing the
TLS handshake, takes discovered pairing requests (`POST /mesh/pair` with mode
`discovered`, a check code both screens show), and advertises `_mads._tcp` over mDNS
on a LAN mesh network. Outside the window there is no unauthenticated endpoint.

The **paired devices** come from the Data Service (`internal/paired`): loaded at
start and followed through `peer.*` events, retrying with backoff while the Data
Service is down (or a reload fails), backing off at most 5 s; the listener
never waits on it, and refuses every device
(`devices_unavailable`) until the first load. After each reload, requests in
flight are re-checked: a device unpaired, or whose grant changed, has its open
requests (streams included) ended.

It does **not**:
- Own state. There is no database; the paired-device list is a read-only copy
  of the Data Service's `mesh_peers`.
- Write to the Data Service, except:
  - `device.refused` audit events (debounced per device, code and route);
  - `mesh.listening`: where it listens on each network, its fingerprint and
    the Tailscale check (what the Router's Settings and pairing codes read);
  - a joiner's pairing request (a code redeemed, or a discovered request
    during the setup window), which the host's user then approves or
    declines, and a joiner's "the number didn't match" (undoes that
    pairing);
  - closing the setup window after abandoned pairings;
  - forgetting a paired device that asks to be forgotten (`/mesh/forget`,
    docs/MESH-NETWORK-DESIGN.md F6).
- Serve any UI.

The Router reaches it only to check that this device's own Mesh Service
answers at an address (`GET /mesh/self`, before showing or sharing it).
Otherwise it learns it exists only from the headers on the requests it
forwards: `X-Mads-Mesh-Peer` (the calling device's name here, which also
marks the request as relayed), `X-Mads-Mesh-Level` (its effective level),
`X-Mads-Mesh-Device` (its full fingerprint) and `X-Mads-Actor` (the device,
as the actor of what it does).

## Run it alone

```sh
make build && ../../bin/mesh                      # listens on this device's chosen mesh network
../../bin/mesh -addr 127.0.0.1:17380 -fs-root off  # loopback, for poking at it locally
```

**Flags:**
- `-router-url` (default `http://localhost:17373`).
- `-addr` (default none): listen only here, whatever the setting says (tests,
  manual setups). `-port` (default: the setting's, 17380).
- `-localapi-socket` (default: found as tailscale's own CLI finds it: Linux
  `/var/run/tailscale/tailscaled.sock`, mode 0666; the macOS app's localhost port and
  token; or the open-source macOS daemon's `/var/run/tailscaled.socket`).
- `-require-tailnet` (default `true`): the Tailscale network's identity check.
  Refuse-only: the paired device's grant is its level.
  **Precedence:** left unset, this host's own setting decides (the Tailscale
  network's `tailnet_check` in the `mesh_settings` row, `PUT /devices/settings` on
  the Router). The Mesh Service follows `device.settings_changed`, so a change
  applies live: turning it on ends open streams on the Tailscale network that no
  longer pass the check. It starts with `true` until it has read the setting.
  Given explicitly (`-require-tailnet` or `-require-tailnet=false`), the
  flag wins for as long as the Mesh Service runs that way; it says so in
  `mesh.listening` (`require_tailnet_flag`), and Settings shows the switch
  locked.
- `-platform` (default `runtime.GOOS`): what `/mesh/hello` reports as this
  host's platform; other devices derive its kind (computer, phone) from it.
  `-hosts-sessions` (default `true`): whether hello says this host has mads
  sessions to view. `-mesh-name` (default the short host name, as the
  Router's): how pairing answers name this host.
- `-whois-ttl` (10 s): how long a caller's identity is cached, which bounds how
  quickly a policy change takes effect.
- `-fs-root` (`$HOME`, or `off`).
- `-data-url` (default `http://localhost:17374`): the Data Service the
  paired-device list is read from, with the install token.
- `-mads-home` (`$MADS_HOME`, else `~/.mads`) and `-token-file` (default
  `<mads-home>/local.token`): where this node's install token is read from.
  `<mads-home>/device.key`, this device's identity (`pkg/devicekey`), is created
  there on first start if it's missing; it is the TLS certificate's key, and
  its fingerprint is logged and reported on `/mesh/hello` as `device`. A
  refused key file (loose mode, another owner) is fatal: there is nothing to
  serve TLS with.

It refuses to bind `0.0.0.0` or `[::]`.

From another device on the tailnet, plain HTTP and TLS without a device
certificate get nothing; a viewer Router is the client:

```sh
curl http://pop-os:17380/mesh/hello           # fails: TLS only
curl -k https://pop-os:17380/mesh/hello       # fails: no client certificate
mads pair --list                              # on pop-os: who may connect, at what grant
```

**Linux peers without Go:** see "Deploying to a Linux peer" in
[docs/MESH-AUTH-DESIGN.md](../../docs/MESH-AUTH-DESIGN.md) (the A4/A5 cutover
runbook): every Go binary is cross-compiled on a machine with Go
(`make -C services/mesh build-linux` builds this one) and copied across with
`ui/dist`. To run it without a terminal:

```sh
systemd-run --user --unit mads-mesh --working-directory=$HOME/repo/mads $HOME/repo/mads/bin/mesh
journalctl --user -u mads-mesh -f
systemctl --user restart mads-mesh   # picks up a new binary; only while it's running
systemctl --user stop mads-mesh      # a transient unit is gone once stopped: re-run systemd-run
```

## Test it

```sh
make test       # -race; no tailscaled, no Router, no network beyond loopback
```

- **`internal/authz`:** the route table and the levels.
  - **The guard:** `TestEveryRouterRouteIsClassified` reads the Router's
    registrations out of `services/router/internal/api/api.go` and fails for any
    route this table doesn't classify. `TestNoStaleClassifications` fails for
    entries the Router dropped.
  - `TestClassify` pins the matching, including wrong methods and unclean paths
    (both are Deny).
  - `TestLevelsReach` pins each level's reach: view reads only, chat adds
    exactly the talk-to-a-session routes, control everything forwardable,
    admin equal to control until A6. `TestCeilingRoutes` pins the C1–C3 route
    sets, including every route that can set trust.
- **`internal/tailnet`:** the LocalAPI client against a fake tailscaled on a real
  unix socket. Covers status, WhoIs, grant parsing and merging, unknown addresses,
  and malformed grants.
- **`internal/server`:**
  - Every refusal path (`TestGateRefusals`): no TLS, no certificate, an
    unpaired key, the tailnet factor, a level too low. None of them reaches
    the Router.
  - Levels with and without the tailnet factor, the capability capping the
    grant (`TestLevelsAdmit`).
  - The header rewrite the Router's `localguard` depends on, and the
    authenticated device stamped over whatever the caller claimed.
  - Over real TLS: SSE flushed per event; a revoked device's stream ended
    mid-stream and its next request refused on the same connection; a grant
    change ending streams (`TestRevokedMidStreamEndsTheStream`,
    `TestGrantChangeEndsStreams`).
  - Ceilings (`ceilings_test.go`): C1 allowed/refused/held, C2 on the
    terminal routes, C3 on every trust-setting route with bodies (cased and
    duplicate keys, defaults resolved and pinned, oversized bodies).
  - `device.refused`: debounced per device, code and route; rate-capped.
  - `hello`, including this device's fingerprint.
- **`internal/paired`:** the paired-device list against a real Data Service
  behind its front door: load and follow (pair, grant change, unpair), the
  reload hook, a Data Service that is down at start and comes back, and no
  token.
  - WhoIs caching and revocation.
  - `/mesh/fs/list` boundaries: no files, no hidden folders by default, no
    symlink or `..` escapes from the root.

## API contract

**Counterparties:** paired devices' Routers (mutual TLS), tailscaled (LocalAPI,
read-only: `status`, `whois`), the local Router (HTTP, as a plain loopback
program) and the Data Service (the paired list and settings; it appends
`device.refused` and `mesh.listening`, and redeems pairing codes).

**The handshake:** TLS 1.3 only; this device's certificate; a client
certificate required, carrying an Ed25519 key (anything else fails the
handshake). Whether that key is paired is decided per request, below, so a
refusal can say why and revocation needs no reconnect.

**The gate, every request, in order:**

1. **A browser sent it** (`Origin` or any `Sec-Fetch-*` header): 403 `browser_request`.
2. **The path isn't in clean form** (`.`/`..` segments, doubled slashes) or
   carries an encoded slash anywhere (ids are `ses_` plus hex, so none
   needs one): 400 `bad_path`.
3. **No device certificate** (plain HTTP can't get here at all): 403
   `no_client_certificate`.
4. **With `-require-tailnet`**, before the key is looked at:
   - Not a tailnet peer: 403 `not_tailnet`. tailscaled down: 503 `whois_failed`.
   - Another user's node: 403 `other_user`. A tagged node: 403 `tagged_node`.
   - No mesh capability: 403 `no_capability`.
5. **The paired list isn't loaded yet:** 503 `devices_unavailable`.
5a. **`POST /mesh/pair`, `GET /mesh/pair/{id}`** (pairing by code,
   docs/MESH-AUTH-DESIGN.md D6): the only routes a key that isn't paired
   here may use. `POST` presents a pairing code's secret (`server.PairJoin`)
   and opens a request for this host to approve: 201 `server.PairStatus`.
   Every bad secret (wrong, expired, used, malformed) gets one 403
   `pairing_refused`, audited and debounced; this host's own key is 403
   `pair_self`. A key already paired here re-pairs: its request is taken as
   any other, and approval updates its row in place (F6). The key that
   used a code may present it again for that request's lifetime and gets
   the same request back. Rate-limited
   per address (5, then one per 12 s) and overall (120, then one per
   500 ms): 429. A joiner's own mesh address must be on this tailnet at the
   mesh port (422 `bad_mesh_url`).
   `GET` is the joiner's poll, by the same key only (another key: 404).
6. **The key isn't paired here:** 403 `unpaired_device` (audited), and the
   connection is closed. The effective level is the grant, capped with
   `-require-tailnet` by the capability's.
6a. **`POST /mesh/forget`** (F6): any paired caller, any level, none
   included: the Data Service deletes its row (`device.unpaired`, by
   `itself`) and its live requests end. 204.
7. **`GET /mesh/hello`:** 200 [`Hello`](internal/server/server.go), with
   `access` the effective level and every level below it (empty: granted
   nothing).
8. **`GET /mesh/fs/list?path=&hidden=1`:** needs `control`.
   Returns [`FSListing`](internal/server/fs.go).
9. **The route is Deny** (or unknown, a wrong method, or an unclean path):
   403 `not_exposed`.
10. **Granted nothing, or the route's class is above the level:** 403
   `no_access` (audited).
11. **Ceilings** (audited): C1 `full_trust_chat_refused` / `held_unsupported`,
    C2 `no_shell`, C3 `trust_ceiling`. A C1 or C3 check that can't read the
    Router fails closed: 503 `ceiling_check_failed`.
12. **Otherwise the request is forwarded:**
    - `Host` is set to the Router's.
    - `Origin`, `Referer`, `Cookie` and `Sec-Fetch-*` are removed.
    - `X-Mads-Mesh-Peer` is set to the device's name here (the relay mark),
      `X-Mads-Mesh-Level` to its effective level, `X-Mads-Mesh-Device` to
      its full fingerprint, and `X-Mads-Actor` to
      `device:<name>/<short fingerprint>`, each overwriting any value the
      caller sent.
    - The caller's `Authorization` and `Proxy-Authorization` are removed.
      `Authorization: Bearer <this node's install token>` is added instead (see
      "Authenticating to the Router").
    - The response is streamed, flushed per write, until the device is
      unpaired or its grant changes (checked after every paired-list
      reload, reading nothing else, so at once), or (with the tailnet
      required) its tailnet factor definitely fails or its effective
      level changes (checked every `-whois-ttl`). A transient WhoIs failure
      keeps the last good answer for three TTLs past its expiry.
    - If the Router is down: 502 `router_unreachable`.

Error bodies are `{"code": "...", "error": "..."}`. Refusals marked audited
are also appended as `device.refused` (`contracts.DeviceRefusal`), at most one
per device, code and route per 10 minutes (an unpaired key: per key, whatever
the path; repeats counted), and 30 a minute each for unpaired keys and for
paired devices (the rest counted as dropped). Paths and methods are clipped to
256 bytes in events and logs.

**Connections** (`server.Listen`): at most 64 open (the next waits), at most 8
from one address (past that, closed at once); a connection is closed unless a
paired device is admitted on it within 10 s; with `-require-tailnet`, WhoIs runs
before its TLS handshake and a definite refusal closes it; any refusal on a
connection with no admitted device closes it. Idle connections close after 2
minutes; at most 128 HTTP/2 streams per connection.

**Classes** (`internal/authz/routes.go`), each route at the lowest level that
may use it; a level reaches its class and those below:
- `view`: reads and streams.
- `chat`: send, interrupt, answer a permission prompt, remove a queued
  message, attachments, compact, on existing sessions at their trust.
- `control`: everything else a user does with sessions: start them, trust,
  restart, the agent's terminal, rename, model, archive, git writes.
- `admin`: control plus managing this host's paired devices, by four narrow
  routes: `GET /devices`, `POST /devices/introduce`, `PUT /devices/{fp}/grant`,
  `DELETE /devices/{fp}` (the Router caps them, the Data Service checks
  again). The rest of `/devices` is Deny.
- `deny`: never forwarded. Every library or settings write; config,
  credentials, accounts, profiles, registry, metrics and imports; links and
  orchestration; the Router's `/peers…`; the static UI.

**Ceilings** (`contracts.PeerCeilings`): C1 applies to a chat-level device's
`POST /sessions/{id}/messages`, and `/compact` with focus text, to a full-trust
session; C2 to
the terminal routes (`POST /sessions/{id}/transport`, `/input`, `/resize`),
which any C3 ceiling below full also refuses; C3 to the
trust-setting routes (`POST /sessions`, `POST /sessions/{id}/trust`), read
from the body as the Router reads it (1 MiB cap).

## Authenticating to the Router

The Router's local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md) answers 401 to
any API call without the install token. The Mesh Service is a local program like
the CLI, so it presents that token:
- It reads `$MADS_HOME/local.token` through `localauth.FileSource`.
- It re-reads the file whenever the file changes, so a `mads token rotate` is
  followed without a restart.
- `localauth.Transport` sends it on every forwarded request.

The token is this node's alone and never crosses the mesh:
- **Peers send none.** They are identified by their device certificate (and
  WhoIs plus the tailnet capability, by default).
- **Nothing inbound passes through.** Whatever credential a caller sends is
  dropped before forwarding.
- **Nothing outbound carries one.** The viewer Router never puts its own token on
  requests to a peer's Mesh Service.

Roll out the Router and the Mesh Service together. A front-door Router refuses
a Mesh Service that sends no token, and the viewer then sees 401s from this
peer.

## Critical paths

- **Default deny:** a route not in `authz.Routes` is never forwarded.
  Guarded by `TestEveryRouterRouteIsClassified` and `TestClassify`.
- **Only paired keys get in, at their grant:** `TestGateRefusals`,
  `TestLevelsAdmit`, and `pkg/devicekey`'s handshake tests.
- **A refusal never reaches the Router:** `TestGateRefusals` asserts the fake Router
  saw nothing.
- **Revocation is immediate, streams included:** `TestRevokedMidStreamEndsTheStream`,
  `TestGrantChangeEndsStreams`.
- **Ceilings read what the Router will apply:** `TestTrustCeiling`.
- **The forwarded request passes `localguard` as a program, not a page, as
  the authenticated device:** `TestForwardRewritesForLocalguardAndStampsTheDevice`
  checks `Host` is loopback, no `Origin`/`Sec-Fetch-*`/`Referer`/`Cookie`
  reach the Router, and the caller can't spoof `X-Mads-Mesh-Peer`,
  `X-Mads-Mesh-Level` or `X-Mads-Actor`.
- **SSE is live, not buffered:** `TestEventStreamIsFlushedPerEvent`.
- **A tailnet policy change takes effect within `-whois-ttl`:** `TestWhoIsIsCachedBriefly`.
- **Folder browsing stays under the root:** `TestFSList`.
- **Tailnet-only bind:** `refuseWildcard` (`main_test.go`).
- **The Router gets this node's token, never the caller's:**
  `internal/server/token_test.go` runs against a real `localauth` door.
  - `TestForwardSendsThisNodesInstallToken`.
  - `TestForwardDropsTheCallersAuthorization`.
  - `TestForwardFollowsTokenRotation`.
  - `TestForwardWithoutTokenIsRefusedByTheRouter`.
