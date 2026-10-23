// Catalogue metadata only: no requests to generation or billing endpoints.
export const RATE_REFRESH_MS = 60_000;
export const RATE_MAX_AGE_MS = 5 * 60_000;

// Use concrete platform routes: a free model in one region says nothing about
// the price of a same-name model selected by automatic routing elsewhere.
export function confirmedFreeModels(models = [], now = Date.now()) {
  return models.filter(model => {
    const price = modelPricing(model, now);
    return model.platform && model.platform !== "(别名)" && !model.pricing_from_snapshot && (price.status === "free" || price.freeAllowance === true);
  });
}

// Account grants keep the real credit multiplier; show them separately from
// models whose upstream price is zero.
export function pricingCategory(model, now = Date.now()) {
  const price = modelPricing(model, now);
  return price.freeAllowance ? "allowance" : price.status;
}

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
  if (!p || !["upstream_config", "openrouter_models", "cursor_free_plan", "trae_account_catalog", "kiro_cli_catalog"].includes(p.source) || !Number.isFinite(time) || time > now + 300000) return { status: "unknown" };
  const result = { status: "unknown", observedAt: p.observed_at, raw: typeof p.upstream_label === "string" ? p.upstream_label.slice(0, 128) : "", stale: now - time > RATE_MAX_AGE_MS, refreshFailed: model.pricing_sync_failed === true };
  if (p.source === "openrouter_models" || p.source === "cursor_free_plan") {
    if (p.status === "free" && !result.stale && !result.refreshFailed) result.status = "free";
    return result;
  }
  const rate = p.credit_multiplier;
  if (typeof rate === "number" && Number.isFinite(rate) && rate >= 0) {
    result.multiplier = rate;
    if (!result.stale && !result.refreshFailed) result.status = rate === 0 ? "free" : "credits";
  } else if (p.status === "dynamic" && !result.stale && !result.refreshFailed) result.status = "dynamic";
  if (["trae_account_catalog", "kiro_cli_catalog"].includes(p.source)) {
    const access = model.access, checked = Date.parse(access?.checked_at || "");
    const kiro = p.source === "kiro_cli_catalog";
    result.freeAllowance = model.platform === (kiro ? "kiro" : "trae-cn") && !model.pricing_from_snapshot && p.status === "credits" && result.status === "credits" && !result.stale && !result.refreshFailed && access?.source === (kiro ? "kiro_cli_free_plan" : "trae_free_plan") && access.basis === "recurring_free_allowance" && access.plan === "free" && access.overage_enabled === false && Number.isFinite(access.remaining_credits) && access.remaining_credits > 0 && Number.isFinite(checked) && checked <= now + 60000 && now - checked <= RATE_MAX_AGE_MS;
    // A zero credit rate does not prove account access on this provider.
    if (result.status === "free") result.status = "unknown";
  }
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
  const value = (model) => { const p = modelPricing(model, now); return p.status === "free" ? 0 : p.status === "credits" ? p.multiplier : Infinity; };
  const left = value(a), right = value(b);
  return left === right ? 0 : left < right ? -1 : 1;
}
