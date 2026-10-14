// The Changes demo page's session (npm run files): the made-up OAuth
// device-flow session from docs/mockups/changes-v2.html — 14 turns, three
// commits, files across every tier, scratch files outside the session folder. Demo only: the real
// server computes every field here.
import type { ChangeSet, ChangeTier, ChangedFile, FileDiff } from "../router/types";

const ROOT = "/home/drew/src/mycli";

const COMMITS: [hash: string, message: string, turn: number][] = [
  ["c09e7b2a1f", "cli: login and logout commands", 7],
  ["3f1c2a9d44", "auth: device-flow client with polling", 5],
  ["8be0d41c0e", "auth: store tokens in the OS keychain", 4],
];

const PROMPTS: string[] = [
  "Add device-flow OAuth login to the CLI. Use the same client id as the web app; the token endpoint is in internal/api. Don't touch the existing API-key auth path.",
  "Sounds right. Go ahead and start with the client, keep it in internal/auth.",
  "Can you test it against staging? The device endpoint is https://staging.example.com/device",
  "Store the token in the keychain, not a file. Mac and Linux both.",
  "Handle slow_down properly — staging returned it twice and we hammered the endpoint.",
  "Wire it into the CLI as `mycli login`.",
  "And logout. Commit what you have when tests pass.",
  "The retry test is flaky, fix it",
  "Linux keychain fails when there's no desktop session (CI, ssh). Fall back gracefully.",
  "Benchmark the new retry against the old one and run coverage.",
  "Run CI locally and fix whatever breaks.",
  "Add a Login button in Settings › Account that runs the same flow.",
  "Document it in docs/ and add an e2e test.",
  "Commit what's left.",
];

const turnAt = (n: number) => new Date(Date.UTC(2026, 9, 14, 9, 0) + n * 6 * 60_000).toISOString();

const KIND = { A: "added", M: "modified", D: "deleted" } as const;

function f(
  display: string,
  kind: keyof typeof KIND,
  added: number,
  deleted: number,
  turns: number[],
  tier: ChangeTier,
  x: { commits?: string[]; rule?: string; binary?: boolean } = {},
): ChangedFile {
  const outside = display.startsWith("/");
  const commits = (x.commits ?? []).map((short) => COMMITS.find(([h]) => h.startsWith(short))![0]);
  return {
    path: outside ? display : `${ROOT}/${display}`,
    display,
    outside: outside || undefined,
    change: KIND[kind],
    added: x.binary || outside ? 0 : added,
    deleted: x.binary || outside ? 0 : deleted,
    binary: x.binary,
    counts_unknown: outside || undefined,
    tier,
    scratch_rule: x.rule,
    attributed: true,
    turns,
    commits,
    uncommitted: commits.length === 0 || turns.some((t) => t > 7),
    last_changed: turnAt(Math.max(...turns) + 0.5),
  };
}

const FILES: ChangedFile[] = [
  f("internal/auth/deviceflow.go", "A", 214, 0, [2, 3, 5], "source", { commits: ["3f1c2a9"] }),
  f("internal/auth/poll.go", "A", 96, 0, [3, 5], "source", { commits: ["3f1c2a9"] }),
  f("internal/auth/token.go", "M", 58, 21, [4, 9], "source", { commits: ["8be0d41"] }),
  f("internal/auth/keychain_darwin.go", "A", 88, 0, [4], "source", { commits: ["8be0d41"] }),
  f("internal/auth/keychain_linux.go", "A", 102, 0, [4, 9], "source", { commits: ["8be0d41"] }),
  f("internal/auth/keychain.go", "A", 41, 0, [4], "source", { commits: ["8be0d41"] }),
  f("cmd/mycli/login.go", "A", 131, 0, [6, 7], "source", { commits: ["c09e7b2"] }),
  f("cmd/mycli/logout.go", "A", 44, 0, [7], "source", { commits: ["c09e7b2"] }),
  f("cmd/mycli/root.go", "M", 9, 2, [6], "source", { commits: ["c09e7b2"] }),
  f("internal/httpx/retry.go", "M", 187, 142, [8, 10], "source"),
  f("internal/httpx/client.go", "M", 63, 38, [8], "source"),
  f("internal/httpx/backoff.go", "D", 0, 77, [8], "source"),
  f("internal/api/session.go", "M", 22, 6, [5, 11], "source", { commits: ["3f1c2a9"] }),
  f("ui/src/settings/AccountPane.tsx", "M", 74, 12, [12], "source"),
  f("ui/src/settings/LoginButton.tsx", "A", 58, 0, [12], "source"),
  f("docs/auth.md", "A", 66, 0, [13], "source"),
  f("docs/img/device-flow.png", "A", 0, 0, [13], "source", { binary: true }),
  ...(
    [
      ["internal/auth/deviceflow_test.go", "A", 188, 0, [3, 5], "3f1c2a9"],
      ["internal/auth/poll_test.go", "A", 92, 0, [3], "3f1c2a9"],
      ["internal/auth/token_test.go", "M", 41, 8, [4, 9], "8be0d41"],
      ["internal/auth/keychain_test.go", "A", 77, 0, [4, 9], "8be0d41"],
      ["cmd/mycli/login_test.go", "A", 120, 0, [7], "c09e7b2"],
      ["internal/httpx/retry_test.go", "M", 96, 104, [8, 10]],
      ["internal/httpx/client_test.go", "M", 14, 9, [8]],
      ["ui/src/settings/AccountPane.test.tsx", "M", 38, 4, [12]],
      ["ui/src/settings/LoginButton.test.tsx", "A", 44, 0, [12]],
      ["internal/auth/testdata/device_code.json", "A", 12, 0, [3], "3f1c2a9"],
      ["internal/auth/testdata/token_ok.json", "A", 9, 0, [3], "3f1c2a9"],
      ["internal/auth/testdata/token_pending.json", "A", 6, 0, [3], "3f1c2a9"],
      ["internal/auth/testdata/token_slow_down.json", "A", 6, 0, [5], "3f1c2a9"],
      ["e2e/login.spec.ts", "A", 71, 0, [13]],
    ] as [string, "A" | "M", number, number, number[], string?][]
  ).map(([p, k, a, d, t, c]) => f(p, k, a, d, t, "tests", c ? { commits: [c] } : {})),
  f("go.mod", "M", 3, 0, [4], "config", { commits: ["8be0d41"] }),
  f("go.sum", "M", 12, 0, [4], "config", { commits: ["8be0d41"] }),
  f("ui/package.json", "M", 1, 0, [12], "config"),
  f(".github/workflows/ci.yml", "M", 8, 2, [11], "config"),
  f("Makefile", "M", 4, 1, [11], "config"),
  f(".env.example", "M", 2, 0, [6], "config", { commits: ["c09e7b2"] }),
  f("ui/package-lock.json", "M", 412, 96, [12], "generated"),
  f("internal/api/openapi.gen.go", "M", 340, 12, [11], "generated"),
  ...Array.from({ length: 12 }, (_, i) =>
    f(`ui/src/settings/__snapshots__/Pane${String(i).padStart(2, "0")}.test.tsx.snap`, "M", 6 + ((i * 7) % 30), 3 + ((i * 5) % 11), [12], "generated"),
  ),
  ...Array.from({ length: 8 }, (_, i) =>
    f(
      `internal/mocks/mock_${["store", "client", "clock", "keychain", "poller", "http", "signer", "logger"][i]}.go`,
      i < 4 ? "A" : "M",
      20 + ((i * 13) % 60),
      (i * 3) % 14,
      [i < 4 ? 4 : 8],
      "generated",
      i < 4 ? { commits: ["8be0d41"] } : {},
    ),
  ),
  ...(
    [
      ["test_oauth.py", 48, [3], "name pattern"],
      ["debug_poll.go", 31, [5], "name pattern"],
      ["scratch/notes.md", 22, [2], "scratch dir"],
      ["out.json", 310, [5], "name pattern"],
      ["repro_keychain.sh", 17, [9], "name pattern"],
      ["coverage.out", 1204, [10], "gitignored"],
      ["internal/httpx/bench.txt", 64, [10], "name pattern"],
      ["/tmp/devflow.log", 880, [5], "outside session folder"],
      ["/tmp/curl_device.sh", 9, [3], "outside session folder"],
    ] as [string, number, number[], string][]
  ).map(([p, a, t, rule]) => f(p, "A", a, 0, t, "scratch", { rule })),
];

