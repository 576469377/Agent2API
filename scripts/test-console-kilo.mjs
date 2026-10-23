import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { platformSetup, connectionPlatforms, accountConnection, sanitizePlatforms } from "../internal/web/static/operations.js";
import { modelPricing, confirmedFreeModels } from "../internal/web/static/pricing.js";
import { clientPlatformOptions, selectedClientPlatforms, clientRequest, renderClientSetup } from "../internal/web/static/client-setup.js";

const now = Date.now();
const model = { platform: "kilo", id: "vendor/concrete:free", context_tokens: 1000000, max_output_tokens: 64000, supports_tools: true, pricing: { source: "kilo_models", status: "free", observed_at: new Date(now).toISOString() } };

test("Kilo onboarding uses anonymous verification independently of login and pool controls", () => {
  assert.deepEqual(platformSetup({ id: "kilo" }), { method: "anonymous", account_mode: "single" });
  assert.deepEqual(accountConnection({ platform: "kilo" }), { source: "anonymous", pooled: false });
  assert.equal(sanitizePlatforms([{ id: "kilo" }]).length, 0);
  assert.equal(connectionPlatforms([], [{ id: "kilo" }])[0].enabled, false);
  assert.equal(connectionPlatforms([{ id: "kilo" }], [])[0].enabled, true);
  const app = readFileSync(new URL("../internal/web/static/app.js", import.meta.url), "utf8");
  assert.match(app, /method === "desktop" \|\| method === "anonymous"\) connectPlatform\(platform, "check"\)/);
  assert.match(app, /检测免费通道/);
});

test("Kilo free pricing rejects expired, stale, failed, snapshot and foreign evidence", () => {
  assert.equal(modelPricing(model, now).status, "free");
  assert.deepEqual(confirmedFreeModels([model], now), [model]);
  for (const patch of [
    { platform: "openrouter" }, { pricing_sync_failed: true }, { pricing_from_snapshot: true },
    ...[{ status: "credits" }, { source: "guess" }, { observed_at: new Date(now - 301000).toISOString() }, { expires_at: new Date(now).toISOString() }, { expires_at: "later" }, { expires_at: false }].map(price => ({ pricing: { ...model.pricing, ...price } })),
  ]) assert.equal(confirmedFreeModels([{ ...model, ...patch }], now).length, 0, JSON.stringify(patch));
});

test("Kilo appears in both client selectors and emits only scoped local write actions", () => {
  const state = { busy: false, error: "", result: null, platforms: ["kilo"], apps: ["claude", "codex"] };
  assert.deepEqual(clientPlatformOptions([model]), [{ id: "kilo", name: "Kilo Free", count: 1 }]);
  assert.deepEqual(selectedClientPlatforms(state, [model]), ["kilo"]);
  assert.deepEqual(clientRequest("pi", state, [model]).body, { platforms: ["kilo"] });
  assert.deepEqual(clientRequest("cc-switch", state, [model]).body, { platforms: ["kilo"], apps: ["claude", "codex"] });
  const html = renderClientSetup({ "cc-switch": state, pi: state }, { "cc-switch": { available: true }, pi: { available: true } }, [model], cn => cn);
  assert.match(html, /Kilo Free/);
  assert.match(html, /配置到 cc-switch/);
  assert.match(html, /配置到 pi/);
});
