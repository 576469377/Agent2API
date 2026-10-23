import { allocateHistory } from "./usage-allocation.js";
import { hasTokenUsage } from "./monitor.js";
// UTC calendar aggregates, separate from the latest-200 request log.
export const DAY = 86400000;
export const palette = ["#438ff4", "#2a9c75", "#9b78ec", "#cf7a3c", "#d95e7b", "#288da8", "#a3822c", "#798cae"];
const count = (v) => Number.isFinite(Number(v)) ? Math.max(0, Number(v)) : 0;
export const dateKey = (ms) => new Date(ms).toISOString().slice(0, 10);
export const modelKey = (platform, model) => JSON.stringify([platform || "", model || ""]);
export const tokenUsageMissing = row => row.tokens === 0 && row.unknown_token_requests > 0;
export function modelColor(key) {
  let hash = 0;
  for (const char of key) hash = ((hash * 31) + char.charCodeAt(0)) >>> 0;
  return palette[hash % palette.length];
}
function sourceHistory(metrics, now) {
  if (metrics?.usage_history?.timezone === "UTC" && Array.isArray(metrics.usage_history.days)) return metrics.usage_history;
  const days = new Map();
  for (const r of metrics?.recent || []) {
    const ms = Date.parse(r.time);
    if (!Number.isFinite(ms)) continue;
    const date = dateKey(ms), rows = days.get(date) || [];
    rows.push({ platform: r.platform || "", model: r.model || "", total: 1,
      ok: r.ok ? 1 : 0, failed: r.ok ? 0 : 1, input_tokens: r.input_tokens, output_tokens: r.output_tokens,
      unknown_token_requests: hasTokenUsage(r) ? 0 : 1, duration_sum: r.duration_ms });
    days.set(date, rows);
  }
  return { timezone: "UTC", tracking_since: new Date(now).toISOString(), legacy_requests: metrics?.total || 0,
    retention_days: 366, preview: true, days: [...days].map(([date, models]) => ({ date, models })) };
}
export function buildUsage(metrics, { range = "7", platform = "all", allocation, now = Date.now() } = {}) {
  if (!metrics) return null;
  const history = allocateHistory(sourceHistory(metrics, now), metrics, allocation, now), today = Math.floor(now / DAY) * DAY;
  const tracking = Date.parse(history.tracking_since);
  const trackingMs = Number.isFinite(tracking) ? tracking : now;
  const retention = Math.min(366, Math.max(1, count(history.retention_days) || 366));
  const earliest = today - (retention - 1) * DAY;
  const source = new Map(), platforms = new Set();
  for (const day of history.days) {
    const ms = Date.parse(`${day.date}T00:00:00Z`);
    if (!Number.isFinite(ms) || dateKey(ms) !== day.date || ms < earliest || ms > today) continue;
    const models = new Map();
    for (const row of day.models || []) {
      platforms.add(row.platform || "");
      if (platform !== "all" && (row.platform || "") !== (platform === "unknown" ? "" : platform)) continue;
      const key = modelKey(row.platform, row.model), c = models.get(key) || {
        key, platform: row.platform || "", model: row.model || "", total: 0, ok: 0, failed: 0,
        input_tokens: 0, output_tokens: 0, duration_sum: 0, unknown_token_requests: 0, estimated_requests: 0, estimated_tokens: 0, tokens: 0, color: modelColor(key) };
      for (const field of ["total", "ok", "failed", "input_tokens", "output_tokens", "duration_sum", "unknown_token_requests"]) c[field] += count(row[field]);
      if (row.allocation_generated) { c.estimated_requests += count(row.total); c.estimated_tokens += count(row.input_tokens) + count(row.output_tokens); }
      c.tokens = c.input_tokens + c.output_tokens;
      models.set(key, c);
    }
    source.set(day.date, models);
  }
  const calendar = [];
  for (let ms = earliest; ms <= today; ms += DAY) {
    const date = dateKey(ms), models = [...(source.get(date)?.values() || [])];
    let coverage = history.preview ? models.length ? "partial" : "unknown" : ms >= trackingMs ? "complete" : models.length || date === dateKey(trackingMs) ? "partial" : "unknown";
    const day = { date, ms, coverage, models, tokens: 0, total: 0, input_tokens: 0, output_tokens: 0, failed: 0, unknown_token_requests: 0, estimated_requests: 0, estimated_tokens: 0 };
    for (const row of models) for (const field of ["tokens", "total", "input_tokens", "output_tokens", "failed", "unknown_token_requests", "estimated_requests", "estimated_tokens"]) day[field] += row[field];
    if (day.estimated_requests || day.estimated_tokens) day.coverage = "estimated";
    calendar.push(day);
  }
  const first = Math.max(earliest, Math.min(today, ...[...source.keys(), dateKey(trackingMs)].map((d) => Date.parse(`${d}T00:00:00Z`))));
  const start = range === "all" ? first : today - ((range === "30" ? 30 : 7) - 1) * DAY;
  const days = calendar.filter((d) => d.ms >= start), groups = new Map();
  for (const day of days) for (const row of day.models) {
    const c = groups.get(row.key) || { ...row, total: 0, ok: 0, failed: 0, input_tokens: 0, output_tokens: 0, duration_sum: 0, tokens: 0, unknown_token_requests: 0, estimated_requests: 0, estimated_tokens: 0 };
    for (const field of ["total", "ok", "failed", "input_tokens", "output_tokens", "duration_sum", "tokens", "unknown_token_requests", "estimated_requests", "estimated_tokens"]) c[field] += row[field];
    groups.set(row.key, c);
  }
  const models = [...groups.values()].sort((a, b) => b.tokens - a.tokens || b.total - a.total || a.key.localeCompare(b.key));
  const totals = { tokens: 0, total: 0, input_tokens: 0, output_tokens: 0, failed: 0, unknown_token_requests: 0, estimated_requests: 0, estimated_tokens: 0 };
  for (const day of days) for (const field of Object.keys(totals)) totals[field] += day[field];
  let streak = 0, longest = 0;
  for (const day of days) { streak = day.total > day.estimated_requests ? streak + 1 : 0; longest = Math.max(streak, longest); }
  const peak = days.reduce((max, day) => day.tokens > (max?.tokens || 0) ? day : max, null);
  return { ...totals, days, calendar, models, platforms: [...platforms].sort(), peak,
    activeDays: days.filter((d) => d.total > d.estimated_requests).length, longestStreak: longest, trackingSince: trackingMs,
    allocation: history.allocation, hasEstimates: totals.estimated_requests > 0 || totals.estimated_tokens > 0,
    sources: history.sources || [], preview: !!history.preview, legacyRequests: count(history.legacy_requests),
    partial: days.some((d) => d.coverage !== "complete"), retention };
}

