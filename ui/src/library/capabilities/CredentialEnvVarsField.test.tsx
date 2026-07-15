import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { CredentialEnvVarsField } from "./CredentialEnvVarsField";

describe("CredentialEnvVarsField", () => {
  it("renders only the env var name — never a value-shaped string", () => {
    render(<CredentialEnvVarsField value={["GITHUB_TOKEN"]} onChange={vi.fn()} />);

    expect(screen.getByText("GITHUB_TOKEN ×")).toBeTruthy();
    // The DOM must never contain anything resembling a credential value
    // (e.g. a long opaque token string) — only the name we seeded.
    const rendered = document.body.textContent ?? "";
    expect(rendered).not.toMatch(/gh[po]_[A-Za-z0-9]{20,}/);
    expect(rendered).not.toContain("sk-");
  });

  it("adding a name calls onChange with names only, never a value field", () => {
    const onChange = vi.fn();
    render(<CredentialEnvVarsField value={[]} onChange={onChange} />);

    fireEvent.change(screen.getByPlaceholderText("e.g. GITHUB_TOKEN"), {
      target: { value: "ANTHROPIC_API_KEY" },
    });
    fireEvent.click(screen.getByText("Add"));

    expect(onChange).toHaveBeenCalledWith(["ANTHROPIC_API_KEY"]);
    // The call is a plain string array — no object with a value/secret field.
    const [names] = onChange.mock.calls[0] as [unknown];
    expect(Array.isArray(names)).toBe(true);
    expect(typeof (names as string[])[0]).toBe("string");
  });

  it("removing a name calls onChange without transmitting anything but names", () => {
    const onChange = vi.fn();
    render(<CredentialEnvVarsField value={["GITHUB_TOKEN", "ANTHROPIC_API_KEY"]} onChange={onChange} />);

    fireEvent.click(screen.getByText("GITHUB_TOKEN ×"));

    expect(onChange).toHaveBeenCalledWith(["ANTHROPIC_API_KEY"]);
  });
});
