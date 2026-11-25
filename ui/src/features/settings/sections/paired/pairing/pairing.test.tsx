import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, type JoinStatus as Status, type PairRequest } from "../../../../../router/devices";
import { CopyCode, READY_RETRY_MS } from "./CopyCode";
import { JoinStatus } from "./JoinStatus";
import { initialApprovalLevel, PAIR_ARM_MS, PairRequestPrompt, promptView, shownFor } from "./PairRequestPrompt";
import { holdPairPromptOpen, PairRequestWatcher } from "./PairRequestWatcher";
import { PasteCode } from "./PasteCode";
import { ToastViewport } from "../../../../../components/Toast/Toast";
import { fp, MAC, row, SELF, stubRouter, type Call } from "../testRouter";

const SPARK_FP = fp("K");

function code(over: Record<string, unknown> = {}): string {
  const body = { n: "spark", u: "http://spark.ts.net:17380", f: SPARK_FP, e: Math.floor(Date.now() / 1000) + 300, s: btoa("x".repeat(32)).replace(/=+$/, ""), ...over };
  return `mads-pair-1:${btoa(JSON.stringify(body)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "")}`;
}

const request = (over: Partial<PairRequest> = {}): PairRequest => ({
  id: "pr_1",
  fingerprint: SPARK_FP,
  name: "spark",
  platform: "darwin",
  state: "pending",
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 300_000).toISOString(),
  ...over,
});