/** `extraTurns` pads the session with prompt-only turns (the strip's long-session layout). */
export function demoChangeSet(extraTurns = 0): ChangeSet {
  const files = FILES.map((x) => ({ ...x }));
  const prompts = [...PROMPTS, ...Array.from({ length: extraTurns }, (_, i) => `Follow-up question ${i + 1}`)];
  return {
    baseline: "start",
    baselines: [
      { id: "start", label: "Session start", available: true },
      { id: "base", label: "Base branch · feat/cli-auth", available: true },
      { id: "main", label: "main", available: true },
    ],
    git: true,
    files,
    turns: prompts.map((prompt, i) => ({
      n: i + 1,
      prompt,
      at: turnAt(i + 1),
      files: files.filter((x) => x.turns.includes(i + 1)).length,
    })),
    commits: COMMITS.map(([hash, message, turn]) => ({
      hash,
      message,
      timestamp: turnAt(turn + 0.8),
      paths: files.filter((x) => x.commits.includes(hash)).map((x) => x.path),
    })),
    totals: {
      files: files.length,
      added: files.reduce((s, x) => s + x.added, 0),
      deleted: files.reduce((s, x) => s + x.deleted, 0),
    },
    notes: ["line counts unavailable for 2 files outside a repo"],
    computed_at: turnAt(15),
  };
}

/** Paths the demo marks as already looked at, so the "new" dots vary. */
export function demoSeen(cs: ChangeSet): Record<string, string> {
  return Object.fromEntries(
    cs.files.filter((x) => Math.max(...x.turns) <= 5 && x.tier !== "source").map((x) => [x.path, x.last_changed]),
  );
}

/** A believable diff for any demo file. */
export function demoDiff(path: string, label = "", change?: string): FileDiff {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const diff: FileDiff = {
    path,
    language: name.endsWith(".go") ? "go" : name.endsWith(".tsx") || name.endsWith(".ts") ? "typescript" : "plaintext",
    original: `// ${name}${label}\nfunc (c *Client) do(req *http.Request) (*http.Response, error) {\n\tfor attempt := 0; attempt < c.maxRetries; attempt++ {\n\t\tresp, err := c.http.Do(req)\n\t\ttime.Sleep(backoff.Fixed(attempt))\n\t}\n}\n`,
    modified: `// ${name}${label}\nfunc (c *Client) do(req *http.Request) (*http.Response, error) {\n\treturn retry.Do(req.Context(), c.policy, func() (*http.Response, error) {\n\t\tresp, err := c.http.Do(req)\n\t\tswitch resp.StatusCode {\n\t\tcase 409, 425, 429, 503, 504:\n\t\t\treturn nil, retry.After(resp.Header.Get("Retry-After"))\n\t\t}\n\t\treturn resp, nil\n\t})\n}\n`,
  };
  // Added and deleted files have nothing on one side, as the router serves them.
  if (change === "added") diff.original = "";
  if (change === "deleted") diff.modified = "";
  return diff;
}
