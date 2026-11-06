import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import type { CommitPlan } from "../../router/types";
import { CommitDialog } from "./CommitDialog";

function open(client: FakeRouterClient, props: Partial<Parameters<typeof CommitDialog>[0]> = {}) {
  const onClose = vi.fn();
  const onCommitted = vi.fn();
  const onOpenFile = vi.fn();
  render(
    <CommitDialog
      open
      sessionId="session-isolated"
      routerClient={client}
      fastModel
      onClose={onClose}
      onCommitted={onCommitted}
      onOpenFile={onOpenFile}
      {...props}
    />,
  );
  return { onClose, onCommitted, onOpenFile };
}

const commitButton = () =>
  screen.getByRole("button", { name: /^Commit \d+ (files?|repos?)$|^Committing…$/ }) as HTMLButtonElement;

const WORKTREE = "/home/user/.mads/session-worktrees/session-isolated";
const MADS = "/home/user/repo/mads";
const LEARN = "/home/user/repo/learnanywhere";

/** The test session's shape (COMMIT-LAND-PUSH): started in ~/repo, work in two repos and outside both. */
const twoRepos: CommitPlan = {
  repos: [
    {
      root: MADS,
      branch: "main",
      files: [
        { path: "ui/src/conversation/useRunway.ts", kind: "modified", mine: true },
        { path: "ui/src/features/sessions/launchpad/Launchpad.tsx", kind: "modified", mine: true },
        {
          path: "docs/mockups/launchpad-pills-v2.html",
          kind: "added",
          by: { id: "ses_pills", name: "Launchpad Pills UX Polish" },
          since: "session",
        },
        { path: "README.md", kind: "modified", since: "session" },
        { path: "notes.txt", kind: "modified", since: "before" },
        { path: "scratch.sh", kind: "added" },
      ],
    },
    {
      root: LEARN,
      branch: "ios-port",
      files: [
        { path: "ios/LearnAnywhere/Settings.swift", kind: "modified", mine: true },
        { path: "ios/LearnAnywhere/SettingsTests.swift", kind: "added", mine: true },
      ],
    },
  ],
  outside: [
    { path: "/home/user/repo/mads-ux-mockups-v2.html", kind: "added" },
    { path: "/home/user/repo/mads-launchpad-toolbar-mockups.html", kind: "modified" },
  ],
};

function repoItem(name: string) {
  return within(screen.getByRole("group", { name: "Repos" })).getByRole("button", { name: new RegExp(`^${name}`) });
}

