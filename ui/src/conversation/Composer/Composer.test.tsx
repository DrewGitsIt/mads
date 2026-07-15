import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Composer } from "./Composer";
import { FakeRouterClient } from "../../router/fixtures";

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
});
