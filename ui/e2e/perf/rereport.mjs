// Rewrites a run's report.md from its raw results (after a report change):
//   node ui/e2e/perf/rereport.mjs perf-results/<stamp>
import { readFileSync } from "node:fs";
import path from "node:path";
import { writeReport } from "./report.ts";

const dir = path.resolve(process.argv[2] ?? "");
const plan = JSON.parse(readFileSync(path.join(dir, "plan.json"), "utf8"));
plan.manifestPath = path.join(dir, "manifest.json");
if (!plan.scenarios.length) plan.scenarios = ["1-cold-load", "2-switch-session", "3-open-delegate", "3b-open-delegate-nofile", "4-retro-roundtrip", "5-launchpad"];
console.log(writeReport(plan));
