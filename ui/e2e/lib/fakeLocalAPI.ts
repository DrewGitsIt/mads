import http from "node:http";
import { mkdtempSync, rmSync } from "node:fs";
import path from "node:path";

/**
 * A stand-in tailscaled LocalAPI on a Unix socket, for a Router started
 * with -localapi-socket (docs/MESH-NETWORK-DESIGN.md "As built: Part 1",
 * test hooks): the few routes mads reads (status, whois,
 * login-interactive), answered from `state`. Nothing here talks to a real
 * tailscaled or reads the real macOS app's token.
 */
export interface FakeTailnetPeer {
  HostName: string;
  DNSName: string;
  OS: string;
  TailscaleIPs: string[];
  Online: boolean;
  UserID?: number;
}

export interface FakeLocalAPI {
  socket: string;
  state: {
    backendState: string;
    /** This node's key expiry (null: expiry off). */
    keyExpiry: string | null;
    authURL: string;
    peers: FakeTailnetPeer[];
    /** WhoIs answers: the remote's user. */
    userID: number;
    logins: number;
  };
  close(): Promise<void>;
}

const SELF_USER = 4242;

export async function startFakeLocalAPI(): Promise<FakeLocalAPI> {
  // A short path: a Unix socket's path is limited to ~104 bytes on macOS.
  const dir = mkdtempSync("/tmp/mads-lapi-");
  const socket = path.join(dir, "ts.sock");
  const state: FakeLocalAPI["state"] = { backendState: "Running", keyExpiry: null, authURL: "", peers: [], userID: SELF_USER, logins: 0 };
  const server = http.createServer((req, res) => {
    const url = new URL(req.url ?? "/", "http://local-tailscaled.sock");
    const json = (status: number, body: unknown) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };
    const p = url.pathname.replace(/^\/localapi\/v0\//, "");
    if (req.method === "GET" && p === "status") {
      const peer: Record<string, unknown> = {};
      if (url.searchParams.get("peers") !== "false") {
        state.peers.forEach((n, i) => (peer[`nodekey:${i}`] = { UserID: SELF_USER, Tags: [], ...n }));
      }
      return json(200, {
        BackendState: state.backendState,
        AuthURL: state.authURL,
        Self: {
          HostName: "netbox",
          DNSName: "netbox.tail0e2e.ts.net.",
          OS: "macOS",
          UserID: SELF_USER,
          TailscaleIPs: ["127.0.0.1"],
          Online: true,
          KeyExpiry: state.keyExpiry,
          Expired: false,
        },
        Peer: peer,
        CurrentTailnet: { Name: "e2e@example.com", MagicDNSSuffix: "tail0e2e.ts.net" },
      });
    }
    if (req.method === "GET" && p === "whois") {
      return json(200, {
        Node: { Name: "spark.tail0e2e.ts.net.", User: state.userID, Tags: [], PrimaryRoutes: [], AllowedIPs: ["127.0.0.1/32"] },
        UserProfile: { ID: state.userID, LoginName: "e2e@example.com" },
        CapMap: {},
      });
    }
    if (req.method === "POST" && p === "login-interactive") {
      state.logins++;
      state.authURL = "https://login.tailscale.com/a/e2e";
      res.writeHead(204).end();
      return;
    }
    json(404, { error: `no route ${p}` });
  });
  await new Promise<void>((r) => server.listen(socket, r));
  return {
    socket,
    state,
    async close() {
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
      rmSync(dir, { recursive: true, force: true });
    },
  };
}
