import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { DelayMode, Plan, Result } from "./measure";
import type { Manifest } from "./scenarios";

/**
 * The run's report: per scenario and delay mode, deterministic metrics as
 * exact values (⚠ when repetitions of one build disagree: the metric or the
 * harness isn't as deterministic as it should be), timings as median and
 * IQR, and the candidate/baseline ratio of medians. Failed and missing
 * repetitions are listed and counted, never dropped silently.
 */

const DET_LABELS: Record<string, string> = {
  events_stream_bytes_proxy: "/events stream bytes sent (proxy)",
  events_stream_bytes: "/events bytes the page received (CDP; less if it closed a stream mid-replay)",
  events_stream_frames: "/events SSE frames (proxy)",
  events_stream_messages_cdp: "/events SSE messages (CDP)",
  events_stream_connects: "/events (re)connects",
  events_from_zero: "…of which with no `since` (replay from 0)",
  events_batch_requests: "GET /events batch pages",
  events_batch_bytes: "GET /events batch bytes",
  requests_short: "short requests",
  requests_delayed: "…of which delayed by the proxy",
  output_streams_held: "/output streams held open by the proxy",
  peak_busy_sockets_proxy: "peak busy sockets (proxy; secondary)",
  peak_connections_cdp: "peak connections (CDP connectionId; secondary)",
  peak_sockets_lsof: "peak sockets to the proxy (lsof; secondary)",
};

const TIMING_LABELS: Record<string, string> = {
  settled_ms: "click → settled, all legs (ms)",
  last_events_byte_ms: "click → last /events byte (ms)",
  all_sockets_busy_ms: "time with all 6 sockets busy (ms)",
  queued_total_ms: "total queued time, all requests (ms)",
  queue_depth_max: "max queue depth (requests waiting for a socket)",
  queued_p50_ms: "queued p50 (ms)",
  queued_p95_ms: "queued p95 (ms)",
  queued_max_ms: "queued max (ms)",
  queued_over_1s: "requests queued > 1 s",
  cancelled: "requests cancelled",
  unanswered_at_end: "requests unanswered at window end",
  window_ms: "measured window (ms)",
  longtask_total_ms: "main-thread long tasks (ms)",
  router_cpu_ms: "Router CPU (ms)",
  data_cpu_ms: "Data Service CPU (ms)",
  children_cpu_ms: "session/pipeline children CPU (ms)",
  router_peak_rss_mb: "Router peak RSS (MB)",
  data_peak_rss_mb: "Data Service peak RSS (MB)",
};

const isBytes = (k: string) => k.includes("bytes");

function quantile(xs: number[], q: number): number {
  const s = [...xs].sort((a, b) => a - b);
  if (s.length === 0) return NaN;
  const pos = (s.length - 1) * q;
  const lo = Math.floor(pos);
  const hi = Math.ceil(pos);
  return s[lo] + (s[hi] - s[lo]) * (pos - lo);
}

