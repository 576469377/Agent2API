import { test } from "node:test";
import assert from "node:assert/strict";
import { requestKey, accountKey, filterRequests, summarizeRequests, selectTrend, sortUsage } from "../internal/web/static/monitor.js";

const now = Date.UTC(2026, 9, 12, 1, 0);
const records = [
  { time: new Date(now - 60000).toISOString(), platform: "cn", model: "shared", model_requested: "alias", account: "same", protocol: "messages", ok: true, status: 200, duration_ms: 1000, input_tokens: 50, output_tokens: 100, gen_ms: 500 },
  { time: new Date(now - 120000).toISOString(), platform: "global", model: "shared", account: "same", protocol: "messages", ok: false, status: 429, error: "rate limit", duration_ms: 200, input_tokens: 0 },
  { time: new Date(now - 3600000).toISOString(), platform: "global", model: "other", account: "other", protocol: "chat", ok: true, status: 200, duration_ms: 3000, input_tokens: 200, output_tokens: 900, gen_ms: 3000 },
];
test("Request drill-down isolates a platform even with identical account/model labels", () => {
  assert.notEqual(requestKey(records[0]), requestKey({ ...records[0], platform: "global" }));
  assert.notEqual(requestKey(records[0]), requestKey({ ...records[0], protocol: "chat" }));
  const filters = { platform: "global", model: "shared", account: accountKey(records[1]), status: "failed", protocol: "messages", range: "15" };
  assert.deepEqual(filterRequests(records, filters, now), [records[1]]);
  assert.equal(filterRequests(records, { account: "label:same" }, now).length, 2);
});
test("Filters combine time, requested aliases, status codes and errors without mutating source", () => {
  assert.equal(filterRequests(records, { range: "30" }, now).length, 2);
  assert.deepEqual(filterRequests(records, { query: "alias" }, now), [records[0]]);
  assert.deepEqual(filterRequests(records, { query: "429" }, now), [records[1]]);
  assert.deepEqual(filterRequests(records, { query: "RATE LIMIT" }, now), [records[1]]);
  assert.equal(filterRequests(records, { platform: "absent" }, now).length, 0);
  const original = structuredClone(records);
  assert.equal(filterRequests(records, { sort: "tokens" }, now)[0], records[2]);
  assert.equal(filterRequests(records, { sort: "latency" }, now)[0], records[2]);
  assert.deepEqual(records, original);
});
test("Sample quantiles and TPS use the correct observations rather than total latency", () => {
  const sample = summarizeRequests(records);
  assert.equal(sample.total, 3);
  assert.equal(sample.ok, 2);
  assert.equal(sample.failed, 1);
  assert.equal(sample.p50, 1000);
  assert.equal(sample.p95, 3000);
  assert.equal(sample.input_tokens, 250);
  assert.equal(sample.output_tokens, 1000);
  assert.equal(sample.avg_tps, 1000 / 3.5);
  assert.equal(sample.tps_samples, 2);
  const empty = summarizeRequests([]);
  assert.equal(empty.success_rate, null);
  assert.equal(empty.p95, null);
  assert.equal(empty.avg_tps, null);
  assert.equal(summarizeRequests([{ output_tokens: 100, gen_ms: 0 }]).avg_tps, null);
});
test("Trend windows use real minute buckets, with no manufactured long-term history", () => {
  const series = Array.from({ length: 30 }, (_, i) => ({ minute: i + 100, total: i }));
  assert.equal(selectTrend(series, 15).length, 15);
  assert.equal(selectTrend([...series].reverse(), 30).length, 30);
  assert.equal(selectTrend(series, 60).length, 30);
  assert.deepEqual(selectTrend([], 30), []);
  assert.equal(series[0].minute, 100);
});
test("Usage rankings sort cumulative metrics without overwriting gateway aggregates", () => {
  const rows = [{ key: "a", total: 20, failed: 1, input_tokens: 2 }, { key: "b", total: 10, failed: 3, input_tokens: 100 }];
  assert.equal(sortUsage(rows)[0].key, "a");
  assert.equal(sortUsage(rows, "tokens")[0].key, "b");
  assert.equal(sortUsage(rows, "failed")[0].key, "b");
  assert.equal(rows[0].key, "a");
});
