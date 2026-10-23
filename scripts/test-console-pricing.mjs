import { test } from "node:test";
import assert from "node:assert/strict";
import { mergePricingSnapshot, modelPricing, pricingCategory, confirmedFreeModels, compareModelRates, rateRefreshDue, verifyRateObservations, pricingChanges, RATE_MAX_AGE_MS } from "../internal/web/static/pricing.js";

const now = Date.UTC(2026, 9, 12, 1, 0);
const pricing = (rate, status = "credits") => ({ status, credit_multiplier: rate, source: "upstream_config", observed_at: new Date(now).toISOString() });

test("Kiro Free requires its own fresh account evidence and keeps the actual positive rate", () => {
  const model = { platform:"kiro", id:"qwen3-coder-next", max_output_tokens:0, pricing:{...pricing(.05),source:"kiro_cli_catalog"}, access:{source:"kiro_cli_free_plan",basis:"recurring_free_allowance",plan:"free",overage_enabled:false,remaining_credits:49,checked_at:new Date(now).toISOString()} };
  assert.equal(modelPricing(model,now).multiplier,.05);
  assert.equal(pricingCategory(model,now),"allowance");
  assert.deepEqual(confirmedFreeModels([model],now),[model]);
  for(const patch of [{source:"trae_free_plan"},{plan:"pro"},{remaining_credits:0},{overage_enabled:true}]) assert.notEqual(pricingCategory({...model,access:{...model.access,...patch}},now),"allowance");
  assert.notEqual(pricingCategory({...model,pricing_from_snapshot:true},now),"allowance");
});
test("TRAE free allowance preserves its credit price and requires current account evidence", () => {
  const model = { platform: "trae-cn", id: "deepseek-v4.1-flash", pricing: { ...pricing(.5), source: "trae_account_catalog" }, access: { source: "trae_free_plan", basis: "recurring_free_allowance", plan: "free", overage_enabled: false, remaining_credits: 500, checked_at: new Date(now).toISOString() } };
  assert.equal(modelPricing(model, now).status, "credits");
  assert.equal(modelPricing(model, now).multiplier, .5);
  assert.equal(pricingCategory(model, now), "allowance");
  assert.deepEqual(confirmedFreeModels([model], now), [model]);
  for (const patch of [
    { access: undefined }, { platform: "workbuddy" }, { platform: "(别名)" },
    { pricing_from_snapshot: true }, { pricing_sync_failed: true },
    ...[{ plan: "pro" }, { overage_enabled: true }, { overage_enabled: undefined }, { source: "guess" }, { basis: "trial" }, { remaining_credits: 0 }, { remaining_credits: "500" }, { remaining_credits: Infinity }, { checked_at: "invalid" }, { checked_at: new Date(now - RATE_MAX_AGE_MS - 1).toISOString() }].map(access => ({ access: { ...model.access, ...access } })),
    { pricing: { ...model.pricing, credit_multiplier: 0 } },
    { pricing: { ...model.pricing, status: "unknown" } },
    { pricing: { ...model.pricing, observed_at: new Date(now - RATE_MAX_AGE_MS - 1).toISOString() } },
  ]) {
    assert.deepEqual(confirmedFreeModels([{ ...model, ...patch }], now), [], JSON.stringify(patch));
    assert.notEqual(pricingCategory({ ...model, ...patch }, now), "allowance");
  }
});
test("OpenRouter free pricing keeps freshness checks without inventing credit multipliers", () => {
  const model = { platform: "openrouter", id: "author/model:free", pricing: { status: "free", source: "openrouter_models", observed_at: new Date(now).toISOString() } };
  assert.equal(modelPricing(model, now).status, "free");
  assert.equal(modelPricing(model, now).multiplier, undefined);
  assert.equal(modelPricing(model, now + RATE_MAX_AGE_MS + 1).status, "unknown");
  assert.equal(modelPricing({ ...model, pricing_sync_failed: true }, now).status, "unknown");
  assert.equal(modelPricing({ ...model, pricing: { ...model.pricing, status: "unknown" } }, now).status, "unknown");
  assert.equal(compareModelRates(model, { pricing: pricing(.1) }, now), -1);
});
test("Pricing snapshot keeps identical model names isolated by platform and never overrides native metadata", () => {
  const models = [{ platform: "cn", id: "same" }, { platform: "global", id: "same" }, { platform: "global", id: "native", pricing: { status: "unknown" } }];
  const snapshot = { version: 1, models: [{ platform: "cn", id: "same", pricing: pricing(.11) }, { platform: "global", id: "same", pricing: pricing(0) }, { platform: "global", id: "native", pricing: pricing(0) }] };
  const result = mergePricingSnapshot(models, snapshot);
  assert.equal(modelPricing(result[0], now).status, "credits");
  assert.equal(modelPricing(result[1], now).status, "free");
  assert.equal(modelPricing(result[2], now).status, "unknown");
  assert.equal(models[0].pricing, undefined);
  assert.equal(mergePricingSnapshot(models, { ...snapshot, version: 2 }), models);
  assert.equal(modelPricing(mergePricingSnapshot(models, { version: 1, models: [snapshot.models[0], snapshot.models[0]] })[0], now).status, "unknown");
});
test("Missing, invalid and stale pricing cannot become free", () => {
  for (const p of [undefined, {}, pricing(null, "free"), pricing(-1), pricing(Infinity), { ...pricing(0), source: "guess" }, { ...pricing(0), observed_at: "invalid" }, { ...pricing(0), observed_at: new Date(now + 3600000).toISOString() }]) assert.equal(modelPricing({ pricing: p }, now).status, "unknown");
  const old = modelPricing({ pricing: pricing(0) }, now + 25 * 3600000);
  assert.equal(old.status, "unknown"); assert.equal(old.stale, true);
  assert.equal(modelPricing({ pricing: pricing(undefined, "dynamic") }, now).status, "dynamic");
  assert.equal(modelPricing({ pricing: pricing(.001) }, now).multiplier, .001);
});

