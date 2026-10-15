// Historical allocation is a derived view; persisted observations stay intact.
// The owner explicitly assigns undated usage to the domestic DeepSeek model.
export const HISTORY_ALLOCATION_TARGET = Object.freeze({ platform: "workbuddy", model: "deepseek-v4.1-flash" });
const DAY = 86400000;
const integer = value => Number.isSafeInteger(Number(value)) && Number(value) >= 0 ? Number(value) : 0;
const date = ms => new Date(ms).toISOString().slice(0, 10);
function parseDate(value) {
  if (typeof value !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return NaN;
  const ms = Date.parse(value + "T00:00:00Z");
  return Number.isFinite(ms) && date(ms) === value ? ms : NaN;
}

// Exact largest-remainder allocation, including multi-billion token totals.
export function distributeInteger(total, weights) {
  total = integer(total);
  const values = weights.map(integer), denominator = values.reduce((sum, value) => sum + BigInt(value), 0n);
  if (!denominator) return values.map(() => 0);
  const rows = values.map((value, index) => {
    const numerator = BigInt(total) * BigInt(value);
    return { index, value: Number(numerator / denominator), remainder: numerator % denominator };
  });
  let remaining = total - rows.reduce((sum, row) => sum + row.value, 0);
  const order = [...rows].sort((a, b) => a.remainder === b.remainder ? a.index - b.index : a.remainder > b.remainder ? -1 : 1);
  for (let i = 0; i < remaining; i++) order[i].value++;
  return rows.map(row => row.value);
}

export function allocationPeriod(history, settings = {}, now = Date.now()) {
  const tracking = Date.parse(history.tracking_since || "");
  const boundary = Math.floor((Number.isFinite(tracking) ? tracking : now) / DAY) * DAY;
  const days = Math.max(1, Math.min(365, integer(settings.days) || 30));
  const explicit = !!(settings.start || settings.end);
  const start = explicit ? parseDate(settings.start) : boundary - days * DAY;
  const end = explicit ? parseDate(settings.end) : boundary - DAY;
  const oldest = Math.floor(now / DAY) * DAY - (Math.min(366, integer(history.retention_days) || 366) - 1) * DAY;
  if (!Number.isFinite(start) || !Number.isFinite(end) || start > end || end >= boundary || (end - start) / DAY >= 366) return { error: "invalid_period" };
  if (end < oldest) return { error: "outside_retention" };
  const from = Math.max(start, oldest);
  return { start: date(from), end: date(end), dates: Array.from({ length: Math.round((end - from) / DAY) + 1 }, (_, i) => date(from + i * DAY)) };
}

export function allocateHistory(history, metrics, settings, now = Date.now()) {
  if (!settings?.enabled) return history;
  const period = allocationPeriod(history, settings, now);
  if (period.error) return { ...history, allocation: period };
  const fields = ["total", "failed", "input_tokens", "output_tokens"];
  const actual = Object.fromEntries(fields.map(field => [field, 0]));
  // A derived view can be recomputed without accumulating estimates twice.
  const days = (history.days || []).filter(day => !day.models?.length || day.models.some(row => !row.allocation_generated)).map(day => ({ ...day, models: (day.models || []).filter(row => !row.allocation_generated) }));
  for (const day of days) for (const row of day.models) {
    for (const field of fields) actual[field] += integer(row[field]);
  }
  const gap = Object.fromEntries(fields.map(field => [field, Math.max(0, integer(metrics[field]) - actual[field])]));
  const excess = Object.fromEntries(fields.map(field => [field, Math.max(0, actual[field] - integer(metrics[field]))]));
  const allocation = { start: period.start, end: period.end, ...gap, excess, method: "uniform", ...HISTORY_ALLOCATION_TARGET };
  if (!fields.some(field => gap[field] > 0)) return { ...history, days, allocation };
  const columns = Object.fromEntries(["total", "input_tokens", "output_tokens"].map(field => [field, distributeInteger(gap[field], period.dates.map(() => 1))]));
  const failure = distributeInteger(Math.min(gap.failed, gap.total), columns.total);
  const dayIndex = new Map(days.map(day => [day.date, day]));
  for (const [i, key] of period.dates.entries()) {
    const row = { ...HISTORY_ALLOCATION_TARGET, total: columns.total[i], ok: columns.total[i] - failure[i], failed: failure[i], input_tokens: columns.input_tokens[i], output_tokens: columns.output_tokens[i], duration_sum: 0, allocation_generated: true };
    if (!row.total && !row.input_tokens && !row.output_tokens) continue;
    let day = dayIndex.get(key);
    if (!day) { day = { date: key, models: [] }; dayIndex.set(key, day); }
    day.models = [...day.models, row];
  }
  return { ...history, days: [...dayIndex.values()].sort((a, b) => a.date.localeCompare(b.date)), allocation };
}
