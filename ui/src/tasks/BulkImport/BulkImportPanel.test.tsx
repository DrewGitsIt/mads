import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { BulkImportPanel } from "./BulkImportPanel";
import { FakeRouterClient } from "../../router/fixtures";

describe("BulkImportPanel", () => {
  it("shows a parse error for invalid JSON", () => {
    render(
      <BulkImportPanel sessionId="session-active" routerClient={new FakeRouterClient()} onClose={vi.fn()} />,
    );
    fireEvent.change(screen.getByLabelText("Tasks JSON"), { target: { value: "not json" } });
    fireEvent.click(screen.getByText("Validate"));
    expect(screen.getByText("Not valid JSON.")).toBeTruthy();
  });

  it("shows a parsed count for valid JSON", () => {
    render(
      <BulkImportPanel sessionId="session-active" routerClient={new FakeRouterClient()} onClose={vi.fn()} />,
    );
    fireEvent.change(screen.getByLabelText("Tasks JSON"), {
      target: { value: '[{"title": "a"}, {"title": "b"}]' },
    });
    fireEvent.click(screen.getByText("Validate"));
    expect(screen.getByText("2 task(s) parsed.")).toBeTruthy();
  });

  it("imports successfully and closes the sheet", async () => {
    const client = new FakeRouterClient();
    const onImported = vi.fn();
    const onClose = vi.fn();
    render(
      <BulkImportPanel
        sessionId="session-active"
        routerClient={client}
        onClose={onClose}
        onImported={onImported}
      />,
    );
    fireEvent.change(screen.getByLabelText("Tasks JSON"), {
      target: { value: '[{"title": "Imported task"}]' },
    });
    fireEvent.click(screen.getByText("Import"));

    await waitFor(() => expect(onImported).toHaveBeenCalled());
    expect(onClose).toHaveBeenCalled();
    expect(onImported.mock.calls[0][0][0].title).toBe("Imported task");
  });

  it("renders a RouterValidationError's field errors without closing", async () => {
    const client = new FakeRouterClient();
    const onClose = vi.fn();
    render(
      <BulkImportPanel sessionId="session-active" routerClient={client} onClose={onClose} />,
    );
    fireEvent.change(screen.getByLabelText("Tasks JSON"), {
      target: { value: '[{"description": "missing a title"}]' },
    });
    fireEvent.click(screen.getByText("Import"));

    await waitFor(() => expect(screen.getByText(/title: required/)).toBeTruthy());
    expect(onClose).not.toHaveBeenCalled();
  });
});
