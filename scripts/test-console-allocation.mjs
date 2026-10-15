import test from "node:test";
import assert from "node:assert/strict";
import { allocateHistory, allocationPeriod, distributeInteger } from "../internal/web/static/usage-allocation.js";
import { buildUsage, heatmapColumns } from "../internal/web/static/usage.js";
import { usageView, usageTooltip, usageMethodologyView } from "../internal/web/static/usage-view.js";
import { escapeHTML, number, compact, icon } from "../internal/web/static/core.js";

const now = Date.parse("2026-10-15T17:50:00Z");
const allocation = { enabled: true, days: 30 };
const metrics = {
  total: 30321, failed: 321, input_tokens: 2345678901, output_tokens: 345678901,
  by_model: [{ key: "hy3", total: 12345, input_tokens: 1000000000, output_tokens: 200000000 }, { key: "flash", total: 17976, input_tokens: 1345678901, output_tokens: 145678901 }],
  usage_history: { timezone: "UTC", tracking_since: "2026-10-15T12:00:00Z", legacy_requests: 28966, retention_days: 366,
    days: [{ date: "2026-10-15", models: [
      { platform: "workbuddy", model: "hy3", total: 321, ok: 310, failed: 11, input_tokens: 2000000, output_tokens: 200000 },
      { platform: "workbuddy-global", model: "hy3", total: 1234, ok: 1212, failed: 22, input_tokens: 8000000, output_tokens: 800000 },
    ] }] },
};
const state = { allocation, now, range: "all", platform: "all", metric: "tokens", heatMetric: "tokens", hidden: new Set() };

test("30-day legacy allocation reconciles all request/token totals without changing real history", () => {
  const original = JSON.stringify(metrics), usage = buildUsage(metrics, state);
  assert.equal(usage.total, metrics.total);
  assert.equal(usage.input_tokens, metrics.input_tokens);
  assert.equal(usage.output_tokens, metrics.output_tokens);
  assert.equal(usage.failed, metrics.failed);
  assert.equal(usage.tokens, 2691357802);
  assert.equal(usage.estimated_requests, 28766);
  assert.equal(usage.allocation.start, "2026-09-15");
  assert.equal(usage.allocation.end, "2026-10-14");
  assert.equal(usage.days.filter(day => day.coverage === "estimated").length, 30);
  for (const field of ["total", "input_tokens", "output_tokens"]) {
    const values = usage.days.slice(0, 30).map(day => day[field]);
    assert.ok(Math.max(...values) - Math.min(...values) <= 1, field + " must be uniform by date");
  }
  assert.equal(usage.days.at(-1).total, 1555);
  assert.equal(usage.activeDays, 1);
  assert.equal(usage.longestStreak, 1);
  assert.equal(JSON.stringify(metrics), original);
  const weekly = heatmapColumns(usage.calendar, "weekly", "tokens");
  assert.equal(weekly.reduce((sum, column) => sum + column.value, 0), usage.tokens);
  for (const day of usage.days) for (const row of day.models) assert.equal(row.ok + row.failed, row.total);
});

test("shared bare model names are allocated to history, never attributed to either region", () => {
  const usage = buildUsage(metrics, state);
  assert.ok(usage.models.some(row => row.platform === "" && row.model === "hy3"));
  assert.equal(buildUsage(metrics, { ...state, platform: "workbuddy" }).total, 321);
  assert.equal(buildUsage(metrics, { ...state, platform: "workbuddy-global" }).total, 1234);
  assert.equal(buildUsage(metrics, { ...state, platform: "unknown" }).total, 28766);
  assert.equal(buildUsage(metrics, { ...state, allocation: { enabled: false } }).total, 1555);
});

test("integer remainders stay exact for large totals and allocation can be recomputed or moved", () => {
  assert.deepEqual(distributeInteger(11, [1, 1, 1]), [4, 4, 3]);
  assert.equal(distributeInteger(2691357802, [1500000000, 1000000000, 7]).reduce((sum, value) => sum + value, 0), 2691357802);
  const once = allocateHistory(metrics.usage_history, metrics, allocation, now);
  assert.deepEqual(allocateHistory(once, metrics, allocation, now), once);
  const moved = buildUsage({ ...metrics, usage_history: once }, { ...state, allocation: { enabled: true, days: 7 } });
  assert.equal(moved.total, metrics.total);
  assert.equal(moved.days[0].date, "2026-10-08");
});

test("invalid dates, live-day overlap and excess archived data never overwrite measured records", () => {
  assert.equal(allocationPeriod(metrics.usage_history, { start: "2026-02-30", end: "2026-10-14" }, now).error, "invalid_period");
  assert.equal(allocationPeriod(metrics.usage_history, { start: "2026-10-14", end: "2026-10-15" }, now).error, "invalid_period");
  const excess = buildUsage({ ...metrics, input_tokens: 1 }, state);
  assert.equal(excess.input_tokens, 10000000);
  assert.equal(excess.allocation.excess.input_tokens, 9999999);
  const missingGroups = buildUsage({ ...metrics, by_model: [] }, state);
  assert.equal(missingGroups.total, metrics.total);
  assert.equal(missingGroups.tokens, metrics.input_tokens + metrics.output_tokens);
});

test("chart and detail views identify estimated dates and do not fabricate request log links", () => {
  const ui = { e: escapeHTML, n: number, compact, icon, L: zh => zh };
  const html = usageView(metrics, state, ui), methodology = usageMethodologyView(metrics, state, ui);
  assert.ok(html.includes("含历史均摊"));
  assert.ok(html.includes("历史汇总"));
  assert.ok(usageTooltip(metrics, state, 0, ui).includes("估算"));
  assert.ok(methodology.includes("2026-09-15"));
  assert.ok(methodology.includes("28,766"));
  assert.ok(html.includes("仅汇总"));
});
