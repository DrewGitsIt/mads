import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useTemplateValidation } from "./useTemplateValidation";

describe("useTemplateValidation", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reports all seeded errors together, not just the first", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            errors: [
              { field: "stages[0].name", message: "duplicate stage name \"build\" (also stages[1])" },
              { field: "stages[1].agent", message: "unknown agent \"nope\" — not in the registry (spec §9)" },
              {
                field: "stages[2].on_fail.loop_back",
                message: "target \"test\" is not an earlier stage — loops must point backward (spec §6)",
              },
            ],
          }),
          { status: 400 },
        ),
      ),
    );

    const { result } = renderHook(() => useTemplateValidation());
    let ok = true;
    await act(async () => {
      ok = await result.current.validate("name: x\nstages: []");
    });

    expect(ok).toBe(false);
    expect(result.current.valid).toBe(false);
    expect(result.current.errors).toHaveLength(3);
    expect(result.current.errors.map((e) => e.field)).toEqual([
      "stages[0].name",
      "stages[1].agent",
      "stages[2].on_fail.loop_back",
    ]);
  });

  it("reports valid:true with no errors on success", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({ valid: true }), { status: 200 })),
    );

    const { result } = renderHook(() => useTemplateValidation());
    let ok = false;
    await act(async () => {
      ok = await result.current.validate("name: x\nstages:\n  - name: build\n    command: make\n");
    });

    expect(ok).toBe(true);
    expect(result.current.valid).toBe(true);
    expect(result.current.errors).toHaveLength(0);
  });
});
