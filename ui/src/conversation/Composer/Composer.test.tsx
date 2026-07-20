import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Composer } from "./Composer";
import { FakeRouterClient } from "../../router/fixtures";
import type { Agent, Model } from "../../router/types";

// React mirrors a controlled textarea's value into a text-node child (an SSR
// carryover), so plain screen.getByText("/agent") can false-positive-match
// the textarea itself when the typed command equals the label under test.
// Scoping to the listbox sidesteps that collision.
function menu() {
  return within(screen.getByRole("listbox"));
}

function fixtureAgent(id: string): Agent {
  return { id, name: id, prompt: "", created_at: "", updated_at: "" };
}

describe("Composer", () => {
  it("resolves @ mentionables against the faked Router response set and shows a menu", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    fireEvent.change(textarea, { target: { value: "check @deep" } });

    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());
    expect(screen.getByText("deep-research")).toBeTruthy();
  });

  it("selects a mention candidate, splicing the token into the value and staging it", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "check @deep" } });
    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());

    fireEvent.mouseDown(screen.getByText("deep-research"));

    expect(textarea.value).toBe("check @deep-research ");
    // staged in the injected-content preview, distinct from the menu option
    expect(screen.getAllByText("deep-research").length).toBeGreaterThan(0);
  });

  it("resolves / commands against skill capabilities, badging unhealthy ones", async () => {
    const client = new FakeRouterClient();
    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        agentId="autonomous-executor"
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    fireEvent.change(textarea, { target: { value: "/security" } });

    await waitFor(() => expect(screen.getByText("/security-review")).toBeTruthy());
    expect(screen.getByText("unhealthy")).toBeTruthy();
  });

  it("⌘E toggles the expanded composer height", () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/);

    expect(textarea.className).not.toMatch(/expanded/);
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });
    expect(textarea.className).toMatch(/expanded/);
    fireEvent.keyDown(textarea, { key: "e", metaKey: true });
    expect(textarea.className).not.toMatch(/expanded/);
  });

  it("sends the message via the Router client and clears the composer", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello there" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "hello there",
        mentions: [],
      }),
    );
    expect(textarea.value).toBe("");
  });

  it("auto-sends initialValue on mount and clears the textarea once it succeeds", async () => {
    const client = new FakeRouterClient();
    const sendMessage = vi.spyOn(client, "sendMessage");

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        initialValue="fix the deploy script"
        autoSendInitial
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    expect(textarea.value).toBe("fix the deploy script");

    await waitFor(() =>
      expect(sendMessage).toHaveBeenCalledWith("session-active", {
        text: "fix the deploy script",
        mentions: [],
      }),
    );
    await waitFor(() => expect(textarea.value).toBe(""));
  });

  it("preserves the initial text and calls onSendError when the auto-send fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("network down"));
    const onSendError = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        initialValue="fix the deploy script"
        autoSendInitial
        onSendError={onSendError}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith("network down"));
    expect(textarea.value).toBe("fix the deploy script");
  });

  it("keeps the typed text in the textarea when a manual send fails", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "sendMessage").mockRejectedValue(new Error("boom"));
    const onSendError = vi.fn();

    render(<Composer routerClient={client} sessionId="session-active" onSendError={onSendError} />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: "hello there" } });
    fireEvent.keyDown(textarea, { key: "Enter" });

    await waitFor(() => expect(onSendError).toHaveBeenCalledWith("boom"));
    expect(textarea.value).toBe("hello there");
  });

  it("'/agent' descends to registry agents; selecting one rebinds the session, clears the composer, and confirms — no chip, no send", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listAgents").mockResolvedValue([fixtureAgent("investigator")]);
    const sendMessage = vi.spyOn(client, "sendMessage");
    const onUpdateSession = vi.fn();
    const onCommandExecuted = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        onUpdateSession={onUpdateSession}
        onCommandExecuted={onCommandExecuted}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/agent" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/agent" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/agent" }));
    expect(textarea.value).toBe("/agent ");

    await waitFor(() =>
      expect(menu().getByRole("option", { name: "investigator" })).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "investigator" }));

    expect(onUpdateSession).toHaveBeenCalledWith({ agent: "investigator" });
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith("Agent set to investigator"),
    );
    expect(textarea.value).toBe("");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
    expect(sendMessage).not.toHaveBeenCalled();
  });

  it("'/model' descends to the session's provider model list and rebinds on select", async () => {
    const client = new FakeRouterClient();
    const models: Model[] = [{ id: "claude-opus-4-8", name: "Claude Opus 4.8" }];
    vi.spyOn(client, "listModels").mockResolvedValue(models);
    const onUpdateSession = vi.fn();
    const onCommandExecuted = vi.fn();

    render(
      <Composer
        routerClient={client}
        sessionId="session-active"
        provider="claudecode"
        onUpdateSession={onUpdateSession}
        onCommandExecuted={onCommandExecuted}
      />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/model" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/model" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/model" }));
    expect(textarea.value).toBe("/model ");

    await waitFor(() =>
      expect(menu().getByRole("option", { name: "Claude Opus 4.8" })).toBeTruthy(),
    );
    fireEvent.mouseDown(menu().getByRole("option", { name: "Claude Opus 4.8" }));

    expect(client.listModels).toHaveBeenCalledWith("claudecode");
    expect(onUpdateSession).toHaveBeenCalledWith({ model: "claude-opus-4-8" });
    await waitFor(() =>
      expect(onCommandExecuted).toHaveBeenCalledWith("Model set to Claude Opus 4.8"),
    );
    expect(textarea.value).toBe("");
  });

  it("a no-argument command (/task) executes on first select — inserts text, stages no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/task" } });
    await waitFor(() => expect(menu().getByRole("option", { name: "/task" })).toBeTruthy());

    fireEvent.mouseDown(menu().getByRole("option", { name: "/task" }));

    expect(textarea.value).toBe("/task ");
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
  });

  it("unrecognized text after / just filters — no menu, no crash, no chip", async () => {
    const client = new FakeRouterClient();
    render(<Composer routerClient={client} sessionId="session-active" />);
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    fireEvent.change(textarea, { target: { value: "/zzzznotacommand" } });
    await new Promise((r) => setTimeout(r, 200));

    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.queryByLabelText(/Remove /)).toBeNull();
    expect(textarea.value).toBe("/zzzznotacommand");
  });

  it("drives the two-level command menu entirely by keyboard: open, descend, arrow, back, execute", async () => {
    const client = new FakeRouterClient();
    vi.spyOn(client, "listAgents").mockResolvedValue([
      fixtureAgent("autonomous-executor"),
      fixtureAgent("investigator"),
    ]);
    const onUpdateSession = vi.fn();

    render(
      <Composer routerClient={client} sessionId="session-active" onUpdateSession={onUpdateSession} />,
    );
    const textarea = screen.getByPlaceholderText(/Message the agent/) as HTMLTextAreaElement;

    // open — typing "/" surfaces the root command list
    fireEvent.change(textarea, { target: { value: "/agent" } });
    await waitFor(() => expect(screen.getByRole("listbox")).toBeTruthy());

    // descend — Enter on the "/agent" group advances to level 2
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(textarea.value).toBe("/agent ");
    await waitFor(() =>
      expect(menu().getByRole("option", { name: "investigator" })).toBeTruthy(),
    );

    // arrow — ArrowDown moves off the first leaf onto the second
    fireEvent.keyDown(textarea, { key: "ArrowDown" });

    // back — Escape drops the argument, returning to level 1
    fireEvent.keyDown(textarea, { key: "Escape" });
    expect(textarea.value).toBe("/agent");
    await waitFor(() => expect(menu().getByRole("option", { name: "/agent" })).toBeTruthy());

    // descend again, then execute via Enter on the (reset) first leaf
    fireEvent.keyDown(textarea, { key: "Enter" });
    expect(textarea.value).toBe("/agent ");
    await waitFor(() =>
      expect(menu().getByRole("option", { name: "autonomous-executor" })).toBeTruthy(),
    );
    fireEvent.keyDown(textarea, { key: "Enter" });

    expect(onUpdateSession).toHaveBeenCalledWith({ agent: "autonomous-executor" });
    expect(textarea.value).toBe("");
  });
});
