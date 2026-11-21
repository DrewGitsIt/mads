import type { RouterClient } from "../../router/client";
import type { Attachment, AttachmentOwner } from "../../router/types";

/**
 * One file staged in a composer: uploading as soon as it's pasted, dropped
 * or picked, then ready (the Router has it on disk and an id to send) or
 * failed. Only metadata lives here — the bytes go from the browser's own
 * file handle straight to the Router, never through JS memory, so staging
 * many or large files costs nothing to hold.
 */
export interface StagedAttachment {
  /** Local and stable from the moment it's staged — the server id comes later. */
  key: string;
  name: string;
  size: number;
  status: "uploading" | "ready" | "failed";
  attachment?: Attachment;
  error?: string;
}

const EMPTY: StagedAttachment[] = [];

let nextKey = 0;

/**
 * A composer's staged attachments. Held at module scope (see `stagedFor`)
 * rather than in component state — same reason as composerDrafts.ts: the
 * composer unmounts when you navigate away, and neither the staged files
 * nor an upload still in flight should be lost when it does.
 */
export class StagedAttachments {
  private items: StagedAttachment[] = EMPTY;
  private readonly listeners = new Set<() => void>();
  /** Keys removed while their upload was in flight: delete the file when it lands. */
  private readonly abandoned = new Set<string>();
  /**
   * Each item's current upload attempt. A retarget starts a new attempt, so a
   * reply for an older one (an upload to the device we just left) is stale:
   * its file is deleted where it landed, never adopted.
   */
  private readonly attempts = new Map<string, number>();
  /**
   * The picked/pasted files themselves, kept only by a list that can change
   * device before it's sent (the launchpad, mesh mode) so they can be sent
   * again. A picked or dropped File is a handle to the file on disk; a pasted
   * image is held in memory until the send, a remove or a clear lets go.
   */
  private readonly files = new Map<string, Blob>();

  constructor(
    private ownerValue: AttachmentOwner,
    private client: RouterClient,
    private readonly retainFiles = false,
  ) {}

  get owner(): AttachmentOwner {
    return this.ownerValue;
  }