test("Cursor Free allowance requires fresh explicit plan observations", () => {
  const p = { status: "free", source: "cursor_free_plan", observed_at: new Date(now).toISOString(), upstream_label: "Free allowance" };
  assert.equal(modelPricing({ pricing: p }, now).status, "free");
  assert.equal(modelPricing({ pricing: p }, now + RATE_MAX_AGE_MS + 1).status, "unknown");
  assert.equal(modelPricing({ pricing: { ...p, status: "unknown" } }, now).status, "unknown");
});
test("Rate ordering puts zero before charged rates and unknowns last", () => {
  const rows = [{ id: "unknown" }, { id: "costly", pricing: pricing(2) }, { id: "zero", pricing: pricing(0) }, { id: "low", pricing: pricing(.11) }];
  assert.deepEqual(rows.sort((a,b) => compareModelRates(a,b,now)).map((row) => row.id), ["zero", "low", "costly", "unknown"]);
});

test("rate freshness is five minutes; refreshes use an independent one-minute cadence", () => {
  assert.equal(modelPricing({ pricing: pricing(0) }, now + RATE_MAX_AGE_MS).status, "free");
  assert.equal(modelPricing({ pricing: pricing(0) }, now + RATE_MAX_AGE_MS + 1).status, "unknown");
  assert.equal(rateRefreshDue(0, now), true);
  assert.equal(rateRefreshDue(now, now + 59999), false);
  assert.equal(rateRefreshDue(now, now + 60000), true);
  assert.equal(rateRefreshDue(now, now + 100, true), true);
});

test("successful browser receipt cannot verify a stale hub cache or legacy snapshot", () => {
  const rows = [{ platform: "cn", id: "same", pricing: pricing(0) }, { platform: "global", id: "same", pricing: { ...pricing(0), observed_at: new Date(now + 1000).toISOString() } }];
  const verified = verifyRateObservations(rows, now + 1999);
  assert.equal(modelPricing(verified[0], now + 1999).status, "unknown");
  assert.equal(modelPricing(verified[1], now + 1999).status, "free");
  assert.equal(modelPricing({ pricing: pricing(0), pricing_sync_failed: true }, now).status, "unknown");
  assert.equal(rows[0].pricing_sync_failed, undefined);
  const snapshot = mergePricingSnapshot([{ platform: "cn", id: "same" }], { version: 1, models: [rows[0]] });
  assert.equal(modelPricing(verifyRateObservations(snapshot, now)[0], now).status, "unknown");
});

test("free-to-paid alerts preserve regional identity and do not recur for unchanged rates", () => {
  const before = [{ platform: "cn", id: "same", pricing: pricing(0) }, { platform: "global", id: "same", pricing: pricing(0) }];
  const after = [{ ...before[0], pricing: pricing(2) }, before[1]];
  const changes = pricingChanges(before, after, now);
  assert.equal(changes.length, 1);
  assert.equal(changes[0].routingId, "cn/same");
  assert.equal(changes[0].from.status, "free");
  assert.equal(changes[0].to.multiplier, 2);
  assert.deepEqual(pricingChanges(after, after, now), []);
  assert.deepEqual(pricingChanges(before, before.map(m => ({ ...m, pricing_sync_failed: true })), now), []);
  assert.equal(pricingChanges(before, [{ ...before[0], pricing: pricing(undefined, "dynamic") }], now)[0].to.status, "dynamic");
});
