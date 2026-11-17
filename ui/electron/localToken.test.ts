import { chmodSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { authHeaders, bearerInjector, readToken, readTokenChecked, routerFilter, tokenPath } from "./localToken";

describe("localToken", () => {
  it("reads $MADS_HOME/local.token, trimmed, or empty when absent", () => {
    const home = mkdtempSync(join(tmpdir(), "mads-electron-token-"));
    const path = tokenPath({ MADS_HOME: home });
    expect(path).toBe(join(home, "local.token"));
    expect(readToken(path)).toBe("");
    writeFileSync(path, "abc\n", { mode: 0o600 });
    expect(readToken(path)).toBe("abc");
  });

  it("refuses a token file other users can read or write", () => {
    const home = mkdtempSync(join(tmpdir(), "mads-electron-token-"));
    const path = join(home, "local.token");
    writeFileSync(path, "abc\n", { mode: 0o600 });
    for (const mode of [0o644, 0o640, 0o602]) {
      chmodSync(path, mode);
      const r = readTokenChecked(path);
      expect(r.token, mode.toString(8)).toBe("");
      expect(r.why).toContain("chmod 600");
    }
  });

  it("sends a bearer only when there is a token", () => {
    expect(authHeaders("abc")).toEqual({ Authorization: "Bearer abc" });
    expect(authHeaders("")).toEqual({});
  });

  it("filters to the Router's origin only", () => {
    expect(routerFilter("http://localhost:17373")).toEqual({ urls: ["http://localhost:17373/*"] });
  });

  it("adds the bearer to the Router's requests, replacing the page's own, and to nothing else", () => {
    let tok = "t1";
    const inject = bearerInjector("http://localhost:17373", () => tok);
    const run = (url: string, headers: Record<string, string> = {}) => {
      let out: Record<string, string | string[]> = {};
      inject({ url, requestHeaders: headers }, (r) => (out = r.requestHeaders));
      return out;
    };
    expect(run("http://localhost:17373/sessions", { Accept: "x" })).toEqual({ Accept: "x", Authorization: "Bearer t1" });
    expect(run("http://localhost:17373/events", { authorization: "Bearer page-chose-this" })).toEqual({ Authorization: "Bearer t1" });
    // Another port, another host, a look-alike: untouched.
    for (const url of ["http://localhost:5173/x", "http://127.0.0.1:17373/x", "http://localhost:173730/x", "https://example.com/"]) {
      expect(run(url, { A: "1" }), url).toEqual({ A: "1" });
    }
    // Follows the token file (main refreshes the cached value on change).
    tok = "t2";
    expect(run("http://localhost:17373/config").Authorization).toBe("Bearer t2");
    tok = "";
    expect(run("http://localhost:17373/config")).toEqual({});
  });
});
