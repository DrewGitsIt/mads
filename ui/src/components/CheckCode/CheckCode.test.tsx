import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { CheckCode, groupDigits } from "./CheckCode";

describe("CheckCode", () => {
  it("groups six digits in threes, and reads them digit by digit", () => {
    expect(groupDigits("482913")).toBe("482 913");
    expect(groupDigits("12")).toBe("12");
    render(<CheckCode code="482913">Check pop-os shows the same number.</CheckCode>);
    expect(screen.getByLabelText("Check code 4 8 2 9 1 3").textContent).toBe("482 913");
    expect(screen.getByText("Check pop-os shows the same number.")).toBeTruthy();
  });
});
