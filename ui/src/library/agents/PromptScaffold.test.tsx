import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { PromptScaffold } from "./PromptScaffold";

describe("PromptScaffold", () => {
  it("renders structured-prompt guidance", () => {
    render(<PromptScaffold />);
    expect(screen.getByText("Prompt scaffold")).toBeTruthy();
    expect(screen.getByText(/Boundaries:/)).toBeTruthy();
  });
});
