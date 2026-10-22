import test from "node:test";
import assert from "node:assert/strict";
import { requestOutcome, filterRequests } from "../internal/web/static/monitor.js";
import { requestInsights, recentHealthView, requestSampleView, requestDiagnostic, gatewayPolicyView } from "../internal/web/static/insights.js";
import { livePhaseGroups, liveUsageView, usageView } from "../internal/web/static/usage-view.js";
import { heatmapColumns, DAY } from "../internal/web/static/usage.js";
import { modelPricing, confirmedFreeModels } from "../internal/web/static/pricing.js";
import { escapeHTML, number, compact, duration, timestamp, icon } from "../internal/web/static/core.js";

const now = Date.parse("2026-10-23T03:00:00Z");
const ui = { e: escapeHTML, n: number, compact, duration, timestamp, icon, L: zh => zh };
const row = (extra = {}) => ({ time: new Date(now - 1000).toISOString(), model: "m", platform: "workbuddy-global", ok: false, status: 503, ...extra });

test("request outcome facets preserve all unsuccessful counts and isolate client cancellation", () => {
  const records = [row({ ok: true, status: 200 }), row({ status: 499 }), row({ error_code: "routing_timeout" }), row({ status: 429 }), row({ status: 400, attempt_count: 2 })];
  assert.deepEqual(records.map(requestOutcome), ["ok", "canceled", "timeout", "limited", "failed"]);
  assert.equal(filterRequests(records, { status: "failed" }).length, 4);
  assert.equal(filterRequests(records, { status: "errors" }).length, 3);
  assert.equal(filterRequests(records, { status: "timeout" }).length, 1);
  assert.equal(filterRequests(records, { status: "canceled" }).length, 1);
  assert.equal(filterRequests(records, { status: "retried" }).length, 1);
  assert.equal(requestOutcome(row({ ok: true, status: 200, error_code: "old_timeout" })), "ok");
});

test("first-output quantiles and cache fractions use their measured samples without invented zeroes", () => {
  const records = [row({ first_output_ms: 100, input_tokens: 100, cached_tokens: 80, output_tokens: 2 }), row({ first_output_ms: 300, input_tokens: 900, cached_tokens: 90 }), row({ input_tokens: 100000 }), row({ first_output_ms: null }), row({ input_tokens: 100, cached_tokens: 101 })];
  const result = requestInsights(records);
  assert.equal(result.firstOutputP95, 300);
  assert.equal(result.firstOutputSamples, 2);
  assert.equal(result.cacheRatio, .17);
  assert.equal(result.cacheSamples, 2);
  assert.equal(requestInsights([row()]).firstOutputP95, null);
  assert.equal(requestInsights([row()]).cacheRatio, null);
  assert.equal(requestInsights([row({ input_tokens: 100, usage_reported: true })]).cacheRatio, 0);
  const html = requestSampleView([row()], 200, ui);
  assert.ok(html.includes("0 条已测量"));
  assert.ok(!html.includes("NaN"));
  assert.ok(!html.includes("Infinity"));
});

test("recent health uses the actual current window and never treats historical totals as recent failures", () => {
  const metrics = { total: 90000, failed: 500, recent: [row({ ok: true, status: 200 }), row({ time: new Date(now - 16 * 60000).toISOString() })] };
  const html = recentHealthView(metrics, ui, now);
  assert.ok(html.includes("按发起时间"));
  assert.ok(html.includes('data-status="errors"'));
  assert.ok(!html.includes("500"));
  assert.ok(!html.includes("90,000"));
  assert.equal(recentHealthView(null, ui, now), "");
});

test("copyable diagnostics exclude free-form errors, personal paths, account identifiers and message data", () => {
  const privateValue = "PRIVATE-CREDENTIAL-AND-PROMPT";
  const json = requestDiagnostic(row({ request_id: "request-123", account: privateValue, error: privateValue, prompt: privateValue, headers: { Authorization: privateValue }, metadata: { user_id: privateValue }, credential_path: privateValue, attempts: [{ number: 1, platform: "global", status: 503, account_id: privateValue, authorization: privateValue }] }), "0.2.0");
  assert.ok(!json.includes(privateValue));
  const value = JSON.parse(json);
  assert.equal(value.request_id, "request-123");
  assert.equal(value.schema, "agent2api-request-diagnostic-v1");
  assert.deepEqual(value.attempts, [{ number: 1, platform: "global", status: 503 }]);
});

