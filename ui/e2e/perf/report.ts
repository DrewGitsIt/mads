import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { Plan, Result } from "./measure";
import type { Manifest } from "./scenarios";

/**
 * The run's report: per scenario, deterministic metrics as exact values
 * (flagged ⚠ when repetitions of one build disagree, which means the metric
 * or the harness isn't as deterministic as it should be), timings as median
 * and IQR, and the candidate/baseline ratio of medians.
 */

const DET_LABELS: Record<string, string> = {
  events_stream_bytes: "/events stream bytes (CDP)",
  events_stream_bytes_proxy: "/events stream bytes (proxy)",
  events_stream_frames: "/events SSE frames (proxy)",
  events_stream_messages_cdp: "/events SSE messages (CDP)",
  events_stream_connects: "/events (re)connects",
  events_from_zero: "…of which with no `since` (replay from 0)",
  events_batch_requests: "GET /events batch pages",
  events_batch_bytes: "GET /events batch bytes",
  requests_short: "short requests",
  requests_delayed: "…of which delayed by the proxy",
  peak_connections_cdp: "peak connections (CDP connectionId)",
  peak_sockets_proxy: "peak open sockets (proxy)",
  peak_inflight_proxy: "peak in-flight requests (proxy)",
  peak_sockets_lsof: "peak sockets (lsof, browser side)",
};

const TIMING_LABELS: Record<string, string> = {
  settled_ms: "click → settled, all legs (ms)",
  replay_ms: "click → last /events byte (ms)",
  window_ms: "measured window (ms)",
  blocked_p50_ms: "queued+stalled p50 (ms)",
  blocked_p95_ms: "queued+stalled p95 (ms)",
  blocked_max_ms: "queued+stalled max (ms)",
  blocked_over_1s: "requests queued+stalled > 1 s",
  unanswered_at_end: "requests unanswered at window end",
  longtask_total_ms: "main-thread long tasks (ms)",
  longtask_count: "long tasks (count)",
  router_cpu_ms: "Router CPU (ms)",
  data_cpu_ms: "Data Service CPU (ms)",
  router_peak_rss_mb: "Router peak RSS (MB)",
  data_peak_rss_mb: "Data Service peak RSS (MB)",
};

const BYTES = new Set(["events_stream_bytes", "events_stream_bytes_proxy", "events_batch_bytes"]);

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
  const b = BYTES.has(key);
  if (lo === hi) return { text: fmtNum(lo, b), value: lo };
  // Within 0.1%: the boot sweep's own events carry wall-clock timestamps,
  // so byte totals can differ by a few bytes between repetitions.
  if (b && (hi - lo) / Math.max(hi, 1) <= 0.001) return { text: `${fmtNum(hi, b)} (±${hi - lo} B)`, value: quantile(vals, 0.5) };
  return { text: `⚠ ${fmtNum(lo, b)} … ${fmtNum(hi, b)}`, value: quantile(vals, 0.5) };
}

function timingCell(vals: number[]): { text: string; value: number } {
  if (vals.length === 0) return { text: "–", value: NaN };
  const med = quantile(vals, 0.5);
  if (vals.length < 2) return { text: fmtNum(Math.round(med)), value: med };
  return { text: `${fmtNum(Math.round(med))} [${fmtNum(Math.round(quantile(vals, 0.25)))}–${fmtNum(Math.round(quantile(vals, 0.75)))}]`, value: med };
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
  for (const sc of readdirSync(raw)) {
    for (const f of readdirSync(path.join(raw, sc))) {
      if (f.endsWith(".json")) out.push(JSON.parse(readFileSync(path.join(raw, sc, f), "utf8")) as Result);
    }
  }
  return out;
}

