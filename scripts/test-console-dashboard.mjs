import { test } from "node:test";
import assert from "node:assert/strict";
import { dashboardMetrics, accountReadiness, usageShares } from "../internal/web/static/dashboard.js";

test("Window indicators aggregate only the selected minute buckets and weight generation time", () => {
  const series = Array.from({ length: 30 }, (_, i) => ({ minute: 100 + i, total: 2, ok: 1, failed: 1, input_tokens: 100, output_tokens: 20, gen_ms: i < 15 ? 100 : 1000, gen_tok: 20 }));
  const metrics = { total: 4000, ok: 3900, input_tokens: 800000, series };
  const window = dashboardMetrics(metrics, "window", 15);
  assert.equal(window.total, 30);
  assert.equal(window.input_tokens, 1500);
  assert.equal(window.success_rate, .5);
  assert.equal(window.avg_tps, 20);
  assert.equal(window.average_rpm, 2);
  assert.equal(dashboardMetrics(metrics, "window", 30).avg_tps, 600 / 16.5);
  assert.equal(dashboardMetrics(metrics).total, 4000);
  assert.equal(dashboardMetrics(metrics).input_tokens, 800000);
  assert.equal(series[0].minute, 100);
});
test("Unloaded data and empty measured windows never invent a success rate or speed", () => {
  assert.equal(dashboardMetrics(null), null);
  assert.equal(dashboardMetrics({ total: 10 }, "window"), null);
  const empty = dashboardMetrics({ series: [{ minute: 1, total: 0 }] }, "window");
  assert.equal(empty.total, 0);
  assert.equal(empty.success_rate, null);
  assert.equal(empty.avg_tps, null);
  assert.equal(dashboardMetrics({ total: 0 }).success_rate, null);
  assert.equal(dashboardMetrics({ total: 10, tps_samples: 0, avg_tps: 100 }).avg_tps, null);
});
test("Readiness excludes disabled and invalid credentials even if a health flag is stale", () => {
  assert.equal(accountReadiness(null), null);
  assert.deepEqual(accountReadiness([{ healthy: true }, { healthy: false }, { state: "disabled", healthy: true }, { state: "blocked", healthy: true }, { reason: "auth", healthy: true }]), { total: 5, enabled: 4, ready: 1, attention: 3, disabled: 1 });
});
test("Usage percentages use all rows as the denominator, not only the top four", () => {
  const rows = [50, 20, 10, 10, 10].map((total, i) => ({ key: String(i), total }));
  assert.equal(usageShares(rows)[0].share, .5);
  assert.equal(usageShares(rows).length, 4);
  assert.equal(usageShares([{ total: 0 }])[0].share, 0);
  assert.equal(rows.length, 5);
});
