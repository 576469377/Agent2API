// Estimated date allocation is a derived view. Never mutate persisted history
// or infer a platform from a bare model name shared by several providers.
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
  const observedModels = new Map();
  // A derived view can be recomputed without accumulating estimates twice.
  const days = (history.days || []).filter(day => !day.models?.length || day.models.some(row => !row.allocation_generated)).map(day => ({ ...day, models: (day.models || []).filter(row => !row.allocation_generated) }));
  for (const day of days) for (const row of day.models) {
    const key = row.model || "(unknown)", model = observedModels.get(key) || Object.fromEntries(fields.map(field => [field, 0]));
    for (const field of fields) { actual[field] += integer(row[field]); model[field] += integer(row[field]); }
    observedModels.set(key, model);
  }
  const gap = Object.fromEntries(fields.map(field => [field, Math.max(0, integer(metrics[field]) - actual[field])]));
  const excess = Object.fromEntries(fields.map(field => [field, Math.max(0, actual[field] - integer(metrics[field]))]));
  const allocation = { start: period.start, end: period.end, ...gap, excess, method: "uniform" };
  if (!fields.some(field => gap[field] > 0)) return { ...history, days, allocation };
  const modelWeights = new Map();
  for (const row of metrics.by_model || []) {
    const key = row.key || "(unknown)";
    const previous = modelWeights.get(key) || Object.fromEntries(fields.map(field => [field, 0]));
    for (const field of fields) previous[field] += integer(row[field]);
    modelWeights.set(key, previous);
  }
  for (const [key, weights] of modelWeights) for (const field of fields) weights[field] = Math.max(0, weights[field] - integer(observedModels.get(key)?.[field]));
  const residual = Object.fromEntries(fields.map(field => [field, Math.max(0, gap[field] - [...modelWeights.values()].reduce((sum, weights) => sum + weights[field], 0))]));
  if (fields.some(field => residual[field] > 0)) modelWeights.set("(unattributed)", residual);
  const models = [...modelWeights.keys()].sort();
  const totals = {};
  for (const field of ["total", "input_tokens", "output_tokens"]) totals[field] = distributeInteger(gap[field], models.map(key => modelWeights.get(key)[field]));
  // Request outcomes remain consistent even when archived per-model counters
  // overlap the dated records differently from lifetime counters.
  totals.failed = distributeInteger(Math.min(gap.failed, gap.total), totals.total);
  const dayIndex = new Map(days.map(day => [day.date, day]));
  // Balance both axes: exact model totals and a uniform total per date.
  const remaining = Object.fromEntries(["total", "input_tokens", "output_tokens"].map(field => [field, distributeInteger(gap[field], period.dates.map(() => 1))]));
  for (const [modelIndex, model] of models.entries()) {
    const columns = Object.fromEntries(Object.entries(remaining).map(([field, capacity]) => {
      const values = distributeInteger(totals[field][modelIndex], capacity);
      remaining[field] = capacity.map((value, i) => value - values[i]);
      return [field, values];
    }));
    const failure = distributeInteger(totals.failed[modelIndex], columns.total);
    for (const [i, key] of period.dates.entries()) {
      const row = { platform: "", model, total: columns.total[i], ok: columns.total[i] - failure[i], failed: failure[i], input_tokens: columns.input_tokens[i], output_tokens: columns.output_tokens[i], duration_sum: 0, allocation_generated: true };
      if (!row.total && !row.input_tokens && !row.output_tokens) continue;
      let day = dayIndex.get(key);
      if (!day) { day = { date: key, models: [] }; dayIndex.set(key, day); }
      day.models = [...day.models, row];
    }
  }
  return { ...history, days: [...dayIndex.values()].sort((a, b) => a.date.localeCompare(b.date)), allocation };
}