// Published API snapshots are replaced, never modified. Reuse their derived
// calendar between chart hovers and renders; retain only the last filter for
// each snapshot and release old snapshots through the weak key.
const usageSnapshots = new WeakMap();
export function cachedUsage(metrics, options = {}) {
  if (!metrics) return null;
  const now = options.now ?? Date.now();
  const key = JSON.stringify([Math.floor(now / DAY), options.range ?? "7", options.platform ?? "all", options.allocation ?? null]);
  const previous = usageSnapshots.get(metrics);
  if (previous?.key === key) return previous.usage;
  const usage = buildUsage(metrics, { ...options, now });
  usageSnapshots.set(metrics, { key, usage });
  return usage;
}

// Top models plus an explicit remainder keep the curve totals accountable.
const curveSnapshots = new WeakMap();
export function curveSeries(usage, metric = "tokens", hidden = new Set()) {
  let cached = curveSnapshots.get(usage);
  if (!cached) { cached = new Map(); curveSnapshots.set(usage, cached); }
  if (!cached.has(metric)) cached.set(metric, buildCurveSeries(usage, metric));
  return cached.get(metric).map(model => ({ ...model, hidden: hidden.has(model.key) }));
}
function buildCurveSeries(usage, metric) {
  const ranked = [...usage.models].sort((a, b) => b[metric] - a[metric] || a.key.localeCompare(b.key));
  const top = ranked.slice(0, 6), tail = new Set(ranked.slice(6).map((m) => m.key));
  const models = [...top, ...(tail.size ? [{ key: "other", model: "other", platform: "", color: "#8493a7" }] : [])];
  return models.map((model) => {
    const points = usage.days.map((day) => {
      const rows = day.models.filter(row => model.key === "other" ? tail.has(row.key) : row.key === model.key);
      const value = rows.reduce((sum, row) => sum + row[metric], 0);
      const missingTokens = rows.some(row => row.unknown_token_requests > 0);
      return { value: day.coverage === "unknown" || metric === "tokens" && value === 0 && missingTokens ? null : value, missingTokens };
    });
    return { ...model, values: points.map(point => point.value), missingTokens: points.map(point => point.missingTokens) };
  });
}
// Monotone cubic interpolation: no negative/overshooting invented peaks.
export function curvePath(values, x, y) {
  const segments = [], points = [];
  const flush = () => {
    if (!points.length) return;
    const slopes = points.slice(1).map((p, i) => (p[1] - points[i][1]) / (p[0] - points[i][0]));
    const tangents = points.map((_, i) => i === 0 ? slopes[0] || 0 : i === points.length - 1 ? slopes.at(-1) || 0 : slopes[i - 1] * slopes[i] <= 0 ? 0 : 2 / (1 / slopes[i - 1] + 1 / slopes[i]));
    let path = `M${points[0][0]},${points[0][1]}`;
    for (let i = 1; i < points.length; i++) {
      const [ax, ay] = points[i - 1], [bx, by] = points[i], dx = (bx - ax) / 3;
      path += ` C${ax + dx},${ay + tangents[i - 1] * dx} ${bx - dx},${by - tangents[i] * dx} ${bx},${by}`;
    }
    segments.push(path); points.length = 0;
  };
  values.forEach((value, i) => { if (value == null) flush(); else points.push([x(i), y(value)]); }); flush();
  return segments.join(" ");
}

