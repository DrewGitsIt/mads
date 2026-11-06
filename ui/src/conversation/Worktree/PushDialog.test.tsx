import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { FakeRouterClient, fixtureIsolatedSession, fixturePushPreview } from "../../router/fixtures";
import type { PushPreview, Session } from "../../router/types";
import { PushDialog } from "./PushDialog";

const SID = fixtureIsolatedSession.id;

function open(
  client: FakeRouterClient,
  props: Partial<Parameters<typeof PushDialog>[0]> = {},
  session: Session = { ...fixtureIsolatedSession, uncommitted_count: 0 },
) {
  const onClose = vi.fn();
  const onPushed = vi.fn();
  const onAskAgent = vi.fn();
  render(
    <PushDialog
      open
      sessionId={SID}
      routerClient={client}
      session={session}
      target="branch"
      onClose={onClose}
      onPushed={onPushed}
      onAskAgent={onAskAgent}
      {...props}
    />,
  );
  return { onClose, onPushed, onAskAgent };
}

const preview = (over: Partial<PushPreview>): PushPreview => ({ ...structuredClone(fixturePushPreview), ...over });
const primary = () => screen.getByRole("button", { name: /^(Push|Rebase onto .* and push|Pushing…|Rebasing and pushing…)$/ }) as HTMLButtonElement;

