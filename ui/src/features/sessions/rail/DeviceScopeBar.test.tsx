import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getMeshSnapshot, refreshPeers, resetMeshForTests } from "../../../router/mesh";
import type { PeerStatus } from "../../../router/types";
import { DeviceScopeBar } from "./DeviceScopeBar";

const peer = (over: Partial<PeerStatus> = {}): PeerStatus => ({
  id: "per_1",
  name: "pop-os",
  mesh_url: "http://pop-os.ts.net:17380",
  created_at: "",
  updated_at: "",
  reachability: "connected",
  ...over,
});

function servePeers(peers: PeerStatus[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(JSON.stringify(peers), { status: 200 })),
  );
}

describe("DeviceScopeBar", () => {
  beforeEach(() => {
    localStorage.clear();
    resetMeshForTests();
  });
  afterEach(() => {
    resetMeshForTests();
    vi.unstubAllGlobals();
  });

  it("isn't there on a single-device install", async () => {
    servePeers([]);
    const { container } = render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    expect(container.innerHTML).toBe("");
  });

  it("isn't there when every device is hidden", async () => {
    servePeers([peer({ hidden: true })]);
    const { container } = render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    expect(container.innerHTML).toBe("");
  });

  it("offers This device, All devices and each device, and switches", async () => {
    servePeers([peer(), peer({ id: "per_2", name: "spark", reachability: "unreachable" })]);
    render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    const trigger = screen.getByRole("button", { name: "Show sessions from: This device" });
    fireEvent.click(trigger);
    const items = screen.getAllByRole("menuitemradio").map((i) => i.textContent);
    expect(items).toEqual(["This device", "All devices", "pop-os", "sparkoffline"]);
    fireEvent.click(screen.getByRole("menuitemradio", { name: /pop-os/ }));
    expect(getMeshSnapshot().scope).toBe("peer:pop-os");
    expect(screen.getByRole("button", { name: "Show sessions from: pop-os" })).toBeTruthy();
  });

  it("falls back to This device when the chosen device is gone", async () => {
    localStorage.setItem("mads.mesh.scope", "peer:old-box");
    resetMeshForTests();
    servePeers([peer()]);
    render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    expect(screen.getByRole("button", { name: "Show sessions from: This device" })).toBeTruthy();
  });

  it("says which device is down, on All devices", async () => {
    localStorage.setItem("mads.mesh.scope", "all");
    resetMeshForTests();
    servePeers([peer({ reachability: "unreachable", reason: "connection refused" })]);
    render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    expect((await screen.findByText("pop-os offline")).getAttribute("title")).toContain("connection refused");
  });

  it("says a device isn't paired rather than offline", async () => {
    localStorage.setItem("mads.mesh.scope", "all");
    resetMeshForTests();
    servePeers([peer({ reachability: "unpaired", reason: "no key is pinned for pop-os" })]);
    render(<DeviceScopeBar />);
    await act(() => refreshPeers());
    expect((await screen.findByText("pop-os not paired")).getAttribute("title")).toContain("no key is pinned");
  });
});