describe("CommitDialog — one repo (D2, mockup §2)", () => {
  it("lists the session's files, all checked, with their kind, and the repo and branch above", async () => {
    const client = new FakeRouterClient();
    open(client);
    await screen.findByText("scripts/deploy.sh");
    expect(client.commitPlanCalls).toEqual(["session-isolated"]);
    const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes).toHaveLength(4);
    expect(boxes.every((b) => b.checked)).toBe(true);
    expect(screen.getByTitle("deleted").textContent).toBe("D");
    expect(screen.getAllByTitle("modified")).toHaveLength(2);
    expect(screen.getByText("session-isolated · mads/session/session-isolated · as you")).toBeTruthy();
    // No repo list for one repo.
    expect(screen.queryByRole("group", { name: "Repos" })).toBeNull();
    expect(commitButton().textContent).toBe("Commit 4 files");
  });

  it("toggles a file out of the commit, and the group's check all / uncheck all", async () => {
    open(new FakeRouterClient());
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    expect(commitButton().textContent).toBe("Commit 3 files");
    const mine = screen.getByRole("group", { name: "Changed by this session" });
    fireEvent.click(within(mine).getByRole("button", { name: "check all" }));
    expect(commitButton().textContent).toBe("Commit 4 files");
    fireEvent.click(within(mine).getByRole("button", { name: "uncheck all" }));
    expect(commitButton().textContent).toBe("Commit 0 files");
    expect(commitButton().disabled).toBe(true);
  });

  it("opens a file's diff from its path, as an absolute path", async () => {
    const { onOpenFile } = open(new FakeRouterClient());
    fireEvent.click(await screen.findByRole("button", { name: "scripts/deploy.sh" }));
    expect(onOpenFile).toHaveBeenCalledWith(`${WORKTREE}/scripts/deploy.sh`);
  });

  it("needs a message before it commits", async () => {
    open(new FakeRouterClient());
    await screen.findByText("scripts/deploy.sh");
    expect(commitButton().disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    expect(commitButton().disabled).toBe(false);
  });

  it("drafts with the Fast Model for its repo, and says when the diff was cut", async () => {
    const client = new FakeRouterClient();
    client.draftOutcome = { message: "deploy: use codenames", truncated: true };
    open(client);
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    fireEvent.click(screen.getByRole("button", { name: "Draft with Fast Model" }));
    await waitFor(() =>
      expect((screen.getByLabelText("Commit message") as HTMLTextAreaElement).value).toBe("deploy: use codenames"),
    );
    expect(client.draftCalls[0]).toEqual({
      sessionId: "session-isolated",
      paths: ["scripts/deploy.sh", "scripts/lib/runner_detect.sh", ".github/workflows/deploy.yml"],
      repo: WORKTREE,
    });
    expect(screen.getByText(/saw only part of it/)).toBeTruthy();
  });

  it("hides the draft link without a Fast Model", async () => {
    open(new FakeRouterClient(), { fastModel: false });
    await screen.findByText("scripts/deploy.sh");
    expect(screen.queryByRole("button", { name: "Draft with Fast Model" })).toBeNull();
  });

  it("commits the checked files in their repo with the message and closes", async () => {
    const client = new FakeRouterClient();
    const { onClose, onCommitted } = open(client);
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    fireEvent.click(commitButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(client.commitCalls[0].req).toEqual({
      repo: WORKTREE,
      paths: ["scripts/deploy.sh", "scripts/lib/runner_detect.sh", ".github/workflows/deploy.yml"],
      message: "Fix deploy",
    });
    expect(onCommitted).toHaveBeenCalledWith(expect.objectContaining({ hash: "a41c09e5d2" }));
  });

  it("shows git's output verbatim on git_failed and keeps the selection", async () => {
    const client = new FakeRouterClient();
    client.commitOutcome = {
      code: "git_failed",
      error: "git refused the commit.",
      detail: "lint-staged: 2 errors\n  scripts/deploy.sh:4 SC2086",
    };
    const { onClose } = open(client);
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    fireEvent.click(commitButton());
    const detail = await screen.findByText(/lint-staged: 2 errors/);
    expect(detail.tagName).toBe("PRE");
    expect(detail.textContent).toBe("lint-staged: 2 errors\n  scripts/deploy.sh:4 SC2086");
    expect(screen.getByText("git refused the commit.")).toBeTruthy();
    expect(onClose).not.toHaveBeenCalled();
    expect((screen.getByLabelText("Include scripts/old_detect.sh") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("Commit message") as HTMLTextAreaElement).value).toBe("Fix deploy");
  });

  it("names a path already committed elsewhere instead of failing the commit", async () => {
    const client = new FakeRouterClient();
    client.commitOutcome = {
      hash: "b52aa01",
      message: "Fix deploy",
      undoable: true,
      paths: [
        { path: "scripts/deploy.sh", status: "committed" },
        { path: "scripts/lib/runner_detect.sh", status: "already_committed" },
      ],
    };
    const { onClose, onCommitted } = open(client);
    fireEvent.change(await screen.findByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    await screen.findByText("scripts/deploy.sh");
    fireEvent.click(commitButton());
    await waitFor(() => expect(onCommitted).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
    expect(await screen.findByText(/Committed 1 file as/)).toBeTruthy();
    expect(screen.getAllByText("already committed").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Done" })).toBeTruthy();
  });

  it("opens on a one-click Commit's refusal, its selection checked in the repo it targeted", async () => {
    open(new FakeRouterClient(), {
      initialError: {
        code: "draft_failed",
        error: "The Fast Model couldn't draft a message.",
        detail: "timeout",
        selection: ["scripts/deploy.sh"],
      },
    });
    expect(await screen.findByText("The Fast Model couldn't draft a message.")).toBeTruthy();
    expect(screen.getByText("timeout")).toBeTruthy();
    expect((screen.getByLabelText("Include scripts/deploy.sh") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Include scripts/old_detect.sh") as HTMLInputElement).checked).toBe(false);
  });

  it("starts someone else's files unchecked, under their own group, saying whose they are", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = { repos: [twoRepos.repos[0]], outside: [] };
    open(client);
    const others = await screen.findByRole("group", { name: "Changed by someone else" });
    expect(within(others).getAllByRole("checkbox").every((b) => !(b as HTMLInputElement).checked)).toBe(true);
    const mine = screen.getByRole("group", { name: "Changed by this session" });
    expect(within(mine).getAllByRole("checkbox").every((b) => (b as HTMLInputElement).checked)).toBe(true);
    expect(screen.getByText("mads · main · as you")).toBeTruthy();

    const note = (path: string) => within(others).getByText(path).parentElement!.lastElementChild!.textContent;
    expect(note("docs/mockups/launchpad-pills-v2.html")).toBe("session “Launchpad Pills UX Polish”");
    expect(note("README.md")).toBe("changed while this session ran");
    expect(note("notes.txt")).toBe("uncommitted before this session started");
    expect(note("scratch.sh")).toBe("not by this session");

    // Checked, someone else's file commits with the session's own.
    fireEvent.click(within(others).getByLabelText("Include README.md"));
    expect(commitButton().textContent).toBe("Commit 3 files");
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "ui: runway" } });
    fireEvent.click(commitButton());
    await waitFor(() => expect(client.commitCalls).toHaveLength(1));
    expect(client.commitCalls[0].req.paths).toEqual([
      "ui/src/conversation/useRunway.ts",
      "ui/src/features/sessions/launchpad/Launchpad.tsx",
      "README.md",
    ]);
  });
});

describe("CommitDialog — several repos (D2, mockup §1 B)", () => {
  it("lists the repos on the left, then Not in a repo, and shows the selected repo's files", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = twoRepos;
    open(client);
    await screen.findByText("ui/src/conversation/useRunway.ts");
    const items = within(screen.getByRole("group", { name: "Repos" })).getAllByRole("button");
    expect(items.map((b) => b.textContent)).toEqual([
      "mads2 of 6 files · needs a message · main",
      "learnanywhere2 of 2 files · needs a message · ios-port",
      "Not in a repo2 files · can't commit",
    ]);
    expect(items[0].getAttribute("title")).toBe(MADS);
    expect(items[0].getAttribute("aria-current")).toBe("true");
    expect(items[0].getAttribute("data-status")).toBe("need");
    expect(commitButton().textContent).toBe("Commit 2 repos");
    expect(commitButton().disabled).toBe(true);
    expect(screen.getByText("mads needs a message · learnanywhere needs a message")).toBeTruthy();

    fireEvent.click(repoItem("learnanywhere"));
    expect(screen.getByText("ios/LearnAnywhere/Settings.swift")).toBeTruthy();
    expect(screen.queryByText("ui/src/conversation/useRunway.ts")).toBeNull();

    fireEvent.click(repoItem("Not in a repo"));
    expect(screen.getByText("~/repo/mads-ux-mockups-v2.html")).toBeTruthy();
    expect(
      screen.getByText("These can't be committed where they are. Drag one onto a repo on the left, or use Move to…"),
    ).toBeTruthy();
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
  });

  it("keeps one message per repo, and the dots and footer say which are ready", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = twoRepos;
    open(client);
    fireEvent.change(await screen.findByLabelText("Commit message for mads"), { target: { value: "ui: runway" } });
    expect(repoItem("mads").getAttribute("data-status")).toBe("ready");
    expect(repoItem("mads").textContent).toContain("message ✓");
    expect(screen.getByText("mads ready · learnanywhere needs a message")).toBeTruthy();
    expect(commitButton().disabled).toBe(true);

    fireEvent.click(repoItem("learnanywhere"));
    expect((screen.getByLabelText("Commit message for learnanywhere") as HTMLTextAreaElement).value).toBe("");
    const mine = screen.getByRole("group", { name: "Changed by this session" });
    fireEvent.click(within(mine).getByRole("button", { name: "uncheck all" }));
    // Nothing selected: grey, and no longer counted.
    expect(repoItem("learnanywhere").getAttribute("data-status")).toBe("skip");
    expect(commitButton().textContent).toBe("Commit 1 repo");
    expect(commitButton().disabled).toBe(false);
  });

  it("drafts each repo's message for its own files, and Draft all fills the empty ones", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = twoRepos;
    client.draftOutcome = { message: "drafted" };
    open(client);
    fireEvent.change(await screen.findByLabelText("Commit message for mads"), { target: { value: "mine" } });
    fireEvent.click(screen.getByRole("button", { name: "Draft all messages" }));
    await waitFor(() => expect(client.draftCalls).toHaveLength(1));
    expect(client.draftCalls[0]).toEqual({
      sessionId: "session-isolated",
      paths: ["ios/LearnAnywhere/Settings.swift", "ios/LearnAnywhere/SettingsTests.swift"],
      repo: LEARN,
    });
    await waitFor(() => expect(repoItem("learnanywhere").getAttribute("data-status")).toBe("ready"));
    expect((screen.getByLabelText("Commit message for mads") as HTMLTextAreaElement).value).toBe("mine");
  });

  it("commits the repos one at a time; a refusal keeps its selection, shows git's output and is selected", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = twoRepos;
    client.commitOutcome = (req) =>
      req.repo === MADS
        ? {
            code: "git_failed",
            error: "git refused the commit.",
            detail: "pre-commit: eslint found 2 problems",
          }
        : undefined;
    const { onClose, onCommitted } = open(client);
    fireEvent.change(await screen.findByLabelText("Commit message for mads"), { target: { value: "ui: runway" } });
    fireEvent.click(within(screen.getByRole("group", { name: "Changed by someone else" })).getByLabelText("Include README.md"));
    fireEvent.click(repoItem("learnanywhere"));
    fireEvent.change(screen.getByLabelText("Commit message for learnanywhere"), { target: { value: "ios: settings" } });
    fireEvent.click(commitButton());

    await waitFor(() => expect(client.commitCalls).toHaveLength(2));
    expect(client.commitCalls.map((c) => c.req)).toEqual([
      {
        repo: MADS,
        paths: ["ui/src/conversation/useRunway.ts", "ui/src/features/sessions/launchpad/Launchpad.tsx", "README.md"],
        message: "ui: runway",
      },
      {
        repo: LEARN,
        paths: ["ios/LearnAnywhere/Settings.swift", "ios/LearnAnywhere/SettingsTests.swift"],
        message: "ios: settings",
      },
    ]);
    expect(onCommitted).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();

    // The failed repo is selected, with git's words and its selection.
    const detail = await screen.findByText("pre-commit: eslint found 2 problems");
    expect(detail.tagName).toBe("PRE");
    expect(repoItem("mads").getAttribute("aria-current")).toBe("true");
    expect(repoItem("mads").getAttribute("data-status")).toBe("failed");
    expect(repoItem("mads").textContent).toContain("commit failed · selection kept");
    expect((screen.getByLabelText("Include README.md") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Commit message for mads") as HTMLTextAreaElement).value).toBe("ui: runway");

    // The other one committed, and drops out of the count.
    expect(repoItem("learnanywhere").getAttribute("data-status")).toBe("committed");
    expect(repoItem("learnanywhere").textContent).toContain("committed a41c09e");
    expect(commitButton().textContent).toBe("Commit 1 repo");
    expect(screen.getByRole("button", { name: "Done" })).toBeTruthy();

    // Fixed: the retry commits only the failed repo, then closes.
    client.commitOutcome = undefined;
    fireEvent.click(commitButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(client.commitCalls).toHaveLength(3);
    expect(client.commitCalls[2].req.repo).toBe(MADS);
  });

  it("uses layout B for one repo plus files outside every repo", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = { repos: [twoRepos.repos[1]], outside: twoRepos.outside };
    open(client);
    await screen.findByText("ios/LearnAnywhere/Settings.swift");
    expect(within(screen.getByRole("group", { name: "Repos" })).getAllByRole("button")).toHaveLength(2);
    expect(commitButton().textContent).toBe("Commit 1 repo");
  });
});

