import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { SessionWakeError } from "../../router/client";
import { RefusedSend } from "./RefusedSend";
import { refusalOf } from "./sendRefusal";

describe("refusalOf", () => {
  it("reads the host's refusal codes, and nothing else", () => {
    expect(refusalOf(new SessionWakeError("no", "full_trust_chat_refused", false), "hi")).toEqual({ text: "hi", kind: "c1", message: "no" });
    expect(refusalOf(new SessionWakeError("held", "held_unsupported", false), "hi")?.kind).toBe("c1");
    expect(refusalOf(new SessionWakeError('POST /sessions/{id}/messages needs "chat" here; this device has "view"', "no_access", false), "hi")?.kind).toBe("view");
    expect(refusalOf(new SessionWakeError("this host grants device mac nothing", "no_access", false), "hi")?.kind).toBe("none");
    expect(refusalOf(new SessionWakeError("spawn", "spawn_failed", true), "hi")).toBeNull();
    expect(refusalOf(new Error("boom"), "hi")).toBeNull();
  });
});

describe("RefusedSend", () => {
  it("keeps the message, says Not sent and why, and retries", () => {
    const onRetry = vi.fn();
    render(<RefusedSend refused={{ text: "check the rollback", kind: "c1", message: "" }} host="pop-os" device="Drews-iPhone" onRetry={onRetry} />);
    expect(screen.getByText("check the rollback")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("Not sent This device can't message Full access sessions on pop-os.");
    fireEvent.click(screen.getByRole("button", { name: "Why?" }));
    expect(document.body.textContent).toContain("Drews-iPhone may chat with mads there, and pop-os refuses its messages to Full access sessions.");
    expect(document.body.textContent).toContain("Settings → Paired devices → Drews-iPhone");
    expect(screen.getByRole("button", { name: "Hide" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalled();
  });

  it("says when the device may only view there", () => {
    render(<RefusedSend refused={{ text: "hi", kind: "view", message: "" }} host="pop-os" device="kitchen" onRetry={() => {}} />);
    expect(screen.getByRole("status").textContent).toContain("This device may only view mads on pop-os.");
    fireEvent.click(screen.getByRole("button", { name: "Why?" }));
    expect(document.body.textContent).toContain("Sending needs chat with.");
  });
});
