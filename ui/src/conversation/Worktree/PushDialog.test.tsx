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
});

describe("PushDialog — one repo listed", () => {
  it("keeps the single layout and its own rebase button when only one repo has commits to push", async () => {
    const client = new FakeRouterClient();
    client.pushPreview = preview({ behind: 1, incoming: [{ hash: "aaaaaaa111", subject: "fix" }] });
    open(client, {}, {
      ...fixtureIsolatedSession,
      uncommitted_count: 0,
      repos: [{ root: "/home/user/.mads/session-worktrees/session-isolated", primary: true, unpushed_count: 2 }],
    });
    expect(await screen.findByText("Push mads/session/session-isolated to origin?")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Repos" })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.getByRole("button", { name: "Rebase onto origin and push" })).toBeTruthy();
    expect(client.pushPreviewCalls).toEqual([{ sessionId: SID, repo: undefined, target: "branch" }]);
  });
});

describe("PushDialog — several repos (D5 \"Several repos\", layout B)", () => {
  const WT = "/home/user/.mads/session-worktrees/session-isolated";
  const LEARN = "/home/user/repo/learnanywhere";
  const NOTES = "/home/user/repo/notes";
  const session: Session = {
    ...fixtureIsolatedSession,
    uncommitted_count: 0,
    unlanded_count: 0,
    unpushed_count: 6,
    repos: [
      { root: WT, primary: true, isolated: true, unpushed_count: 3 },
      { root: LEARN, unpushed_count: 1 },
      { root: NOTES, unpushed_count: 2 },
    ],
  };
  const previews: Record<string, PushPreview> = {
    [WT]: preview({ repo: WT, target: "base", branch: "main", remote_branch: "main", head: "aaa0000001", ahead: 3 }),
    [LEARN]: preview({
      repo: LEARN,
      branch: "ios-port",
      remote_branch: "ios-port",
      head: "bbb0000002",
      ahead: 1,
      commits: [{ hash: "bbb0000002", subject: "ios: settings" }],
      behind: 2,
      incoming: [{ hash: "ccc0000003", subject: "theirs" }],
    }),
    [NOTES]: preview({ repo: NOTES, branch: "main", remote_branch: "main", head: "ddd0000004", ahead: 2 }),
  };

  function multiClient() {
    const client = new FakeRouterClient();
    client.pushPreview = (opts) => structuredClone(previews[opts.repo!]);
    return client;
  }
  const item = (name: string) =>
    within(screen.getByRole("group", { name: "Repos" })).getByRole("button", { name: new RegExp(`^${name}`) });
  const pushButton = () => screen.getByRole("button", { name: /^Push \d+ repos?$|^Pushing…$/ }) as HTMLButtonElement;

  it("fetches every repo's preview at once, each item saying so until its own arrives", async () => {
    const client = multiClient();
    const answers = new Map<string, (p: PushPreview) => void>();
    vi.spyOn(client, "getPushPreview").mockImplementation(
      (_sid, opts) => new Promise((r) => answers.set(opts!.repo!, r)),
    );
    open(client, { target: "base" }, session);
    expect(screen.getByText("Push this session's repos?")).toBeTruthy();
    expect(answers.size).toBe(3);
    expect(item("learnanywhere").textContent).toContain("Fetching…");
    expect(pushButton().disabled).toBe(true);

    await act(async () => answers.get(LEARN)!(previews[LEARN]));
    expect(item("learnanywhere").textContent).toBe("learnanywhere↑1 · ↓2 · ios-port");
    expect(item("session-isolated").textContent).toContain("Fetching…");
    await act(async () => {
      answers.get(WT)!(previews[WT]);
      answers.get(NOTES)!(previews[NOTES]);
    });
    expect(item("session-isolated").textContent).toBe("session-isolated↑3 · main");
    expect(item("session-isolated").getAttribute("title")).toBe(WT);
  });

  it("asks each repo for its own target: the session's isolated repo its base, the others their branch", async () => {
    const client = multiClient();
    open(client, { target: "branch" }, session);
    await waitFor(() => expect(client.pushPreviewCalls).toHaveLength(3));
    expect(client.pushPreviewCalls).toEqual([
      { sessionId: SID, repo: WT, target: "base" },
      { sessionId: SID, repo: LEARN, target: "branch" },
      { sessionId: SID, repo: NOTES, target: "branch" },
    ]);
  });

  it("leaves a repo that's behind out until its own rebase box is ticked", async () => {
    const client = multiClient();
    open(client, { target: "base" }, session);
    await waitFor(() => expect(item("learnanywhere").getAttribute("data-status")).toBe("need"));
    expect(item("session-isolated").getAttribute("data-status")).toBe("ready");
    expect(pushButton().textContent).toBe("Push 2 repos");

    fireEvent.click(item("learnanywhere"));
    expect(screen.getByText("origin/ios-port has 2 commits you don't have")).toBeTruthy();
    expect(screen.getByText("Left out of this push until you tick it.")).toBeTruthy();
    // The single pane's rebase button isn't here: the box is the opt-in.
    expect(screen.queryByRole("button", { name: /Rebase onto/ })).toBeNull();
    fireEvent.click(screen.getByLabelText("Rebase onto origin/ios-port first"));
    expect(item("learnanywhere").getAttribute("data-status")).toBe("ready");
    expect(pushButton().textContent).toBe("Push 3 repos");
  });

  it("holds a repo whose rebase can't run, saying why and naming the files", async () => {
    const client = multiClient();
    const blocked = {
      ...previews[LEARN],
      rebase_blocked: { reason: "overlap", detail: "Incoming commits change files you haven't committed.", files: ["Settings.swift"] },
    };
    client.pushPreview = (opts) => structuredClone(opts.repo === LEARN ? blocked : previews[opts.repo!]);
    open(client, { target: "base" }, session);
    await waitFor(() => expect(item("learnanywhere").getAttribute("data-status")).toBe("blocked"));
    expect(item("learnanywhere").textContent).toContain("can't push");
    fireEvent.click(item("learnanywhere"));
    expect((screen.getByLabelText("Rebase onto origin/ios-port first") as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText("Incoming commits change files you haven't committed.")).toBeTruthy();
    expect(screen.getByText("Settings.swift")).toBeTruthy();
    expect(pushButton().textContent).toBe("Push 2 repos");
  });

  it("pushes one repo at a time; a refusal stays in its pane, the others push, and the first failure is selected", async () => {
    const client = multiClient();
    client.pushOutcome = (req) =>
      req.repo === NOTES
        ? { outcome: "refused", reason: "push_failed", detail: "remote: Permission to notes denied." }
        : {
            outcome: "pushed",
            remote: "origin",
            remote_branch: previews[req.repo!].remote_branch,
            pushed: previews[req.repo!].ahead,
            rebased: req.rebase,
          };
    const { onClose, onPushed } = open(client, { target: "base" }, session);
    await waitFor(() => expect(item("learnanywhere").getAttribute("data-status")).toBe("need"));
    fireEvent.click(item("learnanywhere"));
    fireEvent.click(screen.getByLabelText("Rebase onto origin/ios-port first"));
    fireEvent.click(pushButton());

    await waitFor(() => expect(client.pushCalls).toHaveLength(3));
    expect(client.pushCalls.map((c) => c.req)).toEqual([
      { repo: WT, target: "base", expect: "aaa0000001" },
      { repo: LEARN, target: "branch", rebase: true, expect: "bbb0000002" },
      { repo: NOTES, target: "branch", expect: "ddd0000004" },
    ]);
    await waitFor(() => expect(item("notes").getAttribute("aria-current")).toBe("true"));
    expect(onPushed).toHaveBeenCalledTimes(2);
    expect(onClose).not.toHaveBeenCalled();
    expect(item("session-isolated").textContent).toContain("✓ pushed");
    expect(item("learnanywhere").getAttribute("data-status")).toBe("pushed");
    expect(item("notes").getAttribute("data-status")).toBe("failed");
    const pre = screen.getByText("remote: Permission to notes denied.");
    expect(pre.tagName).toBe("PRE");
    expect(pushButton().textContent).toBe("Push 1 repo");
    expect(screen.getByRole("button", { name: "Done" })).toBeTruthy();

    fireEvent.click(item("session-isolated"));
    expect(screen.getByText("Pushed 3 commits to origin/main.")).toBeTruthy();
  });

  it("fetches a repo's preview again when its branch moved, and closes once everything has pushed", async () => {
    const client = multiClient();
    const twoRepos: Session = { ...session, repos: session.repos!.slice(0, 2) };
    let moved = true;
    client.pushOutcome = (req) => {
      if (req.repo === WT && moved) {
        moved = false;
        return { outcome: "refused", reason: "moved" };
      }
      return { outcome: "pushed", remote: "origin", pushed: 1 };
    };
    const { onClose } = open(client, { target: "base" }, twoRepos);
    await waitFor(() => expect(item("learnanywhere").getAttribute("data-status")).toBe("need"));
    fireEvent.click(item("learnanywhere"));
    fireEvent.click(screen.getByLabelText("Rebase onto origin/ios-port first"));
    fireEvent.click(pushButton());
    expect(await screen.findByText(/The branch moved since this opened/)).toBeTruthy();
    expect(item("session-isolated").getAttribute("aria-current")).toBe("true");
    expect(client.pushPreviewCalls.filter((c) => c.repo === WT)).toHaveLength(2);
    await waitFor(() => expect(pushButton().textContent).toBe("Push 1 repo"));
    fireEvent.click(pushButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("asks the agent to rebase a repo whose rebase conflicted", async () => {
    const client = multiClient();
    client.pushOutcome = (req) =>
      req.repo === LEARN ? { outcome: "conflicted", files: ["Settings.swift"] } : { outcome: "pushed", pushed: 1 };
    const { onAskAgent } = open(client, { target: "base" }, session);
    await waitFor(() => expect(item("learnanywhere").getAttribute("data-status")).toBe("need"));
    fireEvent.click(item("learnanywhere"));
    fireEvent.click(screen.getByLabelText("Rebase onto origin/ios-port first"));
    fireEvent.click(pushButton());
    fireEvent.click(await screen.findByRole("button", { name: "Ask the agent to rebase" }));
    expect(onAskAgent).toHaveBeenCalledWith(
      "Please rebase ios-port onto origin/ios-port and resolve the conflicts in: Settings.swift.",
    );
  });
});
