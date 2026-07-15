import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ModelPill } from "./ModelPill";

describe("ModelPill", () => {
  it("groups models by provider and reports the selection", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify(["claude-code"]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(
          JSON.stringify([{ id: "claude-opus-4.6", name: "claude-opus-4.6", default: true }]),
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
});