afterEach(() => {
  resetDevicesForTests();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("PasteCode", () => {
  it("previews the host from the code, and says what's wrong with a bad one", () => {
    stubRouter({});
    render(<PasteCode self={SELF} rows={[MAC]} onJoined={() => {}} onCancel={() => {}} />);
    const box = screen.getByLabelText("Pairing code from the other device");
    const ask = screen.getByRole("button", { name: "Ask to pair" }) as HTMLButtonElement;
    expect(ask.disabled).toBe(true);
    fireEvent.change(box, { target: { value: "pop-os UQDU-PBGT" } });
    expect(screen.getByRole("alert").textContent).toBe("That isn't a mads pairing code. Copy the whole line from the other device.");
    fireEvent.change(box, { target: { value: code() } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.querySelector("[data-code-preview]")!.textContent).toContain("spark");
    expect(document.querySelector("[data-code-preview]")!.textContent).toContain("KKKK-KKKK-KKKK-KKKK");
    expect(ask.disabled).toBe(false);
  });

  it("refuses this device's own code and an expired one; a device already paired (and still paired there) offers Update addresses", () => {
    stubRouter({});
    render(<PasteCode self={SELF} rows={[{ ...MAC, pairing_there: "paired" }]} onJoined={() => {}} onCancel={() => {}} />);
    const box = screen.getByLabelText("Pairing code from the other device");
    fireEvent.change(box, { target: { value: code({ f: SELF.fingerprint }) } });
    expect(screen.getByRole("alert").textContent).toBe("That's this device's own code.");
    fireEvent.change(box, { target: { value: code({ f: MAC.fingerprint }) } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.querySelector("[data-update-addresses]")!.textContent).toBe(
      "Alexs-MacBook-Air is already paired here. This updates where this pop-os looks for it.",
    );
    expect(screen.getByRole("button", { name: "Update addresses" })).toBeTruthy();
    fireEvent.change(box, { target: { value: code({ e: 1 }) } });
    expect(screen.getByRole("alert").textContent).toBe("That code has expired. Ask for a new one.");
  });

  it("sends it, and shows the host's refusal", async () => {
    const calls = stubRouter({
      "POST /devices/pair": () => new Response(JSON.stringify({ code: "host_key_mismatch", error: "the device answering isn't spark" }), { status: 502 }),
    });
    render(<PasteCode self={SELF} rows={[]} onJoined={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("The device answering isn't spark."));
    expect(calls[0].body).toEqual({ code: code() });
  });
});

describe("PasteCode, a host already paired here (F6 R6, R7)", () => {
  const SPARK = row({ name: "spark", fingerprint: SPARK_FP, mesh_url: "http://spark:1", remote: { platform: "linux", hosts_sessions: true } });

  it("a host not known to still have this device is asked first; repair_offered offers Re-pair, which re-posts with repair", async () => {
    const calls = stubRouter({
      "POST /devices/pair": (c: Call) =>
        (c.body as { repair?: boolean }).repair
          ? { id: "j1", state: "pending", host: { name: "spark", fingerprint: SPARK_FP }, expires_at: new Date(Date.now() + 300_000).toISOString() }
          : new Response(JSON.stringify({ code: "repair_offered", error: "x", peer_id: "per_spark", name: "spark" }), { status: 409 }),
    });
    const onJoined = vi.fn();
    render(<PasteCode self={SELF} rows={[{ ...SPARK, pairing_there: "not_paired" }]} onJoined={onJoined} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    expect(document.querySelector("[data-update-addresses]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    const line = await waitFor(() => {
      const el = document.querySelector("[data-repair-offered]");
      if (!el) throw new Error("no repair line");
      return el;
    });
    expect(line.textContent).toContain("spark no longer has this pop-os paired.");
    fireEvent.click(screen.getByRole("button", { name: "Re-pair" }));
    await waitFor(() => expect(onJoined).toHaveBeenCalled());
    expect(calls.map((c) => c.body)).toEqual([{ code: code() }, { code: code(), repair: true }]);
  });

  it("an unchecked host is asked first too; already_paired_update stays Update addresses", async () => {
    const calls = stubRouter({
      "POST /devices/pair": (c: Call) =>
        (c.body as { update_addresses?: boolean }).update_addresses
          ? { update_addresses: true, peer_id: "per_spark", name: "spark" }
          : new Response(JSON.stringify({ code: "already_paired_update", error: "x", name: "spark", peer_id: "per_spark" }), { status: 409 }),
      "GET /devices": [],
    });
    const onUpdated = vi.fn();
    render(<PasteCode self={SELF} rows={[SPARK]} onJoined={() => {}} onCancel={() => {}} onUpdated={onUpdated} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    fireEvent.click(await screen.findByRole("button", { name: "Update addresses" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledWith("spark"));
    expect(calls.filter((c) => c.path === "/devices/pair").map((c) => c.body)).toEqual([{ code: code() }, { code: code(), update_addresses: true }]);
  });

  it("a retryable failure: Try again with the same code, held back for Retry-After", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let n = 0;
    const calls = stubRouter({
      "POST /devices/pair": () =>
        ++n === 1
          ? new Response(JSON.stringify({ code: "rate_limited", error: "x", retryable: true, retry_after: 2 }), { status: 429 })
          : new Response(JSON.stringify({ code: "pairing_unavailable", error: "x", retryable: true }), { status: 503 }),
    });
    render(<PasteCode self={SELF} rows={[]} onJoined={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    const wait = (await screen.findByRole("button", { name: /^Try again in \d s$/ })) as HTMLButtonElement;
    expect(wait.disabled).toBe(true);
    expect(screen.getByRole("alert").textContent).toBe("spark is getting too many pairing attempts right now. Wait a moment, then try again.");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_500);
    });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("spark's mads isn't ready. Try again in a moment."));
    expect((screen.getByRole("button", { name: "Try again" }) as HTMLButtonElement).disabled).toBe(false);
    expect(calls).toHaveLength(2);
  });

  it("couldn't connect: Couldn't reach, its tips, and Try again with the same code (R7)", async () => {
    const calls = stubRouter({
      "POST /devices/pair": () => new Response(JSON.stringify({ code: "host_unreachable", error: "x", retryable: true, port: "17380" }), { status: 502 }),
    });
    render(<PasteCode self={SELF} rows={[]} onJoined={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    await waitFor(() => expect(document.querySelector("[data-tips=host_unreachable]")).toBeTruthy());
    const tips = document.querySelector("[data-tips=host_unreachable]")!;
    expect(tips.textContent).toContain("Couldn't reach spark, so the code wasn't used.");
    expect(tips.textContent).toContain("port 17380");
    expect(screen.queryByRole("button", { name: "Check again" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(calls.filter((c) => c.path === "/devices/pair")).toHaveLength(2));
  });

  it("a failure that isn't retryable gets no Try again", async () => {
    stubRouter({
      "POST /devices/pair": () => new Response(JSON.stringify({ code: "pairing_refused", error: "that code was already used" }), { status: 403 }),
    });
    render(<PasteCode self={SELF} rows={[]} onJoined={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText("Pairing code from the other device"), { target: { value: code() } });
    fireEvent.click(screen.getByRole("button", { name: "Ask to pair" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("That code was already used."));
    expect(screen.queryByRole("button", { name: /Try again/ })).toBeNull();
  });
});

describe("CopyCode", () => {
  it("right after setup (no listener yet), it waits, never back to choosing a network (T5)", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let n = 0;
    stubRouter({
      "POST /devices/pairing-code": () =>
        ++n < 3
          ? new Response(JSON.stringify({ code: "no_mesh_network", error: "x" }), { status: 409 })
          : { id: "pc_1", code: "mads-pair-2:abc", expires_at: new Date(Date.now() + 300_000).toISOString(), host: { name: "pop-os", fingerprint: SELF.fingerprint } },
      "GET /devices/pair-requests": [],
    });
    const onSetUp = vi.fn();
    render(<CopyCode host="pop-os" onClose={() => {}} onSetUp={onSetUp} configured />);
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("Getting this device ready…"));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(READY_RETRY_MS * 2 + 50);
    });
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("mads-pair-2:abc"));
    expect(onSetUp).not.toHaveBeenCalled();
  });

  it("with no mesh network at all, Copy starts Set up mesh", async () => {
    stubRouter({ "POST /devices/pairing-code": () => new Response(JSON.stringify({ code: "no_mesh_network", error: "x" }), { status: 409 }), "GET /devices/pair-requests": [] });
    const onSetUp = vi.fn();
    render(<CopyCode host="pop-os" onClose={() => {}} onSetUp={onSetUp} configured={false} />);
    await waitFor(() => expect(onSetUp).toHaveBeenCalled());
  });

  it("shows the code, single use, with its countdown; Close retires it", async () => {
    const calls = stubRouter({
      "POST /devices/pairing-code": { id: "pc_1", code: "mads-pair-1:abc", expires_at: new Date(Date.now() + 300_000).toISOString(), host: { name: "pop-os", fingerprint: SELF.fingerprint } },
      "DELETE /devices/pairing-code/pc_1": null,
      "GET /devices/pair-requests": [],
    });
    const onClose = vi.fn();
    render(<CopyCode host="pop-os" onClose={onClose} />);
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("mads-pair-1:abc"));
    expect(screen.getByText(/Works once · expires in [45]:\d\d$/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.path === "/devices/pairing-code/pc_1")).toBe(true));
  });

  it("says when it expired, and makes a new one", async () => {
    let n = 0;
    stubRouter({
      "POST /devices/pairing-code": () => ({ id: `pc_${++n}`, code: `mads-pair-1:c${n}`, expires_at: new Date(Date.now() + (n === 1 ? -1000 : 300_000)).toISOString(), host: { name: "pop-os", fingerprint: "" } }),
      "GET /devices/pair-requests": [],
    });
    render(<CopyCode host="pop-os" onClose={() => {}} />);
    await waitFor(() => expect(screen.getByRole("dialog", { name: "Pairing code expired" })).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Make a new code" }));
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("mads-pair-1:c2"));
  });

  it("explains when this device can't make one", async () => {
    stubRouter({
      "POST /devices/pairing-code": () => new Response(JSON.stringify({ code: "no_mesh_service", error: "this device's Mesh Service has never run" }), { status: 409 }),
      "GET /devices/pair-requests": [],
    });
    render(<CopyCode host="pop-os" onClose={() => {}} />);
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("this device's Mesh Service has never run"));
  });
});

describe("PairRequestPrompt", () => {
  it("shows who's asking with the whole fingerprint, suggests by kind, never offers administer", () => {
    stubRouter({});
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    const prompt = screen.getByRole("alertdialog", { name: "spark wants to join your mesh" });
    expect(prompt.textContent).toContain("macOS · KKKK-KKKK-KKKK-KKKK · used this pop-os's pairing code");
    expect(within(prompt).getByLabelText(`Fingerprint ${SPARK_FP.match(/.{4}/g)!.join(" ")}`)).toBeTruthy();
    // It points at what the GUI user has: This device's fingerprint on the other device, not a CLI command.
    expect(prompt.textContent).toContain("Compare with the fingerprint next to This device on spark");
    expect(prompt.textContent).not.toContain("mads device");
    // The same lines as the chip's popover and a row's details (Q9).
    const g = SPARK_FP.match(/.{4}/g)!;
    expect([...prompt.querySelectorAll("[aria-label^='Fingerprint '] > div")].map((d) => d.textContent)).toEqual([g.slice(0, 7).join("-") + "-", g.slice(7).join("-")]);
    expect(within(prompt).getAllByRole("radio").map((r) => r.textContent)).toEqual(["not use", "view", "chat with", "control"]);
    expect(within(prompt).getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
    expect(prompt.textContent).toContain("spark will be able to start Full access sessions");
    fireEvent.click(within(prompt).getByRole("radio", { name: "view" }));
    expect(prompt.textContent).not.toContain("will be able to start Full access sessions");
  });

  it("a key already paired here: it wants to pair again, and approving replaces that (F6 R2)", () => {
    stubRouter({});
    render(<PairRequestPrompt request={request({ repair: { peer_id: "per_spark", level: "control" } })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.querySelector("[data-repair]")!.textContent).toBe("spark wants to pair again. It's paired here now (may control); approving replaces that.");
    expect(screen.getByRole("alertdialog", { name: "spark wants to pair again" })).toBeTruthy();
  });

  it("a re-pairing approves from the prompt: at the level it has, or raised with the warning on screen", async () => {
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
    const onDone = vi.fn();
    const { unmount } = render(
      <PairRequestPrompt request={request({ repair: { peer_id: "per_spark", level: "control" } })} selfName="pop-os" host="pop-os" onDone={onDone} />,
    );
    await waitFor(() => expect((screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(screen.getByRole("button", { name: /^Add/ }));
    expect(screen.queryByRole("alertdialog", { name: "Let spark control mads on this pop-os?" })).toBeNull();
    await waitFor(() => expect(onDone).toHaveBeenCalledWith({ approved: true, level: "control", name: "spark" }));
    expect(calls.find((c) => c.path.endsWith("/approve"))?.body).toEqual({ level: "control" });
    unmount();
    render(<PairRequestPrompt request={request({ repair: { peer_id: "per_spark", level: "view" } })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    await waitFor(() => expect((screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    expect(document.body.textContent).toContain("spark will be able to start Full access sessions");
    fireEvent.click(screen.getByRole("button", { name: "Add with control" }));
    expect(screen.queryByRole("alertdialog", { name: "Let spark control mads on this pop-os?" })).toBeNull();
    await waitFor(() => expect(calls.filter((c) => c.path.endsWith("/approve")).length).toBe(2));
  });

  it("a new key says nothing about pairing again", () => {
    stubRouter({});
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.querySelector("[data-repair]")).toBeNull();
  });

  it("suggests chat for a phone and view for an unknown platform", () => {
    stubRouter({});
    const { unmount } = render(<PairRequestPrompt request={request({ platform: "ios" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(screen.getByRole("radio", { name: "chat with" }).getAttribute("aria-checked")).toBe("true");
    unmount();
    render(<PairRequestPrompt request={request({ platform: "" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(screen.getByRole("radio", { name: "view" }).getAttribute("aria-checked")).toBe("true");
  });

  it("approves at the chosen level, or declines", async () => {
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "POST /devices/pair-requests/pr_1/decline": request({ state: "declined" }), "GET /devices/pair-requests": [], "GET /devices": [] });
    const onDone = vi.fn();
    const { unmount } = render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={onDone} />);
    fireEvent.click(screen.getByRole("radio", { name: "chat with" }));
    await waitFor(() => expect((screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(screen.getByRole("button", { name: /^Add/ }));
    await waitFor(() => expect(onDone).toHaveBeenCalledWith({ approved: true, level: "chat", name: "spark" }));
    expect(calls.find((c) => c.path.endsWith("/approve"))?.body).toEqual({ level: "chat" });
    unmount();
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={onDone} />);
    fireEvent.click(screen.getByRole("button", { name: "Decline" }));
    await waitFor(() => expect(calls.some((c) => c.path.endsWith("/decline"))).toBe(true));
  });

  it("shows the verified tailnet node, and warns when the name is already taken", () => {
    stubRouter({});
    render(<PairRequestPrompt request={request({ tailnet_node: "spark-mbp", name_collides: true })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    const prompt = screen.getByRole("alertdialog", { name: "spark wants to join your mesh" });
    expect(prompt.textContent).toContain("Tailscale says spark-mbp");
    // Only when it says something the name doesn't (Q12).
    cleanup();
    render(<PairRequestPrompt request={request({ tailnet_node: "spark.tailnet.ts.net" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.body.textContent).not.toContain("Tailscale says");
    expect(within(prompt).getByRole("note").textContent).toContain("You already have a device called spark. Compare fingerprints");
  });

  it("a request answered elsewhere says so", async () => {
    stubRouter({
      "POST /devices/pair-requests/pr_1/approve": () => new Response(JSON.stringify({ code: "already_answered", error: "x" }), { status: 409 }),
      "GET /devices/pair-requests": [],
    });
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    await waitFor(() => expect((screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(screen.getByRole("button", { name: /^Add/ }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("This request was already answered, here or from the CLI."));
  });

  it("opens with Decline focused and Pair held back for a moment", () => {
    vi.useFakeTimers();
    stubRouter({});
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.activeElement?.textContent).toBe("Decline");
    const pair = screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement;
    expect(pair.disabled).toBe(true);
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS - 1));
    expect(pair.disabled).toBe(true);
    act(() => vi.advanceTimersByTime(1));
    expect(pair.disabled).toBe(false);
  });

  it("the starting level is one switch: the kind's suggestion, or view with the suggestion one click away", () => {
    expect(initialApprovalLevel("computer", "suggested")).toBe("control");
    expect(initialApprovalLevel("phone", "suggested")).toBe("chat");
    expect(initialApprovalLevel("computer", "view")).toBe("view");
    stubRouter({});
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    fireEvent.click(screen.getByRole("radio", { name: "view" }));
    fireEvent.click(screen.getByRole("button", { name: "control" }));
    expect(screen.getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
  });

  describe("pairing at control", () => {
    it("Add names the level, and at control it is the confirmation: the warning is on screen, no second dialog (P1)", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved", name: "spark" }), "GET /devices/pair-requests": [], "GET /devices": [] });
      const onDone = vi.fn();
      render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={onDone} />);
      // Control is preselected for a computer (APPROVAL_DEFAULT "suggested"), its warning showing.
      expect(screen.getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
      const prompt = screen.getByRole("alertdialog", { name: "spark wants to join your mesh" });
      expect(prompt.textContent).toContain("spark will be able to start Full access sessions, run a shell, and commit, land and push on pop-os.");
      const add = screen.getByRole("button", { name: "Add with control" }) as HTMLButtonElement;
      // Still held back for a moment, Decline focused: a stray click approves nothing.
      expect(add.disabled).toBe(true);
      expect(document.activeElement?.textContent).toBe("Decline");
      fireEvent.click(add);
      expect(calls.some((c) => c.path.endsWith("/approve"))).toBe(false);
      act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
      fireEvent.click(add);
      expect(screen.queryByRole("alertdialog", { name: /^Let spark/ })).toBeNull();
      await waitFor(() => expect(onDone).toHaveBeenCalledWith({ approved: true, level: "control", name: "spark" }));
      expect(calls.filter((c) => c.path.endsWith("/approve")).map((c) => c.body)).toEqual([{ level: "control" }]);
    });

    it("the button follows the level", () => {
      stubRouter({});
      render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
      fireEvent.click(screen.getByRole("radio", { name: "chat with" }));
      expect(screen.getByRole("button", { name: "Add with chat" })).toBeTruthy();
      fireEvent.click(screen.getByRole("radio", { name: "not use" }));
      expect(screen.getByRole("button", { name: "Add without access" })).toBeTruthy();
    });

    it("below control, Pair approves at once", async () => {
      const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
      render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
      fireEvent.click(screen.getByRole("radio", { name: "chat with" }));
      await waitFor(() => expect((screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
      fireEvent.click(screen.getByRole("button", { name: /^Add/ }));
      await waitFor(() => expect(calls.some((c) => c.path.endsWith("/approve"))).toBe(true));
      expect(screen.queryByRole("alertdialog", { name: /Let spark/ })).toBeNull();
    });
  });
});

describe("PairRequestPrompt, a found device (M1, M2)", () => {
  const found = (over: Partial<PairRequest> = {}) => request({ mode: "discovered", vouched: true, check_code: "482913", network_label: "Tailscale", ...over });
  const approveBodies = (calls: { path: string; body?: unknown }[]) => calls.filter((c) => c.path.endsWith("/approve")).map((c) => c.body);
  const addBtn = () => screen.getByRole("button", { name: /^Add/ }) as HTMLButtonElement;

  it("vouched: says so, and the approval says the vouch was shown", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
    render(<PairRequestPrompt request={found()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(screen.getByText(/Tailscale confirms spark is signed in to your tailnet as you/)).toBeTruthy();
    expect(document.querySelector("[data-check-code]")).toBeNull();
    fireEvent.click(screen.getByRole("radio", { name: "view" }));
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
    fireEvent.click(addBtn());
    await waitFor(() => expect(approveBodies(calls)).toEqual([{ level: "view", shown_vouched: true }]));
  });

  it("a vouch withdrawn mid-prompt re-arms Add and refocuses Decline; a click meant for the old view approves nothing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
    const { rerender } = render(<PairRequestPrompt request={found()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    fireEvent.click(screen.getByRole("radio", { name: "view" }));
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
    expect(addBtn().disabled).toBe(false);
    addBtn().focus();
    rerender(<PairRequestPrompt request={found({ vouch_withheld: "another_request" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    // The number shows now; Add waits again, Decline has the focus.
    expect(document.querySelector('[data-check-code="482913"]')).toBeTruthy();
    expect(addBtn().disabled).toBe(true);
    expect(document.activeElement?.textContent).toBe("Decline");
    fireEvent.click(addBtn());
    expect(approveBodies(calls)).toEqual([]);
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
    fireEvent.click(addBtn());
    await waitFor(() => expect(approveBodies(calls)).toEqual([{ level: "view", check_code_confirmed: true }]));
  });

  it("at control, a change mid-prompt re-arms Add with control: a click meant for the vouch approves nothing", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
    const { rerender } = render(<PairRequestPrompt request={found()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
    expect(addBtn().textContent).toBe("Add with control");
    rerender(<PairRequestPrompt request={found({ vouch_withheld: "another_request" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.querySelector('[data-check-code="482913"]')).toBeTruthy();
    expect(addBtn().disabled).toBe(true);
    fireEvent.click(addBtn());
    expect(approveBodies(calls)).toEqual([]);
  });

  it("a new number re-arms too", () => {
    vi.useFakeTimers();
    stubRouter({});
    const { rerender } = render(<PairRequestPrompt request={found({ vouched: false })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
    expect(addBtn().disabled).toBe(false);
    rerender(<PairRequestPrompt request={found({ vouched: false, check_code: "111222" })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(addBtn().disabled).toBe(true);
  });

  it("a found device with no number to compare can only be declined", () => {
    vi.useFakeTimers();
    stubRouter({});
    render(<PairRequestPrompt request={found({ vouched: false, check_code: undefined })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    act(() => vi.advanceTimersByTime(PAIR_ARM_MS * 2));
    expect(screen.queryByRole("button", { name: /^Add/ })).toBeNull();
    expect(document.querySelector("[data-no-check-code]")).toBeTruthy();
    expect(shownFor(promptView(found({ vouched: false, check_code: undefined })))).toBeUndefined();
  });

  it("a name collision gives one instruction", () => {
    stubRouter({});
    render(<PairRequestPrompt request={found({ vouched: true, vouch_withheld: "name_collides", name_collides: true })} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    const text = document.body.textContent ?? "";
    expect(text).not.toContain("Compare fingerprints");
    expect(text).toContain("Compare the number");
  });
});

describe("PairRequestWatcher", () => {
  it("away from Paired devices, a toast says who's asking and opens the prompt", async () => {
    stubRouter({ "GET /devices/pair-requests": [request()], "GET /devices/self": SELF });
    render(
      <ToastViewport>
        <PairRequestWatcher />
      </ToastViewport>,
    );
    await waitFor(() => expect(screen.getByText("spark wants to join your mesh")).toBeTruthy());
    expect(screen.queryByRole("alertdialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Review…" }));
    expect(screen.getByRole("alertdialog", { name: "spark wants to join your mesh" })).toBeTruthy();
  });

  it("on Paired devices, the prompt opens by itself", async () => {
    stubRouter({ "GET /devices/pair-requests": [request()], "GET /devices/self": SELF });
    const release = holdPairPromptOpen();
    render(<PairRequestWatcher />);
    await waitFor(() => expect(screen.getByRole("alertdialog", { name: "spark wants to join your mesh" })).toBeTruthy());
    act(() => release());
  });
});

describe("JoinStatus", () => {
  const pending: Status = { id: "pr_1", state: "pending", host: { name: "spark", fingerprint: SPARK_FP }, expires_at: new Date(Date.now() + 300_000).toISOString() };
  const sparkRow = row({ name: "spark", fingerprint: SPARK_FP, mesh_url: "http://spark:1", viewed: true, remote: { platform: "darwin", hosts_sessions: true, level: "control", as_of: new Date().toISOString() } });

  it("waits for the host, then shows what it granted and asks this device's own answer", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const calls = stubRouter({
      "GET /devices/pair/pr_1": { ...pending, state: "approved", level: "control" },
      "GET /devices": [sparkRow],
      [`PUT /devices/${SPARK_FP}/grant`]: {},
    });
    render(<JoinStatus initial={pending} selfName="Alexs-MacBook-Air" host="MacBook-Air" rows={[sparkRow]} onClose={() => {}} />);
    expect(screen.getByRole("dialog", { name: "Waiting for spark" })).toBeTruthy();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2100);
    });
    const done = await screen.findByRole("dialog", { name: "Paired with spark" });
    expect(done.textContent).toContain("spark lets this MacBook-Air control mads there.");
    expect(within(done).getByRole("switch").getAttribute("aria-checked")).toBe("true");
    // The reverse starts at "not use", with a hint for a computer.
    expect(within(done).getByRole("radio", { name: "not use" }).getAttribute("aria-checked")).toBe("true");
    expect(done.textContent).toContain("Suggested: control");
    fireEvent.click(within(done).getByRole("radio", { name: "view" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ level: "view" }));
  });

  it("raising the host to control asks first, as the table does (Q8); Cancel sets nothing", async () => {
    const approved = { ...pending, state: "approved" as const, level: "control" as const };
    const calls = stubRouter({ "GET /devices": [sparkRow], [`PUT /devices/${SPARK_FP}/grant`]: {} });
    render(<JoinStatus initial={approved} selfName="Alexs-MacBook-Air" host="MacBook-Air" rows={[sparkRow]} onClose={() => {}} />);
    const done = screen.getByRole("dialog", { name: "Paired with spark" });
    fireEvent.click(within(done).getByRole("radio", { name: "control" }));
    const confirm = screen.getByRole("alertdialog", { name: "Let spark control mads on this MacBook-Air?" });
    expect(confirm.textContent).toContain("spark will be able to start Full access sessions, run a shell, and commit, land and push on Alexs-MacBook-Air.");
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    // Escape closes the confirmation only, not the dialog under it.
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(screen.getByRole("dialog", { name: "Paired with spark" })).toBeTruthy();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    fireEvent.click(within(done).getByRole("radio", { name: "control" }));
    fireEvent.click(screen.getByRole("button", { name: "Let it control" }));
    await waitFor(() => expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ level: "control" }));
  });

  it("says when the host declined", () => {
    stubRouter({});
    render(<JoinStatus initial={{ ...pending, state: "declined" }} selfName="x" host="x" rows={[]} onClose={() => {}} />);
    expect(screen.getByRole("dialog", { name: "spark declined" }).textContent).toContain("Nothing was paired.");
  });

  it("the waiting screen shows this device's own short fingerprint", () => {
    stubRouter({});
    render(
      <JoinStatus
        initial={{ id: "KKKK-KKKK-KKKK-KKKK:pr_1", request_id: "pr_1", self: "SSSS-SSSS-SSSS-SSSS", state: "pending", host: { name: "spark", fingerprint: fp("K") }, expires_at: new Date().toISOString() }}
        selfName="pop-os"
        host="pop-os"
        rows={[]}
        onClose={() => {}}
      />,
    );
    expect(document.querySelector("[data-join-self]")!.textContent).toBe("This device: SSSS-SSSS-SSSS-SSSS. spark will see it in its prompt: check they match.");
  });
});