  /**
   * Where a staged file's bytes (or thumbnail) are served: from the Router
   * this list uploaded it through, which after a retarget may not be the one
   * the surrounding UI is about to use.
   */
  attachmentUrl(attachmentId: string, variant?: "thumbnail"): string {
    return this.client.attachmentUrl(this.ownerValue, attachmentId, variant);
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  getSnapshot = (): StagedAttachment[] => this.items;

  /** Server ids of every upload that's landed, in staging order. */
  readyIds(): string[] {
    return this.items.flatMap((i) =>
      i.status === "ready" && i.attachment ? [i.attachment.id] : [],
    );
  }

  uploading(): boolean {
    return this.items.some((i) => i.status === "uploading");
  }

  add(files: File[], now: Date = new Date()): void {
    if (files.length === 0) return;
    const staged = files.map((file) => ({
      file,
      item: {
        key: `att-${++nextKey}`,
        name: stagedName(file, now),
        size: file.size,
        status: "uploading" as const,
      },
    }));
    this.set([...this.items, ...staged.map((s) => s.item)]);
    for (const { file, item } of staged) {
      if (this.retainFiles) this.files.set(item.key, file);
      this.upload(item.key, file, item.name);
    }
  }

  private upload(key: string, file: Blob, name: string): void {
    const attempt = (this.attempts.get(key) ?? 0) + 1;
    this.attempts.set(key, attempt);
    const owner = this.ownerValue;
    const client = this.client;
    client.uploadAttachment(owner, file, name).then(
      (attachment) => {
        if (this.attempts.get(key) !== attempt) {
          // Retargeted (or removed) while this was in flight.
          void client.deleteAttachment(owner, attachment.id).catch(() => {});
          return;
        }
        if (this.abandoned.delete(key)) {
          void client.deleteAttachment(owner, attachment.id).catch(() => {});
          return;
        }
        this.update(key, { status: "ready", attachment });
      },
      (err: unknown) => {
        if (this.attempts.get(key) !== attempt) return;
        this.abandoned.delete(key);
        this.update(key, {
          status: "failed",
          error: err instanceof Error ? err.message : String(err),
        });
      },
    );
  }

  /**
   * Moves a not-yet-sent list to another device's Router (mesh mode: the
   * launchpad's "on" pill changed). Files already uploaded are deleted
   * where they are, and every kept file uploads again, under a fresh draft,
   * through `client`. A no-op for the client it already uses.
   */
  retarget(client: RouterClient): void {
    if (client === this.client) return;
    const oldClient = this.client;
    const oldOwner = this.ownerValue;
    this.client = client;
    if ("draft" in oldOwner) this.ownerValue = { draft: newDraftId() };
    for (const item of this.items) {
      this.attempts.set(item.key, (this.attempts.get(item.key) ?? 0) + 1);
      if (item.status === "ready" && item.attachment) {
        void oldClient.deleteAttachment(oldOwner, item.attachment.id).catch(() => {});
      }
    }
    this.abandoned.clear();
    this.set(
      this.items.map((i) =>
        this.files.has(i.key)
          ? { key: i.key, name: i.name, size: i.size, status: "uploading" as const }
          : { ...i, status: "failed" as const, attachment: undefined, error: "Attach this file again" },
      ),
    );
    for (const item of this.items) {
      const file = this.files.get(item.key);
      if (file) this.upload(item.key, file, item.name);
    }
  }

  /** The user took it back before sending: the stored file goes too. */
  remove(key: string): void {
    const item = this.items.find((i) => i.key === key);
    if (!item) return;
    this.set(this.items.filter((i) => i.key !== key));
    this.files.delete(key);
    if (item.status === "uploading") this.abandoned.add(key);
    else if (item.attachment) {
      void this.client.deleteAttachment(this.ownerValue, item.attachment.id).catch(() => {});
    }
  }

  /** After a send: the files now belong to the message, so they stay on disk. */
  clear(): void {
    this.files.clear();
    this.set(EMPTY);
  }

  /**
   * Re-points this list at a new owner without touching the files — the
   * Router has just moved a launchpad draft's directory to the session
   * created from it (POST /sessions' attachment_draft).
   */
  rehome(owner: AttachmentOwner): void {
    this.ownerValue = owner;
    // Sent: the session has them now, and its device can't change.
    this.files.clear();
  }

  private update(key: string, patch: Partial<StagedAttachment>): void {
    if (!this.items.some((i) => i.key === key)) return;
    this.set(this.items.map((i) => (i.key === key ? { ...i, ...patch } : i)));
  }

  private set(items: StagedAttachment[]): void {
    this.items = items;
    for (const l of this.listeners) l();
  }
}

const lists = new Map<string, StagedAttachments>();

/** A live session's composer attachments. */
export function stagedForSession(sessionId: string, client: RouterClient): StagedAttachments {
  const key = `session:${sessionId}`;
  let list = lists.get(key);
  if (!list) {
    list = new StagedAttachments({ session: sessionId }, client);
    lists.set(key, list);
  }
  return list;
}

const LAUNCHPAD = "launchpad";

/** The launchpad's attachments, under a draft id until a session exists. */
export function stagedForLaunchpad(client: RouterClient): StagedAttachments {
  let list = lists.get(LAUNCHPAD);
  if (!list) {
    list = new StagedAttachments({ draft: newDraftId() }, client, true);
    lists.set(LAUNCHPAD, list);
  }
  return list;
}

/**
 * The launchpad's goal just became `sessionId` and the Router adopted its
 * draft: its staged attachments become that session's (so the opening
 * message's auto-send carries them), and the launchpad starts a fresh draft.
 */
export function handLaunchpadToSession(sessionId: string): void {
  const list = lists.get(LAUNCHPAD);
  if (!list) return;
  lists.delete(LAUNCHPAD);
  list.rehome({ session: sessionId });
  lists.set(`session:${sessionId}`, list);
}

/** Test-only: forget every list so module state doesn't leak between cases. */
export function clearAllStagedAttachments(): void {
  lists.clear();
}

function newDraftId(): string {
  return `draft_${crypto.randomUUID().replaceAll("-", "")}`;
}

/**
 * Keeps a file's own name, except for a pasted screenshot: the clipboard
 * calls every image "image.png", which tells the agent (and you, scrolling
 * back) nothing, so it's named for when it was pasted instead.
 */
export function stagedName(file: File, now: Date): string {
  if (file.name && file.name !== "image.png") return file.name;
  const ext = file.type.startsWith("image/")
    ? file.type.slice("image/".length).replace("jpeg", "jpg")
    : "bin";
  const p = (n: number) => String(n).padStart(2, "0");
  const stamp = `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}-${p(now.getHours())}${p(now.getMinutes())}${p(now.getSeconds())}`;
  return `screenshot-${stamp}.${ext}`;
}

export function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unitIndex]}`;
}
