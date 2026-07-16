import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { LibrarySurface } from "./LibrarySurface";
import { FakeEventSource } from "../router/testFakeEventSource";
import { FLAGS } from "../featureFlags";

describe("LibrarySurface", () => {
  beforeEach(() => {
    FakeEventSource.instances = [];
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("[]", { status: 200 })));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    FLAGS.remoteRegistry = false;
  });

  it("switches the active shelf via nav", async () => {
    render(<LibrarySurface />);
    await waitFor(() => expect(screen.getByText("No agents yet.")).toBeTruthy());

    fireEvent.click(screen.getByText("Capabilities"));
    await waitFor(() => expect(screen.getByText("No capabilities yet.")).toBeTruthy());

    fireEvent.click(screen.getByText("Workspaces"));
    await waitFor(() => expect(screen.getByText("No workspaces yet.")).toBeTruthy());

    fireEvent.click(screen.getByText("Templates"));
    await waitFor(() => expect(screen.getByText("No templates yet.")).toBeTruthy());
  });

  it("hides the Registry nav button when the remote-registry flag is off", async () => {
    FLAGS.remoteRegistry = false;
    render(<LibrarySurface />);
    await waitFor(() => expect(screen.getByText("No agents yet.")).toBeTruthy());
    expect(screen.queryByText("Registry")).toBeNull();
  });

  it("shows the Registry nav button and shelf when the flag is on", async () => {
    FLAGS.remoteRegistry = true;
    render(<LibrarySurface />);
    await waitFor(() => expect(screen.getByText("No agents yet.")).toBeTruthy());
    fireEvent.click(screen.getByText("Registry"));
    await waitFor(() => expect(screen.getByLabelText("Search the registry")).toBeTruthy());
  });
});
