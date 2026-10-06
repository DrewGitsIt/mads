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

  constructor(
    private ownerValue: AttachmentOwner,
    private readonly client: RouterClient,
  ) {}

  get owner(): AttachmentOwner {
    return this.ownerValue;
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
    const owner = this.ownerValue;
    for (const { file, item } of staged) {
      this.client.uploadAttachment(owner, file, item.name).then(
        (attachment) => {
          if (this.abandoned.delete(item.key)) {
            void this.client.deleteAttachment(owner, attachment.id).catch(() => {});
            return;
          }
          this.update(item.key, { status: "ready", attachment });
        },
        (err: unknown) => {
          this.abandoned.delete(item.key);
          this.update(item.key, {
            status: "failed",
            error: err instanceof Error ? err.message : String(err),
          });
        },
      );
    }
  }

  /** The user took it back before sending: the stored file goes too. */
  remove(key: string): void {
    const item = this.items.find((i) => i.key === key);
    if (!item) return;
    this.set(this.items.filter((i) => i.key !== key));
    if (item.status === "uploading") this.abandoned.add(key);
    else if (item.attachment) {
      void this.client.deleteAttachment(this.ownerValue, item.attachment.id).catch(() => {});
    }
  }

  /** After a send: the files now belong to the message, so they stay on disk. */
  clear(): void {
    this.set(EMPTY);
  }

  /**
   * Re-points this list at a new owner without touching the files — the
   * Router has just moved a launchpad draft's directory to the session
   * created from it (POST /sessions' attachment_draft).
   */
  rehome(owner: AttachmentOwner): void {
    this.ownerValue = owner;
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
    list = new StagedAttachments({ draft: newDraftId() }, client);
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