describe("PushDialog (D5, mockup §3)", () => {
  it("fetches first, then asks to push the commits it lists", async () => {
    const client = new FakeRouterClient();
    let answer!: (p: PushPreview) => void;
    client.pushPreview = () => preview({});
    vi.spyOn(client, "getPushPreview").mockReturnValueOnce(new Promise((r) => (answer = r)));
    open(client);
    expect(screen.getByRole("status").textContent).toBe("Fetching the remote…");
    expect(primary().disabled).toBe(true);
    await act(async () => answer(preview({ ahead: 60 })));

    expect(screen.getByText("Push mads/session/session-isolated to origin?")).toBeTruthy();
    const list = screen.getByRole("list", { name: "Commits to push" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "c0ffee1 deploy: detect Ubuntu 24.04 runners by codename",
      "b0a7ed5 deploy: add runner_detect.sh",
      "and 58 more",
    ]);
    expect(screen.getByText(/fetched just now · fast-forward · never forced/)).toBeTruthy();
    expect(primary().textContent).toBe("Push");
  });

  it("pushes what it showed, then hands the result back and closes", async () => {
    const client = new FakeRouterClient();
    const { onClose, onPushed } = open(client, { target: "base" });
    await waitFor(() => expect(client.pushPreviewCalls).toEqual([{ sessionId: SID, repo: undefined, target: "base" }]));
    fireEvent.click(await screen.findByRole("button", { name: "Push" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(client.pushCalls[0].req).toEqual({ target: "base", expect: "c0ffee1234" });
    expect(onPushed).toHaveBeenCalledWith(expect.objectContaining({ outcome: "pushed", pushed: 2, remote: "origin" }));
  });

  it("names a new branch and says so", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({ new_branch: true });
    open(client);
    expect(await screen.findByText("Push mads/session/session-isolated to origin as a new branch?")).toBeTruthy();
  });

  it("shows a failed fetch in red", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({ fetch_error: "Could not resolve host: github.com" });
    open(client);
    expect(await screen.findByText("Couldn't fetch origin: Could not resolve host: github.com")).toBeTruthy();
    expect(screen.queryByText(/fetched just now/)).toBeNull();
  });

  it("when the remote is ahead, lists what's coming and rebases only through its own button", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({
      remote_branch: "main",
      behind: 2,
      incoming: [
        { hash: "aaaaaaa111", subject: "fix: theirs" },
        { hash: "bbbbbbb222", subject: "docs: theirs" },
      ],
    });
    const { onPushed } = open(client);
    expect(await screen.findByText("origin/main has 2 commits you don't have")).toBeTruthy();
    expect(within(screen.getByRole("list", { name: "Incoming commits" })).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByText(/fast-forward/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Push" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Rebase onto origin and push" }));
    await waitFor(() => expect(onPushed).toHaveBeenCalled());
    expect(client.pushCalls[0].req).toEqual({ target: "branch", rebase: true, expect: "c0ffee1234" });
  });

  it("holds the rebase back, saying why and naming the files, when it can't run", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({
      behind: 1,
      incoming: [{ hash: "aaaaaaa111", subject: "fix: theirs" }],
      rebase_blocked: {
        reason: "overlap",
        detail: "The incoming commits change files you haven't committed.",
        files: ["scripts/deploy.sh", "README.md"],
      },
    });
    open(client);
    const button = (await screen.findByRole("button", { name: "Rebase onto origin and push" })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(screen.getByText("The incoming commits change files you haven't committed.")).toBeTruthy();
    expect(screen.getByText("scripts/deploy.sh")).toBeTruthy();
    expect(screen.getByText("README.md")).toBeTruthy();
  });

  it("offers no push when it's blocked, only why", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({
      remote: undefined,
      commits: [],
      ahead: 0,
      blocked: { reason: "no_remote", detail: "This repo has no remote to push to." },
    });
    open(client);
    expect(await screen.findByText("This repo has no remote to push to.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Push/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
  });

  it("shows git's own words when git push says no, and stays open", async () => {
    const client = new FakeRouterClient();
    client.pushOutcome = {
      outcome: "refused",
      reason: "push_failed",
      detail: "remote: error: GH006: Protected branch update failed for refs/heads/main.",
    };
    const { onClose, onPushed } = open(client);
    fireEvent.click(await screen.findByRole("button", { name: "Push" }));
    const pre = await screen.findByText(/GH006: Protected branch/);
    expect(pre.tagName).toBe("PRE");
    expect(screen.getByText("git push said no. Nothing was forced.")).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
    expect(onPushed).not.toHaveBeenCalled();
  });

  it("fetches again when the branch moved since the confirmation, and says so", async () => {
    const client = new FakeRouterClient();
    let calls = 0;
    client.pushPreview = () =>
      ++calls === 1
        ? preview({})
        : preview({ head: "d00d000000", commits: [{ hash: "d00d000000", subject: "agent: one more" }], ahead: 3 });
    client.pushOutcome = { outcome: "refused", reason: "moved" };
    open(client);
    fireEvent.click(await screen.findByRole("button", { name: "Push" }));
    expect(await screen.findByText(/The branch moved since this opened/)).toBeTruthy();
    expect(await screen.findByText("agent: one more")).toBeTruthy();
    expect(client.pushPreviewCalls).toHaveLength(2);

    client.pushOutcome = undefined;
    fireEvent.click(screen.getByRole("button", { name: "Push" }));
    await waitFor(() => expect(client.pushCalls).toHaveLength(2));
    expect(client.pushCalls[1].req.expect).toBe("d00d000000");
  });

  it("names the conflicting files after an undone rebase, and asks the agent to rebase", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({ remote_branch: "main", behind: 1, incoming: [{ hash: "aaaaaaa111", subject: "fix" }] });
    client.pushOutcome = { outcome: "conflicted", files: ["a.go", "b.go"] };
    const { onAskAgent, onClose } = open(client);
    fireEvent.click(await screen.findByRole("button", { name: "Rebase onto origin and push" }));
    expect(
      await screen.findByText("Rebasing onto origin/main hit conflicts in 2 files, so it was undone. Nothing was pushed."),
    ).toBeTruthy();
    expect(screen.getByText("a.go")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Ask the agent to rebase" }));
    expect(onAskAgent).toHaveBeenCalledWith(
      "Please rebase mads/session/session-isolated onto origin/main and resolve the conflicts in: a.go, b.go.",
    );
    expect(onClose).toHaveBeenCalled();
  });

  it("has no Ask the agent without somewhere to draft it", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({ behind: 1, incoming: [] });
    client.pushOutcome = { outcome: "conflicted", files: ["a.go"] };
    open(client, { onAskAgent: undefined });
    fireEvent.click(await screen.findByRole("button", { name: /Rebase onto/ }));
    await screen.findByText("a.go");
    expect(screen.queryByRole("button", { name: "Ask the agent to rebase" })).toBeNull();
  });

  it("picks one repo when several have commits to push; another repo pushes its branch", async () => {
    const client = new FakeRouterClient();
    const session: Session = {
      ...fixtureIsolatedSession,
      uncommitted_count: 0,
      unlanded_count: 0,
      unpushed_count: 4,
      repos: [
        { root: "/home/user/.mads/session-worktrees/session-isolated", primary: true, isolated: true, unpushed_count: 3 },
        { root: "/home/user/repo/learnanywhere", unpushed_count: 1 },
      ],
    };
    open(client, { target: "base" }, session);
    const picker = await screen.findByRole("radiogroup", { name: "Repo to push" });
    const radios = within(picker).getAllByRole("radio") as HTMLInputElement[];
    expect(picker.textContent).toBe("session-isolated↑3learnanywhere↑1");
    expect(radios[0].checked).toBe(true);
    await waitFor(() =>
      expect(client.pushPreviewCalls.at(-1)).toEqual({
        sessionId: SID,
        repo: "/home/user/.mads/session-worktrees/session-isolated",
        target: "base",
      }),
    );
    fireEvent.click(radios[1]);
    await waitFor(() =>
      expect(client.pushPreviewCalls.at(-1)).toEqual({ sessionId: SID, repo: "/home/user/repo/learnanywhere", target: "branch" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Push" }));
    await waitFor(() => expect(client.pushCalls).toHaveLength(1));
    expect(client.pushCalls[0].req).toEqual({ repo: "/home/user/repo/learnanywhere", target: "branch", expect: "c0ffee1234" });
  });
});
