import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { TrustPill } from "./TrustPill";
import { trustLevels } from "./trustLevels";

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
