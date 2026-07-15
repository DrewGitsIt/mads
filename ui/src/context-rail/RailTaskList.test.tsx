import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { RailTaskList } from "./RailTaskList";
import { FakeRouterClient, fixtureTasks } from "../router/fixtures";

const tasksById = Object.fromEntries(fixtureTasks.map((t) => [t.id, t]));

describe("RailTaskList", () => {
  it("pins draft cards above the regular compact list", () => {
    const { container } = render(
      <RailTaskList tasks={fixtureTasks} tasksById={tasksById} routerClient={new FakeRouterClient()} />,
    );
    const html = container.innerHTML;
    expect(html.indexOf("Add circuit breaker")).toBeLessThan(
      html.indexOf("Add retry backoff to the ingest worker"),
    );
  });

  it("filters the non-draft list by a single status", () => {
    render(
      <RailTaskList
        tasks={fixtureTasks}
        tasksById={tasksById}
        routerClient={new FakeRouterClient()}
        statusFilter="done"
      />,
    );
    expect(screen.getByText("Add retry metrics dashboard")).toBeTruthy();
    expect(screen.queryByText("Write regression test for the retry race")).toBeNull();
  });

  it("invokes onSelectTask when a compact row is clicked", () => {
    const onSelectTask = vi.fn();
    render(
      <RailTaskList
        tasks={fixtureTasks}
        tasksById={tasksById}
        routerClient={new FakeRouterClient()}
        onSelectTask={onSelectTask}
      />,
    );
    fireEvent.click(screen.getByText("Add retry backoff to the ingest worker"));
    expect(onSelectTask).toHaveBeenCalledWith(
      fixtureTasks.find((t) => t.id === "tsk_ready"),
    );
  });
});