function fmtNum(v: number, bytes = false): string {
  if (!Number.isFinite(v)) return "–";
  if (bytes) return v >= 1e6 ? `${(v / 1e6).toFixed(2)} MB` : v >= 1e3 ? `${(v / 1e3).toFixed(1)} kB` : `${v} B`;
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

function detCell(vals: number[], key: string): { text: string; value: number } {
  if (vals.length === 0) return { text: "–", value: NaN };
  const lo = Math.min(...vals);
  const hi = Math.max(...vals);
  const b = isBytes(key);
  if (lo === hi) return { text: fmtNum(lo, b), value: lo };
  // Within 0.1%: the boot sweep's own events carry wall-clock timestamps,
  // so byte totals can differ by a few bytes between repetitions.
  if (b && (hi - lo) / Math.max(hi, 1) <= 0.001) return { text: `${fmtNum(hi, b)} (±${hi - lo} B)`, value: quantile(vals, 0.5) };
  return { text: `⚠ ${fmtNum(lo, b)} … ${fmtNum(hi, b)}`, value: quantile(vals, 0.5) };
}

function timingCell(vals: number[]): { text: string; value: number } {
  const v = vals.filter(Number.isFinite);
  if (v.length === 0) return { text: "–", value: NaN };
  const med = quantile(v, 0.5);
  if (v.length < 2) return { text: fmtNum(Math.round(med)), value: med };
  return { text: `${fmtNum(Math.round(med))} [${fmtNum(Math.round(quantile(v, 0.25)))}–${fmtNum(Math.round(quantile(v, 0.75)))}]`, value: med };
}

function ratio(cand: number, base: number): string {
  if (!Number.isFinite(cand) || !Number.isFinite(base)) return "–";
  if (base === 0) return cand === 0 ? "1.00" : "∞";
  return (cand / base).toFixed(2);
}

export function loadResults(runDir: string): Result[] {
  const raw = path.join(runDir, "raw");
  if (!existsSync(raw)) return [];
  const out: Result[] = [];
  const walk = (d: string) => {
    for (const f of readdirSync(d)) {
      const p = path.join(d, f);
      if (statSync(p).isDirectory()) walk(p);
      else if (f.endsWith(".json")) out.push(JSON.parse(readFileSync(p, "utf8")) as Result);
    }
  };
  walk(raw);
  return out;
}

export function renderReport(plan: Plan, results: Result[], manifest?: Manifest): string {
  const labels = plan.builds.map((b) => b.label);
  const [base, cand] = labels;
  const modes: DelayMode[] = plan.modes ?? ["on"];
  const lines: string[] = [];
  lines.push(`# Perf run ${path.basename(plan.runDir)}`, "");
  lines.push(`- builds: ${plan.builds.map((b) => `**${b.label}** = ${b.desc ?? "?"}`).join(" vs ")}`);
  for (const [k, v] of Object.entries(plan.meta)) lines.push(`- ${k}: ${typeof v === "string" ? v : JSON.stringify(v)}`);
  lines.push(
    `- browser: ${plan.browser}; N = ${plan.n}; delay modes: ${modes.join(", ")} ` +
      `(on = ${plan.delays || "defaults: repos 10 s, changeset 6 s, worktree/refresh 16 s"}); ` +
      `/output held open: ${plan.holdOutput ? "yes" : "no"}${plan.legacyRouteGuard ? "; LEGACY context.route guard ON (B1 comparison)" : ""}`,
  );
  if (manifest) {
    lines.push(
      `- workload: profile ${manifest.profile}, ${manifest.total_events} events, ${(manifest.total_bytes / 1e6).toFixed(1)} MB payload, ` +
        `${manifest.sessions.length} sessions; parent has ${manifest.parent.delegates} delegates, ` +
        `${manifest.counts["session.subagent_event"] ?? 0} subagent events in the log`,
    );
  }
  lines.push("", "Deterministic metrics are exact values; ⚠ marks one that varied across a build's repetitions (min … max); (±N B) is a byte total that varied by under 0.1%.");
  lines.push("Timings are median [IQR]. Ratio is cand/base of the medians. Settled = data marker visible, every short request started before it done or failed, and /events quiet 500 ms.", "");

  // Every repetition the plan asked for, so missing ones show.
  for (const sid of plan.scenarios) {
    for (const mode of modes) {
      const rs = results.filter((r) => r.scenario === sid && (r.mode ?? "on") === mode);
      lines.push(`## ${sid} — delays ${mode}`, "");
      const missing: string[] = [];
      for (const l of labels) {
        for (let rep = 1; rep <= plan.n; rep++) if (!rs.some((r) => r.build === l && r.rep === rep)) missing.push(`${l} rep ${rep}`);
      }
      if (missing.length) lines.push(`- ✗ MISSING (worker died, timed out, or the run stopped): ${missing.join(", ")}`);
      for (const f of rs.filter((r) => !r.ok)) lines.push(`- ✗ FAILED ${f.build} rep ${f.rep}: ${f.error}`);
      const capped = rs.filter((r) => r.windowCapped);
      if (capped.length) lines.push(`- window or settle hit its cap in ${capped.length} repetition(s): numbers are lower bounds`);
      const ok = rs.filter((r) => r.ok);
      const by = (label: string) => ok.filter((r) => r.build === label);
      lines.push("");
      lines.push(cand ? `| metric | ${base} | ${cand} | ratio |` : `| metric | ${base} |`, cand ? "|---|---|---|---|" : "|---|---|");
      const row = (label: string, cells: { text: string; value: number }[]) => {
        if (cand) lines.push(`| ${label} | ${cells[0].text} | ${cells[1].text} | ${ratio(cells[1].value, cells[0].value)} |`);
        else lines.push(`| ${label} | ${cells[0].text} |`);
      };
      row("repetitions ok", labels.map((l) => ({ text: `${by(l).length} / ${plan.n}`, value: by(l).length })));
      if (ok.length === 0) {
        lines.push("");
        continue;
      }
      const headline = ["all_sockets_busy_ms", "queued_total_ms", "queue_depth_max"];
      for (const k of headline) row(`**${TIMING_LABELS[k]}**`, labels.map((l) => timingCell(by(l).map((r) => r.timing[k]))));
      const legNames = ok[0].legs.map((l) => l.name);
      legNames.forEach((n, i) => {
        row(`leg “${n}” click → settled (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.settledMs ?? NaN))));
        row(`leg “${n}” click → last /events byte (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.lastEventsByteMs ?? NaN))));
        row(`leg “${n}” /events bytes`, labels.map((l) => detCell(by(l).map((r) => r.legs[i]?.eventsBytes ?? NaN), "bytes")));
      });
      for (const [k, label] of Object.entries(DET_LABELS)) {
        if (!ok.some((r) => k in r.det)) continue;
        row(label, labels.map((l) => detCell(by(l).map((r) => r.det[k]), k)));
      }
      for (const [k, label] of Object.entries(TIMING_LABELS)) {
        if (headline.includes(k) || !ok.some((r) => k in r.timing)) continue;
        row(label, labels.map((l) => timingCell(by(l).map((r) => r.timing[k]))));
      }
      lines.push("");
      for (const l of labels) {
        const first = by(l)[0];
        if (!first) continue;
        if (first.eventsConnects.length) {
          const parts = first.eventsConnects.map(
            (c) => `${c.since === null ? "open before" : c.since === 0 ? "since=0" : `since=${c.since}`} (${c.types} types): ${fmtNum(c.bytes, true)}, ${c.frames} frames`,
          );
          lines.push(`- ${l} /events streams (rep ${first.rep}): ${parts.join("; ")}`);
        }
        const worst = Object.entries(first.queuedByRoute ?? {})
          .sort((a, b) => b[1].max - a[1].max)
          .slice(0, 5)
          .map(([k, v]) => `${k} ×${v.n} max ${v.max} ms`);
        if (worst.length) lines.push(`- ${l} longest-queued routes (rep ${first.rep}): ${worst.join("; ")}`);
        const foreign = [...new Set(by(l).flatMap((r) => r.browserForeign ?? []))];
        if (foreign.length) lines.push(`- ${l} ⚠ browser connections other than the proxy: ${foreign.join("; ")}`);
      }
      lines.push("");
    }
  }
  return lines.join("\n");
}

/** Whether every planned repetition exists and succeeded. */
export function allOk(plan: Plan, results: Result[]): boolean {
  const want = plan.scenarios.length * (plan.modes ?? ["on"]).length * plan.n * plan.builds.length;
  return results.length === want && results.every((r) => r.ok);
}

export function writeReport(plan: Plan): string {
  const results = loadResults(plan.runDir);
  const manifest = existsSync(plan.manifestPath) ? (JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest) : undefined;
  const md = renderReport(plan, results, manifest);
  const out = path.join(plan.runDir, "report.md");
  writeFileSync(out, md + "\n");
  writeFileSync(path.join(plan.runDir, "results.json"), JSON.stringify({ plan, manifest, results, allOk: allOk(plan, results) }, null, 2));
  return out;
}
