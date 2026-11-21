import http2 from "node:http2";
import { randomBytes } from "node:crypto";
import type { TLSSocket } from "node:tls";
import type { AddressInfo } from "node:net";
import { certFingerprint, newDevice } from "./meshTLS";

/**
 * A small stand-in Mesh Service for the Paired devices journeys
 * (docs/MESH-AUTH-DESIGN.md D2, D6): mutual TLS with its own device key,
 * only `allow`'s client key let in, answering the few mesh routes a host's
 * Settings exercises — /mesh/hello (what it lets the caller do), the
 * joiner's /mesh/pair and its poll, and an admin's DELETE /devices/{fp}.
 * Everything is in this process: no tailscaled, no real tailnet.
 */
export interface StandIn {
  name: string;
  fingerprint: string;
  /** http://127.0.0.1:port, as a mesh_url is stored (always dialled with TLS). */
  meshURL: string;
  /** The hello's access list ([] reads as "doesn't let this device use mads"). */
  access: string[];
  platform: string;
  /** /mesh/pair's request, and what the next poll answers. */
  pair: { requested: number; state: "pending" | "approved" | "declined"; level: string };
  /** DELETE /devices/{fp} calls it saw. */
  deletes: string[];
  /** A pairing code for this device, in pkg/paircode's format. */
  code(expiresInSec?: number): string;
  close(): Promise<void>;
}

export async function startStandIn(opts: { name: string; allow: string; access?: string[]; platform?: string }): Promise<StandIn> {
  const device = newDevice(opts.name);
  const sockets = new Set<TLSSocket>();
  const s: Omit<StandIn, "meshURL" | "close" | "code"> = {
    name: opts.name,
    fingerprint: device.fingerprint,
    access: opts.access ?? ["view", "chat", "control"],
    platform: opts.platform ?? "darwin",
    pair: { requested: 0, state: "pending", level: "control" },
    deletes: [],
  };
  const server = http2.createSecureServer(
    { key: device.keyPem, cert: device.certPem, minVersion: "TLSv1.3", requestCert: true, rejectUnauthorized: false, allowHTTP1: true },
    (req, res) => {
      const json = (status: number, body: unknown) => {
        res.writeHead(status, { "Content-Type": "application/json" });
        res.end(JSON.stringify(body));
      };
      const url = req.url ?? "";
      if (url === "/mesh/hello") {
        return json(200, {
          node: opts.name,
          dns_name: `${opts.name}.ts.net`,
          mads_commit: "e2e",
          mesh_api: 2,
          access: s.access,
          device: device.fingerprint,
          platform: s.platform,
          hosts_sessions: true,
        });
      }
      if (req.method === "POST" && url === "/mesh/pair") {
        s.pair.requested++;
        return json(201, { request_id: "pr_standin", state: "pending", expires_at: new Date(Date.now() + 300_000).toISOString() });
      }
      if (req.method === "GET" && url === "/mesh/pair/pr_standin") {
        return json(200, { state: s.pair.state, level: s.pair.state === "approved" ? s.pair.level : "" });
      }
      // A host with no sessions yet: its list is empty.
      if (req.method === "GET" && /^\/sessions(\?|$)/.test(url)) return json(200, []);
      const del = /^\/devices\/([A-Z2-7]{52})(\?also_introduced=1)?$/.exec(url);
      if (req.method === "DELETE" && del) {
        s.deletes.push(del[1]);
        return json(200, { introduced_by_this: [], also_removed: [] });
      }
      json(404, { code: "not_found", error: "no such route on the stand-in" });
    },
  );
  server.on("secureConnection", (sock: TLSSocket) => {
    const cert = sock.getPeerCertificate();
    if (!cert?.raw || certFingerprint(cert.raw) !== opts.allow) {
      sock.destroy();
      return;
    }
    sockets.add(sock);
    sock.on("close", () => sockets.delete(sock));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const port = (server.address() as AddressInfo).port;
  const meshURL = `http://127.0.0.1:${port}`;
  return Object.assign(s, {
    meshURL,
    code(expiresInSec = 300) {
      const body = {
        n: opts.name,
        u: meshURL,
        f: device.fingerprint,
        e: Math.floor(Date.now() / 1000) + expiresInSec,
        s: randomBytes(32).toString("base64url"),
      };
      return `mads-pair-1:${Buffer.from(JSON.stringify(body)).toString("base64url")}`;
    },
    async close() {
      for (const sock of sockets) sock.destroy();
      await new Promise((r) => server.close(r));
    },
  });
}