describe("CommitDialog — nothing to commit", () => {
  it("says nothing here can be committed when every changed file is outside a repo", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = { repos: [], outside: [{ path: "/home/user/notes/plan.md", kind: "added" }] };
    const { onClose } = open(client);
    expect(await screen.findByText("Nothing here can be committed")).toBeTruthy();
    expect(screen.getByText("None of this session's changed files are in a git repo")).toBeTruthy();
    expect(screen.getByText("~/notes/plan.md")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Commit/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("says nothing is left when every file is committed", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = { repos: [], outside: [] };
    open(client);
    expect(await screen.findByText(/Nothing left to commit/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Close" })).toBeTruthy();
  });
});

describe("CommitDialog — moving a file into a repo (D4)", () => {
  const MOCKUP = "/home/user/repo/mads-ux-mockups-v2.html";

  function movePlan(): CommitPlan {
    return { ...structuredClone(twoRepos), move_targets: [MADS, LEARN, "/home/user/repo/notes"] };
  }

  async function openOutside(client: FakeRouterClient) {
    const utils = open(client);
    await screen.findByText("ui/src/conversation/useRunway.ts");
    fireEvent.click(repoItem("Not in a repo"));
    return utils;
  }

  const outsideRow = (path: string) => document.querySelector<HTMLElement>(`[data-path="${path}"]`)!;
  const picker = () => screen.getByTestId("move-picker");
  const folderField = () => within(picker()).getByRole("combobox") as HTMLInputElement;

  it("drags a file onto a repo in the list, which opens the picker in that repo's pane", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    await openOutside(client);
    const row = outsideRow(MOCKUP);
    expect(row.getAttribute("draggable")).toBe("true");
    expect(row.textContent).toContain("⋮⋮");

    fireEvent.dragStart(row);
    // Every repo in the list that takes moves is a drop target while dragging.
    expect(repoItem("mads").getAttribute("data-drop")).toBe("target");
    expect(repoItem("learnanywhere").getAttribute("data-drop")).toBe("target");
    fireEvent.dragOver(repoItem("learnanywhere"));
    expect(repoItem("learnanywhere").getAttribute("data-drop")).toBe("over");
    fireEvent.drop(repoItem("learnanywhere"));

    expect(repoItem("learnanywhere").getAttribute("aria-current")).toBe("true");
    expect(repoItem("learnanywhere").getAttribute("data-drop")).toBeNull();
    expect(within(picker()).getByText("mads-ux-mockups-v2.html")).toBeTruthy();
    expect(picker().textContent).toContain("→ learnanywhere");
    expect(picker().textContent).toContain("~/repo/learnanywhere/");
    expect(picker().textContent).toContain("Moves it out of ~/repo. The agent is told the new path.");
    expect(picker().textContent).toContain("This happens now, not on Commit — Cancel won't put it back.");
    // The repo's own files are still below the picker.
    expect(screen.getByText("ios/LearnAnywhere/Settings.swift")).toBeTruthy();
    await waitFor(() => expect(client.moveFolderCalls).toEqual([{ sessionId: "session-isolated", repo: LEARN, ext: ".html" }]));
  });

  it("only accepts a drop on repos the file can move into", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = { ...movePlan(), move_targets: [LEARN] };
    await openOutside(client);
    fireEvent.dragStart(outsideRow(MOCKUP));
    expect(repoItem("mads").getAttribute("data-drop")).toBeNull();
    fireEvent.drop(repoItem("mads"));
    expect(screen.queryByTestId("move-picker")).toBeNull();
    fireEvent.dragEnd(outsideRow(MOCKUP));
    expect(repoItem("learnanywhere").getAttribute("data-drop")).toBeNull();
  });

  it("Move to… lists every move target and works from the keyboard, a repo not in the list included", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    await openOutside(client);
    const trigger = screen.getByRole("button", { name: "Move mads-ux-mockups-v2.html to…" });
    fireEvent.click(trigger);
    const menu = screen.getByRole("menu");
    const items = within(menu).getAllByRole("menuitem");
    expect(items.map((i) => i.textContent)).toEqual([
      "mads~/repo/mads",
      "learnanywhere~/repo/learnanywhere",
      "notes~/repo/notes",
    ]);
    await waitFor(() => expect(document.activeElement).toBe(items[0]));
    fireEvent.keyDown(menu, { key: "ArrowUp" });
    expect(document.activeElement).toBe(items[2]);
    // Escape closes the menu, not the dialog.
    fireEvent.keyDown(menu, { key: "Escape" });
    expect(screen.queryByRole("menu")).toBeNull();
    expect(screen.getByRole("alertdialog")).toBeTruthy();

    fireEvent.click(trigger);
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^notes/ }));
    // No uncommitted files there yet: a pane with just the picker.
    expect(picker().textContent).toContain("→ notes");
    expect(within(screen.getByRole("group", { name: "Repos" })).queryByRole("button", { name: /^notes/ })).toBeNull();
    fireEvent.click(within(picker()).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("move-picker")).toBeNull();
    expect(repoItem("Not in a repo").getAttribute("aria-current")).toBe("true");
  });

  it("suggests the repo's folders, fills in one already holding files like it, and filters as you type", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    await openOutside(client);
    fireEvent.click(screen.getByRole("button", { name: "Move mads-ux-mockups-v2.html to…" }));
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^mads/ }));
    await waitFor(() => expect(folderField().value).toBe("docs/mockups/"));
    let options = within(picker()).getAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual(["docs/mockups/3 .html files"]);

    fireEvent.change(folderField(), { target: { value: "" } });
    options = within(picker()).getAllByRole("option");
    expect(options).toHaveLength(5);
    expect(options[0].getAttribute("aria-selected")).toBe("true");

    fireEvent.change(folderField(), { target: { value: "u" } });
    expect(within(picker()).getAllByRole("option").map((o) => o.textContent)).toEqual(["ui/", "ui/src/"]);
    fireEvent.keyDown(folderField(), { key: "ArrowDown" });
    fireEvent.keyDown(folderField(), { key: "Enter" });
    expect(folderField().value).toBe("ui/src/");
    expect(client.moveCalls).toHaveLength(0);

    fireEvent.change(folderField(), { target: { value: "do" } });
    fireEvent.click(within(picker()).getByRole("option", { name: "docs/" }));
    expect(folderField().value).toBe("docs/");
    // Fetched once for the repo and extension.
    expect(client.moveFolderCalls).toHaveLength(1);
  });

  it("leaves the field empty when no folder holds files like it", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    client.moveFolders = [{ path: "docs/" }, { path: "ui/" }];
    await openOutside(client);
    fireEvent.click(screen.getByRole("button", { name: "Move mads-ux-mockups-v2.html to…" }));
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^mads/ }));
    await waitFor(() => expect(within(picker()).getAllByRole("option")).toHaveLength(2));
    expect(folderField().value).toBe("");
  });

  it("shows a refusal in the picker, in the Router's words, and keeps it open", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    client.moveOutcome = {
      code: "exists",
      error: "docs/mockups/mads-ux-mockups-v2.html already exists: rename it, or pick another folder.",
    };
    await openOutside(client);
    fireEvent.click(screen.getByRole("button", { name: "Move mads-ux-mockups-v2.html to…" }));
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^mads/ }));
    await waitFor(() => expect(folderField().value).toBe("docs/mockups/"));
    fireEvent.click(within(picker()).getByRole("button", { name: "Move here" }));
    expect(
      await within(picker()).findByText(
        "docs/mockups/mads-ux-mockups-v2.html already exists: rename it, or pick another folder.",
      ),
    ).toBeTruthy();
    expect(client.moveCalls[0].req).toEqual({ path: MOCKUP, repo: MADS, dir: "docs/mockups/" });
    expect(folderField().value).toBe("docs/mockups/");
    expect((within(picker()).getByRole("button", { name: "Move here" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("moves the file, reloads the plan with it checked and tagged, and keeps the other repos as they were", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = movePlan();
    await openOutside(client);
    // The user's work in learnanywhere, before the move.
    fireEvent.click(repoItem("learnanywhere"));
    fireEvent.change(screen.getByLabelText("Commit message for learnanywhere"), { target: { value: "ios: settings" } });
    fireEvent.click(screen.getByLabelText("Include ios/LearnAnywhere/SettingsTests.swift"));
    // …and in mads: someone else's file checked.
    fireEvent.click(repoItem("mads"));
    fireEvent.click(screen.getByLabelText("Include README.md"));

    fireEvent.click(repoItem("Not in a repo"));
    fireEvent.click(screen.getByRole("button", { name: "Move mads-ux-mockups-v2.html to…" }));
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^mads/ }));
    await waitFor(() => expect(folderField().value).toBe("docs/mockups/"));
    const plans = client.commitPlanCalls.length;
    fireEvent.keyDown(folderField(), { key: "Enter" });

    const moved = await screen.findByLabelText("Include docs/mockups/mads-ux-mockups-v2.html");
    expect(client.commitPlanCalls.length).toBe(plans + 1);
    expect(screen.queryByTestId("move-picker")).toBeNull();
    expect((moved as HTMLInputElement).checked).toBe(true);
    expect(moved.parentElement!.textContent).toContain("moved");
    expect(within(screen.getByRole("group", { name: "Changed by this session" })).getByText("docs/mockups/mads-ux-mockups-v2.html")).toBeTruthy();
    expect((screen.getByLabelText("Include README.md") as HTMLInputElement).checked).toBe(true);
    expect(repoItem("mads").getAttribute("aria-current")).toBe("true");
    expect(repoItem("Not in a repo").textContent).toContain("1 file · can't commit");

    fireEvent.click(repoItem("learnanywhere"));
    expect((screen.getByLabelText("Commit message for learnanywhere") as HTMLTextAreaElement).value).toBe("ios: settings");
    expect((screen.getByLabelText("Include ios/LearnAnywhere/SettingsTests.swift") as HTMLInputElement).checked).toBe(false);
  });

  it("from the outside-only dialog, Move to… puts a file in a repo and the dialog becomes a Commit", async () => {
    const client = new FakeRouterClient();
    client.commitPlan = {
      repos: [],
      outside: [{ path: "/home/user/notes/plan.md", kind: "added" }],
      move_targets: [MADS],
    };
    client.moveFolders = [{ path: "docs/", matches: 4 }];
    const { onClose } = open(client);
    expect(await screen.findByText("Use Move to… to put one in a repo.")).toBeTruthy();
    // No repo list to drop on, so no dragging.
    expect(outsideRow("/home/user/notes/plan.md").getAttribute("draggable")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Move plan.md to…" }));
    fireEvent.click(within(screen.getByRole("menu")).getByRole("menuitem", { name: /^mads/ }));
    await waitFor(() => expect(folderField().value).toBe("docs/"));
    expect(picker().textContent).toContain("Moves it out of ~/notes.");
    fireEvent.click(within(picker()).getByRole("button", { name: "Move here" }));

    expect(await screen.findByText("mads · as you")).toBeTruthy();
    expect(screen.getByText("Commit this session's changes")).toBeTruthy();
    expect((screen.getByLabelText("Include docs/plan.md") as HTMLInputElement).checked).toBe(true);
    expect(commitButton().textContent).toBe("Commit 1 file");
    expect(onClose).not.toHaveBeenCalled();
  });
});