export function renderReport(plan: Plan, results: Result[], manifest?: Manifest): string {
  const labels = plan.builds.map((b) => b.label);
  const [base, cand] = labels;
  const lines: string[] = [];
  lines.push(`# Perf run ${path.basename(plan.runDir)}`, "");
  lines.push(`- builds: ${plan.builds.map((b) => `**${b.label}** = ${b.desc ?? "?"}`).join(" vs ")}`);
  for (const [k, v] of Object.entries(plan.meta)) lines.push(`- ${k}: ${typeof v === "string" ? v : JSON.stringify(v)}`);
  lines.push(`- browser: ${plan.browser}; N = ${plan.n}; delays: ${plan.delays || "defaults (repos 10 s, changeset 6 s, worktree/refresh 16 s)"}`);
  if (manifest) {
    lines.push(
      `- workload: profile ${manifest.profile}, ${manifest.total_events} events, ${(manifest.total_bytes / 1e6).toFixed(1)} MB payload, ` +
        `${manifest.sessions.length} sessions; parent has ${manifest.parent.delegates} delegates, ` +
        `${manifest.counts["session.subagent_event"] ?? 0} subagent events in the log`,
    );
  }
  lines.push("", "Deterministic metrics are exact values; ⚠ marks one that varied across a build's repetitions (min … max); (±N B) is a byte total that varied by under 0.1%.");
  lines.push("Timings are median [IQR]. Ratio is cand/base of the medians.", "");

  for (const sid of plan.scenarios) {
    const rs = results.filter((r) => r.scenario === sid);
    lines.push(`## ${sid}`, "");
    const failed = rs.filter((r) => !r.ok);
    for (const f of failed) lines.push(`- ✗ ${f.build} rep ${f.rep}: ${f.error}`);
    const capped = rs.filter((r) => r.windowCapped);
    if (capped.length) lines.push(`- window hit its cap in ${capped.length} repetition(s): numbers are lower bounds`);
    if (failed.length || capped.length) lines.push("");
    const ok = rs.filter((r) => r.ok);
    const by = (label: string) => ok.filter((r) => r.build === label);
    const head = cand ? `| metric | ${base} | ${cand} | ratio |` : `| metric | ${base} |`;
    const sep = cand ? "|---|---|---|---|" : "|---|---|";
    lines.push(head, sep);
    const row = (label: string, cells: { text: string; value: number }[]) => {
      if (cand) lines.push(`| ${label} | ${cells[0].text} | ${cells[1].text} | ${ratio(cells[1].value, cells[0].value)} |`);
      else lines.push(`| ${label} | ${cells[0].text} |`);
    };
    const detKeys = Object.keys(DET_LABELS).filter((k) => ok.some((r) => k in r.det));
    for (const k of detKeys) row(DET_LABELS[k], labels.map((l) => detCell(by(l).map((r) => r.det[k]), k)));
    const legNames = ok[0]?.legs.map((l) => l.name) ?? [];
    legNames.forEach((n, i) => row(`leg “${n}” click → settled (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.settledMs ?? NaN)))));
    for (const [k, label] of Object.entries(TIMING_LABELS)) {
      if (!ok.some((r) => k in r.timing)) continue;
      row(label, labels.map((l) => timingCell(by(l).map((r) => r.timing[k]))));
    }
    lines.push("");
    // Where the replay came from: each (re)connect's cursor, first repetition per build.
    for (const l of labels) {
      const first = by(l)[0];
      if (!first || first.eventsConnects.length === 0) continue;
      const parts = first.eventsConnects.map(
        (c) => `${c.since === null ? "open before" : c.since === 0 ? "since=0" : `since=${c.since}`} (${c.types} types): ${fmtNum(c.bytes, true)}, ${c.frames} frames`,
      );
      lines.push(`- ${l} /events streams: ${parts.join("; ")}`);
    }
    // The slowest routes to get a socket, first repetition per build.
    for (const l of labels) {
      const first = by(l)[0];
      if (!first) continue;
      const worst = Object.entries(first.blockedByRoute)
        .sort((a, b) => b[1].max - a[1].max)
        .slice(0, 5)
        .map(([k, v]) => `${k} ×${v.n} max ${v.max} ms`);
      if (worst.length) lines.push(`- ${l} most-blocked routes: ${worst.join("; ")}`);
    }
    lines.push("");
  }
  return lines.join("\n");
}

export function writeReport(plan: Plan): string {
  const results = loadResults(plan.runDir);
  const manifest = existsSync(plan.manifestPath) ? (JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest) : undefined;
  const md = renderReport(plan, results, manifest);
  const out = path.join(plan.runDir, "report.md");
  writeFileSync(out, md + "\n");
  writeFileSync(path.join(plan.runDir, "results.json"), JSON.stringify({ plan, manifest, results }, null, 2));
  return out;
}
