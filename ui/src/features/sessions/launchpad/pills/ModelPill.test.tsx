import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ModelPill } from "./ModelPill";

describe("ModelPill", () => {
  it("groups models by provider and reports the selection", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({ models: [{ id: "claude-opus-4.6", name: "claude-opus-4.6", default: true }] }),
          { status: 200 },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    const onChange = vi.fn();

    render(<ModelPill onChange={onChange} />);
    fireEvent.click(screen.getByText("Select model"));

    await waitFor(() => expect(screen.getByText("claude-code")).toBeTruthy());
    fireEvent.click(screen.getByText("claude-opus-4.6"));

    expect(onChange).toHaveBeenCalledWith(
      "claude-code",
      expect.objectContaining({ id: "claude-opus-4.6" }),
    );
    vi.unstubAllGlobals();
  });

  it("shows the resolved display name on the closed pill when the model is known", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({ models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true }] }),
          { status: 200 },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<ModelPill provider="claude-code" model="claude-sonnet-5" onChange={vi.fn()} />);

    await waitFor(() => expect(screen.getByText(/Claude Sonnet 5/)).toBeTruthy());
    vi.unstubAllGlobals();
  });

  it("falls back to the raw model ID on the closed pill when the model is unknown", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({ models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true }] }),
          { status: 200 },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<ModelPill provider="claude-code" model="claude-retired-1" onChange={vi.fn()} />);

    await waitFor(() => expect(screen.getByText(/claude-retired-1/)).toBeTruthy());
    vi.unstubAllGlobals();
  });

  it("shows the resolved default model and provider on the closed pill when unset", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify([{ name: "claude-code", matrix: {} }]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({ models: [{ id: "claude-sonnet-5", name: "Claude Sonnet 5", default: true }] }),
          { status: 200 },
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <ModelPill
        resolved={{ value: "claude-sonnet-5", source: "provider_default" }}
        resolvedProvider="claude-code"
        onChange={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(screen.getByText(/Claude Sonnet 5 · claude-code \(default\)/)).toBeTruthy(),
    );
    vi.unstubAllGlobals();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<ModelPill onChange={vi.fn()} />);
    expect(screen.getByText("Select model")).toBeTruthy();
    vi.unstubAllGlobals();
  });
});
