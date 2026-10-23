import { performance } from "node:perf_hooks";
import { execFileSync } from "node:child_process";
import { cpus } from "node:os";
import * as current from "../internal/web/static/usage.js";

// Example: node scripts/benchmark-console-usage.mjs cc85016
// The reference is loaded as source; dependencies and synthetic input are shared.
const commit = execFileSync("git", ["rev-parse", "--verify", "--end-of-options", `${process.argv[2] || "HEAD"}^{commit}`], { encoding: "utf8" }).trim();
const baseURL = new URL("../internal/web/static/", import.meta.url);
const original = execFileSync("git", ["show", `${commit}:internal/web/static/usage.js`], { encoding: "utf8" })
  .replace(/from "\.\/([^"]+)"/g, (_, filename) => `from ${JSON.stringify(new URL(filename, baseURL).href)}`);
const baseline = await import("data:text/javascript;base64," + Buffer.from(original).toString("base64"));
const now = Date.parse("2026-10-24T12:00:00Z"), today = Math.floor(now / current.DAY) * current.DAY;
const metrics = { usage_history: { timezone: "UTC", retention_days: 366, tracking_since: "2025-10-24T00:00:00Z",
  days: Array.from({ length: 366 }, (_, day) => ({ date: new Date(today - (365-day) * current.DAY).toISOString().slice(0, 10),
    models: Array.from({ length: 200 }, (_, model) => ({ platform: "test", model: `m${model}`, total: 1, ok: 1, input_tokens: 100+model, output_tokens: 10 })) })) } };
function measure(module) {
  const usage = module.buildUsage(metrics, { range: "all", now, allocation: { enabled: false } });
  for (let i=0; i<3; i++) module.curveSeries(usage);
  const batches = [];
  for (let run=0; run<5; run++) {
    const start = performance.now();
    for (let i=0; i<100; i++) module.curveSeries(usage, "tokens", new Set(i % 2 ? [usage.models[0].key] : []));
    batches.push(performance.now()-start);
  }
  return { batches_ms: batches, median_ms: [...batches].sort((a,b)=>a-b)[2], tokens: usage.tokens };
}
const result = { baseline_commit: commit, platform: process.platform, architecture: process.arch, node: process.version,
  cpu: cpus()[0]?.model, days: 366, models: 200, iterations_per_batch: 100,
  scope: "Warm curveSeries derivation only; does not measure rendering or model latency", baseline: measure(baseline), current: measure(current) };
process.stdout.write(JSON.stringify(result, null, 2)+"\n");
