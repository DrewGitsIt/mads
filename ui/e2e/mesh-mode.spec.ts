import http from "node:http";
import http2 from "node:http2";
import type { TLSSocket } from "node:tls";
import { createHash, X509Certificate } from "node:crypto";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { AddressInfo } from "node:net";
import { test, expect } from "./fixtures";
import { bootTopology, type Topology } from "./lib/topology";
import { startFastModelFixture, type FastModelFixture } from "./lib/fastModelFixture";
import { dropTextFile, pasteScreenshot } from "./lib/attachments";
import { createSession, createWorkspace, pollTranscriptContains, sendChatMessage, setConfig, setupWorkspace } from "./lib/seed";
import { apiFetch } from "./lib/auth";
import { certFingerprint, deviceFingerprint, newDevice } from "./lib/meshTLS";
import { startStandIn } from "./lib/standinMesh";

// Mesh mode (docs/MESH-MODE-DESIGN.md): two real stacks — "viewer" and
// "peer" — with a stand-in Mesh Service in front of the peer's Router. The
// real one (services/mesh) needs tailscaled for WhoIs and is covered by its
// own Go tests; this one answers /mesh/hello and forwards the rest the way
// the real one does (Host rewritten, browser headers refused, the caller's
// Authorization dropped and the PEER's own install token sent to the peer
// Router), so the journey exercises the viewer Router's routing and the
// whole UI for real. Each topology has its own token
// (docs/LOCAL-FRONT-DOOR-DESIGN.md); the page carries the viewer's, as
// Electron's main process would, and the viewer's token must never reach
// the stand-in.
//
// Like the real one, the stand-in speaks mutual TLS with pinned keys
// (docs/MESH-AUTH-DESIGN.md D2): TLS 1.3 (HTTP/2) with its own Ed25519
// device certificate, a client certificate required, and only the viewer's
// device key (the viewer's $MADS_HOME/device.key) let in. The viewer adds
// it pinned to the stand-in's fingerprint, so every request of the journey
// crosses a verified, reused connection.
async function chooseScope(page: import("@playwright/test").Page, option: string) {
  await page.getByRole("button", { name: /^Show sessions from: / }).click();
  await page.getByRole("menuitemradio", { name: new RegExp(`^${option}`) }).click();
}

