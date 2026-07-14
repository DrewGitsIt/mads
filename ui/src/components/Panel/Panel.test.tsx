import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Panel, PanelBody, PanelFooter, PanelHeader } from "./Panel";

describe("Panel", () => {
  it("renders header, body, and footer slots", () => {
    render(
      <Panel>
        <PanelHeader>Title</PanelHeader>
        <PanelBody>Body</PanelBody>
        <PanelFooter>Footer</PanelFooter>
      </Panel>,
    );
    expect(screen.getByText("Title")).toBeTruthy();
    expect(screen.getByText("Body")).toBeTruthy();
    expect(screen.getByText("Footer")).toBeTruthy();
  });
});
