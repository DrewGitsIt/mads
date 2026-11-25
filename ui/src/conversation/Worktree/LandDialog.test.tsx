import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { FakeRouterClient, fixtureIsolatedRepoState, fixtureIsolatedSession, fixtureLandPreview, fixtureRepoWorktree } from "../../router/fixtures";
import { FakeEventSource } from "../../router/testFakeEventSource";
import type { LandPreview, LandResult, Session } from "../../router/types";
import { LandDialog } from "./LandDialog";

const SID = fixtureIsolatedSession.id;
const NESTED = "/home/user/repo/mads-nested";

// The dialog reads the repo state store, which listens on the event bus.
beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});
afterEach(() => vi.unstubAllGlobals());

function open(
  client: FakeRouterClient,
  props: Partial<Parameters<typeof LandDialog>[0]> = {},
  session: Session = { ...fixtureIsolatedSession, uncommitted_count: 0 },
) {
  const onClose = vi.fn();
  const onLanded = vi.fn();
  const onResult = vi.fn();
  const onCommitDialog = vi.fn();
  render(
    <LandDialog
      open
      sessionId={SID}
      routerClient={client}
      session={session}
      fastModel
      onClose={onClose}
      onLanded={onLanded}
      onResult={onResult}
      onCommitDialog={onCommitDialog}
      {...props}
    />,
  );
  return { onClose, onLanded, onResult, onCommitDialog };
}

const preview = (over: Partial<LandPreview>): LandPreview => ({ ...structuredClone(fixtureLandPreview), ...over });
const message = () => screen.getByLabelText("Landing message") as HTMLTextAreaElement;
const landButton = () => screen.getByRole("button", { name: /^Land into |^Landing…$/ }) as HTMLButtonElement;

async function ready(title = "Land mads/session/session-isolated into main?") {
  return screen.findByRole("alertdialog", { name: title });
}

