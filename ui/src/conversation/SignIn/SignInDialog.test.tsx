import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { FakeRouterClient } from "../../router/fixtures";
import type { SessionAuth } from "../../router/types";
import { makeFakeShellHost } from "../ShellDock/fakeShellHost";
import { SignInDialog } from "./SignInDialog";

const auth: SessionAuth & { login: NonNullable<SessionAuth["login"]> } = {
  provider: "claude-code",
  account: "claude-code:/Users/me/.claude-work",
  signed_out: true,
  login: { argv: ["/usr/local/bin/claude", "auth", "login"], env: ["CLAUDE_CONFIG_DIR=/Users/me/.claude-work"] },
};

function setup(withHost = true) {
  const fake = makeFakeShellHost();
  const client = new FakeRouterClient();
  const onSignedIn = vi.fn();
  const onClose = vi.fn();
  const view = render(
    <SignInDialog
      auth={auth}
      provider="Claude Code"
      routerClient={client}
      shellHost={withHost ? fake.host : undefined}
      onSignedIn={onSignedIn}
      onClose={onClose}
    />,
  );
  return { ...fake, client, onSignedIn, onClose, view };
}

describe("SignInDialog", () => {
  it("runs the login with its env and, on exit 0, marks the account signed in", async () => {
    const { host, emitExit, client, onSignedIn } = setup();
    expect(screen.getByRole("heading", { name: "Sign in to Claude Code" })).toBeTruthy();
    await waitFor(() => expect(host.run).toHaveBeenCalledTimes(1));
    expect(host.run).toHaveBeenCalledWith(auth.login.argv, expect.objectContaining({ env: auth.login.env }));
    await waitFor(() => expect(host.onExit).toHaveBeenCalled());

    emitExit("run-1", 0);
    await waitFor(() => expect(onSignedIn).toHaveBeenCalledOnce());
    expect(client.signedIn).toEqual([auth.account]);
  });

  it("closes as signed in even if the Router couldn't be told", async () => {
    const { host, emitExit, client, onSignedIn } = setup();
    vi.spyOn(client, "markSignedIn").mockRejectedValue(new Error("router request failed: 500"));
    await waitFor(() => expect(host.onExit).toHaveBeenCalled());
    emitExit("run-1", 0);
    await waitFor(() => expect(onSignedIn).toHaveBeenCalledOnce());
  });

  it("stays open on a non-zero exit, and Try again runs the login afresh", async () => {
    const { host, emitExit, client, onSignedIn } = setup();
    await waitFor(() => expect(host.onExit).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();

    emitExit("run-1", 1);
    expect(await screen.findByText("Sign-in didn't finish")).toBeTruthy();
    expect(onSignedIn).not.toHaveBeenCalled();
    expect(client.signedIn).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(host.run).toHaveBeenCalledTimes(2));
    expect(screen.queryByText("Sign-in didn't finish")).toBeNull();
    // The finished try's view ended its (already gone) command.
    expect(host.end).toHaveBeenCalledWith("run-1");
  });

  it("ends the login when cancelled", async () => {
    const { host, onClose, view } = setup();
    await waitFor(() => expect(host.onExit).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClose).toHaveBeenCalledOnce();
    view.unmount();
    expect(host.end).toHaveBeenCalledWith("run-1");
  });

  it("shows the command to copy where there's no shell host", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const { onClose } = setup(false);
    const line = "CLAUDE_CONFIG_DIR=/Users/me/.claude-work /usr/local/bin/claude auth login";
    expect(screen.getByText(line)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(line);
    expect(await screen.findByRole("button", { name: "Copied" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalledOnce();
  });
});
