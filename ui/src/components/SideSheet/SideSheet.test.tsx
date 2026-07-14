import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  SideSheet,
  SideSheetBody,
  SideSheetFooter,
  SideSheetHeader,
} from "./SideSheet";

describe("SideSheet", () => {
  it("renders header, body, and footer slots", () => {
    render(
      <SideSheet>
        <SideSheetHeader>Title</SideSheetHeader>
        <SideSheetBody>Body</SideSheetBody>
        <SideSheetFooter>Footer</SideSheetFooter>
      </SideSheet>,
    );
    expect(screen.getByText("Title")).toBeTruthy();
    expect(screen.getByText("Body")).toBeTruthy();
    expect(screen.getByText("Footer")).toBeTruthy();
  });

  it("applies the requested width", () => {
    render(<SideSheet width={500}>content</SideSheet>);
    expect(screen.getByRole("complementary").style.width).toBe("500px");
  });
});
