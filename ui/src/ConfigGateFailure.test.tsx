import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ConfigGateFailure } from "./ConfigGateFailure";

describe("ConfigGateFailure", () => {
  afterEach(() => {
    delete (window as { mads?: unknown }).mads;
  });

  // H7: in Electron, the 401 screen names the token file main couldn't use,
  // and why — main tells it the path, never the token.
  it("names the token file and the problem main reported", async () => {
    (window as { mads?: unknown }).mads = {
      auth: { tokenPath: async () => ({ path: "/home/u/.mads/local.token", problem: "no install token at /home/u/.mads/local.token" }) },
    };
    render(<ConfigGateFailure onRetry={() => {}} unauthorized />);
    await waitFor(() => expect(screen.getByText("/home/u/.mads/local.token")).toBeTruthy());
    expect(screen.getByText(/no install token at/)).toBeTruthy();
    expect(screen.getByText(/Restart the app/)).toBeTruthy();
  });
});
