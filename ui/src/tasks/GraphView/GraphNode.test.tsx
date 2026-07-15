import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { GraphNode } from "./GraphNode";
import { layout } from "./layout";
import { fixtureTasks } from "../../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

function nodeFor(id: string) {
  const { nodes } = layout(fixtureTasks);
  const n = nodes.find((node) => node.id === id);
  if (!n) throw new Error(`no layout node for ${id}`);
  return n;
}

function renderSvg(children: React.ReactNode) {
  return render(<svg>{children}</svg>);
}

describe("GraphNode", () => {
  it("dashes the ring for a dependency-queued (calm) block", () => {
    renderSvg(<GraphNode node={nodeFor("tsk_queued")} tasksById={tasksById} />);
    const g = screen.getByText("Roll retry backoff out to the export worker").closest("g")!;
    expect(g.getAttribute("data-status")).toBe("Queued");
  });

  it("marks a needs-human block distinctly, no dashed ring", () => {
    renderSvg(<GraphNode node={nodeFor("tsk_needs_human")} tasksById={tasksById} />);
    const g = screen.getByText("Rotate the staging database credentials").closest("g")!;
    expect(g.getAttribute("data-status")).toBe("Needs human");
  });

  it("invokes onClick with the task", () => {
    const onClick = vi.fn();
    renderSvg(<GraphNode node={nodeFor("tsk_ready")} tasksById={tasksById} onClick={onClick} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledWith(fixtureTasks.find((t) => t.id === "tsk_ready"));
  });
});
