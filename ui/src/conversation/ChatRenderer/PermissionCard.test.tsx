import { describe, expect, it, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PermissionCard } from "./PermissionCard";
import type { PermissionRequest } from "../../router/types";

describe("PermissionCard", () => {
  const pending: PermissionRequest = {
    id: "sess-1-perm-1",
    tool: "Bash",
    input: { command: "rm -rf /tmp/x" },
    status: "pending",
  };

  it("shows Allow/Deny buttons for a pending request", () => {
    render(<PermissionCard request={pending} onResolve={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Allow" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deny" })).toBeTruthy();
    expect(screen.getByText("Bash")).toBeTruthy();
    expect(screen.getByText(/command: rm -rf/)).toBeTruthy();
  });

  it("calls onResolve with allow when Allow is clicked", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined);
    render(<PermissionCard request={pending} onResolve={onResolve} />);
    fireEvent.click(screen.getByRole("button", { name: "Allow" }));
    expect(onResolve).toHaveBeenCalledWith("sess-1-perm-1", "allow");
  });

  it("calls onResolve with deny when Deny is clicked", async () => {
    const onResolve = vi.fn().mockResolvedValue(undefined);
    render(<PermissionCard request={pending} onResolve={onResolve} />);
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    expect(onResolve).toHaveBeenCalledWith("sess-1-perm-1", "deny");
  });

  it("disables both buttons while a decision is resolving", async () => {
    let resolveFn: () => void = () => {};
    const onResolve = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          resolveFn = resolve;
        }),
    );
    render(<PermissionCard request={pending} onResolve={onResolve} />);
    fireEvent.click(screen.getByText("Allow"));
    expect(screen.getByText("Allowing…")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deny" }).hasAttribute("disabled")).toBe(true);
    resolveFn();
    await waitFor(() => expect(screen.getByText("Allow")).toBeTruthy());
  });

  it("shows no actions and a status chip for a resolved request", () => {
    const allowed: PermissionRequest = { ...pending, status: "allowed" };
    render(<PermissionCard request={allowed} onResolve={vi.fn()} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("allowed")).toBeTruthy();
  });

  it("shows an expired label for an unanswered, timed-out request", () => {
    const expired: PermissionRequest = { ...pending, status: "expired" };
    render(<PermissionCard request={expired} onResolve={vi.fn()} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText(/expired/)).toBeTruthy();
  });

  it("shows a cancelled label when the agent stopped waiting", () => {
    const cancelled: PermissionRequest = { ...pending, status: "cancelled" };
    render(<PermissionCard request={cancelled} onResolve={vi.fn()} />);
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("cancelled — the agent stopped waiting")).toBeTruthy();
  });

  it("shows the harness's reason when present", () => {
    render(
      <PermissionCard
        request={{ ...pending, reason: "Path is outside allowed working directories" }}
        onResolve={vi.fn()}
      />,
    );
    expect(screen.getByText("Path is outside allowed working directories")).toBeTruthy();
  });
});
