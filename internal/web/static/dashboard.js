import { selectTrend } from "./monitor.js";

// Keep persisted totals, minute buckets and request samples separate. Empty
// windows have zero volume but no measured success rate or generation speed.
export function dashboardMetrics(metrics, scope = "all", minutes = 30) {
  if (!metrics) return null;
  const series = selectTrend(metrics.series, minutes);
  const sum = (key) => series.reduce((total, row) => total + (Number(row[key]) || 0), 0);
  if (scope === "all") return {
    total: metrics.total || 0, ok: metrics.ok || 0, failed: metrics.failed || 0,
    success_rate: metrics.total > 0 ? metrics.ok / metrics.total : null,
    input_tokens: metrics.input_tokens || 0, output_tokens: metrics.output_tokens || 0,
    avg_tps: metrics.tps_samples > 0 && Number.isFinite(metrics.avg_tps) ? metrics.avg_tps : null,
    tps_samples: metrics.tps_samples || 0, series,
  };
  if (!series.length) return null;
  const total = sum("total"), ok = sum("ok"), genMs = sum("gen_ms");
  return {
    total, ok, failed: sum("failed"), success_rate: total > 0 ? ok / total : null,
    input_tokens: sum("input_tokens"), output_tokens: sum("output_tokens"),
    avg_tps: genMs > 0 ? sum("gen_tok") / (genMs / 1000) : null,
    average_rpm: series.length ? total / series.length : null, series,
  };
}

export function accountReadiness(accounts) {
  if (!accounts) return null;
  const enabled = accounts.filter((a) => a.state !== "disabled");
  const ready = enabled.filter((a) => a.healthy && a.state !== "blocked" && !["unauthorized", "auth"].includes(a.reason));
  return { total: accounts.length, enabled: enabled.length, ready: ready.length,
    attention: enabled.length - ready.length, disabled: accounts.length - enabled.length };
}

export function usageShares(rows = []) {
  const total = rows.reduce((sum, row) => sum + Math.max(0, Number(row.total) || 0), 0);
  return [...rows].sort((a, b) => b.total - a.total).slice(0, 4).map((row) => ({
    ...row, share: total ? Math.max(0, Number(row.total) || 0) / total : 0,
  }));
}
