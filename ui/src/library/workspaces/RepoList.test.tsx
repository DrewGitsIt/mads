import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { RepoList } from "./RepoList";

describe("RepoList", () => {
  it("adds a repo, marking the first one primary", () => {
    const onChange = vi.fn();
    render(<RepoList repos={[]} onChange={onChange} />);

    fireEvent.change(screen.getByLabelText("Repo path"), {
      target: { value: "./services/router" },
    });
    fireEvent.click(screen.getByText("Add repo"));

    expect(onChange).toHaveBeenCalledWith([{ path: "./services/router", primary: true }]);
  });

  it("adds a folder chosen in the native dialog straight away", async () => {
    window.mads = { dialog: { pickDirectory: vi.fn().mockResolvedValue("/home/me/repo/site") } };
    const onChange = vi.fn();
    render(<RepoList repos={[{ path: "~/repo/mads", primary: true }]} onChange={onChange} />);

    fireEvent.click(screen.getByText("Browse…"));

    await waitFor(() =>
      expect(onChange).toHaveBeenCalledWith([
        { path: "~/repo/mads", primary: true },
        { path: "/home/me/repo/site", primary: false },
      ]),
    );
    delete window.mads;
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
