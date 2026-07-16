import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UpdatesAboutPanel } from "./UpdatesAboutPanel";
import { fixtureUpdateCheckResult, fixtureVersionInfo } from "../../../router/fixtures";

describe("UpdatesAboutPanel", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function stubFetch() {
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/version")) {
        return Promise.resolve(new Response(JSON.stringify(fixtureVersionInfo), { status: 200 }));
      }
      if (url.endsWith("/update/check")) {
        return Promise.resolve(new Response(JSON.stringify(fixtureUpdateCheckResult), { status: 200 }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    return fetchMock;
  }

  it("renders version info and an enabled update-check button, without checking the feed", async () => {
    const fetchMock = stubFetch();
    render(<UpdatesAboutPanel />);

    await waitFor(() => expect(screen.getByText(/version 0\.1\.0-dev/i)).toBeTruthy());
    expect((screen.getByRole("button", { name: /check for updates/i }) as HTMLButtonElement).disabled).toBe(
      false,
    );
    expect(fetchMock.mock.calls.some((c) => String(c[0]).endsWith("/update/check"))).toBe(false);
  });

  it("checks for updates when the button is clicked", async () => {
    stubFetch();
    render(<UpdatesAboutPanel />);
    await waitFor(() => expect(screen.getByText(/version 0\.1\.0-dev/i)).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /check for updates/i }));

    await waitFor(() => expect(screen.getByText(/latest build/i)).toBeTruthy());
  });

  it("renders About copy", () => {
    stubFetch();
    render(<UpdatesAboutPanel />);
    expect(screen.getByText(/orchestrates multi-agent/i)).toBeTruthy();
  });
});