// Lifetime counters never depend on a date filter or the bounded request log.
export function lifetimeUsage(metrics) {
  if (!metrics) return null;
  const input = count(metrics.input_tokens), output = count(metrics.output_tokens);
  return { total: count(metrics.total), input, output, tokens: input + output, failed: count(metrics.failed) };
}
export function usageComposition(rows, metric = "tokens", limit = 5) {
  const value = row => metric === "total" ? count(row.total) : count(row.input_tokens) + count(row.output_tokens);
  const ranked = (rows || []).map(row => ({ ...row, value: value(row) })).sort((a,b) => b.value - a.value || String(a.key).localeCompare(String(b.key)));
  const total = ranked.reduce((sum,row) => sum + row.value, 0);
  const top = ranked.filter(row => row.value > 0).slice(0,limit);
  return { total, top, other: total - top.reduce((sum,row) => sum + row.value,0) };
}

// Natural-week columns follow ZCode's daily/weekly/cumulative interaction.
// Unknown history remains unknown instead of being padded as measured zero.
export function heatmapColumns(calendar, mode = "daily", metric = "tokens") {
  if (!calendar?.length) return [];
  const cells = [...calendar];
  for (let i=0,n=new Date(cells[0].ms).getUTCDay();i<n;i++) cells.unshift(null);
  while(cells.length % 7) cells.push(null);
  const weeks = Array.from({length:cells.length/7},(_,i)=>cells.slice(i*7,i*7+7));
  let cumulative=0, observedDays=0, incomplete=false, cumulativeEstimated=false, cumulativeUnknownTokens=0;
  const columns=weeks.map(days=>{
    const real=days.filter(Boolean), known=real.filter(d=>d.coverage!=="unknown");
    const value=known.reduce((sum,d)=>sum+count(d[metric]),0);
    const unknownTokens=known.reduce((sum,d)=>sum+count(d.unknown_token_requests),0);
    const partial=real.some(d=>d.coverage!=="complete"||metric==="tokens"&&d.unknown_token_requests>0);
    const estimated=known.some(day=>day.coverage==="estimated"||day.estimated_requests>0||day.estimated_tokens>0);
    cumulativeEstimated ||= estimated;
    cumulative+=value; observedDays+=known.length; incomplete ||= partial; cumulativeUnknownTokens+=unknownTokens;
    const end=real.at(-1)?.date, start=real[0]?.date;
    return {days,start,end,value:mode==="cumulative"?cumulative:value,estimated:mode==="cumulative"?cumulativeEstimated:estimated,unknown_token_requests:mode==="cumulative"?cumulativeUnknownTokens:unknownTokens,
      coverage:mode==="cumulative" ? observedDays ? incomplete?"partial":"complete" : "unknown" : known.length ? partial?"partial":"complete":"unknown",
      monthDate:real.find(d=>d.date.endsWith("-01"))?.date||start};
  });
  const maximum=Math.max(1,...columns.map(c=>c.value));
  const dayMaximum=Math.max(1,...calendar.map(d=>count(d[metric])));
  return columns.map(c=>{
    const level=c.value>0?Math.min(4,Math.max(1,Math.ceil(c.value/maximum*4))):0;
    const filled=c.value>0?Math.min(7,Math.max(1,Math.ceil(c.value/maximum*7))):0;
    return {...c,cells:c.days.map((day,i)=> mode==="daily" ? {day,date:day?.date,estimated:day?.coverage==="estimated"||day?.estimated_requests>0||day?.estimated_tokens>0,level:day?.[metric]>0?Math.min(4,Math.max(1,Math.ceil(day[metric]/dayMaximum*4))):0,coverage:day?.coverage||"blank",value:day?.[metric]||0} : {day,date:day?.date,estimated:c.estimated,level:i>=7-filled?level:0,coverage:c.coverage,value:c.value})};
  });
}

export function liveGeneration(data) {
  if (!data || !Array.isArray(data.active)) return null;
  const active=data.active;
  const charactersPerSecond=active.reduce((sum,r)=>sum+count(r.characters_per_second),0);
  const calibrated=active.length>0&&active.every(r=>Number.isFinite(r.estimated_tokens_per_second));
  return {active, inFlight:count(data.in_flight), charactersPerSecond,
    tokensPerSecond:calibrated?active.reduce((sum,r)=>sum+r.estimated_tokens_per_second,0):null,
    lastSpeed:data.last_completed?.gen_ms>0?data.last_completed.output_tokens/(data.last_completed.gen_ms/1000):null,
    lastCompleted:data.last_completed||null, sampledAt:Date.parse(data.sampled_at)};
}
export function sampleLive(points,data) {
  const live=liveGeneration(data);
  if(!live||!Number.isFinite(live.sampledAt)) return points;
  const previous=points.at(-1);
  if(previous&&previous.time>=live.sampledAt)return points;
  const gap=previous&&live.sampledAt-previous.time>3000?[{time:previous.time+1,value:null}]:[];
  return [...points,...gap,{time:live.sampledAt,value:live.charactersPerSecond}].filter(p=>p.time>=live.sampledAt-30000);
}