test.describe("mesh mode", () => {
  test.describe.configure({ mode: "serial" });

  let fastModel: FastModelFixture;
  let viewer: Topology;
  let peer: Topology;
  let mesh: http2.Http2SecureServer;
  let peerSessionId: string;
  let peerDir: string;
  // Every Authorization header the stand-in Mesh Service was sent.
  const meshSawAuth: string[] = [];
  // When set, the stand-in answers matching requests itself: a peer
  // misbehaving on purpose.
  let meshAnswer: ((req: http2.Http2ServerRequest, res: http2.Http2ServerResponse) => boolean) | null = null;
  // GET /links reads the stand-in relayed for the viewer Router.
  const meshSawLinks = { bulk: 0 };
  // The event-bus reads the stand-in relayed: head reads, and the URLs of
  // streams (a remote session's live-only bus).
  const meshSawEvents = { heads: 0, streams: [] as string[] };
  // TLS connections the stand-in accepted, and client keys it refused.
  let meshHandshakes = 0;
  const meshRefusedClients: string[] = [];
  const meshSockets = new Set<TLSSocket>();
  // The stand-in's open requests to the peer Router (SSE streams among
  // them), ended at teardown so the peer Router can shut down cleanly and
  // stop its session children rather than be killed with them still running.
  const upstream = new Set<http.ClientRequest>();

  // The page talks to the viewer's Router with the viewer's own token, as
  // Electron's main process would add it below the page.
  async function openViewer(page: import("@playwright/test").Page) {
    await page.setExtraHTTPHeaders({ Authorization: `Bearer ${viewer.token}` });
    await page.goto(viewer.routerURL);
  }

  async function onboard(t: Topology): Promise<string> {
    const dir = mkdtempSync(path.join(tmpdir(), "mads-ui-smoke-mesh-"));
    setupWorkspace(dir);
    const ws = await createWorkspace(t.routerURL, { name: "mesh ws", repos: [{ path: dir, primary: true }] });
    await setConfig(t.routerURL, { default_workspace: ws.id, default_provider: "fake", onboarding_complete: true });
    return dir;
  }

  test.beforeAll(async () => {
    fastModel = await startFastModelFixture();
    viewer = await bootTopology({ fastModelURL: fastModel.url });
    peer = await bootTopology({ fastModelURL: fastModel.url });
    await onboard(viewer);
    peerDir = await onboard(peer);
    // The stand-in's folder browser is rooted at the peer workspace's parent,
    // as the real one is rooted at the peer user's $HOME.
    const fsRoot = path.dirname(peerDir);

    const s = await createSession(peer.routerURL, { name: "peerbox session", provider: "fake" });
    peerSessionId = s.id;
    await sendChatMessage(peer.routerURL, s.id, "echo hello-from-peer");
    await pollTranscriptContains(peer.routerURL, s.id, "you said hello-from-peer");

    const target = new URL(peer.routerURL);
    // The stand-in's certificates are hand-built DER: every one must parse
    // (a padded serial failed about 1 in 256 runs). Cheap to prove each run.
    for (let i = 0; i < 1000; i++) new X509Certificate(newDevice("check").certPem);
    const device = newDevice("peerbox");
    const viewerDevice = deviceFingerprint(viewer.homeDir);
    mesh = http2.createSecureServer(
      { key: device.keyPem, cert: device.certPem, minVersion: "TLSv1.3", requestCert: true, rejectUnauthorized: false, allowHTTP1: true },
      (req, res) => {
        const auth = req.headers.authorization ?? req.headers["proxy-authorization"];
        if (auth) meshSawAuth.push(String(auth));
        if (req.headers.origin || req.headers["sec-fetch-site"]) {
          res.writeHead(403, { "Content-Type": "application/json" }).end('{"code":"browser_request"}');
          return;
        }
        if (meshAnswer?.(req, res)) return;
        if (req.url?.startsWith("/mesh/fs/list")) {
          const asked = new URL(req.url, "http://x").searchParams.get("path") || fsRoot;
          const dirs = readdirSync(asked)
            .filter((n) => {
              // The temp dir churns under other processes: an entry can vanish mid-listing.
              try {
                return !n.startsWith(".") && statSync(path.join(asked, n)).isDirectory();
              } catch {
                return false;
              }
            })
            .map((n) => ({ name: n, path: path.join(asked, n) }));
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ path: asked, parent: asked === fsRoot ? undefined : path.dirname(asked), entries: dirs }));
          return;
        }
        if (req.url === "/mesh/hello") {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(
            JSON.stringify({ node: "peerbox", dns_name: "peerbox.ts.net", mads_commit: "e2e", mesh_api: 2, access: ["view", "chat", "control"], device: device.fingerprint }),
          );
          return;
        }
        if (/^\/links(\?|$)/.test(req.url ?? "")) meshSawLinks.bulk++;
        if (req.url?.split("?")[0] === "/events/head") meshSawEvents.heads++;
        if (req.url?.split("?")[0] === "/events" && String(req.headers.accept ?? "").includes("text/event-stream")) {
          meshSawEvents.streams.push(req.url);
        }
        // Like services/mesh: never pass the caller's credential through; the
        // peer Router gets the peer's own install token. HTTP/2's pseudo
        // headers aren't HTTP/1.1 headers.
        const headers: http.OutgoingHttpHeaders = {};
        for (const [k, v] of Object.entries(req.headers)) if (!k.startsWith(":")) headers[k] = v;
        headers.host = target.host;
        headers.authorization = `Bearer ${peer.token}`;
        delete headers["proxy-authorization"];
        const up = http.request(
          { host: target.hostname, port: target.port, method: req.method, path: req.url, headers },
          (upRes) => {
            // Connection-specific headers don't exist in HTTP/2.
            const out = { ...upRes.headers };
            for (const h of ["connection", "keep-alive", "transfer-encoding", "upgrade", "proxy-connection"]) delete out[h];
            res.writeHead(upRes.statusCode ?? 502, out);
            upRes.pipe(res);
          },
        );
        upstream.add(up);
        up.on("close", () => upstream.delete(up));
        up.on("error", () => {
          if (!res.headersSent) res.writeHead(502);
          res.end();
        });
        // The caller went away (a closed stream): stop reading from the peer too.
        res.on("close", () => up.destroy());
        req.pipe(up);
      },
    );
    // Only the viewer's device key gets in, as a host that paired it.
    mesh.on("secureConnection", (sock: TLSSocket) => {
      const cert = sock.getPeerCertificate();
      const fp = cert?.raw ? certFingerprint(cert.raw) : "(none)";
      if (fp !== viewerDevice) {
        meshRefusedClients.push(fp);
        sock.destroy();
        return;
      }
      meshHandshakes++;
      meshSockets.add(sock);
      sock.on("close", () => meshSockets.delete(sock));
    });
    await new Promise<void>((r) => mesh.listen(0, "127.0.0.1", r));
    const meshURL = `https://127.0.0.1:${(mesh.address() as AddressInfo).port}`;

    expect(viewer.token).not.toBe(peer.token);
    // No key, no device: the viewer won't dial what it can't verify.
    const add = (body: object) =>
      apiFetch(`${viewer.routerURL}/peers`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    const unpinned = await add({ mesh_url: meshURL });
    expect(unpinned.status).toBe(422);
    expect(((await unpinned.json()) as { code: string }).code).toBe("peer_unpaired");
    // Pinned to the wrong key: the handshake is refused before any request.
    const wrong = await add({ mesh_url: meshURL, fingerprint: viewerDevice });
    expect(wrong.status).toBe(422);
    expect(meshHandshakes).toBe(0);

    const added = await add({ mesh_url: meshURL, fingerprint: device.fingerprint });
    expect(added.status).toBe(201);
  });

  test.afterAll(async () => {
    // Stacks first, assertions after: a failed check (or a beforeAll that
    // never got far) must not leave two topologies running.
    try {
      for (const up of upstream) up.destroy();
      for (const sock of meshSockets) sock.destroy();
      if (mesh) await new Promise((r) => mesh.close(r));
    } finally {
      await viewer?.teardown();
      await peer?.teardown();
      await fastModel?.close();
    }
    // The viewer's install token never left the viewer: its Router sent the
    // stand-in Mesh Service no credential at all, on any request.
    expect(meshSawAuth, "credentials the viewer Router sent across the mesh").toEqual([]);
    // Only the viewer's own device key ever connected, and it kept its
    // connection: one TLS handshake for the whole journey (allowing one
    // more should a dial race at the first request open a second).
    expect(meshRefusedClients).toEqual([]);
    expect(meshHandshakes).toBeGreaterThan(0);
    expect(meshHandshakes).toBeLessThanOrEqual(2);
  });

  test("view and chat with a session on another device", async ({ page }) => {
    await openViewer(page);
    const rail = page.locator('[aria-label="Session list"]');

    // This device is the default: the peer's session isn't listed yet.
    await expect(page.getByRole("button", { name: "Show sessions from: This device" })).toBeVisible();
    await expect(rail.getByText("peerbox session", { exact: true })).toHaveCount(0);

    await chooseScope(page, "All devices");
    const card = rail.locator(`[data-rail-id="${peerSessionId}"]`);
    await expect(card).toBeVisible();
    await expect(card.locator('[data-rail-host="peerbox"]')).toHaveText("peerbox");
    // Labels resolve against the peer's library, never a raw id.
    await expect(card).not.toContainText("wsp_");

    // Open it: the transcript comes from the peer.
    await card.click();
    const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
    await expect(composer).toBeVisible();
    await expect(page.getByText("you said hello-from-peer")).toBeVisible();

    // Chat: the message runs on the peer, and the reply streams back here.
    await composer.fill("echo hi-from-viewer");
    await page.getByRole("button", { name: /^(Send|Queue)$/ }).click();
    await expect(page.getByText("you said hi-from-viewer")).toBeVisible({ timeout: 20_000 });
    await pollTranscriptContains(peer.routerURL, peerSessionId, "you said hi-from-viewer");
    // That session's live updates ride its host's own bus: a head read
    // there, then a stream filtered to the session, from that head.
    expect(meshSawEvents.heads).toBeGreaterThan(0);
    const stream = meshSawEvents.streams
      .map((u) => new URL(u, "http://stand-in"))
      .find((u) => u.searchParams.get("session_id") === peerSessionId);
    expect(stream?.searchParams.get("since")).toMatch(/^\d+$/);

    // No shell for a session on another device.
    await page.getByLabel("Workspace actions").click();
    await expect(page.getByRole("button", { name: /Open shell|Show shell/ })).toHaveCount(0);
    await page.keyboard.press("Escape");

    // The choice sticks across a reload.
    await page.reload();
    await expect(page.getByRole("button", { name: "Show sessions from: All devices" })).toBeVisible();
  });

  test("start a session on another device from the Launchpad", async ({ page }) => {
    await openViewer(page);
    await page.getByRole("button", { name: /New session/ }).click();

    await page.locator('[data-launch-host=""]').click();
    await page.getByRole("button", { name: /^peerbox/ }).click();
    await expect(page.locator('[data-launch-host="peerbox"]')).toBeVisible();

    // Folders come from the peer: browse into its workspace and use it.
    await page.getByRole("button", { name: /^folder/ }).click();
    const browser = page.locator('[data-remote-folders="peerbox"]');
    await browser.getByRole("button", { name: `${path.basename(peerDir)}/` }).click();
    await expect(browser).toContainText(peerDir);
    await browser.getByRole("button", { name: "Use this folder" }).click();

    await page.getByLabel("Describe your goal").fill("echo started-remotely");
    await page.getByRole("button", { name: "Start session" }).click();

    // It runs on the peer, opens here, and the reply streams back.
    await expect(page.getByText("you said started-remotely")).toBeVisible({ timeout: 20_000 });
    const onPeer = (await (await apiFetch(`${peer.routerURL}/sessions`)).json()) as { id: string; name: string; cwd?: string; dir?: string }[];
    const started = onPeer.find((s) => s.name.includes("started-remotely"));
    expect(started).toBeTruthy();
    // In the folder browsed to on the peer, not anything of this machine's.
    expect(started!.dir).toBe(peerDir);
    const card = page.locator(`[data-rail-id="${started!.id}"]`);
    await expect(card.locator('[data-rail-host="peerbox"]')).toBeVisible();
  });

  test("attachments follow the device they're started on", async ({ page }) => {
    await openViewer(page);
    await page.getByRole("button", { name: "New session", exact: true }).click();
    const goalBox = page.getByLabel("Describe your goal");
    const token = `${Date.now()}`;
    await goalBox.fill(`echo look ${token}`);

    // Attached while this device is the target...
    await pasteScreenshot(goalBox);
    const shotChip = page.getByLabel(/^Remove screenshot-.*\.png$/);
    const chipThumb = shotChip.locator("xpath=..").locator("img");
    await expect.poll(() => chipThumb.evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(320);

    // ...then the session moves to the peer: the file goes with it.
    // Blur first: the goal's key hint collapses on blur and moves the pills
    // mid-click (a launchpad quirk that predates mesh mode).
    await goalBox.blur();
    await page.locator('[data-launch-host=""]').click();
    await page.getByRole("button", { name: /^peerbox/ }).click();
    await expect.poll(() => chipThumb.evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(320);
    await expect(chipThumb).toHaveAttribute("src", /\/peers\/peerbox\/attachment-drafts\//);

    await page.getByRole("button", { name: "Start session" }).click();
    const panel = page.locator("#workspace-panel-conversation");
    const bubbleThumb = panel.getByAltText(/^screenshot-.*\.png$/);
    await expect.poll(() => bubbleThumb.evaluate((i: HTMLImageElement) => i.naturalWidth), { timeout: 20_000 }).toBe(320);
    await expect(panel.getByText(new RegExp(`you said look ${token}`))).toBeVisible({ timeout: 20_000 });

    // An open remote conversation's composer attaches straight to its device.
    const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
    await dropTextFile(page, composer, "trace.txt");
    await expect(page.getByLabel("Remove trace.txt")).toBeVisible();
    const send = page.getByRole("button", { name: /^(Send|Queue)$/ });
    await expect(send).toBeEnabled();
    await send.click();
    await expect(panel.getByRole("link", { name: /trace\.txt/ })).toBeVisible();
  });

  test("git and session management on another device: commit, land, archive", async ({ page }) => {
    const git = (...args: string[]) =>
      execFileSync("git", ["-c", "user.email=e2e@smoke.test", "-c", "user.name=e2esmoke", ...args], { cwd: peerDir, stdio: "pipe" })
        .toString()
        .trim();
    const base = git("branch", "--show-current");
    const token = `${Date.now()}`;
    const file = `mesh-${token}.txt`;
    const goal = `write-file ${file} alpha ${token}`;

    await openViewer(page);
    await page.getByRole("button", { name: "New session", exact: true }).click();
    await page.locator('[data-launch-host=""]').click();
    await page.getByRole("button", { name: /^peerbox/ }).click();
    await page.getByLabel("Describe your goal").fill(goal);
    const [created] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/peers/peerbox/sessions")),
      page.getByRole("button", { name: "Start session" }).click(),
    ]);
    const id = ((await created.json()) as { session: { id: string } }).session.id;
    await expect(page.getByText(`wrote ${file}`).first()).toBeVisible({ timeout: 20_000 });

    // Commit, from this device, in the peer's worktree.
    const work = page.locator("[data-work-state]");
    await expect(work).toHaveAttribute("data-work-state", "commit", { timeout: 15_000 });
    await work.click();
    const dialog = page.getByRole("alertdialog", { name: "Commit this session's changes" });
    await expect(dialog.getByLabel(`Include ${file}`)).toBeChecked();
    await dialog.getByLabel("Commit message").fill(`Add ${file}`);
    await dialog.getByRole("button", { name: "Commit 1 file" }).click();
    const panel = page.locator("#workspace-panel-conversation");
    await expect(panel.locator('[data-worktree-card="committed"]').last()).toContainText(`Add ${file}`);

    // Land: confirmed with its message (the Land preview and draft are read
    // and written over the mesh too), and the peer's own checkout gets it.
    await expect(work).toHaveAttribute("data-work-state", "land");
    await work.click();
    const landDialog = page.getByRole("alertdialog", { name: /^Land / });
    await expect(landDialog).toBeVisible();
    await landDialog.getByLabel("Landing message").fill(`Land ${file}`);
    await landDialog.getByRole("button", { name: /^Land into / }).click();
    await expect(landDialog).toHaveCount(0, { timeout: 20_000 });
    await expect(panel.locator('[data-worktree-card="landed"]')).toHaveCount(1);
    expect(git("show", `${base}:${file}`)).toBe(`alpha ${token}`);

    // Archive, from this device's rail: gone on the peer too.
    const rail = page.locator('[aria-label="Session list"]');
    const row = rail.locator(`[data-rail-id="${id}"]`);
    await row.hover();
    await row.locator("[aria-label^='Archive']").click();
    const confirm = page.getByRole("alertdialog");
    await confirm.getByRole("button", { name: /^((Close|Stop) and delete|Delete)$/ }).click();
    await expect(row).toHaveCount(0);
    await expect.poll(async () => (await apiFetch(`${peer.routerURL}/sessions/${id}`)).status, { timeout: 10_000 }).toBe(404);
  });

  test("the device menu: one device on its own, and a scope change never swaps the open pane", async ({ page }) => {
    await openViewer(page);
    await chooseScope(page, "peerbox");
    const rail = page.locator('[aria-label="Session list"]');
    const peerCard = rail.locator(`[data-rail-id="${peerSessionId}"]`);
    await expect(peerCard).toBeVisible();
    // Only that device's sessions: every card carries its host chip.
    const cards = rail.locator("[data-rail-card]");
    await expect.poll(async () => (await cards.count()) === (await rail.locator('[data-rail-card]:has([data-rail-host="peerbox"])').count())).toBe(true);

    // Open the peer's session, then show This device only: the pane stays.
    await peerCard.click();
    await expect(page.getByText("you said hello-from-peer")).toBeVisible();
    await chooseScope(page, "This device");
    await expect(peerCard).toHaveCount(0);
    await page.waitForTimeout(1500);
    await expect(page.getByText("you said hello-from-peer")).toBeVisible();
  });

  test("a session on another device shows its links in the rail", async ({ page }) => {
    await setConfig(viewer.routerURL, { feature_flags: { linked_sessions: true } });
    await setConfig(peer.routerURL, { feature_flags: { linked_sessions: true } });
    // Two sessions on the peer, in one tree (linked sessions share one), and
    // a Monitor link between them made on the peer itself.
    async function linkedPairOnPeer(label: string) {
      const stamp = `${label} ${Date.now()}`;
      const holder = await createSession(peer.routerURL, { name: `holder ${stamp}`, provider: "fake", isolation: "current_dir" });
      const subject = await createSession(peer.routerURL, { name: `subject ${stamp}`, provider: "fake", isolation: "current_dir" });
      return {
        holder,
        subject,
        link: async () => {
          const res = await apiFetch(`${peer.routerURL}/links`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ granting_session_id: holder.id, subject_session_id: subject.id, preset: "monitor" }),
          });
          expect(res.status).toBe(201);
        },
      };
    }
    const rail = page.locator('[aria-label="Session list"]');
    const nested = (name: string) => rail.locator('[class*="subjects"]').getByText(name, { exact: true });

    // The Router reads the peer's links in bulk. The link is made
    // after the rail is up, so its event stays on the peer's bus: the peer
    // poll is what brings it in.
    const current = await linkedPairOnPeer("current");
    // Two sessions on this device, in one tree, so they can link.
    const here = await createSession(viewer.routerURL, { name: `local linkable ${Date.now()}`, provider: "fake", isolation: "current_dir" });
    const here2 = await createSession(viewer.routerURL, { name: `local pickable ${Date.now()}`, provider: "fake", isolation: "current_dir" });
    await openViewer(page);
    await chooseScope(page, "All devices");
    await expect(rail.locator(`[data-rail-id="${current.subject.id}"]`)).toBeVisible();
    await expect(nested(current.subject.name)).toHaveCount(0);
    await current.link();
    await expect(nested(current.subject.name)).toBeVisible({ timeout: 25_000 });
    // Links on another device's sessions are read-only here: no chain
    // button on its cards, while this device's sessions keep theirs.
    await expect(rail.getByRole("button", { name: `Links for ${current.holder.name}` })).toHaveCount(0);
    await expect(rail.getByRole("button", { name: `Links for ${current.subject.name}` })).toHaveCount(0);
    await expect(rail.getByRole("button", { name: `Links for ${here.name}` })).toHaveCount(1);

    // Editing a local session's links, the rail picks only sessions on
    // this device: the peer's are greyed out as "different device".
    await rail.getByText(here.name, { exact: true }).hover();
    await rail.getByRole("button", { name: `Links for ${here.name}` }).click();
    const editor = page.getByRole("dialog", { name: `Links · ${here.name}` });
    await expect(editor).toBeVisible();
    const cardOf = (name: string) => rail.getByText(name, { exact: true }).locator("xpath=ancestor::*[@data-rail-card][1]");
    await expect(cardOf(current.holder.name)).toHaveAttribute("data-link-edit", "blocked");
    await expect(cardOf(current.holder.name)).toContainText("different device");
    // aria-disabled: Playwright would wait for it to enable, so click it
    // the way a pointer would anyway.
    await cardOf(current.holder.name).click({ force: true });
    await expect(editor.getByRole("group", { name: new RegExp(`^Edit link .*${current.holder.name}`) })).toHaveCount(0);
    await cardOf(here2.name).click();
    await expect(editor.getByRole("group", { name: `Edit link ${here.name} → ${here2.name}` })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    await page.getByRole("alertdialog").getByRole("button", { name: "Discard" }).click();
    await expect(editor).not.toBeVisible();
    expect(meshSawLinks.bulk).toBeGreaterThan(0);
  });

  test("a peer can't redirect the browser to this device's own API, nor serve it a page", async ({ page }) => {
    const local = await createSession(viewer.routerURL, { name: `local target ${Date.now()}`, provider: "fake" });
    const trustBefore = ((await (await apiFetch(`${viewer.routerURL}/sessions/${local.id}`)).json()) as { trust?: string }).trust;
    // The peer answers a trust change on its own session with a redirect to
    // this device's session: the browser must never get to replay it here.
    meshAnswer = (req, res) => {
      if (req.method === "POST" && req.url === `/sessions/${peerSessionId}/trust`) {
        res.writeHead(307, { Location: `/sessions/${local.id}/trust`, "Set-Cookie": "mads=x; Path=/" }).end();
        return true;
      }
      if (req.url?.startsWith(`/sessions/${peerSessionId}/attachments/`)) {
        res.writeHead(200, { "Content-Type": "text/html" }).end("<script>fetch('/credentials')</script>");
        return true;
      }
      return false;
    };
    try {
      page.allowStatus(`/sessions/${peerSessionId}/trust`);
      await openViewer(page);
      const got = await page.evaluate(async ([peerId]) => {
        const trust = await fetch(`/sessions/${peerId}/trust`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ trust: "full" }),
        });
        const page = await fetch(`/sessions/${peerId}/attachments/att_x`);
        return {
          status: trust.status,
          redirected: trust.redirected,
          code: ((await trust.json()) as { code?: string }).code,
          pageType: page.headers.get("content-type"),
          // The Router's own frame-ancestors rides beside the sandbox.
          sandboxed: /(^|, )sandbox; default-src 'none'/.test(page.headers.get("content-security-policy") ?? ""),
        };
      }, [peerSessionId]);
      expect(got).toEqual({ status: 502, redirected: false, code: "peer_redirect", pageType: "application/octet-stream", sandboxed: true });
      const after = ((await (await apiFetch(`${viewer.routerURL}/sessions/${local.id}`)).json()) as { trust?: string }).trust;
      expect(after).toBe(trustBefore);
      expect(await page.context().cookies()).toEqual([]);
    } finally {
      meshAnswer = null;
    }
  });

  // Settings → Paired devices (docs/MESH-AUTH-DESIGN.md D9). These run last:
  // the final one removes the peer.
  async function openPairedDevices(page: import("@playwright/test").Page) {
    await openViewer(page);
    await page.getByRole("button", { name: /Settings/ }).first().click();
    await expect(page.getByRole("heading", { name: "Paired devices" })).toBeVisible();
  }

  test("a peer whose mads has no GET /events/head reads as needing an update, and the window carries on", async ({ page }) => {
    // The peer predates the live-only event bus (THE CONTRACT in
    // ui/src/router/events.ts): its Mesh Service's route table has no
    // GET /events/head, so it answers as services/mesh does for any route
    // it doesn't list. Only that peer's sessions lose live updates; their
    // REST reads still run, and this device's bus, and the window, carry on.
    meshAnswer = (req, res) => {
      if (req.method === "GET" && req.url?.split("?")[0] === "/events/head") {
        res.writeHead(403, { "Content-Type": "application/json" });
        res.end('{"code":"not_exposed","error":"this route isn\'t available over the mesh"}');
        return true;
      }
      return false;
    };
    try {
      page.allowStatus("/events/head");
      await openViewer(page);
      await chooseScope(page, "All devices");
      const rail = page.locator('[aria-label="Session list"]');
      await rail.locator(`[data-rail-id="${peerSessionId}"]`).click();
      // Degraded, not stuck: the transcript (a REST read behind that bus's
      // barrier) loads, with a note that nothing live will follow.
      await expect(page.getByText("you said hello-from-peer")).toBeVisible();
      await expect(page.locator('[data-live-updates="unavailable"]')).toContainText("Live updates unavailable from peerbox");
      await expect(page.locator("[data-devices-down]")).toHaveText("peerbox needs update", { timeout: 15_000 });
      await expect(page.getByText("Restart mads (scripts/app.sh --restart)")).toHaveCount(0);
      // This device's bus still delivers: a session created here shows up.
      const here = await createSession(viewer.routerURL, { name: `still live ${Date.now()}`, provider: "fake" });
      await chooseScope(page, "This device");
      await expect(rail.locator(`[data-rail-id="${here.id}"]`)).toBeVisible({ timeout: 15_000 });
    } finally {
      meshAnswer = null;
    }
  });

  test("a send the host refuses for this device's grant stays as Not sent, with Why and Retry", async ({ page }) => {
    // The peer refuses this device's messages to its Full access sessions
    // (C1), as services/mesh answers.
    meshAnswer = (req, res) => {
      if (req.method === "POST" && req.url === `/sessions/${peerSessionId}/messages`) {
        res.writeHead(403, { "Content-Type": "application/json" });
        res.end('{"code":"full_trust_chat_refused","error":"this device may not message full-trust sessions here"}');
        return true;
      }
      return false;
    };
    try {
      page.allowStatus(`/sessions/${peerSessionId}/messages`);
      await openViewer(page);
      await chooseScope(page, "All devices");
      await page.locator('[aria-label="Session list"]').locator(`[data-rail-id="${peerSessionId}"]`).click();
      const composer = page.getByPlaceholder("Message the agent — @ to mention, / for commands");
      await composer.fill("echo refused-then-sent");
      await page.getByRole("button", { name: /^(Send|Queue)$/ }).click();
      const refused = page.locator('[data-refused-send="c1"]');
      await expect(refused).toContainText("Not sent");
      await expect(refused).toContainText("This device can't message Full access sessions on peerbox.");
      await expect(composer).toHaveValue("");
      await refused.getByRole("button", { name: "Why?" }).click();
      await expect(refused).toContainText("Settings → Paired devices");
      // Allowed again on the peer: Retry sends it.
      meshAnswer = null;
      await refused.getByRole("button", { name: "Retry" }).click();
      await expect(page.getByText("you said refused-then-sent")).toBeVisible({ timeout: 20_000 });
      await expect(refused).toHaveCount(0);
    } finally {
      meshAnswer = null;
    }
  });

  test("Paired devices: the peer's row reads both directions; a level change and Show sessions save at once", async ({ page }) => {
    await openPairedDevices(page);
    const row = page.locator('[data-device="peerbox"]');
    // What peerbox may do here (nothing yet), what it lets this device do
    // (its hello: control), and the status, worded by direction.
    await expect(row.getByRole("button", { name: /What peerbox may do with mads on this/ })).toHaveText("not use");
    await expect(row.locator("[data-lets]")).toHaveText("control");
    await expect(row.locator("[data-status]")).toContainText("Connected");
    await expect(row.getByRole("switch", { name: "Show mads sessions from peerbox" })).toHaveAttribute("aria-checked", "true");

    const grant = (r: import("@playwright/test").Response) => r.request().method() === "PUT" && /\/devices\/[A-Z2-7]{52}\/grant$/.test(r.url());
    const [viewPut] = await Promise.all([
      page.waitForResponse(grant),
      (async () => {
        await row.getByRole("button", { name: /What peerbox may do/ }).click();
        await page.getByRole("option", { name: /^view/ }).click();
      })(),
    ]);
    expect(viewPut.status()).toBe(200);
    expect(viewPut.request().postDataJSON()).toEqual({ level: "view" });
    await expect(row.getByRole("button", { name: /What peerbox may do/ })).toHaveText("view");

    // Raising to control asks first.
    await row.getByRole("button", { name: /What peerbox may do/ }).click();
    await page.getByRole("option", { name: /^control/ }).click();
    const confirm = page.getByRole("alertdialog", { name: /Let peerbox control mads on this/ });
    await expect(confirm).toContainText("start Full access sessions");
    const [controlPut] = await Promise.all([page.waitForResponse(grant), confirm.getByRole("button", { name: "Let it control" }).click()]);
    expect(controlPut.request().postDataJSON()).toEqual({ level: "control" });
    await expect(row.getByRole("button", { name: /What peerbox may do/ })).toHaveText("control");

    // The row's details: its limits and fingerprint, and its activity.
    await row.locator("td").first().click();
    await expect(page.getByText("Messages to Full access sessions")).toBeVisible();
    await expect(page.getByRole("list", { name: "Device activity" }).first()).toContainText("changed from view to control");

    // Show sessions off, then on: the existing /peers routes.
    const sw = row.getByRole("switch", { name: "Show mads sessions from peerbox" });
    const [off] = await Promise.all([
      page.waitForResponse((r) => r.request().method() === "DELETE" && r.url().endsWith("/peers/peerbox")),
      sw.click(),
    ]);
    expect(off.status()).toBe(204);
    await expect(sw).toHaveAttribute("aria-checked", "false");
    const [on] = await Promise.all([page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/peers")), sw.click()]);
    expect(on.status()).toBe(201);
    await expect(sw).toHaveAttribute("aria-checked", "true");
  });

  test("Paired devices: pair with a device by pasting its code", async ({ page }) => {
    const spark = await startStandIn({ name: "sparkbox", allow: deviceFingerprint(viewer.homeDir) });
    try {
      await openPairedDevices(page);
      await page.getByRole("button", { name: "Pair a device" }).click();
      await page.getByRole("button", { name: /Paste a pairing code/ }).click();
      const box = page.getByLabel("Pairing code from the other device");
      await box.fill("not a code");
      await expect(page.getByRole("alert")).toContainText("isn't a mads pairing code");
      await box.fill(spark.code());
      await expect(page.locator("[data-code-preview]")).toContainText("sparkbox");
      await page.getByRole("button", { name: "Ask to pair" }).click();
      await expect(page.getByRole("dialog", { name: "Waiting for sparkbox" })).toBeVisible();
      expect(spark.pair.requested).toBe(1);
      // sparkbox's user approves it, at control.
      spark.pair.state = "approved";
      const done = page.getByRole("dialog", { name: "Paired with sparkbox" });
      await expect(done).toContainText("sparkbox lets this", { timeout: 15_000 });
      await expect(done).toContainText("control");
      // This device's answer starts at "not use"; choosing saves it.
      const [put] = await Promise.all([
        page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith(`/devices/${spark.fingerprint}/grant`)),
        done.getByRole("radio", { name: "view" }).click(),
      ]);
      expect(put.request().postDataJSON()).toEqual({ level: "view" });
      await done.getByRole("button", { name: "Done" }).click();
      const row = page.locator('[data-device="sparkbox"]');
      await expect(row.getByRole("button", { name: /What sparkbox may do/ })).toHaveText("view");
      await expect(row.locator("[data-lets]")).toHaveText("control");
    } finally {
      await apiFetch(`${viewer.routerURL}/devices/${spark.fingerprint}/remove-everywhere`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      });
      await spark.close();
    }
  });

  test("Paired devices: a pairing request is approved from anywhere in the app", async ({ page }) => {
    // This device's Mesh Service once announced itself (pairing codes carry
    // its address); the e2e stack runs none.
    const ev = await apiFetch(`${viewer.dataURL}/events`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        type: "mesh.listening",
        actor: "mesh",
        payload: { node: "viewer", mesh_url: "http://127.0.0.1:9", fingerprint: deviceFingerprint(viewer.homeDir), platform: "linux", hosts_sessions: true, require_tailnet: true },
      }),
    });
    expect(ev.status).toBe(201);
    // The Router caches what it read for a few seconds.
    let made = { code: "" };
    await expect
      .poll(async () => {
        const r = await apiFetch(`${viewer.routerURL}/devices/pairing-code`, { method: "POST" });
        if (r.status === 201) made = (await r.json()) as { code: string };
        return r.status;
      }, { timeout: 20_000 })
      .toBe(201);
    await openViewer(page);
    // A device presents the code (its Mesh Service would, from here).
    const secret = JSON.parse(Buffer.from(made.code.split(":")[1], "base64url").toString()) as { s: string };
    const joiner = newDevice("thinkpad").fingerprint;
    const redeem = await apiFetch(`${viewer.dataURL}/pairing/redeem`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ secret_hash: createHash("sha256").update(Buffer.from(secret.s, "base64url")).digest("hex"), fingerprint: joiner, name: "thinkpad", platform: "linux" }),
    });
    expect(redeem.status).toBeLessThan(300);
    // Not in Settings: a toast says who's asking, and opens the prompt.
    const toast = page.getByRole("status").filter({ hasText: "thinkpad wants to pair" });
    await expect(toast).toBeVisible({ timeout: 15_000 });
    await toast.getByRole("button", { name: "Review…" }).click();
    const prompt = page.getByRole("alertdialog", { name: "thinkpad wants to pair" });
    await expect(prompt).toContainText("Linux");
    // The whole fingerprint, to compare; administer is never offered.
    await expect(prompt.getByLabel(/^Fingerprint /)).toBeVisible();
    await expect(prompt.getByRole("radio", { name: "administer" })).toHaveCount(0);
    await prompt.getByRole("radio", { name: "chat with" }).click();
    await prompt.getByRole("button", { name: "Pair" }).click();
    await expect(page.getByText("thinkpad is paired")).toBeVisible();
    await page.getByRole("button", { name: /Settings/ }).first().click();
    await expect(page.locator('[data-device="thinkpad"]').getByRole("button", { name: /What thinkpad may do/ })).toHaveText("chat with");
    const rm = await apiFetch(`${viewer.routerURL}/devices/${joiner}/remove-everywhere`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
    expect(rm.status).toBe(200);
  });

  test("Paired devices: Remove device… removes the peer here", async ({ page }) => {
    await openPairedDevices(page);
    const row = page.locator('[data-device="peerbox"]');
    await row.locator("td").first().click();
    await page.getByRole("button", { name: "Remove device…" }).click();
    const dialog = page.getByRole("alertdialog", { name: "Remove peerbox?" });
    await expect(dialog).toContainText("(this device)");
    await expect(dialog).toContainText("never seen can't be checked");
    await dialog.getByRole("button", { name: "Remove device", exact: true }).click();
    const results = page.getByRole("dialog", { name: /peerbox removed from this/ });
    await expect(results.getByRole("list", { name: "Results by device" })).toContainText("Removed");
    await results.getByRole("button", { name: "Done" }).click();
    await expect(row).toHaveCount(0);
    const left = (await (await apiFetch(`${viewer.routerURL}/devices`)).json()) as { name: string }[];
    expect(left.map((d) => d.name)).not.toContain("peerbox");
  });
});
