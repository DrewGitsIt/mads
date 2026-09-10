import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { TemplatesShelf } from "./TemplatesShelf";
import { FakeEventSource } from "../../router/testFakeEventSource";

vi.mock("@monaco-editor/react", () => ({
  Editor: (props: { value: string }) => <textarea data-testid="fake-yaml-editor" value={props.value} readOnly />,
}));

/**
 * Routes fetch by URL suffix so a test can give /templates and
 * /templates/validate-all independent responses — needed because the shelf
 * now fires both on mount and a single blanket mock (as the older tests
 * above use) can't tell them apart.
 */
function mockFetchRoutes(routes: [suffix: string, response: () => Response][]) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input.toString();
    const match = routes.find(([suffix]) => url.endsWith(suffix));
    return Promise.resolve(match ? match[1]() : new Response("{}", { status: 404 }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("TemplatesShelf", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("shows the empty state when there are no templates", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("No templates yet.")).toBeTruthy());
  });

  it("opens the editor for a new template", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("No templates yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("New template"));
    expect(screen.getByRole("complementary")).toBeTruthy();
  });

  it("renders a card per fetched template", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify([
            {
              id: "t1",
              name: "deploy",
              source: "name: deploy\nstages: []\n",
              version: 1,
              created_at: "t",
              updated_at: "t",
            },
          ]),
          { status: 200 },
        ),
      ),
    );
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("deploy")).toBeTruthy());
  });

  it("shows the template's version as a quiet chip", async () => {
    mockFetchRoutes([
      [
        "/templates",
        () =>
          new Response(
            JSON.stringify([
              {
                id: "t1",
                name: "deploy",
                source: "name: deploy\nstages: []\n",
                version: 3,
                created_at: "t",
                updated_at: "t",
              },
            ]),
            { status: 200 },
          ),
      ],
    ]);
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("v3")).toBeTruthy());
  });

  it("badges a template that fails the validate-all sweep", async () => {
    mockFetchRoutes([
      [
        "/templates/validate-all",
        () =>
          new Response(
            JSON.stringify({
              results: [
                { id: "t1", name: "deploy", valid: true, errors: [] },
                {
                  id: "t2",
                  name: "old-one",
                  valid: false,
                  errors: [{ field: "source", message: "unknown field \"retires\"" }],
                },
              ],
            }),
            { status: 200 },
          ),
      ],
      [
        "/templates",
        () =>
          new Response(
            JSON.stringify([
              { id: "t1", name: "deploy", source: "name: deploy\nstages: []\n", version: 1, created_at: "t", updated_at: "t" },
              { id: "t2", name: "old-one", source: "name: old-one\nstages: []\n", version: 1, created_at: "t", updated_at: "t" },
            ]),
            { status: 200 },
          ),
      ],
    ]);
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("old-one")).toBeTruthy());
    await waitFor(() => expect(screen.getAllByText("Invalid")).toHaveLength(1));
    // The valid template never gets the badge.
    expect(
      screen.getByText("deploy").closest("button")!.textContent,
    ).not.toContain("Invalid");
  });

  it("still lists templates normally when the validate-all sweep fails", async () => {
    mockFetchRoutes([
      ["/templates/validate-all", () => new Response("boom", { status: 500 })],
      [
        "/templates",
        () =>
          new Response(
            JSON.stringify([
              { id: "t1", name: "deploy", source: "name: deploy\nstages: []\n", version: 1, created_at: "t", updated_at: "t" },
            ]),
            { status: 200 },
          ),
      ],
    ]);
    render(<TemplatesShelf />);
    await waitFor(() => expect(screen.getByText("deploy")).toBeTruthy());
    expect(screen.queryByText("Invalid")).toBeNull();
  });
});
