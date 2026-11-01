import { EventEmitter } from "node:events";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerLinkHandlers } from "./linkOpener";

/**
 * linkOpener against a mocked Electron (ipcMain, dialog, shell) and a mocked
 * spawn, with real files in a temp folder. Covers the decisions main makes
 * that page content must not be able to skip: the editor-command approval,
 * the runnable-file prompt, and what happens when nothing opens a file.
 */

const handlers = new Map<string, (event: unknown, ...args: unknown[]) => unknown>();
const showMessageBox = vi.fn();
const openExternal = vi.fn();
const openPath = vi.fn();
const showItemInFolder = vi.fn();
const spawnMock = vi.fn();

vi.mock("electron", () => ({
  ipcMain: { handle: (channel: string, fn: (event: unknown, ...args: unknown[]) => unknown) => handlers.set(channel, fn) },
  dialog: { showMessageBox: (...args: unknown[]) => showMessageBox(...args) },
  shell: {
    openExternal: (...args: unknown[]) => openExternal(...args),
    openPath: (...args: unknown[]) => openPath(...args),
    showItemInFolder: (...args: unknown[]) => showItemInFolder(...args),
  },
  BrowserWindow: { fromWebContents: () => null },
}));

vi.mock("node:child_process", async (importOriginal) => {
  const actual = await importOriginal<typeof import("node:child_process")>();
  const spawn = (...args: unknown[]) => spawnMock(...args);
  return { ...actual, default: { ...actual, spawn }, spawn };
});


let dir: string;
let userData: string;
let editorCommand: string | undefined;

function invoke(channel: string, ...args: unknown[]) {
  return handlers.get(channel)!({ sender: {} }, ...args);
}

/** The next message box answers with the button labelled `label`. */
function answer(label: string) {
  showMessageBox.mockImplementationOnce(async (options: { buttons: string[] }) => ({
    response: options.buttons.indexOf(label),
  }));
}

function spawnSucceeds() {
  spawnMock.mockImplementation(() => {
    const child = Object.assign(new EventEmitter(), { unref: vi.fn() });
    queueMicrotask(() => child.emit("spawn"));
    return child;
  });
}

beforeEach(() => {
  handlers.clear();
  vi.clearAllMocks();
  dir = mkdtempSync(join(tmpdir(), "links-"));
  userData = join(dir, "userData");
  mkdirSync(userData);
  editorCommand = undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: true, json: async () => ({ editor_command: editorCommand }) })),
  );
  openPath.mockResolvedValue("");
  registerLinkHandlers({ routerBaseUrl: "http://router.test", userDataDir: userData });
});

afterEach(() => {
  vi.unstubAllGlobals();
  rmSync(dir, { recursive: true, force: true });
});

