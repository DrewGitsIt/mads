import { describe, expect, it } from "vitest";
import { agentIntentNotes, agentIntents, intentEnforcementNote, intentSupport } from "./intents";

describe("agentIntents", () => {
  it("is empty when the agent declares no restrictions", () => {
    expect(agentIntents(undefined)).toEqual([]);
    expect(agentIntents({})).toEqual([]);
  });

  it("includes deny_commands when denied_commands is non-empty", () => {
    expect(agentIntents({ denied_commands: ["rm"] })).toEqual(["deny_commands"]);
  });

  it("includes path_scope when allowed_paths is non-empty", () => {
    expect(agentIntents({ allowed_paths: ["src/"] })).toEqual(["path_scope"]);
  });

  it("includes both when both are declared", () => {
    expect(agentIntents({ denied_commands: ["rm"], allowed_paths: ["src/"] })).toEqual([
      "deny_commands",
      "path_scope",
    ]);
  });
});

describe("intentSupport", () => {
  it("treats a missing matrix as unsupported", () => {
    expect(intentSupport(undefined, "deny_commands")).toBe("unsupported");
  });

  it("treats an intent absent from the matrix as unsupported", () => {
    expect(intentSupport({}, "deny_commands")).toBe("unsupported");
  });

  it("reads the declared support level", () => {
    expect(intentSupport({ deny_commands: "native" }, "deny_commands")).toBe("native");
    expect(intentSupport({ path_scope: "emulated" }, "path_scope")).toBe("emulated");
  });
});

describe("intentEnforcementNote", () => {
  it("never claims enforcement for unsupported", () => {
    expect(intentEnforcementNote("unsupported")).toMatch(/not enforced/);
  });

  it("flags emulated as best-effort, not a guarantee", () => {
    expect(intentEnforcementNote("emulated")).toMatch(/best-effort/);
  });

  it("says enforced only for native", () => {
    expect(intentEnforcementNote("native")).toMatch(/^enforced/);
  });
});

describe("agentIntentNotes", () => {
  it("is empty when the agent declares nothing, regardless of matrix", () => {
    expect(agentIntentNotes(undefined, { deny_commands: "native" })).toEqual([]);
  });

  it("marks a declared intent unenforced when the matrix omits it", () => {
    const notes = agentIntentNotes({ denied_commands: ["rm"] }, {});
    expect(notes).toEqual([
      {
        intent: "deny_commands",
        label: "Block commands",
        support: "unsupported",
        note: "not enforced by the selected provider.",
      },
    ]);
  });

  it("marks a declared intent enforced when the matrix says native", () => {
    const notes = agentIntentNotes({ allowed_paths: ["src/"] }, { path_scope: "native" });
    expect(notes[0].support).toBe("native");
    expect(notes[0].note).toMatch(/^enforced/);
  });

  it("never omits an unsupported declared intent's honesty marker", () => {
    const notes = agentIntentNotes(
      { denied_commands: ["rm"], allowed_paths: ["src/"] },
      { deny_commands: "native", path_scope: "unsupported" },
    );
    const pathNote = notes.find((n) => n.intent === "path_scope");
    expect(pathNote?.note).toBe("not enforced by the selected provider.");
  });
});
