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
});
