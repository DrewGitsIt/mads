import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TemplatesShelf } from "./TemplatesShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string }) => <textarea data-testid="fake-yaml-editor" value={props.value} readOnly />,
}));

describe("TemplatesShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the empty state when there are no templates", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("No templates yet.")).toBeTruthy());
  });

  it("opens the editor for a new template", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("No templates yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("New template"));
    expect(screen.getByRole("complementary")).toBeTruthy();
  });

  it("renders a card per fetched template", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            { id: "t1", name: "deploy", source: "name: deploy\nstages: []\n", created_at: "t", updated_at: "t" },
          ]),
          { status: 200 },
        ),
      ),
    );
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("deploy")).toBeTruthy());
  });
});
