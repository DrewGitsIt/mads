import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WorkspaceStep } from "./WorkspaceStep";

describe("WorkspaceStep", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("creates a workspace and advances on success", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ id: "ws-1", name: "mads" }), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    const onNext = vi.fn();
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(onNext).toHaveBeenCalledTimes(1));
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(init.body as string)).toEqual({ name: "mads", repos: [{ path: "/repo", primary: true }] });
  });

  it("shows an inline error and does not advance on failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("boom", { status: 500 })));
    const onNext = vi.fn();
    render(<WorkspaceStep onNext={onNext} onBack={vi.fn()} />);

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: "mads" } });
    fireEvent.change(screen.getByLabelText(/primary repo path/i), { target: { value: "/repo" } });
    fireEvent.click(screen.getByText("Create workspace"));

    await waitFor(() => expect(screen.getByText(/couldn't create the workspace/i)).toBeTruthy());
    expect(onNext).not.toHaveBeenCalled();
  });
});
