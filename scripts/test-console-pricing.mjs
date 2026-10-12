import { test } from "node:test";
import assert from "node:assert/strict";
import { mergePricingSnapshot, modelPricing, compareModelRates } from "../internal/web/static/pricing.js";

const now = Date.UTC(2026, 9, 12, 1, 0);
const pricing = (rate, status = "credits") => ({ status, credit_multiplier: rate, source: "upstream_config", observed_at: new Date(now).toISOString() });
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
test("Rate ordering puts zero before charged rates and unknowns last", () => {
  const rows = [{ id: "unknown" }, { id: "costly", pricing: pricing(2) }, { id: "zero", pricing: pricing(0) }, { id: "low", pricing: pricing(.11) }];
  assert.deepEqual(rows.sort((a,b) => compareModelRates(a,b,now)).map((row) => row.id), ["zero", "low", "costly", "unknown"]);
});
