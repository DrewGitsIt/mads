import http from "node:http";

const DPA_CONTENT = JSON.stringify({
  dpas: [
    {
      problem: "e2e smoke problem",
      evidence: "e2e smoke evidence",
      root_cause: "e2e smoke root cause",
      remediation_layer: "pipeline_template",
      suggested_action: "e2e smoke action",
    },
  ],
});

// Sentinel text asserted on by linked-sessions.spec.ts's digest-card check.
export const DIGEST_FIXTURE_TEXT =
  "Fixture digest: the agent completed the requested change and updated the tests accordingly.";

// Sentinel task title asserted on by synthesis-proposals.spec.ts's proposal-card check.
export const SYNTHESIS_FIXTURE_TASK_TITLE = "Fixture-proposed follow-up: add a regression test";

const SYNTHESIS_CONTENT = JSON.stringify({
  create_tasks: [
    {
      title: SYNTHESIS_FIXTURE_TASK_TITLE,
      description: "Scripted by the e2e Fast Model fixture.",
    },
  ],
});

function bodyFor(requestText: string): string {
  // synthesis_prompt.go's synthesisSystemPrompt is the only Fast Model
  // consumer whose system prompt mentions "background task synthesizer";
  // digest.go's digestSystemPrompt is the only one mentioning "digest" —
  // every other job (retros) wants the DPA-shaped JSON this fixture
  // originally returned.
  let content: string;
  if (requestText.includes("background task synthesizer")) {
    content = SYNTHESIS_CONTENT;
  } else if (requestText.includes("digest")) {
    // pkg/turndigest drops a digest reply without its "DIGEST:" marker.
    content = `DIGEST: ${DIGEST_FIXTURE_TEXT}`;
  } else {
    content = DPA_CONTENT;
  }
  return JSON.stringify({
    model: "e2esmoke-fixture",
    choices: [{ message: { content } }],
    usage: { prompt_tokens: 1, completion_tokens: 1 },
  });
}

export interface FastModelFixture {
  url: string;
  close(): Promise<void>;
}

/** A scripted OpenAI-compatible Fast Model endpoint, ported from scripts/e2esmoke/main_test.go's fastModelFixture. */
export function startFastModelFixture(): Promise<FastModelFixture> {
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      const chunks: Buffer[] = [];
      req.on("data", (c: Buffer) => chunks.push(c));
      req.on("end", () => {
        const requestText = Buffer.concat(chunks).toString("utf8");
        res.setHeader("Content-Type", "application/json");
        res.end(bodyFor(requestText));
      });
    });
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      if (addr === null || typeof addr === "string") {
        reject(new Error("fastModelFixture: unexpected server address"));
        return;
      }
      resolve({
        url: `http://127.0.0.1:${addr.port}`,
        close: () => new Promise<void>((r) => server.close(() => r())),
      });
    });
  });
}
