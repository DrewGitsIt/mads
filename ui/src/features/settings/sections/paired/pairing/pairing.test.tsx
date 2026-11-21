import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { resetDevicesForTests, type JoinStatus as Status, type PairRequest } from "../../../../../router/devices";
import { CopyCode } from "./CopyCode";
import { JoinStatus } from "./JoinStatus";
import { initialApprovalLevel, PAIR_ARM_MS, PairRequestPrompt } from "./PairRequestPrompt";
import { holdPairPromptOpen, PairRequestWatcher } from "./PairRequestWatcher";
import { PasteCode } from "./PasteCode";
import { ToastViewport } from "../../../../../components/Toast/Toast";
import { fp, MAC, row, SELF, stubRouter } from "../testRouter";

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
    fireEvent.change(box, { target: { value: "pop-os 4TXP-R55O" } });
    expect(screen.getByRole("alert").textContent).toBe("That isn't a mads pairing code. Copy the whole line from the other device.");
    fireEvent.change(box, { target: { value: code() } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.querySelector("[data-code-preview]")!.textContent).toContain("spark");
    expect(document.querySelector("[data-code-preview]")!.textContent).toContain("KKKK-KKKK-KKKK-KKKK");
    expect(ask.disabled).toBe(false);
  });

  it("refuses this device's own code, a device already paired, and an expired code", () => {
    stubRouter({});
    render(<PasteCode self={SELF} rows={[MAC]} onJoined={() => {}} onCancel={() => {}} />);
    const box = screen.getByLabelText("Pairing code from the other device");
    fireEvent.change(box, { target: { value: code({ f: SELF.fingerprint }) } });
    expect(screen.getByRole("alert").textContent).toBe("That's this device's own code.");
    fireEvent.change(box, { target: { value: code({ f: MAC.fingerprint }) } });
    expect(screen.getByRole("alert").textContent).toBe("Drews-MacBook-Air is already paired here. Remove it first to pair again.");
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
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("the device answering isn't spark"));
    expect(calls[0].body).toEqual({ code: code() });
  });
});

