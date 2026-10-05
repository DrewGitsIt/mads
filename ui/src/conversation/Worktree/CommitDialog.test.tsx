import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
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

const commitButton = () => screen.getByRole("button", { name: /^Commit \d+ files?$|^Committing…$/ }) as HTMLButtonElement;

describe("CommitDialog (D5)", () => {
  it("lists only the session's files, all checked, with their kind", async () => {
    open(new FakeRouterClient());
    await screen.findByText("scripts/deploy.sh");
    const boxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(boxes).toHaveLength(4);
    expect(boxes.every((b) => b.checked)).toBe(true);
    expect(screen.getByText("deleted")).toBeTruthy();
    expect(screen.getAllByText("modified")).toHaveLength(2);
    expect(screen.getByText("mads/session/session-isolated").parentElement!.textContent).toBe(
      "on mads/session/session-isolated · as you",
    );
    expect(commitButton().textContent).toBe("Commit 4 files");
  });

  it("toggles a file out of the commit", async () => {
    open(new FakeRouterClient());
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    expect(commitButton().textContent).toBe("Commit 3 files");
  });

  it("opens a file's diff from its path", async () => {
    const { onOpenFile } = open(new FakeRouterClient());
    fireEvent.click(await screen.findByRole("button", { name: "scripts/deploy.sh" }));
    expect(onOpenFile).toHaveBeenCalledWith("/home/user/.mads/session-worktrees/session-isolated/scripts/deploy.sh");
  });

  it("needs a message before it commits", async () => {
    open(new FakeRouterClient());
    await screen.findByText("scripts/deploy.sh");
    expect(commitButton().disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    expect(commitButton().disabled).toBe(false);
  });

  it("drafts with the Fast Model, and says when the diff was cut", async () => {
    const client = new FakeRouterClient();
    client.draftOutcome = { message: "deploy: use codenames", truncated: true };
    open(client);
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    fireEvent.click(screen.getByRole("button", { name: "Draft with Fast Model" }));
    await waitFor(() =>
      expect((screen.getByLabelText("Commit message") as HTMLTextAreaElement).value).toBe("deploy: use codenames"),
    );
    expect(client.draftCalls[0].paths).toEqual([
      "scripts/deploy.sh",
      "scripts/lib/runner_detect.sh",
      ".github/workflows/deploy.yml",
    ]);
    expect(screen.getByText(/saw only part of it/)).toBeTruthy();
  });

  it("hides the draft link without a Fast Model", async () => {
    open(new FakeRouterClient(), { fastModel: false });
    await screen.findByText("scripts/deploy.sh");
    expect(screen.queryByRole("button", { name: "Draft with Fast Model" })).toBeNull();
  });

  it("commits the checked files with the message and closes", async () => {
    const client = new FakeRouterClient();
    const { onClose, onCommitted } = open(client);
    fireEvent.click(await screen.findByLabelText("Include scripts/old_detect.sh"));
    fireEvent.change(screen.getByLabelText("Commit message"), { target: { value: "Fix deploy" } });
    fireEvent.click(commitButton());
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(client.commitCalls[0].req).toEqual({
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
  });

  it("opens on a one-click Commit's refusal with its selection", async () => {
    open(new FakeRouterClient(), {
      initialError: { code: "draft_failed", error: "The Fast Model couldn't draft a message.", detail: "timeout" },
    });
    expect(await screen.findByText("The Fast Model couldn't draft a message.")).toBeTruthy();
    expect(screen.getByText("timeout")).toBeTruthy();
  });
});
