import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ModelField } from "./ModelField";

describe("ModelField", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists only models the provider reports and calls onChange on selection", async () => {
    const fetchMock = vi.fn().mockImplementation((input: URL) => {
      const url = input.toString();
      if (url.endsWith("/providers")) {
        return Promise.resolve(new Response(JSON.stringify(["claude-code"]), { status: 200 }));
      }
      return Promise.resolve(
        new Response(JSON.stringify([{ id: "opus", name: "Opus" }]), { status: 200 }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    const onChange = vi.fn();
    render(<ModelField onChange={onChange} />);

    await waitFor(() => expect(screen.getByText("Opus")).toBeTruthy());
    fireEvent.change(screen.getByLabelText("Preferred model"), { target: { value: "opus" } });
    expect(onChange).toHaveBeenCalledWith("opus");
  });
});
