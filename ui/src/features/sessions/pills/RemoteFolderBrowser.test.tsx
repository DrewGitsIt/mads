import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { RemoteFolderBrowser } from "./RemoteFolderBrowser";

afterEach(() => vi.unstubAllGlobals());

describe("RemoteFolderBrowser", () => {
  it("bounds a listing with a timeout, and says when the device didn't answer", async () => {
    let signal: AbortSignal | null | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        signal = init?.signal;
        return Promise.reject(new DOMException("signal timed out", "TimeoutError"));
      }),
    );
    render(<RemoteFolderBrowser peer="pop-os" onPick={() => {}} />);
    expect(await screen.findByText("pop-os didn't answer in 10 s")).toBeTruthy();
    expect(signal).toBeInstanceOf(AbortSignal);
  });

  it("shows the folders it lists", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(JSON.stringify({ path: "/home/drew", entries: [{ name: "repo", path: "/home/drew/repo" }] }), { status: 200 })),
    );
    render(<RemoteFolderBrowser peer="pop-os" onPick={() => {}} />);
    expect(await screen.findByText("/home/drew")).toBeTruthy();
  });
});
