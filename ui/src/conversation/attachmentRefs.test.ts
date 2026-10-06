import { describe, expect, it } from "vitest";
import { ATTACHMENTS_HEADER, appendAttachments, splitAttachments } from "./attachmentRefs";

const a = {
  id: "1a2b",
  name: "shot.png",
  path: "/Users/me/.mads/attachments/ses_1/1a2b/shot.png",
  size: 1,
  thumbnail: true,
};
const b = {
  id: "3c4d",
  name: "build log.txt",
  path: "/Users/me/.mads/attachments/ses_1/3c4d/build log.txt",
  size: 1,
  thumbnail: false,
};

describe("attachment refs", () => {
  it("round-trips the Router's format", () => {
    const text = appendAttachments("why is this red?\n", [a, b]);
    // Byte-for-byte what services/router/internal/attachments.AppendToMessage writes.
    expect(text).toBe(`why is this red?\n\n${ATTACHMENTS_HEADER}\n- ${a.path}\n- ${b.path}`);
    expect(splitAttachments(text)).toEqual({
      body: "why is this red?",
      attachments: [
        { id: "1a2b", name: "shot.png", path: a.path },
        { id: "3c4d", name: "build log.txt", path: b.path },
      ],
    });
  });

  it("handles a message that is only attachments", () => {
    const text = appendAttachments("  ", [a]);
    expect(text).toBe(`${ATTACHMENTS_HEADER}\n- ${a.path}`);
    expect(splitAttachments(text).body).toBe("");
    expect(splitAttachments(text).attachments).toHaveLength(1);
  });

  it("leaves text alone that only quotes the header", () => {
    const text = `I saw "${ATTACHMENTS_HEADER}" earlier\n\n${ATTACHMENTS_HEADER}\nnot a path list`;
    expect(splitAttachments(text)).toEqual({ body: text, attachments: [] });
    expect(splitAttachments("plain")).toEqual({
      body: "plain",
      attachments: [],
    });
  });
});
