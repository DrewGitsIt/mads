import { describe, expect, it } from "vitest";
import type { ToolUse } from "../../router/types";
import { linkToolTitle, toolTitle } from "./toolDisplay";

const call = (name: string, input?: Record<string, unknown>): ToolUse => ({
  id: "t1",
  name,
  input,
});

// Link ID → the linked session's name, as useLinkNames resolves it.
const resolve = (id: string) =>
  id === "lnk_0123456789ab" ? "MADS Pipeline Design Study" : undefined;

describe("linkToolTitle", () => {
  it("titles the mads-links verbs by session name", () => {
    expect(linkToolTitle(call("mcp__mads-links__list_linked_sessions"))).toBe(
      "List linked sessions",
    );
    expect(
      linkToolTitle(call("mcp__mads-links__message_session", { session: "Docs", text: "hi" })),
    ).toBe("Message → Docs");
    expect(linkToolTitle(call("mcp__mads-links__get_session_summary", { session: "Docs" }))).toBe(
      "Read summary → Docs",
    );
    expect(
      linkToolTitle(
        call("mcp__mads-links__get_session_turns", { session: "Docs", from_turn: 260, count: 5 }),
      ),
    ).toBe("Read turns 260–264 → Docs");
    expect(
      linkToolTitle(call("mcp__mads-links__get_session_turns", { session: "Docs", from_turn: 7 })),
    ).toBe("Read turn 7 → Docs");
    expect(
      linkToolTitle(call("mcp__mads-links__get_session_turns", { session: "Docs", count: 3 })),
    ).toBe("Read last 3 turns → Docs");
    expect(linkToolTitle(call("mcp__mads-links__get_session_turns", { session: "Docs" }))).toBe(
      "Read last turn → Docs",
    );
    expect(linkToolTitle(call("mcp__mads-links__check_mailbox", { session: "Docs" }))).toBe(
      "Check mailbox → Docs",
    );
    expect(
      linkToolTitle(
        call("mcp__mads-links__set_attention", { session: "Docs", cadence: "on_signal" }),
      ),
    ).toBe("Set attention → Docs: on_signal");
  });

  it("resolves a session ID passed as `session` to a name", () => {
    const byId = (id: string) => (id === "ses_8828632c3616ad3b" ? "Docs" : undefined);
    expect(
      linkToolTitle(
        call("mcp__mads-links__check_mailbox", { session: "ses_8828632c3616ad3b" }),
        byId,
      ),
    ).toBe("Check mailbox → Docs");
  });

  it("resolves legacy link_id and subject_session_id through the resolver", () => {
    const legacy = "mcp__orchestrate-ses_8828632c3616ad3b__";
    expect(
      linkToolTitle(
        call(`${legacy}send_message`, { link_id: "lnk_0123456789ab", text: "x" }),
        resolve,
      ),
    ).toBe("Message → MADS Pipeline Design Study");
    expect(
      linkToolTitle(
        call(`${legacy}get_turns`, {
          subject_session_id: "lnk_0123456789ab",
          from_turn: 1,
          to_turn: 3,
        }),
        resolve,
      ),
    ).toBe("Read turns 1–3 → MADS Pipeline Design Study");
    expect(linkToolTitle(call(`${legacy}list_links`))).toBe("List linked sessions");
  });

  it("falls back to a short ID when the legacy ID is unknown", () => {
    expect(
      linkToolTitle(
        call("mcp__orchestrate-ses_8828632c3616ad3b__get_summary", {
          subject_session_id: "ses_ffeeddccbbaa9988",
        }),
        resolve,
      ),
    ).toBe("Read summary → ses_ffeeddcc…");
  });

  it("leaves other tools alone", () => {
    expect(linkToolTitle(call("Bash", { command: "ls" }))).toBeUndefined();
    expect(linkToolTitle(call("mcp__other__send_message", { link_id: "x" }))).toBeUndefined();
    expect(toolTitle({ ...call("Bash"), title: "Run ls" })).toBe("Run ls");
  });

  it("wins over a server-built title", () => {
    expect(
      toolTitle({
        ...call("mcp__mads-links__check_mailbox", { session: "Docs" }),
        title: "check_mailbox",
      }),
    ).toBe("Check mailbox → Docs");
  });
});
