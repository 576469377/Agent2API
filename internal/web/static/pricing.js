// Catalogue metadata only: no requests to generation or billing endpoints.
export const RATE_REFRESH_MS = 60_000;
export const RATE_MAX_AGE_MS = 5 * 60_000;

export function rateRefreshDue(lastAttempt, now = Date.now(), manual = false) {
  return manual || !lastAttempt || now - lastAttempt >= RATE_REFRESH_MS;
}

export function mergePricingSnapshot(models, snapshot) {
  if (snapshot?.version !== 1 || !Array.isArray(snapshot.models)) return models;
  const index = new Map(), duplicates = new Set();
  for (const row of snapshot.models) {
    if (typeof row?.platform !== "string" || typeof row?.id !== "string") continue;
    const key = JSON.stringify([row.platform, row.id]);
    if (index.has(key)) duplicates.add(key);
    index.set(key, row.pricing);
  }
  return models.map((model) => {
    if (Object.hasOwn(model, "pricing")) return model; // Native catalogue is authoritative.
    const key = JSON.stringify([model.platform, model.id]);
    return index.has(key) && !duplicates.has(key) ? { ...model, pricing: index.get(key), pricing_from_snapshot: true } : model;
  });
}

export function modelPricing(model, now = Date.now()) {
  const p = model?.pricing;
  const time = Date.parse(p?.observed_at || "");
  if (!p || p.source !== "upstream_config" || !Number.isFinite(time) || time > now + 300000) return { status: "unknown" };
  const result = { status: "unknown", observedAt: p.observed_at, raw: typeof p.upstream_label === "string" ? p.upstream_label.slice(0, 128) : "", stale: now - time > RATE_MAX_AGE_MS, refreshFailed: model.pricing_sync_failed === true };
  const rate = p.credit_multiplier;
  if (typeof rate === "number" && Number.isFinite(rate) && rate >= 0) {
    result.multiplier = rate;
    if (!result.stale && !result.refreshFailed) result.status = rate === 0 ? "free" : "credits";
  } else if (p.status === "dynamic" && !result.stale && !result.refreshFailed) result.status = "dynamic";
  return result;
}

// Successful HTTP reads can still contain a hub's last good per-platform cache.
// A refresh must carry observations made during this check, not merely a new
// browser receipt time. RFC3339 upstream timestamps have one-second precision.
export function verifyRateObservations(models, lastAttempt) {
  const cutoff = Math.floor(lastAttempt / 1000) * 1000;
  return models.map(model => ({ ...model, pricing_sync_failed: model.pricing_from_snapshot === true || !Number.isFinite(Date.parse(model.pricing?.observed_at)) || Date.parse(model.pricing.observed_at) < cutoff }));
}

export function pricingChanges(before = [], after = [], now = Date.now()) {
  const key = m => JSON.stringify([m.platform, m.id]);
  const previous = new Map(before.map(m => [key(m), m]));
  return after.flatMap(model => {
    const old = previous.get(key(model));
    if (!old) return [];
    const oldTime = Date.parse(old.pricing?.observed_at);
    const from = modelPricing(old, Number.isFinite(oldTime) ? oldTime : now), to = modelPricing(model, now);
    if (from.status === "unknown" || to.status === "unknown") return [];
    if (from.status === to.status && from.multiplier === to.multiplier) return [];
    return [{ platform: model.platform, id: model.id, routingId: model.routing_id || `${model.platform}/${model.id}`, from, to, observedAt: to.observedAt }];
  });
}

// Unknown/dynamic rates always follow fixed rates; ties keep explicit routes.
export function compareModelRates(a, b, now = Date.now()) {
  const value = (model) => { const p = modelPricing(model, now); return ["free", "credits"].includes(p.status) ? p.multiplier : Infinity; };
  const left = value(a), right = value(b);
  return left === right ? 0 : left < right ? -1 : 1;
}