test("live phase summaries distinguish upload, response waiting, active receipt and pauses", () => {
  const active = [row({ phase: "uploading" }), row({ phase: "waiting_headers" }), row({ phase: "streaming", last_output_at: new Date(now - 100).toISOString() }), row({ phase: "streaming", last_output_at: new Date(now - 5000).toISOString() })];
  const groups = livePhaseGroups(active, ui.L, now);
  assert.deepEqual(groups.map(item => item.label), ["正在上传请求", "等待上游响应", "正在接收", "等待后续输出"]);
  assert.equal(groups.filter(item => item.receiving).length, 1);
  const html = liveUsageView({ sampled_at: new Date(now).toISOString(), in_flight: 4, active: active.map(item => ({ ...item, started_at: new Date(now - 125000).toISOString() })) }, "", [], ui);
  assert.ok(html.includes("已用时 2m 5s"));
  assert.ok(html.includes("正在上传请求"));
  assert.ok(!html.includes("NaN"));
});

test("effective gateway policies preserve unknown fields and do not imply that queueing is always enabled", () => {
  const html = gatewayPolicyView({ gateway: { routing_timeout_sec: 600, max_upstream_attempts: 6, queue_timeout_sec: 30, max_queued_requests: 128 } }, ui);
  assert.ok(html.includes("600.0 s"));
  assert.ok(html.includes("配置账号并发上限"));
  assert.equal(gatewayPolicyView({}, ui), "");
  assert.ok(gatewayPolicyView({ gateway: {} }, ui).includes("未提供"));
});

test("model testing accepts only currently confirmed free concrete routes, including same-name regional conflicts", () => {
  const pricing = { status: "free", credit_multiplier: 0, source: "upstream_config", observed_at: new Date(now).toISOString() };
  const free = { id: "same", platform: "workbuddy-global", pricing };
  const paid = { id: "same", platform: "workbuddy", pricing: { ...pricing, credit_multiplier: .5 } };
  const stale = { id: "stale", platform: "workbuddy", pricing: { ...pricing, observed_at: new Date(now - 360000).toISOString() } };
  assert.deepEqual(confirmedFreeModels([free, paid, stale, { id: "missing", platform: "workbuddy" }, { ...free, platform: "(别名)" }, { ...free, pricing_from_snapshot: true }], now), [free]);
  assert.notEqual(modelPricing(paid, now).status, "free");
});

test("activity calendar follows the main trends and leaves the reading mode under user control", () => {
  const metrics = { usage_history: { timezone: "UTC", tracking_since: new Date(now).toISOString(), retention_days: 366, days: [] } };
  const state = { now, range: "7", platform: "all", metric: "tokens", heatMetric: "tokens", hidden: new Set() };
  const html = usageView(metrics, state, ui);
  assert.ok(html.indexOf("每日用量") < html.indexOf('id="usageCalendarDetails"'));
  assert.ok(html.includes('id="usageCalendarDetails" >'));
  assert.ok(!html.includes('data-action="usage-day"'));
  const expanded = usageView(metrics, { ...state, calendarExpanded: true }, ui);
  assert.ok(expanded.includes('id="usageCalendarDetails" open'));
  assert.ok(expanded.includes('data-action="usage-day"'));
});

test("weekly and cumulative activity retain estimate provenance rather than only labeling daily cells", () => {
  const calendar = Array.from({ length: 21 }, (_, i) => ({ date: new Date(now + i * DAY).toISOString().slice(0, 10), ms: Math.floor(now / DAY) * DAY + i * DAY, total: 1, tokens: 10, coverage: i === 0 ? "estimated" : "complete", estimated_tokens: i === 0 ? 10 : 0 }));
  const weekly = heatmapColumns(calendar, "weekly"), cumulative = heatmapColumns(calendar, "cumulative");
  assert.equal(weekly[0].estimated, true);
  assert.equal(weekly.at(-1).estimated, false);
  assert.equal(cumulative.at(-1).estimated, true);
  assert.ok(cumulative.at(-1).cells.every(cell => cell.estimated));
});
