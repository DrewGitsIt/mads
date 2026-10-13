import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { FakeRouterClient, fixtureConfig } from "../../../router/fixtures";
import { exportCommand } from "./export";
import type { CommandApi } from "./types";

let clicked: string[];
beforeEach(() => {
  clicked = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    clicked.push(this.getAttribute("href") ?? "");
  });
});
afterEach(() => vi.restoreAllMocks());

function setup(exportFormat: "" | "md" | "json") {
  const client = new FakeRouterClient();
  vi.spyOn(client, "getConfig").mockResolvedValue({
    ...fixtureConfig,
    export_format: exportFormat,
  });
  const update = vi
    .spyOn(client, "updateConfig")
    .mockResolvedValue(fixtureConfig);
  let card: ((close: () => void) => ReactNode) | undefined;
  const api = {
    ctx: { routerClient: client, sessionId: "s1" },
    fail: vi.fn(),
    notify: vi.fn(),
    openCard: (r: (close: () => void) => ReactNode) => (card = r),
  } as unknown as CommandApi;
  return { api, update, card: () => card };
}

const url = (f: string) =>
  new FakeRouterClient().exportSessionUrl("s1", f as "md");

describe("/export", () => {
  it("downloads immediately in the saved format", async () => {
    const { api, card } = setup("json");
    await exportCommand.execute({ args: "" }, api);
    expect(clicked).toEqual([url("json")]);
    expect(card()).toBeUndefined();
  });

  it("an argument overrides once without saving", async () => {
    const { api, update } = setup("json");
    await exportCommand.execute({ args: "md" }, api);
    expect(clicked).toEqual([url("md")]);
    expect(update).not.toHaveBeenCalled();
  });

  it("rejects an invalid argument", async () => {
    const { api } = setup("");
    await exportCommand.execute({ args: "pdf" }, api);
    expect(api.fail).toHaveBeenCalledWith(expect.stringContaining("pdf"));
    expect(clicked).toEqual([]);
  });

  it("opens the dialog when no format is saved, and Always saves the preference", async () => {
    const { api, update, card } = setup("");
    await exportCommand.execute({ args: "" }, api);
    expect(clicked).toEqual([]);
    render(<>{card()!(vi.fn())}</>);
    fireEvent.click(screen.getByRole("radio", { name: /JSON/ }));
    expect(
      (screen.getByLabelText("Always use this format") as HTMLInputElement)
        .checked,
    ).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(
        expect.objectContaining({ export_format: "json" }),
      ),
    );
    expect(clicked).toEqual([url("json")]);
  });

  it("unchecked downloads without saving", async () => {
    const { api, update, card } = setup("");
    await exportCommand.execute({ args: "" }, api);
    render(<>{card()!(vi.fn())}</>);
    fireEvent.click(screen.getByLabelText("Always use this format"));
    fireEvent.click(screen.getByRole("button", { name: "Download" }));
    expect(clicked).toEqual([url("md")]);
    expect(update).not.toHaveBeenCalled();
  });
});
