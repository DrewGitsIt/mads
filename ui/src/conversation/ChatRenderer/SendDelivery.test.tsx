import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import type { Delivery } from "./failedSends";
import { formatMessageTime } from "./messageTime";
import { SendDelivery } from "./SendDelivery";

const authFailure: Delivery = { error: "Login expired · Please run /login", errorKind: "auth", attempts: [] };

describe("SendDelivery", () => {
  it("reads signed out, with the reason in the tooltip and Sign in, while the login is signed out", () => {
    const onSignIn = vi.fn();
    const onRetry = vi.fn();
    render(<SendDelivery delivery={authFailure} onRetry={onRetry} signIn={{ provider: "Claude Code", onSignIn }} />);
    const label = screen.getByText("Signed out of Claude Code");
    expect(label.getAttribute("title")).toBe("Login expired · Please run /login");
    expect(screen.queryByText("Not delivered")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(onSignIn).toHaveBeenCalledOnce();
    // Retry stays as it was.
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("reads not delivered once the login is signed in again", () => {
    render(<SendDelivery delivery={authFailure} />);
    expect(screen.getByRole("status").textContent).toContain("Not delivered");
    expect(screen.getByText("Login expired · Please run /login")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  });

  it("offers no Sign in for a failure that isn't about auth", () => {
    render(
      <SendDelivery
        delivery={{ error: "ECONNREFUSED", attempts: [] }}
        signIn={{ provider: "Claude Code", onSignIn: vi.fn() }}
      />,
    );
    expect(screen.getByRole("status").textContent).toContain("Not delivered");
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  });

  it("says when each earlier attempt failed", () => {
    const at = new Date();
    at.setHours(10, 42, 0, 0);
    render(<SendDelivery delivery={{ attempts: [{ error: "Login expired", timestamp: at.toISOString() }] }} />);
    fireEvent.click(screen.getByRole("button", { name: "1 failed attempt" }));
    expect(screen.getByText(`failed at ${formatMessageTime(at)}`)).toBeTruthy();
  });
});
