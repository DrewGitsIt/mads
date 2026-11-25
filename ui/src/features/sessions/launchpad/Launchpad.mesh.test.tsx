import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getMeshSnapshot, peerOfSession, refreshPeers, resetMeshForTests } from "../../../router/mesh";
import { FakeEventSource } from "../../../router/testFakeEventSource";
import type { PeerStatus } from "../../../router/types";
import { Launchpad } from "./Launchpad";

const popOs: PeerStatus = {
  id: "per_1",
  name: "pop-os",
  mesh_url: "http://pop-os.ts.net:17380",
  created_at: "",
  updated_at: "",
  reachability: "connected",
  access: ["view", "control"],
};

/** Answers the Router's routes by path; records every request. */
function stubRouter(peers: PeerStatus[]) {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(typeof input === "string" ? input : input.toString(), "http://localhost:17373");
      const method = init?.method ?? "GET";
      calls.push({ method, path: url.pathname, body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined });
      const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status });
      if (url.pathname === "/peers") return json(peers);
      if (method === "POST" && url.pathname === "/peers/pop-os/sessions") {
        return json({ session: { id: "ses_new", name: "Remote goal", activity: "active" }, addr: "" }, 201);
      }
      if (method === "POST" && url.pathname.includes("/attachment-drafts/")) {
        return json({ id: `att_${calls.length}`, name: "shot.png", size: 3 }, 201);
      }
      if (method === "DELETE") return new Response(null, { status: 204 });
      if (url.pathname.endsWith("/sessions/defaults")) return json({});
      if (url.pathname.endsWith("/config")) return json({});
      return json([]);
    }),
  );
  return calls;
}

describe("Launchpad on another device (mesh mode)", () => {
  beforeEach(() => {
    localStorage.clear();
    resetMeshForTests();
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });
  afterEach(() => {
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("offers no host pill without a device that can take a session", async () => {
    stubRouter([{ ...popOs, access: ["view"] }]);
    render(<Launchpad createSession={vi.fn()} onSessionCreated={vi.fn()} />);
    await act(() => refreshPeers());
    expect(screen.queryByText("This device")).toBeNull();
  });

  it("starts the session on the chosen device, through the peer proxy", async () => {
    const calls = stubRouter([popOs]);
    const localCreate = vi.fn();
    const onSessionCreated = vi.fn();
    render(<Launchpad createSession={localCreate} onSessionCreated={onSessionCreated} />);
    await act(() => refreshPeers());

    fireEvent.click(screen.getByText("This device"));
    fireEvent.click(await screen.findByRole("button", { name: /^pop-os/ }));
    // Per-device reads now go to the peer.
    await waitFor(() => expect(calls.some((c) => c.path === "/peers/pop-os/sessions/defaults")).toBe(true));

    fireEvent.change(screen.getByLabelText("Describe your goal"), { target: { value: "Remote goal" } });
    fireEvent.click(screen.getByRole("button", { name: "Start session" }));

    await waitFor(() => expect(onSessionCreated).toHaveBeenCalled());
    expect(localCreate).not.toHaveBeenCalled();
    const create = calls.find((c) => c.method === "POST" && c.path === "/peers/pop-os/sessions");
    expect(create?.body).toMatchObject({ name: "Remote goal" });
    // Tagged with its host before anything reads it, and the list switched
    // to All devices so it doesn't vanish from the rail.
    expect(onSessionCreated.mock.calls[0][0].host).toMatchObject({ name: "pop-os", self: false });
    expect(peerOfSession("ses_new")).toBe("pop-os");
    expect(getMeshSnapshot().scope).toBe("all");
    // This device's config is never touched for a remote start.
    expect(calls.some((c) => c.path.startsWith("/peers/pop-os/config"))).toBe(false);
  });

  it("staged files follow the device: re-uploaded to the peer and started with it", async () => {
    const calls = stubRouter([popOs]);
    const onSessionCreated = vi.fn();
    render(<Launchpad createSession={vi.fn()} onSessionCreated={onSessionCreated} />);
    await act(() => refreshPeers());

    fireEvent.change(screen.getByLabelText("Attach files"), {
      target: { files: [new File(["png"], "shot.png", { type: "image/png" })] },
    });
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path.startsWith("/attachment-drafts/"))).toBe(true));

    fireEvent.click(screen.getByText("This device"));
    fireEvent.click(await screen.findByRole("button", { name: /^pop-os/ }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST" && c.path.startsWith("/peers/pop-os/attachment-drafts/"))).toBe(true),
    );
    // Gone from this device.
    expect(calls.some((c) => c.method === "DELETE" && c.path.startsWith("/attachment-drafts/"))).toBe(true);

    fireEvent.change(screen.getByLabelText("Describe your goal"), { target: { value: "Remote goal" } });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Start session" }) as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Start session" }));
    await waitFor(() => expect(onSessionCreated).toHaveBeenCalled());
    const upload = calls.find((c) => c.path.startsWith("/peers/pop-os/attachment-drafts/"))!;
    const draft = upload.path.split("/")[4];
    const create = calls.find((c) => c.method === "POST" && c.path === "/peers/pop-os/sessions");
    expect(create?.body).toMatchObject({ attachment_draft: draft });
  });
});