describe("CopyCode", () => {
  it("shows the code, single use, with its countdown; Stop retires it", async () => {
    const calls = stubRouter({
      "POST /devices/pairing-code": { id: "pc_1", code: "mads-pair-1:abc", expires_at: new Date(Date.now() + 300_000).toISOString(), host: { name: "pop-os", fingerprint: SELF.fingerprint } },
      "DELETE /devices/pairing-code/pc_1": null,
      "GET /devices/pair-requests": [],
    });
    const onClose = vi.fn();
    render(<CopyCode host="pop-os" onClose={onClose} />);
    await waitFor(() => expect(screen.getByLabelText("Pairing code").textContent).toBe("mads-pair-1:abc"));
    expect(screen.getByText(/^Works once · expires in [45]:\d\d$/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
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
    const prompt = screen.getByRole("alertdialog", { name: "spark wants to pair" });
    expect(prompt.textContent).toContain("macOS · used this pop-os's pairing code");
    expect(within(prompt).getByLabelText(`Fingerprint ${SPARK_FP.match(/.{4}/g)!.join(" ")}`)).toBeTruthy();
    expect(within(prompt).getAllByRole("radio").map((r) => r.textContent)).toEqual(["not use", "view", "chat with", "control"]);
    expect(within(prompt).getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
    expect(prompt.textContent).toContain("spark will be able to start Full access sessions");
    fireEvent.click(within(prompt).getByRole("radio", { name: "view" }));
    expect(prompt.textContent).not.toContain("will be able to start Full access sessions");
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
    await waitFor(() => expect((screen.getByRole("button", { name: "Pair" }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(screen.getByRole("button", { name: "Pair" }));
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
    const prompt = screen.getByRole("alertdialog", { name: "spark wants to pair" });
    expect(prompt.textContent).toContain("on your tailnet as spark-mbp");
    expect(within(prompt).getByRole("note").textContent).toContain("You already have a device called spark. Compare fingerprints");
  });

  it("a request answered elsewhere says so", async () => {
    stubRouter({
      "POST /devices/pair-requests/pr_1/approve": () => new Response(JSON.stringify({ code: "already_answered", error: "x" }), { status: 409 }),
      "GET /devices/pair-requests": [],
    });
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    await waitFor(() => expect((screen.getByRole("button", { name: "Pair" }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
    fireEvent.click(screen.getByRole("button", { name: "Pair" }));
    fireEvent.click(screen.getByRole("button", { name: "Let it control" }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toBe("This request was already answered, here or from the CLI."));
  });

  it("opens with Decline focused and Pair held back for a moment", () => {
    vi.useFakeTimers();
    stubRouter({});
    render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
    expect(document.activeElement?.textContent).toBe("Decline");
    const pair = screen.getByRole("button", { name: "Pair" }) as HTMLButtonElement;
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
    it("asks first: Pair (by click, Enter or Space) only opens the confirmation; approving takes its own button", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved", name: "spark" }), "GET /devices/pair-requests": [], "GET /devices": [] });
      const onDone = vi.fn();
      render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={onDone} />);
      // Control is preselected for a computer (APPROVAL_DEFAULT "suggested").
      expect(screen.getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
      act(() => vi.advanceTimersByTime(PAIR_ARM_MS));
      const pair = screen.getByRole("button", { name: "Pair" });
      pair.focus();
      // Enter and Space on a focused button are its click.
      fireEvent.keyDown(pair, { key: "Enter" });
      fireEvent.click(pair);
      const confirm = screen.getByRole("alertdialog", { name: "Let spark control mads on this pop-os?" });
      expect(confirm.textContent).toContain("spark will be able to start Full access sessions, run a shell, and commit, land and push on pop-os.");
      // Enter/Space again lands on the confirmation's focused control, Cancel: still nothing approved.
      expect(document.activeElement?.textContent).toBe("Cancel");
      fireEvent.click(document.activeElement!);
      expect(calls.some((c) => c.path.endsWith("/approve"))).toBe(false);
      // Back on the prompt, the level kept; Pair, then Let it control approves.
      expect(screen.getByRole("radio", { name: "control" }).getAttribute("aria-checked")).toBe("true");
      fireEvent.click(screen.getByRole("button", { name: "Pair" }));
      fireEvent.click(screen.getByRole("button", { name: "Let it control" }));
      await waitFor(() => expect(onDone).toHaveBeenCalledWith({ approved: true, level: "control", name: "spark" }));
      expect(calls.filter((c) => c.path.endsWith("/approve")).map((c) => c.body)).toEqual([{ level: "control" }]);
    });

    it("below control, Pair approves at once", async () => {
      const calls = stubRouter({ "POST /devices/pair-requests/pr_1/approve": request({ state: "approved" }), "GET /devices/pair-requests": [], "GET /devices": [] });
      render(<PairRequestPrompt request={request()} selfName="pop-os" host="pop-os" onDone={() => {}} />);
      fireEvent.click(screen.getByRole("radio", { name: "chat with" }));
      await waitFor(() => expect((screen.getByRole("button", { name: "Pair" }) as HTMLButtonElement).disabled).toBe(false), { timeout: 2000 });
      fireEvent.click(screen.getByRole("button", { name: "Pair" }));
      await waitFor(() => expect(calls.some((c) => c.path.endsWith("/approve"))).toBe(true));
      expect(screen.queryByRole("alertdialog", { name: /Let spark/ })).toBeNull();
    });
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
    await waitFor(() => expect(screen.getByText("spark wants to pair")).toBeTruthy());
    expect(screen.queryByRole("alertdialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Review…" }));
    expect(screen.getByRole("alertdialog", { name: "spark wants to pair" })).toBeTruthy();
  });

  it("on Paired devices, the prompt opens by itself", async () => {
    stubRouter({ "GET /devices/pair-requests": [request()], "GET /devices/self": SELF });
    const release = holdPairPromptOpen();
    render(<PairRequestWatcher />);
    await waitFor(() => expect(screen.getByRole("alertdialog", { name: "spark wants to pair" })).toBeTruthy());
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
    render(<JoinStatus initial={pending} selfName="Drews-MacBook-Air" host="MacBook-Air" rows={[sparkRow]} onClose={() => {}} />);
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