describe("links:open", () => {
  it("sends web and localhost URLs to the browser", async () => {
    expect(await invoke("links:open", "https://example.com/x", dir)).toBe("opened");
    expect(await invoke("links:open", "http://localhost:5173/", dir)).toBe("opened");
    expect(openExternal.mock.calls.map((c) => c[0])).toEqual(["https://example.com/x", "http://localhost:5173/"]);
  });

  it("says missing for a file that isn't there, and opens nothing", async () => {
    expect(await invoke("links:open", "nope.md", dir)).toBe("missing");
    expect(openPath).not.toHaveBeenCalled();
  });

  it("opens a document with the default app, resolving against the session folder", async () => {
    writeFileSync(join(dir, "notes.md"), "hi");
    expect(await invoke("links:open", "notes.md", dir)).toBe("opened");
    expect(openPath).toHaveBeenCalledWith(join(dir, "notes.md"));
    expect(showMessageBox).not.toHaveBeenCalled();
  });

  it("drops a :line suffix when no file has that literal name", async () => {
    writeFileSync(join(dir, "a.ts"), "");
    expect(await invoke("links:open", "a.ts:12", dir)).toBe("opened");
    expect(openPath).toHaveBeenCalledWith(join(dir, "a.ts"));
  });

  it("shows the file in the file manager when no app opens it", async () => {
    writeFileSync(join(dir, "x.zzqq"), "");
    openPath.mockResolvedValueOnce("Failed to open path");
    expect(await invoke("links:open", "x.zzqq", dir)).toBe("revealed");
    expect(showItemInFolder).toHaveBeenCalledWith(join(dir, "x.zzqq"));
  });

  describe("a file its default app would run", () => {
    beforeEach(() => {
      writeFileSync(join(dir, "run.sh"), "echo hi");
    });

    it("asks first, and Cancel opens nothing", async () => {
      answer("Cancel");
      expect(await invoke("links:open", "run.sh", dir)).toBe("cancelled");
      expect(openPath).not.toHaveBeenCalled();
      expect(showMessageBox.mock.calls[0][0]).toMatchObject({ defaultId: 2, cancelId: 2 });
    });

    it("opens on Open", async () => {
      answer("Open");
      expect(await invoke("links:open", "run.sh", dir)).toBe("opened");
      expect(openPath).toHaveBeenCalled();
    });

    it("can show it in the file manager instead", async () => {
      answer(process.platform === "darwin" ? "Show in Finder" : "Show in Folder");
      expect(await invoke("links:open", "run.sh", dir)).toBe("revealed");
      expect(openPath).not.toHaveBeenCalled();
      expect(showItemInFolder).toHaveBeenCalled();
    });

    it("sees through a harmless-looking symlink", async () => {
      symlinkSync(join(dir, "run.sh"), join(dir, "notes.txt"));
      answer("Cancel");
      expect(await invoke("links:open", "notes.txt", dir)).toBe("cancelled");
      expect(showMessageBox.mock.calls[0][0].detail).toContain("run.sh");
    });

    it("asks about an extensionless file with an execute bit", async () => {
      writeFileSync(join(dir, "tool"), "");
      chmodSync(join(dir, "tool"), 0o755);
      answer("Cancel");
      expect(await invoke("links:open", "tool", dir)).toBe("cancelled");
    });
  });

  describe("with an editor command in Settings", () => {
    beforeEach(() => {
      writeFileSync(join(dir, "a.ts"), "");
      editorCommand = `"/opt/My Editor/bin/ed" -g`;
      spawnSucceeds();
    });

    it("asks once per command, then runs it without a shell, path as its own argument", async () => {
      answer("Use This Command");
      expect(await invoke("links:open", "a.ts", dir)).toBe("opened");
      expect(showMessageBox.mock.calls[0][0].detail).toContain(editorCommand);
      expect(spawnMock).toHaveBeenCalledWith("/opt/My Editor/bin/ed", ["-g", join(dir, "a.ts")], {
        detached: true,
        stdio: "ignore",
      });
      expect(await invoke("links:open", "a.ts", dir)).toBe("opened");
      expect(showMessageBox).toHaveBeenCalledTimes(1);
    });

    it("asks again when the command changes", async () => {
      answer("Use This Command");
      await invoke("links:open", "a.ts", dir);
      editorCommand = "osascript -e 'do shell script \"x\"'";
      answer("Cancel");
      expect(await invoke("links:open", "a.ts", dir)).toBe("cancelled");
      expect(spawnMock).toHaveBeenCalledTimes(1);
    });

    it("remembers 'use the default app' for that command", async () => {
      answer("Use the Default App");
      expect(await invoke("links:open", "a.ts", dir)).toBe("opened");
      expect(spawnMock).not.toHaveBeenCalled();
      expect(openPath).toHaveBeenCalled();
      await invoke("links:open", "a.ts", dir);
      expect(showMessageBox).toHaveBeenCalledTimes(1);
      expect(JSON.parse(readFileSync(join(userData, "editor-command.json"), "utf8"))).toEqual({
        command: editorCommand,
        approved: false,
      });
    });

    it("reports a command that can't start", async () => {
      spawnMock.mockImplementation(() => {
        const child = Object.assign(new EventEmitter(), { unref: vi.fn() });
        queueMicrotask(() => child.emit("error", Object.assign(new Error("spawn ENOENT"), { code: "ENOENT" })));
        return child;
      });
      answer("Use This Command");
      answer("OK");
      expect(await invoke("links:open", "a.ts", dir)).toBe("failed");
      expect(showMessageBox.mock.calls[1][0]).toMatchObject({ type: "error" });
    });
  });
});

describe("links:open-file", () => {
  it("opens an absolute path through the same flow as a file link", async () => {
    writeFileSync(join(dir, "notes.md"), "");
    expect(await invoke("links:open-file", join(dir, "notes.md"))).toBe("opened");
    expect(openPath).toHaveBeenCalledWith(join(dir, "notes.md"));
  });

  it("takes the name literally — no URL decoding, no :line stripping", async () => {
    const odd = join(dir, "a#b?c%20d:12.ts");
    writeFileSync(odd, "");
    expect(await invoke("links:open-file", odd)).toBe("opened");
    expect(openPath).toHaveBeenCalledWith(odd);
  });

  it("still asks before opening a runnable file", async () => {
    writeFileSync(join(dir, "run.command"), "");
    answer("Cancel");
    expect(await invoke("links:open-file", join(dir, "run.command"))).toBe("cancelled");
    expect(openPath).not.toHaveBeenCalled();
  });

  it("says missing for a file that's gone, and refuses a relative path", async () => {
    expect(await invoke("links:open-file", join(dir, "gone.go"))).toBe("missing");
    writeFileSync(join(dir, "rel.md"), "");
    expect(await invoke("links:open-file", "rel.md")).toBe("none");
    expect(await invoke("links:open-file", 42)).toBe("none");
  });
});

describe("links:describe", () => {
  it("says what each link is, and which files are missing", async () => {
    writeFileSync(join(dir, "here.md"), "");
    expect(
      await invoke("links:describe", ["here.md", "gone.md", "https://e.com", "http://localhost:1/", "mailto:a@b"], dir),
    ).toEqual(["file", "missing", "web", "localhost", "none"]);
  });

  it("ignores junk input", async () => {
    expect(await invoke("links:describe", "not an array", dir)).toEqual([]);
    expect(await invoke("links:describe", [42], dir)).toEqual(["none"]);
  });
});
