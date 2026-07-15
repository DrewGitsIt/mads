import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { RepoList } from "./RepoList";

describe("RepoList", () => {
  it("adds a repo, marking the first one primary", () => {
    const onChange = vi.fn();
    render(<RepoList repos={[]} onChange={onChange} />);

    fireEvent.change(screen.getByPlaceholderText("e.g. ./services/router"), {
      target: { value: "./services/router" },
    });
    fireEvent.click(screen.getByText("Add repo"));

    expect(onChange).toHaveBeenCalledWith([{ path: "./services/router", primary: true }]);
  });

  it("switches the primary repo", () => {
    const onChange = vi.fn();
    render(
      <RepoList
        repos={[
          { path: "./a", primary: true },
          { path: "./b", primary: false },
        ]}
        onChange={onChange}
      />,
    );

    const radios = screen.getAllByRole("radio");
    fireEvent.click(radios[1]);

    expect(onChange).toHaveBeenCalledWith([
      { path: "./a", primary: false },
      { path: "./b", primary: true },
    ]);
  });

  it("removes a repo", () => {
    const onChange = vi.fn();
    render(<RepoList repos={[{ path: "./a", primary: true }]} onChange={onChange} />);

    fireEvent.click(screen.getByText("Remove"));

    expect(onChange).toHaveBeenCalledWith([]);
  });
});
