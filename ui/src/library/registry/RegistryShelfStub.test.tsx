import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { RegistryShelfStub } from "./RegistryShelfStub";

describe("RegistryShelfStub", () => {
  it("renders placeholder copy and nothing else", () => {
    render(<RegistryShelfStub />);
    expect(screen.getByText("Registry")).toBeTruthy();
    expect(screen.getByText("Remote registry browsing lands in a later work item.")).toBeTruthy();
  });
});
