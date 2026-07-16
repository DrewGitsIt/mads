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

const BODY = JSON.stringify({
  model: "e2esmoke-fixture",
  choices: [{ message: { content: DPA_CONTENT } }],
  usage: { prompt_tokens: 1, completion_tokens: 1 },
});

export interface FastModelFixture {
  url: string;
  close(): Promise<void>;
}

/** A scripted OpenAI-compatible Fast Model endpoint, ported from scripts/e2esmoke/main_test.go's fastModelFixture. */
export function startFastModelFixture(): Promise<FastModelFixture> {
  return new Promise((resolve, reject) => {
    const server = http.createServer((_req, res) => {
      res.setHeader("Content-Type", "application/json");
      res.end(BODY);
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
