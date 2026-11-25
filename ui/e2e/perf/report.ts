import { existsSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import type { DelayMode, Plan, Result } from "./measure";
import { DELAYS_OFF_ONLY, type Manifest } from "./scenarios";
import { resolveCheck, UI_ONLY_CHECKS } from "./checkNames";

/**
 * The run's report: per scenario and delay mode, deterministic metrics as
 * exact values (⚠ when repetitions of one build disagree: the metric or the
 * harness isn't as deterministic as it should be), timings as median and
 * IQR, and the candidate/baseline ratio of medians. Failed and missing
 * repetitions are listed and counted, never dropped silently.
 */

const DET_LABELS: Record<string, string> = {
  events_stream_bytes_proxy: "/events stream bytes sent (proxy)",
  events_stream_frames: "/events SSE frames (proxy)",
  events_stream_messages_cdp: "/events SSE messages (CDP)",
  events_stream_connects: "/events (re)connects",
  events_from_zero: "…of which with no `since` (replay from 0)",
  events_declared_start: "…of which declared their start (`: start <seq>`)",
  es_auto_reconnects: "native EventSource auto-reconnects (page probe)",
  events_batch_requests: "GET /events batch pages",
  events_batch_bytes: "GET /events batch bytes",
  requests_short: "short requests",
  requests_delayed: "…of which delayed by the proxy",
  output_streams_held: "/output streams held open by the proxy (heartbeat comments only, no data)",
  peak_busy_sockets_proxy: "peak busy sockets (proxy, authoritative; secondary)",
  peak_sockets_lsof: "peak sockets to the proxy (lsof, authoritative; secondary)",
  prep_append_events: "append phase (before the window): events posted to the Data Service",
  prep_append_payload_bytes: "append phase: payload bytes posted",
  prep_events_stream_bytes: "append phase: /events bytes to the page (live traffic, not in the window)",
  prep_events_frames: "append phase: /events frames to the page",
  prep_events_connects: "append phase: /events (re)connects",
  chat_older_pages: "chat: older transcript pages fetched loading the view",
  chat_rows_loaded: "chat: transcript rows loaded (pages × 100, capped at the conversation)",
  chat_items: "chat: rendered conversation items",
  chat_user_msgs: "chat: user messages rendered (checked against the transcript)",
  chat_time_elements: "chat: <time> elements in the conversation stream",
  chat_dom_nodes_per_row: "chat: conversation-stream DOM nodes per loaded row",
  chat_return_retries: "chat: extra Ctrl+↓↓ presses before the view was back at the tail, following",
  chat_dom_nodes: "chat: DOM nodes, whole page, after loading",
  chat_stream_dom_nodes: "chat: DOM nodes in the conversation stream",
  chat_deltas: "chat: text deltas the page received (first delta → turn end)",
  chat_commits: "chat: React commits in the window",
  chat_proxy_deltas_matched: "chat: the proxy's text_delta stamps matched the page's deltas (1 = yes)",
  chat_bubble_scans: "chat: recorder lookups that fell back to scanning every row",
};

const TIMING_LABELS: Record<string, string> = {
  settled_ms: "click → settled, all legs (ms)",
  marker_ms: "click → marker, in page time, all legs (ms)",
  cdp_lag_max_ms: "harness's CDP lag, worst probe read (ms; diagnostic)",
  byte_idle_fallbacks: "/events streams settled by byte-idle fallback (no id: lines)",
  main_task_ms: "main-thread task time, all legs (CDP TaskDuration, ms)",
  main_script_ms: "main-thread script time, all legs (CDP ScriptDuration, ms)",
  lag_max_ms: "worst main-thread lag (50 ms probe, ms)",
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
  data_conns_closed: "connections to the Data Service closed by clients, boot + window (TIME_WAIT: only the last ~30 s, so long windows undercount)",
  port_drain_wait_ms: "harness wait for TIME_WAIT to drain before boot (ms; not measured time)",
  prep_append_ms: "append phase: posting time (ms; not measured time)",
  prep_catchup_ms: "append phase: last post → page caught up and quiet (ms; not measured time)",
  git_spawns: "git calls by the stack, whole window (shim log, exact)",
  git_router_spawns: "…of which the Router's own (the rest: session children)",
  git_cpu_ms: "git CPU, whole window (ms; the calls' rusage, user + system)",
  git_wall_ms: "git wall time, summed over calls (ms; overlapping calls add)",
  git_changeset_roots: "change-set roots diffed, whole window (shim: `add -A` into a temp shadow index; one per root per change-set compute)",
  git_peak_concurrent_ps: "most git processes running at once (ps every 100 ms, by pid ancestry)",
  git_seen_by_ps: "git processes ps sampling caught (vs the shim's count: what sampling misses)",
  abandoned_requests: "abandoned requests: still in flight when the view left their session",
  abandoned_git_requests: "…of which git routes",
  abandoned_aborted: "…of which the page aborted",
  abandoned_held_total_ms: "abandoned: socket time held after leaving, summed (ms)",
  abandoned_held_max_ms: "abandoned: longest held after leaving (ms)",
  chat_window_ms: "chat: stream window, first delta → turn end (paced: → the reveal's end) (ms)",
  chat_deltas_per_s: "chat: text deltas received per second",
  chat_commits_per_s: "chat: React commits per second",
  chat_commits_per_delta: "chat: React commits per delta",
  chat_busy_pct: "chat: main thread busy (% of the window; trace; over 90% = saturated, a ceiling)",
  chat_busy_ms: "chat: main thread busy in the window (ms; trace)",
  chat_ms_per_s: "chat: main-thread ms per second of window (trace)",
  chat_ms_per_delta: "chat: main-thread ms per delta (trace)",
  chat_js_ms_per_delta: "chat: …JS ms per delta (trace self time)",
  chat_style_ms_per_delta: "chat: …style ms per delta",
  chat_layout_ms_per_delta: "chat: …layout ms per delta",
  chat_paint_ms_per_delta: "chat: …paint/composite ms per delta",
  chat_gc_ms_per_delta: "chat: …GC ms per delta",
  chat_other_ms_per_delta: "chat: …other (task overhead, unattributed) ms per delta",
  chat_js_ms_per_s: "chat: JS ms per second of window (trace self time)",
  chat_style_ms_per_s: "chat: …style ms per second",
  chat_layout_ms_per_s: "chat: …layout ms per second",
  chat_paint_ms_per_s: "chat: …paint/composite ms per second",
  chat_gc_ms_per_s: "chat: …GC ms per second",
  chat_other_ms_per_s: "chat: …other ms per second",
  chat_ms_per_commit: "chat: main-thread ms per React commit (trace; secondary: a candidate can change commits)",
  chat_js_ms_per_commit: "chat: …JS ms per commit",
  chat_style_ms_per_commit: "chat: …style ms per commit",
  chat_layout_ms_per_commit: "chat: …layout ms per commit",
  chat_paint_ms_per_commit: "chat: …paint/composite ms per commit",
  chat_gc_ms_per_commit: "chat: …GC ms per commit",
  chat_other_ms_per_commit: "chat: …other ms per commit",
  chat_frame_p50_ms: "chat: frame interval p50 (rAF, ms)",
  chat_frame_p95_ms: "chat: frame interval p95 (rAF, ms)",
  chat_frames_over_50ms: "chat: frames over 50 ms",
  chat_longtasks: "chat: long tasks in the window",
  chat_longtask_ms: "chat: long-task time in the window (ms)",
  chat_wire_on_screen_p50_ms: "chat: delta sent by the proxy → on screen, p50 (ms; unpaced)",
  chat_wire_on_screen_p95_ms: "chat: delta sent by the proxy → on screen, p95 (ms; unpaced)",
  chat_on_screen_p50_ms: "chat: delta handled by the page → on screen, p50 (ms; unpaced)",
  chat_on_screen_p95_ms: "chat: delta handled by the page → on screen, p95 (ms; unpaced)",
  chat_reveal_after_turn_end_ms: "chat: turn end → the reveal's end (ms; paced)",
  chat_off_bottom_pct: "chat: frames more than 10 px off the bottom (%; runway page turns glide)",
  chat_off_bottom_max_px: "chat: furthest off the bottom (px)",
  chat_end_hidden_pct: "chat: frames with the reply's end out of view (%)",
  chat_end_hidden_max_ms: "chat: longest stretch with the reply's end out of view (ms)",
  chat_probe_ms_per_s: "chat: the recorder's own time, ms per second of window (in-page clock)",
  chat_probe_ms_per_sample: "chat: the recorder's per-frame sample (ms)",
  chat_probe_trace_pct_of_busy: "chat: the recorder's share of busy time (%; trace)",
  chat_probe_layout_ms_per_s: "chat: style + layout inside the recorder's samples, ms per second (trace; forced)",
};

type ChatCol = [string, string, "det" | "timing", 1 | 2];
/** The chat summary's columns, unpaced (renderChatSummary): label → result key. Costs per delta first. */
const CHAT_UNPACED: ChatCol[] = [
  ["rows", "chat_rows_loaded", "det", 1],
  ["nodes/row", "chat_dom_nodes_per_row", "det", 1],
  ["deltas/s", "chat_deltas_per_s", "timing", 1],
  ["busy %", "chat_busy_pct", "timing", 1],
  ["ms/delta", "chat_ms_per_delta", "timing", 2],
  ["JS", "chat_js_ms_per_delta", "timing", 2],
  ["style", "chat_style_ms_per_delta", "timing", 2],
  ["layout", "chat_layout_ms_per_delta", "timing", 2],
  ["paint", "chat_paint_ms_per_delta", "timing", 2],
  ["GC", "chat_gc_ms_per_delta", "timing", 2],
  ["commits/s", "chat_commits_per_s", "timing", 1],
  ["ms/commit", "chat_ms_per_commit", "timing", 2],
  ["frame p50", "chat_frame_p50_ms", "timing", 1],
  ["frame p95", "chat_frame_p95_ms", "timing", 1],
  ["sent → screen p95", "chat_wire_on_screen_p95_ms", "timing", 1],
  ["long tasks", "chat_longtasks", "timing", 1],
];
/** Paced: the window is mostly the reveal (a commit a frame), so per second, not per delta. */
const CHAT_PACED: ChatCol[] = [
  ["rows", "chat_rows_loaded", "det", 1],
  ["window s", "chat_window_ms", "timing", 1],
  ["busy %", "chat_busy_pct", "timing", 1],
  ["ms/s", "chat_ms_per_s", "timing", 1],
  ["JS", "chat_js_ms_per_s", "timing", 1],
  ["style", "chat_style_ms_per_s", "timing", 1],
  ["layout", "chat_layout_ms_per_s", "timing", 1],
  ["paint", "chat_paint_ms_per_s", "timing", 1],
  ["GC", "chat_gc_ms_per_s", "timing", 1],
  ["commits/s", "chat_commits_per_s", "timing", 1],
  ["ms/commit", "chat_ms_per_commit", "timing", 2],
  ["frame p50", "chat_frame_p50_ms", "timing", 1],
  ["frame p95", "chat_frame_p95_ms", "timing", 1],
  ["long tasks", "chat_longtasks", "timing", 1],
];
/** In a saturated cell (busy over 90%) these are the numbers that still move; the rest are ceilings. */
const CHAT_HEADLINE_SATURATED = new Set(["chat_frame_p50_ms", "chat_frame_p95_ms", "chat_commits_per_s", "chat_wire_on_screen_p95_ms"]);
const CHAT_SATURATED_PCT = 90;

/**
 * The chat scenarios' summary: a row per scenario and build, medians over
 * its passing repetitions. Unpaced costs are per delta (the work is a
 * fixed number of deltas; commits are the app's choice); paced per second
 * of window. A saturated cell (busy over 90%) is flagged ⚠: its busy and
 * per-delta costs are ceilings, so its headline is frames, commits/s and
 * on-screen latency (bold), and 10cs measures the cost unsaturated.
 */
export function renderChatSummary(plan: Plan, results: Result[]): string[] {
  const ids = plan.scenarios.filter((s) => /^10/.test(s));
  if (ids.length === 0) return [];
  const med = (sid: string, b: string, k: string, where: "det" | "timing") => {
    const ok = results.filter((r) => r.scenario === sid && r.build === b && r.ok);
    const v = ok.map((r) => (where === "det" ? r.det?.[k] : r.timing?.[k])).filter((x): x is number => typeof x === "number" && Number.isFinite(x));
    return v.length ? quantile(v, 0.5) : NaN;
  };
  const table = (title: string, cols: ChatCol[], sids: string[], note: string) => {
    if (sids.length === 0) return [];
    const out = [`### ${title}`, "", note, "", `| scenario | build | ok | sat | ${cols.map((c) => c[0]).join(" | ")} |`, `|---|---|---|---|${cols.map(() => "---").join("|")}|`];
    for (const sid of sids) {
      for (const b of plan.builds) {
        const n = results.filter((r) => r.scenario === sid && r.build === b.label && r.ok).length;
        const busy = med(sid, b.label, "chat_busy_pct", "timing");
        const sat = busy > CHAT_SATURATED_PCT;
        const cells = cols.map(([, k, where, dp]) => {
          let v = med(sid, b.label, k, where);
          if (k === "chat_window_ms") v /= 1000;
          const t = Number.isFinite(v) ? (dp === 2 ? v.toFixed(2) : fmtNum(Math.round(v * 10) / 10)) : "–";
          return sat && CHAT_HEADLINE_SATURATED.has(k) ? `**${t}**` : t;
        });
        out.push(`| ${sid} | ${b.label} | ${n}/${plan.n} | ${sat ? "⚠" : ""} | ${cells.join(" | ")} |`);
      }
    }
    out.push("");
    return out;
  };
  const lines = [
    "## Chat stream (10x): summary",
    "",
    "Medians over passing repetitions; costs are main-thread ms from the CDP trace (self time by bucket). " +
      "⚠ sat = busy over 90%: the main thread had no idle time, so busy % and ms/delta are ceilings, not costs — read the **bold** columns " +
      "(frames, commits/s, sent → on screen) there, and 10cs for the cost at every page unsaturated.",
    "",
    ...table(
      "Unpaced (window: first delta → turn end; costs per delta)",
      CHAT_UNPACED,
      ids.filter((s) => !s.includes("-paced")),
      "sent → screen: the proxy sent the delta → the first frame showing it (includes the page's backlog).",
    ),
    ...table(
      "Paced (window: first delta → the reveal's end; costs per second)",
      CHAT_PACED,
      ids.filter((s) => s.includes("-paced")),
      "The reveal commits once a frame, so its pace sets commits/s; on-screen latency is the reveal's lag by design and isn't reported.",
    ),
  ];
  const chatRs = results.filter((r) => /^10/.test(r.scenario));
  const passed = chatRs.filter((r) => r.ok && r.checks?.length).length;
  lines.push(
    `Checks (the loaded size; the wire carried the agent's whole stored reply; the streamed render was whole and in order, and still is after the merge; stick-to-bottom held): ${passed} / ${chatRs.length} repetitions passed every one.`,
  );
  for (const f of chatRs.filter((r) => !r.ok)) {
    const bad = (f.checks ?? []).filter((c) => !c.ok).map((c) => `${c.name}: ${c.detail ?? ""}`);
    lines.push(`- ✗ ${f.scenario} ${f.build} rep ${f.rep}: ${bad.length ? bad.join("; ") : f.error}`);
  }
  // The recorder's own cost, so a reader can see what it adds.
  const probe = ids
    .map((sid) => {
      const b = plan.builds[0].label;
      const a = med(sid, b, "chat_probe_ms_per_s", "timing");
      const p = med(sid, b, "chat_probe_trace_pct_of_busy", "timing");
      const l = med(sid, b, "chat_probe_layout_ms_per_s", "timing");
      return Number.isFinite(a) ? `${sid} ${a.toFixed(1)} ms/s (${Number.isFinite(p) ? p.toFixed(1) : "–"}% of busy; forced style+layout ${Number.isFinite(l) ? l.toFixed(2) : "–"} ms/s)` : "";
    })
    .filter(Boolean);
  if (probe.length) lines.push("", `Recorder overhead (${plan.builds[0].label}): ${probe.join("; ")}.`);
  lines.push("");
  return lines;
}

/** The git routes' latency rows (measure.ts result.timing "route <r> …"). */
const GIT_ROUTE_NAMES = ["repos", "changeset", "worktree/refresh", "worktree", "commit-plan", "push", "land"];

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

/** Keys whose values are small per-unit costs: shown to 2 decimals, not rounded to whole ms. */
const FINE = /^chat_.*_per_(delta|commit|sample)$|^chat_.*(_ms_per_s|_pct_of_busy)$|^chat_commits_per_delta$/;

function timingCell(vals: number[], key = ""): { text: string; value: number } {
  const v = vals.filter(Number.isFinite);
  if (v.length === 0) return { text: "–", value: NaN };
  const f = FINE.test(key) ? (x: number) => x.toFixed(2) : (x: number) => fmtNum(Math.round(x));
  const med = quantile(v, 0.5);
  if (v.length < 2) return { text: f(med), value: med };
  return { text: `${f(med)} [${f(quantile(v, 0.25))}–${f(quantile(v, 0.75))}]`, value: med };
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
      `/output held open: ${plan.holdOutput ? "yes" : "no"}${plan.holdOutput && plan.scenarios.some((x) => /^10/.test(x)) ? " (10x: passed through, a reply streams)" : ""}${plan.chatReal ? "; CHAT: a real transcript (no names, content, screenshots or stack logs in these results)" : ""}${plan.realism ? "; REALISM: a sanitised copy of a real database (no names in this report)" : ""}`,
  );
  if (manifest) {
    lines.push(
      `- workload: profile ${manifest.profile}, ${manifest.total_events} events, ${(manifest.total_bytes / 1e6).toFixed(1)} MB payload, ` +
        `${manifest.sessions.length} sessions; parent has ${manifest.parent.delegates} delegates, ` +
        `${manifest.counts["session.subagent_event"] ?? 0} subagent events in the log`,
    );
    if (manifest.chat) {
      const c = manifest.chat;
      const roles = Object.entries(c.by_role).map(([k, v]) => `${k} ${v}`).join(", ");
      lines.push(`- chat conversation (10x): ${c.source}, ${c.turns} turns, ${c.rows} rows (${roles}), ${(c.text_bytes / 1e6).toFixed(1)} MB of text`);
    }
  }
  lines.push("", "Deterministic metrics are exact values; ⚠ marks one that varied across a build's repetitions (min … max); (±N B) is a byte total that varied by under 0.1%.");
  lines.push(
    "Timings are median [IQR]. Ratio is cand/base of the medians.",
    "Settled = data marker visible, then: no non-delayed short request in flight or started for 500 ms; every open /events stream caught up to the log head at the click (or owing nothing); main thread answering the lag probe within 50 ms for 500 ms.",
    "Proxy and lsof counts are authoritative; CDP counts are cross-checks (they miss what the proxy holds or the page abandons). The held /output stream carries heartbeat comments only, no data.",
    "",
  );

  // Every repetition the plan asked for, so missing ones show.
  for (const sid of plan.scenarios) {
    for (const mode of modes) {
      if (DELAYS_OFF_ONLY.test(sid) && mode === "on") continue;
      const rs = results.filter((r) => r.scenario === sid && (r.mode ?? "on") === mode && !r.skipped);
      if (rs.length === 0 && plan.realism) {
        lines.push(`## ${sid} — delays ${mode}`, "", "- not run (realism: no suitable data)", "");
        continue;
      }
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
        row(`leg “${n}” click → marker, page time (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.markerMs ?? NaN))));
        row(`leg “${n}” click → marker seen by harness (ms; diagnostic)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.markerSeenMs ?? NaN))));
        row(`leg “${n}” click → settled (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.settledMs ?? NaN))));
        row(`leg “${n}” click → last /events byte (ms)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.lastEventsByteMs ?? NaN))));
        row(`leg “${n}” /events bytes`, labels.map((l) => detCell(by(l).map((r) => r.legs[i]?.eventsBytes ?? NaN), "bytes")));
        row(`leg “${n}” main-thread task / script (ms)`, labels.map((l) => {
          const t = timingCell(by(l).map((r) => r.legs[i]?.taskMs ?? NaN));
          const sc = timingCell(by(l).map((r) => r.legs[i]?.scriptMs ?? NaN));
          return { text: `${t.text} / ${sc.text}`, value: t.value };
        }));
        if (ok.some((r) => r.legs[i]?.gitSpawns !== undefined)) {
          row(`leg “${n}” git calls / git CPU (ms)`, labels.map((l) => {
            const a = timingCell(by(l).map((r) => r.legs[i]?.gitSpawns ?? NaN));
            const c = timingCell(by(l).map((r) => r.legs[i]?.gitCpuMs ?? NaN));
            return { text: `${a.text} / ${c.text}`, value: c.value };
          }));
          row(`leg “${n}” change-set roots diffed (shim)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.gitChangesetRoots ?? NaN))));
        }
        row(`leg “${n}” Router CPU (ms; ps)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.routerCpuMs ?? NaN))));
        row(`leg “${n}” CDP lag, worst read (ms; diagnostic)`, labels.map((l) => timingCell(by(l).map((r) => r.legs[i]?.cdpLagMaxMs ?? NaN))));
        row(`leg “${n}” lag p95 / max (ms)`, labels.map((l) => {
          const p = timingCell(by(l).map((r) => r.legs[i]?.lagP95Ms ?? NaN));
          const mx = timingCell(by(l).map((r) => r.legs[i]?.lagMaxMs ?? NaN));
          return { text: `${p.text} / ${mx.text}`, value: mx.value };
        }));
      });
      for (const [k, label] of Object.entries(DET_LABELS)) {
        if (!ok.some((r) => k in r.det)) continue;
        row(label, labels.map((l) => detCell(by(l).map((r) => r.det[k]), k)));
      }
      for (const [k, label] of Object.entries(TIMING_LABELS)) {
        if (headline.includes(k) || !ok.some((r) => k in r.timing)) continue;
        row(label, labels.map((l) => timingCell(by(l).map((r) => r.timing[k]), k)));
      }
      // Git routes: per repetition, p50 and max over the window's requests; cells are the median across repetitions.
      for (const g of GIT_ROUTE_NAMES) {
        if (!ok.some((r) => `route ${g} n` in r.timing)) continue;
        const pair = (a: string, b: string) => (l: string) => {
          const x = timingCell(by(l).map((r) => r.timing[`route ${g} ${a}`] ?? NaN));
          const y = timingCell(by(l).map((r) => r.timing[`route ${g} ${b}`] ?? NaN));
          return { text: `${x.text} / ${y.text}`, value: y.value };
        };
        row(`**${g}**: requests`, labels.map((l) => timingCell(by(l).map((r) => r.timing[`route ${g} n`] ?? 0))));
        row(`${g}: server p50 / max (ms)`, labels.map(pair("server p50", "server max")));
        row(`${g}: client p50 / max (ms; includes queueing)`, labels.map(pair("client p50", "client max")));
      }
      lines.push("");
      for (const l of labels) {
        const first = by(l)[0];
        if (!first) continue;
        if (first.eventsConnects.length) {
          const parts = first.eventsConnects.map(
            (c) =>
              `${c.since === null ? "open before" : c.since === 0 ? "since=0" : `since=${c.since}`}` +
              `${c.from === -1 ? " from=head" : c.from != null ? ` start=${c.from}` : ""} (${c.types} types): ${fmtNum(c.bytes, true)}, ${c.frames} frames`,
          );
          lines.push(`- ${l} /events streams (rep ${first.rep}): ${parts.join("; ")}`);
        }
        const worst = Object.entries(first.queuedByRoute ?? {})
          .sort((a, b) => b[1].max - a[1].max)
          .slice(0, 5)
          .map(([k, v]) => `${k} ×${v.n} max ${v.max} ms`);
        if (worst.length) lines.push(`- ${l} longest-queued routes (rep ${first.rep}): ${worst.join("; ")}`);
        if (first.gitBySub && Object.keys(first.gitBySub).length) {
          const top = Object.entries(first.gitBySub)
            .sort((a, b) => b[1].cpuMs - a[1].cpuMs)
            .slice(0, 6)
            .map(([k, v]) => `${k} ×${v.n} ${v.cpuMs} ms`);
          lines.push(`- ${l} git by subcommand, by CPU (rep ${first.rep}): ${top.join("; ")}`);
        }
        const foreign = [...new Set(by(l).flatMap((r) => r.browserForeign ?? []))];
        if (foreign.length) lines.push(`- ${l} ⚠ browser connections other than the proxy: ${foreign.join("; ")}`);
      }
      lines.push("");
    }
  }
  lines.push(...renderChatSummary(plan, results));
  // Correctness checks, per build, step by step; and the live-event smoke after each scenario.
  const checkRs = results.filter((r) => r.scenario.startsWith("check:"));
  if (plan.checks.length) {
    lines.push(...renderChecks(plan, checkRs), "");
  }
  const smokes = results.filter((r) => !r.scenario.startsWith("check:") && r.checks?.length);
  if (smokes.length) {
    const failed = smokes.filter((r) => r.checks.some((c) => !c.ok));
    lines.push(`Live-event smoke after each scenario: ${smokes.length - failed.length} / ${smokes.length} passed` + (failed.length ? ` — ✗ ${failed.map((r) => `${r.scenario} ${r.mode} ${r.build}#${r.rep}`).join(", ")}` : ""), "");
  }
  return lines.join("\n");
}

/** Whether every planned repetition exists and succeeded. */
export function allOk(plan: Plan, results: Result[]): boolean {
  const modes = plan.modes ?? ["on"];
  // Real-git (9x) and chat (10x) scenarios run with delays off only.
  const runs = plan.scenarios.reduce((n, s) => n + (DELAYS_OFF_ONLY.test(s) ? modes.filter((m) => m === "off").length : modes.length), 0);
  const want = (runs * plan.n + plan.checks.length) * plan.builds.length;
  return results.length === want && results.every((r) => r.ok);
}

export function writeReport(plan: Plan): string {
  const results = loadResults(plan.runDir);
  const manifest = existsSync(plan.manifestPath) ? (JSON.parse(readFileSync(plan.manifestPath, "utf8")) as Manifest) : undefined;
  const md = renderReport(plan, results, manifest);
  const out = path.join(plan.runDir, "report.md");
  writeFileSync(out, md + "\n");
  // Realism: the manifest names real sessions; it stays in the run's temp dir.
  const kept = plan.realism ? undefined : manifest;
  writeFileSync(path.join(plan.runDir, "results.json"), JSON.stringify({ plan, manifest: kept, results, allOk: allOk(plan, results) }, null, 2));
  return out;
}

export type StepStatus = "pass" | "fail" | "known-fail" | "FIXED" | "REGRESSION" | "missing";

/**
 * One check's steps across builds. With a candidate: pass (both pass),
 * known-fail-still-failing, FIXED (base fails, cand passes), REGRESSION
 * (base passes, cand fails), fail (both fail, not known on main). Failure
 * messages from both builds are shown side by side.
 */
export function compareCheck(base: Result | undefined, cand: Result | undefined, hasCand: boolean): { step: string; status: StepStatus; base?: string; cand?: string }[] {
  const steps = new Map<string, { b?: Result["checks"][number]; c?: Result["checks"][number] }>();
  for (const s of base?.checks ?? []) steps.set(s.name, { ...steps.get(s.name), b: s });
  for (const s of cand?.checks ?? []) steps.set(s.name, { ...steps.get(s.name), c: s });
  if (steps.size === 0) steps.set("(no steps ran)", {});
  const out: { step: string; status: StepStatus; base?: string; cand?: string }[] = [];
  const msg = (s?: Result["checks"][number], r?: Result) => (s ? (s.ok ? "✓" : `✗ ${s.detail ?? ""}`) : r ? `✗ ${r.error ?? "did not run"}` : "missing");
  for (const [step, { b, c }] of steps) {
    let status: StepStatus;
    if (!hasCand) status = !b ? "missing" : b.ok ? "pass" : b.knownOnMain ? "known-fail" : "fail";
    else if (!b || !c) status = "missing";
    else if (b.ok && c.ok) status = "pass";
    else if (b.ok && !c.ok) status = "REGRESSION";
    else if (!b.ok && c.ok) status = "FIXED";
    else status = b.knownOnMain ? "known-fail" : "fail";
    out.push({ step, status, base: msg(b, base), cand: hasCand ? msg(c, cand) : undefined });
  }
  return out;
}

export function renderChecks(plan: Plan, checkRs: Result[]): string[] {
  const [base, cand] = plan.builds.map((b) => b.label);
  const lines: string[] = ["## Correctness checks", ""];
  const regressions: string[] = [];
  const fixed: string[] = [];
  lines.push(cand ? `| check | step | status | ${base} | ${cand} |` : `| check | step | status | ${base} |`, cand ? "|---|---|---|---|---|" : "|---|---|---|---|");
  const known: string[] = [];
  for (const c of plan.checks) {
    const full = resolveCheck(c);
    const find = (l: string) => checkRs.find((x) => x.scenario === `check:${full}` && x.build === l);
    const b = find(base);
    const k = cand ? find(cand) : undefined;
    const label = UI_ONLY_CHECKS.has(full) ? `${full} (UI-only, not a bus check)` : full;
    for (const row of compareCheck(b, k, !!cand)) {
      if (row.status === "REGRESSION" && !UI_ONLY_CHECKS.has(full)) regressions.push(`${full}: ${row.step}`);
      if (row.status === "FIXED") fixed.push(`${full}: ${row.step}`);
      const st = row.status === "pass" ? "✓ pass" : row.status === "known-fail" ? "✗ known-fail, still failing" : row.status === "FIXED" ? "★ FIXED" : row.status === "REGRESSION" ? "⚠ REGRESSION" : `✗ ${row.status}`;
      lines.push(cand ? `| ${label} | ${row.step} | ${st} | ${row.base} | ${row.cand} |` : `| ${label} | ${row.step} | ${st} | ${row.base} |`);
    }
    for (const s of [...(b?.checks ?? []), ...(k?.checks ?? [])]) if (s.knownOnMain && !s.ok) known.push(`${full} “${s.name}”: ${s.knownOnMain}`);
  }
  if (regressions.length) lines.splice(1, 0, "", `**⚠⚠ CANDIDATE REGRESSION: passes on base, fails on cand: ${regressions.join("; ")}**`);
  if (fixed.length) lines.push("", `★ Fixed by the candidate (fails on base, passes on cand): ${fixed.join("; ")}`);
  if (known.length) lines.push("", "Known to fail on main (not counted against the run):", ...[...new Set(known)].map((x) => `- ${x}`));
  return lines;
}
