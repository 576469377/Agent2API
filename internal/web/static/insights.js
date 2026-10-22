import { filterRequests, requestOutcome, summarizeRequests, hasTokenUsage } from "./monitor.js";

const quantile = (values, q) => {
  const sorted = values.filter(value => Number.isFinite(value) && value >= 0).sort((a, b) => a - b);
  return sorted.length ? sorted[Math.ceil(sorted.length * q) - 1] : null;
};

// All rates and percentiles use only the visible bounded sample, never lifetime counters.
export function requestInsights(rows = []) {
  const outcomes = { ok: 0, canceled: 0, timeout: 0, limited: 0, failed: 0 };
  for (const row of rows) outcomes[requestOutcome(row)]++;
  const cacheRows = rows.filter(row => hasTokenUsage(row) && Number.isFinite(row.input_tokens) && row.input_tokens > 0 &&
    (row.usage_reported || Object.hasOwn(row, "cached_tokens")) &&
    (row.cached_tokens == null || Number.isFinite(row.cached_tokens) && row.cached_tokens >= 0 && row.cached_tokens <= row.input_tokens));
  const cacheInput = cacheRows.reduce((sum, row) => sum + row.input_tokens, 0);
  return {
    ...summarizeRequests(rows), outcomes,
    serviceFailed: rows.length - outcomes.ok - outcomes.canceled,
    retried: rows.filter(row => Number(row.attempt_count) > 1).length,
    firstOutputP95: quantile(rows.map(row => row.first_output_ms), .95),
    firstOutputSamples: rows.filter(row => Number.isFinite(row.first_output_ms) && row.first_output_ms >= 0).length,
    cacheRatio: cacheInput > 0 ? cacheRows.reduce((sum, row) => sum + (row.cached_tokens || 0), 0) / cacheInput : null,
    cacheSamples: cacheRows.length,
  };
}

export function recentHealthView(metrics, ui, now = Date.now()) {
  if (!metrics) return "";
  const { e, L, n, duration } = ui;
  const rows = filterRequests(metrics.recent || [], { range: "15" }, now), stats = requestInsights(rows);
  const tile = (label, value, status, tone = "") => `<button class="health-sample-item ${tone}" data-action="request-preset" data-status="${status}" data-range="15"><span>${e(label)}</span><strong>${n(value)}</strong><small>${e(L("查看调用", "View requests"))} →</small></button>`;
  return `<section class="card recent-health" aria-label="${e(L("近期调用健康", "Recent request health"))}">
    <div class="card-head"><div><h2>${e(L("近期调用", "Recent requests"))}</h2><p>${e(L("近 15 分钟样本 · 按发起时间", "Last 15 minutes of samples · by start time"))}</p></div><span class="scope-pill">${e(L("最多 200 条", "Up to 200 records"))}</span></div>
    <div class="health-sample-grid">${tile(L("已完成", "Completed"), stats.total, "all")}${tile(L("失败 · 不含取消", "Failed · excluding canceled"), stats.serviceFailed, "errors", stats.serviceFailed ? "has-errors" : "")}${tile(L("客户端取消", "Client canceled"), stats.outcomes.canceled, "canceled")}<div class="health-sample-item"><span>${e(L("P95 首段等待", "P95 first output"))}</span><strong>${e(duration(stats.firstOutputP95))}</strong><small>${e(L(`${n(stats.firstOutputSamples)} 条有测量值`, `${n(stats.firstOutputSamples)} measured requests`))}</small></div></div>
  </section>`;
}

export function requestSampleView(rows, buffered, ui) {
  const { e, L, n, duration } = ui, stats = requestInsights(rows);
  const item = (label, value, note = "") => `<span><span>${e(label)}</span><strong>${e(value)}</strong>${note ? `<small>${e(note)}</small>` : ""}</span>`;
  return item(L("匹配样本", "Matched samples"), n(stats.total), `/ ${n(buffered)}`) +
    item(L("失败 / 取消", "Failed / canceled"), `${n(stats.serviceFailed)} / ${n(stats.outcomes.canceled)}`) +
    item(L("P95 完整耗时", "P95 total duration"), duration(stats.p95)) +
    item(L("P95 首段等待", "P95 first output"), duration(stats.firstOutputP95), L(`${n(stats.firstOutputSamples)} 条已测量`, `${n(stats.firstOutputSamples)} measured`)) +
    item(L("缓存读取 / 输入", "Cached / input tokens"), stats.cacheRatio == null ? "—" : `${(stats.cacheRatio * 100).toFixed(1)}%`, L(`${n(stats.cacheSamples)} 条有用量`, `${n(stats.cacheSamples)} usage samples`));
}

export function gatewayPolicyView(config, ui) {
  const { e, L, n, duration } = ui, gateway = config?.gateway;
  if (!gateway) return "";
  const field = (label, value) => `<div><dt>${e(label)}</dt><dd>${e(value)}</dd></div>`;
  const seconds = value => Number.isFinite(value) && value > 0 ? duration(value * 1000) : L("未提供", "Unavailable");
  return `<section class="card gateway-policy"><div class="card-head"><div><h2>${e(L("请求预算与排队", "Request budgets and queue"))}</h2><p>${e(L("当前生效值 · 修改启动配置后重启生效", "Effective values · restart after changing startup configuration"))}</p></div></div><div class="card-body"><dl class="details-list">${field(L("建流总等待（含上传）", "Routing wait, including upload"), seconds(gateway.routing_timeout_sec))}${field(L("上游尝试上限", "Maximum upstream attempts"), gateway.max_upstream_attempts > 0 ? n(gateway.max_upstream_attempts) : L("未提供", "Unavailable"))}${field(L("满载时最多排队", "Maximum queue wait"), seconds(gateway.queue_timeout_sec))}${field(L("每平台最多等待", "Queued requests per platform"), gateway.max_queued_requests > 0 ? n(gateway.max_queued_requests) : L("未提供", "Unavailable"))}</dl><p class="scope-note">${e(L("排队仅在配置账号并发上限且全部满载时发生。WorkBuddy 流式上传使用建流预算；普通请求超时用于非流式聚合。", "Queueing requires an explicit account concurrency cap and no available capacity. WorkBuddy streaming uploads use the routing budget; request timeout limits non-streaming aggregation."))}</p></div></section>`;
}

// The copyable summary is an explicit allow-list: no error body, account identity,
// credential, prompt, output, session key, request headers or filesystem path.
export function requestDiagnostic(row, version = "") {
  const fields = ["request_id", "time", "platform", "model", "protocol", "stream", "ok", "status", "duration_ms", "first_output_ms", "attempt_count", "queue_ms", "error_code", "image_count", "forwarded_image_count"];
  const result = { schema: "agent2api-request-diagnostic-v1", ...(version ? { version } : {}) };
  for (const field of fields) if (row[field] != null) result[field] = row[field];
  if (Array.isArray(row.attempts)) result.attempts = row.attempts.map(attempt => Object.fromEntries(
    ["number", "platform", "status", "duration_ms", "error_code"].filter(key => attempt[key] != null).map(key => [key, attempt[key]])));
  return JSON.stringify(result, null, 2);
}