describe("LandDialog (COMMIT-LAND-PUSH D12)", () => {
  it("reads the branch first, then shows the base, the commits to squash and what Land does", async () => {
    const client = new FakeRouterClient();
    let answer!: (p: LandPreview) => void;
    vi.spyOn(client, "getLandPreview").mockReturnValueOnce(new Promise((r) => (answer = r)));
    open(client);
    expect((await screen.findByRole("status")).textContent).toBe("Reading the branch…");
    await act(async () => answer(preview({ to_land: 60 })));

    await ready();
    expect(screen.getByTestId("land-base").textContent).toBe("Basemainthis session's baseChange");
    const list = screen.getByRole("list", { name: "Commits to land" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "c0ffee1 deploy: detect Ubuntu 24.04 runners by codename",
      "b0a7ed5 deploy: add runner_detect.sh",
      "a11ce90 deploy: drop old_detect.sh",
      "and 57 more",
    ]);
    expect(screen.getByText("Lands as one commit on main; mads/session/session-isolated then moves to it.")).toBeTruthy();
    // The message starts empty, and Land waits for one.
    expect(message().value).toBe("");
    expect(landButton().textContent).toBe("Land into main");
    expect(landButton().disabled).toBe(true);
    fireEvent.change(message(), { target: { value: "   " } });
    expect(landButton().disabled).toBe(true);
    fireEvent.change(message(), { target: { value: "deploy: Ubuntu runners" } });
    expect(landButton().disabled).toBe(false);
  });

  it.each<[string, string]>([
    ["recorded", "from the git worktree add this session ran"],
    ["confirmed", "chosen in an earlier Land"],
    ["reflog", "from the branch's creation"],
    ["default", "the repo's default branch — check it's right"],
    ["session", "this session's base"],
    ["chosen", "your choice"],
  ])("says where a %s base came from", async (source, words) => {
    const client = new FakeRouterClient();
    client.landPreview = preview({ base_source: source });
    open(client);
    await ready();
    expect(screen.getByTestId("land-base").textContent).toContain(words);
  });

  it("changes the base from the branches it could land into, and reads the branch again", async () => {
    const client = new FakeRouterClient();
    open(client);
    await ready();
    expect(screen.queryByRole("combobox", { name: "Base branch" })).toBeNull();
    fireEvent.click(within(screen.getByTestId("land-base")).getByRole("button", { name: "Change" }));
    const select = screen.getByRole("combobox", { name: "Base branch" }) as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual(["main", "release"]);
    fireEvent.change(select, { target: { value: "release" } });

    await ready("Land mads/session/session-isolated into release?");
    expect(client.landPreviewCalls.at(-1)).toEqual({ sessionId: SID, repo: undefined, base: "release" });
    expect(screen.getByTestId("land-base").textContent).toContain("your choice");
    fireEvent.change(message(), { target: { value: "release: deploy" } });
    fireEvent.click(screen.getByRole("button", { name: "Land into release" }));
    await waitFor(() => expect(client.landRequests).toHaveLength(1));
    expect(client.landRequests[0].req).toEqual({ base: "release", message: "release: deploy" });
  });

  it("drafts the message with the Fast Model into the field, and never lands on its own", async () => {
    const client = new FakeRouterClient();
    client.landDraftOutcome = { message: "deploy: detect runners by codename", truncated: true };
    open(client);
    await ready();
    fireEvent.click(screen.getByRole("button", { name: "Draft with Fast Model" }));
    await waitFor(() => expect(message().value).toBe("deploy: detect runners by codename"));
    expect(client.landDraftCalls).toEqual([{ sessionId: SID, repo: undefined, base: undefined }]);
    expect(screen.getByText(/saw only part of it/)).toBeTruthy();
    expect(client.landCalls).toEqual([]);
  });

  it("says why a draft couldn't be made", async () => {
    const client = new FakeRouterClient();
    client.landDraftOutcome = { code: "no_fast_model", error: "No Fast Model is set up." };
    open(client);
    await ready();
    fireEvent.click(screen.getByRole("button", { name: "Draft with Fast Model" }));
    expect(await screen.findByText("No Fast Model is set up.")).toBeTruthy();
    expect(message().value).toBe("");
  });

  it("hides the draft link without a Fast Model", async () => {
    open(new FakeRouterClient(), { fastModel: false });
    await ready();
    expect(screen.queryByRole("button", { name: "Draft with Fast Model" })).toBeNull();
  });

  it("holds Land when it would be refused, saying why", async () => {
    const client = new FakeRouterClient();
    client.landPreview = preview({ blocked: { reason: "busy", detail: "An agent is working in this repo." } });
    open(client);
    await ready();
    expect(screen.getByText("An agent is working in this repo.")).toBeTruthy();
    fireEvent.change(message(), { target: { value: "deploy" } });
    expect(landButton().disabled).toBe(true);
  });

  it("lands with the user's message, hands the result back and closes", async () => {
    const client = new FakeRouterClient();
    const { onLanded, onClose } = open(client);
    await ready();
    fireEvent.change(message(), { target: { value: "  deploy: Ubuntu runners  " } });
    fireEvent.click(landButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(client.landRequests).toEqual([{ sessionId: SID, req: { message: "deploy: Ubuntu runners" } }]);
    expect(onLanded).toHaveBeenCalledWith(expect.objectContaining({ outcome: "landed", base: "main" }));
    // The outcome first: a caller tells a landing from a Cancel by the time it closes.
    expect(onLanded.mock.invocationCallOrder[0]).toBeLessThan(onClose.mock.invocationCallOrder[0]);
  });

  it("runs the caller's land when given one (the conversation's, for its card)", async () => {
    const client = new FakeRouterClient();
    const land = vi.fn().mockResolvedValue({ outcome: "landed", base: "main" } satisfies LandResult);
    open(client, { land });
    await ready();
    fireEvent.change(message(), { target: { value: "deploy" } });
    fireEvent.click(landButton());
    await waitFor(() => expect(land).toHaveBeenCalledWith({ message: "deploy" }));
    expect(client.landCalls).toEqual([]);
  });

  it.each<[LandResult["outcome"], Partial<LandResult>]>([
    ["conflicted", { files: ["a.go"] }],
    ["auto_resolved", { files: ["a.go"], merge_commit: "9f8e7d6" }],
  ])("closes on %s, the conversation's card says what's next", async (outcome, extra) => {
    const client = new FakeRouterClient();
    client.landOutcome = { outcome, base: "main", ...extra };
    const { onResult, onClose, onLanded } = open(client);
    await ready();
    fireEvent.change(message(), { target: { value: "deploy" } });
    fireEvent.click(landButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(onResult).toHaveBeenCalledWith(expect.objectContaining({ outcome }));
    expect(onLanded).not.toHaveBeenCalled();
  });

  it("keeps a refusal in place, in worktreeCopy's words, and stays open with the message", async () => {
    const client = new FakeRouterClient();
    client.landOutcome = { outcome: "refused", reason: "locked", base: "main" };
    const { onClose, onCommitDialog } = open(client);
    await ready();
    fireEvent.change(message(), { target: { value: "deploy" } });
    fireEvent.click(landButton());
    const alert = await screen.findByRole("alert");
    expect(alert.getAttribute("data-reason")).toBe("locked");
    expect(alert.textContent).toContain("Land is busy right now.");
    expect(within(alert).queryByRole("button", { name: "Commit them…" })).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
    expect(onCommitDialog).not.toHaveBeenCalled();
    expect(message().value).toBe("deploy");
  });

  it("shows git's own words for a failed merge", async () => {
    const client = new FakeRouterClient();
    client.landOutcome = { outcome: "refused", reason: "merge_failed", base: "main", detail: "fatal: refusing to merge unrelated histories" };
    open(client);
    await ready();
    fireEvent.change(message(), { target: { value: "deploy" } });
    fireEvent.click(landButton());
    const pre = await screen.findByText("fatal: refusing to merge unrelated histories");
    expect(pre.tagName).toBe("PRE");
  });

  it("offers Commit them… for a refusal over uncommitted files (D6)", async () => {
    const client = new FakeRouterClient();
    client.landOutcome = { outcome: "refused", reason: "dirty", base: "main", files: ["scratch.txt"], outside: ["scratch.txt"] };
    const { onCommitDialog, onClose } = open(client);
    await ready();
    fireEvent.change(message(), { target: { value: "deploy" } });
    fireEvent.click(landButton());
    fireEvent.click(await screen.findByRole("button", { name: "Commit them…" }));
    expect(onCommitDialog).toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  describe("with more than one branch to land", () => {
    function twoBranches(client: FakeRouterClient) {
      client.sessionRepos[SID] = [
        structuredClone(fixtureIsolatedRepoState),
        {
          root: NESTED,
          worktree: fixtureRepoWorktree({
            path: NESTED,
            git_dir: "/home/user/repo/mads/.git",
            branch: "nested-delegates",
            base: "main",
            base_source: "recorded",
            to_land: 1,
          }),
        },
      ];
      client.landPreview = (opts) =>
        opts.repo === NESTED
          ? preview({ repo: NESTED, branch: "nested-delegates", base_source: "recorded", commits: [], to_land: 1 })
          : preview({});
    }

    it("picks one branch at a time, the session's own first", async () => {
      const client = new FakeRouterClient();
      twoBranches(client);
      const { onLanded } = open(client);
      const picker = await screen.findByRole("radiogroup", { name: "Branch to land" });
      expect(picker.textContent).toBe("mads/session/session-isolated→ main↑3nested-delegates→ main↑1");
      const radios = within(picker).getAllByRole("radio") as HTMLInputElement[];
      expect(radios[0].checked).toBe(true);
      await ready();

      fireEvent.change(message(), { target: { value: "written for the first" } });
      fireEvent.click(radios[1]);
      await ready("Land nested-delegates into main?");
      expect(client.landPreviewCalls.at(-1)).toEqual({ sessionId: SID, repo: NESTED, base: undefined });
      expect(screen.getByTestId("land-base").textContent).toContain("from the git worktree add this session ran");
      // Another branch gets its own message.
      expect(message().value).toBe("");
      fireEvent.change(message(), { target: { value: "nested delegates" } });
      fireEvent.click(landButton());
      await waitFor(() => expect(onLanded).toHaveBeenCalled());
      expect(client.landRequests[0].req).toEqual({ repo: NESTED, message: "nested delegates" });
    });

    it("starts on the branch it was opened for", async () => {
      const client = new FakeRouterClient();
      twoBranches(client);
      open(client, { initialRoot: NESTED });
      await ready("Land nested-delegates into main?");
      const radios = within(screen.getByRole("radiogroup", { name: "Branch to land" })).getAllByRole("radio") as HTMLInputElement[];
      expect(radios[1].checked).toBe(true);
    });

    it("has no picker for a single branch", async () => {
      open(new FakeRouterClient());
      await ready();
      expect(screen.queryByRole("radiogroup")).toBeNull();
    });
  });

  it("aborts its preview read when it closes", async () => {
    const client = new FakeRouterClient();
    let signal: AbortSignal | undefined;
    vi.spyOn(client, "getLandPreview").mockImplementation((_id, opts) => {
      signal = opts?.signal;
      return new Promise(() => {});
    });
    const props = {
      sessionId: SID,
      routerClient: client,
      session: { ...fixtureIsolatedSession, uncommitted_count: 0 },
      fastModel: true,
      onClose: vi.fn(),
      onLanded: vi.fn(),
      onResult: vi.fn(),
      onCommitDialog: vi.fn(),
    };
    const { rerender } = render(<LandDialog open {...props} />);
    await waitFor(() => expect(signal).toBeDefined());
    expect(signal!.aborted).toBe(false);
    rerender(<LandDialog open={false} {...props} />);
    expect(signal!.aborted).toBe(true);
  });
});
