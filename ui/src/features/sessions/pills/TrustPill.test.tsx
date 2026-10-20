import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TrustPill } from "./TrustPill";
import { trustFallback, trustLevels } from "./trustLevels";

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

  it("renders an accurate one-line description for every trust option with no provider selected", () => {
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));

    for (const level of trustLevels()) {
      expect(screen.getByText(level.description)).toBeTruthy();
    }
  });

  it("says a restricted level is enforced when the provider's matrix declares it native", () => {
    render(
      <TrustPill
        resolved={{ value: "limited", source: "config" }}
        matrix={{ trust_limited: "native", trust_read_only: "native" }}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Limited \(default\)/));

    for (const level of trustLevels({ trust_limited: "native", trust_read_only: "native" })) {
      expect(screen.getByText(level.description)).toBeTruthy();
    }
    expect(screen.getAllByText(/enforced by the selected provider\.$/)).toHaveLength(2);
  });

  it("says a restricted level is unenforced when the provider's matrix omits it", () => {
    render(
      <TrustPill
        resolved={{ value: "limited", source: "config" }}
        matrix={{}}
        onChange={() => {}}
      />,
    );
    fireEvent.click(screen.getByText(/Limited \(default\)/));

    expect(screen.getAllByText(/not enforced by the selected provider/)).toHaveLength(2);
  });

  it("disables every restricted level on a full-access-only provider, and says why", () => {
    const onChange = vi.fn();
    render(
      <TrustPill
        value="full"
        provider="hermes"
        matrix={{ trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" }}
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByText("Full access"));
    expect(screen.getAllByText(/not available on hermes, which runs at Full access only\.$/)).toHaveLength(3);
    const limited = screen.getByText("Limited").closest("button");
    expect(limited?.disabled).toBe(true);
    fireEvent.click(limited!);
    expect(onChange).not.toHaveBeenCalled();
    // Auto-review is not relabelled as falling back to Limited: Limited isn't available either.
    expect(screen.getByText("Auto-review")).toBeTruthy();
    expect(trustFallback("auto_review", { trust_limited: "unsupported", trust_read_only: "unsupported", trust_auto_review: "unsupported" })).toBeUndefined();
  });

  it("lists the levels in picker order, Auto-review second", () => {
    expect(trustLevels().map((l) => l.value)).toEqual(["full", "auto_review", "limited", "read_only"]);
    render(<TrustPill resolved={{ value: "limited", source: "config" }} onChange={() => {}} />);
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    expect(screen.getByText("Auto-review")).toBeTruthy();
  });

  it("marks Auto-review as running Limited on a provider with no reviewer", () => {
    const onChange = vi.fn();
    render(
      <TrustPill
        resolved={{ value: "limited", source: "config" }}
        matrix={{ trust_limited: "emulated", trust_auto_review: "unsupported" }}
        provider="kiro-cli"
        onChange={onChange}
      />,
    );
    fireEvent.click(screen.getByText(/Limited \(default\)/));
    expect(screen.getByText(/kiro-cli has no reviewer, so this runs as Limited\.$/)).toBeTruthy();
    fireEvent.click(screen.getByText("Auto-review (runs as Limited on kiro-cli)"));
    // Still requested as auto_review — the record keeps the ask, the
    // Router reports the fallback.
    expect(onChange).toHaveBeenCalledWith("auto_review");
  });

  it("shows the fallback on the trigger once Auto-review is picked on such a provider", () => {
    render(
      <TrustPill
        value="auto_review"
        matrix={{ trust_auto_review: "unsupported" }}
        provider="kiro-cli"
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("Auto-review (runs as Limited on kiro-cli)")).toBeTruthy();
  });

  it("leaves Auto-review unannotated where the provider runs a reviewer, or is unknown", () => {
    const native = trustLevels({ trust_auto_review: "native" }, "claude-code");
    expect(native.find((l) => l.value === "auto_review")).toMatchObject({
      label: "Auto-review",
      fallback: undefined,
    });
    expect(trustLevels().find((l) => l.value === "auto_review")?.label).toBe("Auto-review");
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
