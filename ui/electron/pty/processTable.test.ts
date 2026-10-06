// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  createLinuxSource,
  createPsSource,
  describeTree,
  parsePs,
  truncateCommand,
  type ProcFs,
} from "./processTable";

/** A fake /proc: a path -> contents map. A missing path rejects like ENOENT; `denied` like EACCES. */
function fakeProc(files: Record<string, string>, denied: string[] = []): ProcFs {
  return {
    readFile: async (path) => {
      if (denied.includes(path)) throw Object.assign(new Error("EACCES"), { code: "EACCES" });
      if (!(path in files)) throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
      return files[path];
    },
  };
}

const proc = (pid: number, cmd: string[], rssKb: number, children: number[] = []) => ({
  [`/proc/${pid}/cmdline`]: `${cmd.join("\0")}\0`,
  [`/proc/${pid}/status`]: `Name:\tx\nVmRSS:\t   ${rssKb} kB\nThreads:\t1\n`,
  [`/proc/${pid}/task/${pid}/children`]: children.map((c) => `${c} `).join(""),
});

describe("Linux /proc source", () => {
  it("walks nested children and reads cmdline (NUL-separated) and VmRSS", async () => {
    const fs = fakeProc({
      ...proc(100, ["/bin/bash"], 4000, [200, 300]),
      ...proc(200, ["sudo", "python3", "-m", "http.server", "80"], 1000, [210]),
      ...proc(210, ["python3", "-m", "http.server", "80"], 9000), // root-owned, still readable
      ...proc(300, ["tail", "-f", "/var/log/syslog"], 500),
    });
    const tree = await describeTree(createLinuxSource(fs), 100);
    expect(tree.root).toMatchObject({ pid: 100, command: "/bin/bash", rssKb: 4000 });
    expect(tree.descendants.map((p) => [p.pid, p.command, p.rssKb])).toEqual([
      [200, "sudo python3 -m http.server 80", 1000],
      [300, "tail -f /var/log/syslog", 500],
      [210, "python3 -m http.server 80", 9000],
    ]);
  });

  it("skips a child that vanished between the children read and its own reads", async () => {
    const fs = fakeProc({
      ...proc(100, ["bash"], 1, [200, 999]), // 999 has no /proc entry any more
      ...proc(200, ["sleep", "30"], 2),
    });
    const tree = await describeTree(createLinuxSource(fs), 100);
    expect(tree.descendants.map((p) => p.pid)).toEqual([200]);
  });

  it("handles an unreadable (EACCES) process and a missing root without throwing", async () => {
    const fs = fakeProc(
      { ...proc(100, ["bash"], 1, [200]), ...proc(200, ["secret"], 2) },
      ["/proc/200/cmdline"],
    );
    const tree = await describeTree(createLinuxSource(fs), 100);
    expect(tree.descendants).toEqual([]);
    expect(await describeTree(createLinuxSource(fakeProc({})), 5)).toEqual({
      root: undefined,
      descendants: [],
    });
  });

  it("names a process with an empty cmdline by its comm", async () => {
    const fs = fakeProc({
      ...proc(100, ["bash"], 1, [200]),
      "/proc/200/cmdline": "",
      "/proc/200/comm": "kworker\n",
    });
    const tree = await describeTree(createLinuxSource(fs), 100);
    expect(tree.descendants[0]).toMatchObject({ pid: 200, command: "kworker", rssKb: 0 });
  });

  it("does not loop on a pid that lists itself or an ancestor", async () => {
    const fs = fakeProc({
      ...proc(100, ["bash"], 1, [200]),
      ...proc(200, ["loop"], 1, [100, 200]),
    });
    const tree = await describeTree(createLinuxSource(fs), 100);
    expect(tree.descendants.map((p) => p.pid)).toEqual([200]);
  });
});

// Captured from `ps -A -o pid=,ppid=,rss=,command=` on macOS; the command column has spaces.
const PS_OUTPUT = [
  "    1     0  11520 /sbin/launchd",
  "  400     1  30000 /Applications/Mads.app/Contents/MacOS/Mads --flag",
  "  500   400   4096 -zsh",
  "  610   500   2048 /usr/bin/sudo python3 -m http.server 80",
  "  611   610  20480 /Library/Frameworks/Python.framework/Versions/3.12/bin/python3 -m http.server 80",
  "  620   500   1024 /bin/sleep 30",
  "  900     1    512 /Applications/Some App/Helper (Renderer) --type=renderer",
  "",
].join("\n");

describe("parent pids", () => {
  it("reads ppid from /proc/<pid>/stat past a comm with spaces and parens", async () => {
    const fs = fakeProc({ "/proc/4000/stat": "4000 (my (weird) cmd) S 3999 4000 4000 0 -1\n" });
    expect(await createLinuxSource(fs).parent(4000)).toBe(3999);
  });

  it("is undefined when the process is gone", async () => {
    expect(await createLinuxSource(fakeProc({})).parent(4000)).toBeUndefined();
  });

  it("comes from the ps snapshot on macOS", async () => {
    const source = createPsSource(async () => PS_OUTPUT);
    expect(await source.parent(610)).toBe(500);
    expect(await source.parent(99999)).toBeUndefined();
  });
});

describe("ps source", () => {
  it("parses rows, keeping the whole command including spaces", () => {
    const rows = parsePs(PS_OUTPUT);
    expect(rows).toHaveLength(7);
    expect(rows[3]).toEqual({
      pid: 610,
      ppid: 500,
      rssKb: 2048,
      command: "/usr/bin/sudo python3 -m http.server 80",
    });
    expect(rows[6].command).toBe("/Applications/Some App/Helper (Renderer) --type=renderer");
  });

  it("ignores blank and malformed lines", () => {
    expect(parsePs("\ngarbage\n  12  1  5 ok\n")).toEqual([{ pid: 12, ppid: 1, rssKb: 5, command: "ok" }]);
  });

  it("walks the snapshot from the shell and takes it once", async () => {
    let calls = 0;
    const source = createPsSource(async () => {
      calls += 1;
      return PS_OUTPUT;
    });
    const tree = await describeTree(source, 500);
    expect(tree.root).toMatchObject({ pid: 500, command: "-zsh", rssKb: 4096 });
    expect(tree.descendants.map((p) => p.pid)).toEqual([610, 620, 611]);
    expect(calls).toBe(1);
  });

  it("yields an empty tree when ps itself fails", async () => {
    const source = createPsSource(async () => {
      throw new Error("spawn ps ENOENT");
    });
    expect(await describeTree(source, 500)).toEqual({ root: undefined, descendants: [] });
  });
});

describe("truncateCommand", () => {
  it("collapses whitespace and cuts long commands with an ellipsis", () => {
    expect(truncateCommand("a   b\tc")).toBe("a b c");
    const cut = truncateCommand("x".repeat(200));
    expect(cut).toHaveLength(80);
    expect(cut.endsWith("…")).toBe(true);
  });
});
