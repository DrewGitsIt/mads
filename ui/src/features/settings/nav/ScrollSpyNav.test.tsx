import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ScrollSpyNav } from "./ScrollSpyNav";
import { SECTION_IDS, SECTION_LABELS } from "../sections";

describe("ScrollSpyNav", () => {
  it("renders every section in page order", () => {
    render(<ScrollSpyNav activeId="profiles" onNavigate={() => {}} />);
    const rendered = screen.getAllByRole("button").map((b) => b.textContent);
    expect(rendered).toEqual(SECTION_IDS.map((id) => SECTION_LABELS[id]));
    expect(rendered).toEqual([
      "Profiles",
      "New sessions",
      "Agents & models",
      "Workspaces",
      "Pipelines",
      "Synthesis",
      "Appearance",
      "Editor",
      "Credentials & environment",
      "Experimental",
      "System",
      "About mads",
    ]);
  });

  it("separates the groups: profiles | sessions | runs | look & secrets | housekeeping", () => {
    const { container } = render(<ScrollSpyNav activeId="profiles" onNavigate={() => {}} />);
    const sequence = Array.from(container.querySelector("nav")!.children).map((el) =>
      el.getAttribute("role") === "separator" ? "|" : el.textContent,
    );
    expect(sequence).toEqual([
      "Profiles",
      "|",
      "New sessions",
      "Agents & models",
      "Workspaces",
      "|",
      "Pipelines",
      "Synthesis",
      "|",
      "Appearance",
      "Editor",
      "Credentials & environment",
      "|",
      "Experimental",
      "System",
      "About mads",
    ]);
  });

  it("marks the active section and calls onNavigate on click", () => {
    const onNavigate = vi.fn();
    render(<ScrollSpyNav activeId="sessions" onNavigate={onNavigate} />);
    expect(screen.getByText("New sessions").getAttribute("aria-current")).toBe("page");
    fireEvent.click(screen.getByText("Credentials & environment"));
    expect(onNavigate).toHaveBeenCalledWith("credentials");
  });

  it("highlights the trailing About section when active (page-bottom reachability)", () => {
    render(<ScrollSpyNav activeId="about" onNavigate={() => {}} />);
    expect(screen.getByText("About mads").getAttribute("aria-current")).toBe("page");
  });
});
