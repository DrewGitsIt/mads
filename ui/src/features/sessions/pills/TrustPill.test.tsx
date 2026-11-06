import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TrustPill } from "./TrustPill";
import { trustFallback, trustLevels } from "./trustLevels";

const NATIVE = { trust_limited: "native", trust_read_only: "native", trust_auto_review: "native" } as const;

describe("TrustPill", () => {
  it("shows the resolved default trust level when unset", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    expect(screen.getByText(/Limited \(default\)/)).toBeTruthy();
  });

  it("shows the plain placeholder when nothing resolves", () => {
    render(<TrustPill onChange={() => {}} />);
    expect(screen.getByText("Select trust")).toBeTruthy();
  });

  it("reports the selected trust level", () => {
    const onChange = vi.fn();
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={onChange} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    fireEvent.click(screen.getByText("Full access"));
    expect(onChange).toHaveBeenCalledWith("full");
  });

  it("explains the current level in the footer, and the hovered or focused one instead", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} matrix={NATIVE} onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    const detail = screen.getByTestId("trust-detail");
    expect(detail.textContent).toBe("Limited: The agent asks you before running commands or other risky actions.");
    fireEvent.mouseEnter(screen.getByText("Read-only").closest("button")!);
    expect(detail.textContent).toBe("Read-only: Blocks edits and commands entirely.");
    fireEvent.focus(screen.getByText("Full access").closest("button")!);
    expect(detail.textContent).toMatch(/^Full access: /);
  });

  it("shows no ⚠ where the provider runs every level natively", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} matrix={NATIVE} provider="claude-code" onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    expect(screen.queryAllByRole("img", { name: "Not supported by this provider" })).toHaveLength(0);
    expect(trustLevels(NATIVE, "Claude Code").every((l) => l.warning === undefined)).toBe(true);
  });

  it("marks emulated and unsupported levels alike, saying what each runs as", () => {
    const goose = { trust_limited: "emulated", trust_read_only: "unsupported", trust_auto_review: "native" } as const;
    render(<TrustPill resolved={{ value: "full", source: "config" }} matrix={goose} provider="goose" onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Full access \(default\)/));
    // Limited and Read-only rows: one ⚠ each.
    expect(screen.getAllByRole("img", { name: "Not supported by this provider" })).toHaveLength(2);
    fireEvent.mouseEnter(screen.getByText("Limited").closest("button")!);
    expect(screen.getByTestId("trust-detail").textContent).toContain(
      "goose does not support this trust level. Runs best-effort, with mads answering its permission requests.",
    );
    fireEvent.mouseEnter(screen.getByText("Read-only").closest("button")!);
    expect(screen.getByTestId("trust-detail").textContent).toContain(
      "goose does not support this trust level. Runs as Full access.",
    );
  });

  it("says Auto-review runs as Limited on a provider with no reviewer, and still asks for it", () => {
    const onChange = vi.fn();
    render(
      <TrustPill
        resolved={{ value: "limited", source: "config" }}
        matrix={{ trust_limited: "native", trust_read_only: "native", trust_auto_review: "unsupported" }}
        provider="codex"
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    const autoReview = screen.getByText("Auto-review").closest("button")!;
    fireEvent.mouseEnter(autoReview);
    expect(screen.getByTestId("trust-detail").textContent).toContain(
      "Codex (ChatGPT) does not support this trust level. Runs as Limited.",
    );
    fireEvent.click(autoReview);
    // Still requested as auto_review — the record keeps the ask, the
    // Router reports the fallback.
    expect(onChange).toHaveBeenCalledWith("auto_review");
  });

  it("puts the ⚠ and its sentence on the pill when the current level isn't supported", () => {
    render(
      <TrustPill
        value="read_only"
        matrix={{ trust_limited: "native", trust_read_only: "unsupported" }}
        provider="goose"
        onChange={() => {}}
      />,
    );
    const trigger = screen.getByRole("button", { name: /trust Read-only/ });
    expect(trigger.getAttribute("title")).toBe("goose does not support this trust level. Runs as Full access.");
    expect(screen.getByRole("img", { name: "Not supported by this provider" })).toBeTruthy();
  });

  it("warns in the menu but not on the pill before the provider's matrix arrives", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    const trigger = screen.getByRole("button", { name: /trust Limited \(default\)/ });
    expect(trigger.getAttribute("title")).toBeNull();
    fireEvent.click(trigger);
    expect(screen.getByTestId("trust-detail").textContent).toContain(
      "This provider hasn't said whether it supports this trust level.",
    );
  });

  it("preselects Full on a full-access-only provider and leaves the rest unpickable, saying why", () => {
    const onChange = vi.fn();
    const hermes = { trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" } as const;
    // Nothing staged and a configured default of Limited: still shows Full.
    render(<TrustPill resolved={{ value: "limited", source: "config" }} provider="hermes" matrix={hermes} onChange={onChange} />);
    const trigger = screen.getByRole("button", { name: /trust Full access/ });
    expect(trigger.getAttribute("title")).toBe("Hermes can't enforce approvals; it runs at full trust");
    fireEvent.click(trigger);
    const limited = screen.getByText("Limited").closest("button")!;
    expect(limited.getAttribute("aria-disabled")).toBe("true");
    fireEvent.mouseEnter(limited);
    expect(screen.getByTestId("trust-detail").textContent).toContain(
      "Hermes does not support this trust level. It runs at Full access only.",
    );
    fireEvent.click(limited);
    expect(onChange).not.toHaveBeenCalled();
    // Auto-review doesn't claim a Limited fallback: Limited isn't available either.
    expect(trustFallback("auto_review", hermes)).toBeUndefined();
  });

  it("lists the levels in picker order, Auto-review second", () => {
    expect(trustLevels().map((l) => l.value)).toEqual(["full", "auto_review", "limited", "read_only"]);
  });

  it("reports no fallback where the provider runs a reviewer, or is unknown", () => {
    const native = trustLevels({ trust_auto_review: "native" }, "Claude Code");
    expect(native.find((l) => l.value === "auto_review")).toMatchObject({
      label: "Auto-review",
      fallback: undefined,
      warning: undefined,
    });
    expect(trustLevels().find((l) => l.value === "auto_review")?.fallback).toBeUndefined();
  });

  it("does not show an agent-intents block when the agent declares nothing", () => {
    render(
      <TrustPill
        resolved={{ value: "full", source: "config" }}
        agentPermissions={{}}
        intentMatrix={{ deny_commands: "native" }}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Full access \(default\)/));

    expect(screen.queryByText("This agent also declares")).toBeNull();
  });

  it("flags an agent-declared intent the provider does not enforce", () => {
    render(
      <TrustPill
        resolved={{ value: "full", source: "config" }}
        agentPermissions={{ denied_commands: ["rm"] }}
        intentMatrix={{}}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Full access \(default\)/));

    expect(screen.getByText("This agent also declares")).toBeTruthy();
    expect(screen.getByText("Block commands")).toBeTruthy();
    expect(screen.getByText("not enforced by the selected provider.")).toBeTruthy();
  });

  it("marks an agent-declared intent as enforced only when the matrix says native", () => {
    render(
      <TrustPill
        resolved={{ value: "full", source: "config" }}
        agentPermissions={{ allowed_paths: ["src/"] }}
        intentMatrix={{ path_scope: "native" }}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Full access \(default\)/));

    expect(screen.getByText("enforced by the selected provider.")).toBeTruthy();
  });

  it("never claims enforcement for an agent intent absent from the matrix", () => {
    render(
      <TrustPill
        resolved={{ value: "full", source: "config" }}
        agentPermissions={{ denied_commands: ["rm"], allowed_paths: ["src/"] }}
        intentMatrix={{ deny_commands: "native" }}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Full access \(default\)/));

    // deny_commands is native, path_scope is absent from the matrix — must
    // read as unenforced, never silently omitted or upgraded.
    expect(screen.getByText("enforced by the selected provider.")).toBeTruthy();
    expect(screen.getByText("not enforced by the selected provider.")).toBeTruthy();
  });
});
