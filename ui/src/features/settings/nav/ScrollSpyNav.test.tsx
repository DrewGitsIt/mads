import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ScrollSpyNav } from "./ScrollSpyNav";
import { SECTION_IDS, SECTION_LABELS } from "../sections";

describe("ScrollSpyNav", () => {
  it("renders every section in the task's specified order", () => {
    render(<ScrollSpyNav activeId="profiles" onNavigate={() => {}} />);
    const rendered = screen.getAllByRole("button").map((b) => b.textContent);
    expect(rendered).toEqual(SECTION_IDS.map((id) => SECTION_LABELS[id]));
  });

  it("marks the active section and calls onNavigate on click", () => {
    const onNavigate = vi.fn();
    render(<ScrollSpyNav activeId="general" onNavigate={onNavigate} />);
    expect(screen.getByText("General").getAttribute("aria-current")).toBe("page");
    fireEvent.click(screen.getByText("Credentials"));
    expect(onNavigate).toHaveBeenCalledWith("credentials");
  });

  it("highlights the trailing Updates & About section when active (page-bottom reachability)", () => {
    render(<ScrollSpyNav activeId="updates" onNavigate={() => {}} />);
    expect(screen.getByText("Updates & About").getAttribute("aria-current")).toBe("page");
  });
});
