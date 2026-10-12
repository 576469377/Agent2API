// Catalogue metadata only: no requests to generation or billing endpoints.
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
    return index.has(key) && !duplicates.has(key) ? { ...model, pricing: index.get(key) } : model;
  });
}

export function modelPricing(model, now = Date.now()) {
  const p = model?.pricing;
  const time = Date.parse(p?.observed_at || "");
  if (!p || p.source !== "upstream_config" || !Number.isFinite(time) || time > now + 300000) return { status: "unknown" };
  const result = { status: "unknown", observedAt: p.observed_at, raw: typeof p.upstream_label === "string" ? p.upstream_label.slice(0, 128) : "", stale: now - time > 24 * 3600000 };
  const rate = p.credit_multiplier;
  if (typeof rate === "number" && Number.isFinite(rate) && rate >= 0) {
    result.multiplier = rate;
    if (!result.stale) result.status = rate === 0 ? "free" : "credits";
  } else if (p.status === "dynamic" && !result.stale) result.status = "dynamic";
  return result;
}

// Unknown/dynamic rates always follow fixed rates; ties keep explicit routes.
export function compareModelRates(a, b, now = Date.now()) {
  const value = (model) => { const p = modelPricing(model, now); return ["free", "credits"].includes(p.status) ? p.multiplier : Infinity; };
  const left = value(a), right = value(b);
  return left === right ? 0 : left < right ? -1 : 1;
}
