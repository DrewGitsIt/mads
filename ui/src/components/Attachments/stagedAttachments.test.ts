import { describe, expect, it, vi } from "vitest";
import type { RouterClient } from "../../router/client";
import type { Attachment, AttachmentOwner } from "../../router/types";
import { StagedAttachments } from "./stagedAttachments";

/** A client whose uploads settle when the test says, recording every call. */
function fakeClient(label: string) {
  const uploads: { owner: AttachmentOwner; name: string; resolve: (a: Attachment) => void }[] = [];
  const deletes: { owner: AttachmentOwner; id: string }[] = [];
  let n = 0;
  const client = {
    uploadAttachment: vi.fn(
      (owner: AttachmentOwner, _file: Blob, name: string) =>
        new Promise<Attachment>((resolve) => uploads.push({ owner, name, resolve })),
    ),
    deleteAttachment: vi.fn(async (owner: AttachmentOwner, id: string) => {
      deletes.push({ owner, id });
    }),
  } as unknown as RouterClient;
  const land = (i: number) => uploads[i].resolve({ id: `${label}-${++n}`, name: uploads[i].name } as Attachment);
  return { client, uploads, deletes, land };
}

const flush = () => new Promise((r) => setTimeout(r, 0));
const file = (name: string) => new File(["x"], name);

describe("StagedAttachments.retarget (mesh mode)", () => {
  it("moves ready files to the new device under a fresh draft, deleting them where they were", async () => {
    const mac = fakeClient("mac");
    const pop = fakeClient("pop");
    const list = new StagedAttachments({ draft: "d1" }, mac.client, true);
    list.add([file("a.png")]);
    mac.land(0);
    await flush();
    expect(list.readyIds()).toEqual(["mac-1"]);

    list.retarget(pop.client);
    expect(mac.deletes).toEqual([{ owner: { draft: "d1" }, id: "mac-1" }]);
    expect(list.getSnapshot()[0].status).toBe("uploading");
    expect(pop.uploads).toHaveLength(1);
    const newOwner = pop.uploads[0].owner as { draft: string };
    expect(newOwner.draft).not.toBe("d1");
    expect(list.owner).toEqual(newOwner);

    pop.land(0);
    await flush();
    expect(list.readyIds()).toEqual(["pop-1"]);
  });

  it("an upload still in flight to the old device is deleted there when it lands, never adopted", async () => {
    const mac = fakeClient("mac");
    const pop = fakeClient("pop");
    const list = new StagedAttachments({ draft: "d1" }, mac.client, true);
    list.add([file("a.png")]);
    list.retarget(pop.client);
    mac.land(0); // the stale reply
    await flush();
    expect(mac.deletes.map((d) => d.id)).toEqual(["mac-1"]);
    expect(list.readyIds()).toEqual([]);
    pop.land(0);
    await flush();
    expect(list.readyIds()).toEqual(["pop-1"]);
  });

  it("is a no-op for the client it already uses", () => {
    const mac = fakeClient("mac");
    const list = new StagedAttachments({ draft: "d1" }, mac.client, true);
    list.add([file("a.png")]);
    list.retarget(mac.client);
    expect(mac.uploads).toHaveLength(1);
    expect(list.owner).toEqual({ draft: "d1" });
  });

  it("a list that doesn't keep its files can't move them: they ask to be attached again", async () => {
    const mac = fakeClient("mac");
    const pop = fakeClient("pop");
    const list = new StagedAttachments({ draft: "d1" }, mac.client);
    list.add([file("a.png")]);
    mac.land(0);
    await flush();
    list.retarget(pop.client);
    expect(list.getSnapshot()[0]).toMatchObject({ status: "failed", error: "Attach this file again" });
    expect(pop.uploads).toHaveLength(0);
  });

  it("lets go of kept files once sent or cleared", async () => {
    const mac = fakeClient("mac");
    const pop = fakeClient("pop");
    const list = new StagedAttachments({ draft: "d1" }, mac.client, true);
    list.add([file("a.png")]);
    mac.land(0);
    await flush();
    list.rehome({ session: "ses_1" });
    // After the send, nothing is held to re-upload.
    list.retarget(pop.client);
    expect(pop.uploads).toHaveLength(0);
  });
});
