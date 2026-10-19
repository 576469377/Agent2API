// Metrics derived from the gateway's bounded request sample, never full history.
export const requestKey = (row) => JSON.stringify([
  row.time, row.platform || "", row.model, row.account || "",
  row.protocol, row.duration_ms,
]);

export const accountKey = (row) => JSON.stringify([row.platform || "", row.account || ""]);

// A missing final usage report is not a measured zero. Retain numeric legacy
// observations while allowing explicitly reported all-zero usage in new rows.
export const hasTokenUsage = row => row?.usage_reported === true || Number.isFinite(row?.input_tokens) || Number.isFinite(row?.output_tokens);

export function filterRequests(rows, filters = {}, now = Date.now()) {
  const query = (filters.query || "").trim().toLowerCase();
  const since = filters.range && filters.range !== "all" ? now - Number(filters.range) * 60000 : -Infinity;
  return rows.filter((row) =>
    (!filters.platform || filters.platform === "all" || row.platform === filters.platform) &&
    (!filters.status || filters.status === "all" || (filters.status === "ok" ? row.ok : !row.ok)) &&
    (!filters.protocol || filters.protocol === "all" || row.protocol === filters.protocol) &&
    (!filters.account || filters.account === "all" || (filters.account.startsWith("label:") ? (row.account || "") === filters.account.slice(6) : accountKey(row) === filters.account)) &&
    (!filters.model || filters.model === "all" || row.model === filters.model) &&
    (since === -Infinity || new Date(row.time).getTime() >= since) &&
    `${row.model} ${row.model_requested || ""} ${row.platform || ""} ${row.account || ""} ${row.error || ""} ${row.status}`.toLowerCase().includes(query)
  ).sort((a, b) => {
    const difference = filters.sort === "latency" ? (b.duration_ms || 0) - (a.duration_ms || 0)
      : filters.sort === "tokens" ? ((b.input_tokens || 0) + (b.output_tokens || 0)) - ((a.input_tokens || 0) + (a.output_tokens || 0))
      : 0;
    return difference || new Date(b.time) - new Date(a.time);
  });
}

export function summarizeRequests(rows) {
  const total = rows.length, ok = rows.filter((r) => r.ok).length;
  const times = rows.map((r) => r.duration_ms).filter((v) => Number.isFinite(v) && v >= 0).sort((a, b) => a - b);
  const measured = rows.filter((r) => r.gen_ms > 0 && r.output_tokens > 0);
  const sum = (items, key) => items.reduce((value, row) => value + (Number(row[key]) || 0), 0);
  const generation = sum(measured, "gen_ms");
  return {
    total, ok, failed: total - ok, success_rate: total ? ok / total : null,
    input_tokens: sum(rows, "input_tokens"), output_tokens: sum(rows, "output_tokens"),
    avg_latency_ms: times.length ? times.reduce((a, b) => a + b, 0) / times.length : null,
    p50: times.length ? times[Math.ceil(times.length * .5) - 1] : null,
    p95: times.length ? times[Math.ceil(times.length * .95) - 1] : null,
    avg_tps: generation ? sum(measured, "output_tokens") / (generation / 1000) : null,
    tps_samples: measured.length,
  };
}

export function selectTrend(series, minutes = 30) {
  const sorted = [...(series || [])].sort((a, b) => a.minute - b.minute);
  const last = sorted.at(-1)?.minute;
  return sorted.filter((row) => row.minute > last - minutes);
}

export function sortUsage(rows, sort = "total") {
  const value = (row) => sort === "tokens" ? (row.input_tokens || 0) + (row.output_tokens || 0) : (row[sort] || 0);
  return [...rows].sort((a, b) => value(b) - value(a) || String(a.key).localeCompare(String(b.key)));
}
