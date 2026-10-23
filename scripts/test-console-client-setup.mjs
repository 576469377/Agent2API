import { test } from "node:test";
import assert from "node:assert/strict";
import { clientModelHasBudget, clientPlatformOptions, selectedClientPlatforms, clientReady, clientRequest, renderClientSetup, runClientBatch } from "../internal/web/static/client-setup.js";

const now = new Date().toISOString();
const free = (platform, id) => ({ platform, id, supports_tools: true, context_tokens: 128000, max_output_tokens: 32000, pricing: { status: "free", source: platform === "openrouter" ? "openrouter_models" : "upstream_config", credit_multiplier: 0, observed_at: now } });
const models = [free("workbuddy", "a"), free("openrouter", "b")];
const state = () => ({ busy: false, result: null, error: "", platforms: null, apps: ["claude"] });
const L = (cn) => cn;

test("only verified Kiro models may use the documented client output budget when the upstream ceiling is unknown", () => {
  assert.equal(clientModelHasBudget({ platform: "kiro", context_tokens: 200000, max_output_tokens: 0 }), true);
  assert.equal(clientModelHasBudget({ platform: "cursor", context_tokens: 200000, max_output_tokens: 0 }), false);
  assert.equal(clientModelHasBudget({ platform: "kiro", context_tokens: 0, max_output_tokens: 0 }), false);
});

test("client selection groups platforms and emits fixed local actions without credentials or arbitrary paths", () => {
  assert.deepEqual(clientPlatformOptions(models).map(p => p.id), ["workbuddy", "openrouter"]);
  assert.deepEqual(clientPlatformOptions(models).map(p => p.count), [1, 1]);
  assert.equal(clientPlatformOptions([...models, { ...free("workbuddy", "no-tools"), supports_tools: false }])[0].count, 1);
  assert.deepEqual(selectedClientPlatforms(state(), models), ["workbuddy", "openrouter"]);
  const chosen = { ...state(), platforms: ["workbuddy", "unknown"], apps: ["claude", "codex"] };
  assert.deepEqual(clientRequest("cc-switch", chosen, models), { path: "/api/clients/cc-switch", body: { platforms: ["workbuddy"], apps: ["claude", "codex"] }, headers: { "X-Agent2API-Local-Action": "cc-switch-sync" } });
  assert.deepEqual(clientRequest("pi", chosen, models).body, { platforms: ["workbuddy"] });
  assert.throws(() => clientRequest("foreign", state(), models));
});

test("buttons require a usable client, platform selection and cc-switch app selection", () => {
  assert.equal(clientReady("pi", state(), { available: true }, models), true);
  for (const override of [{ busy: true }, { platforms: [] }]) assert.equal(clientReady("pi", { ...state(), ...override }, { available: true }, models), false);
  assert.equal(clientReady("cc-switch", { ...state(), apps: [] }, { available: true }, models), false);
  assert.equal(clientReady("pi", state(), { available: false }, models), false);
});

test("both clients render selectors, pending installation guidance and independent persistent outcomes", () => {
  const states = { "cc-switch": state(), pi: state() };
  states.pi.result = { status: "updated", model_count: 2, provider_count: 2, added: ["a", "b"], retained: 0, removed: [], model_ids: ["workbuddy/<script>"], backup_path: "C:/<private>/models.json" };
  states["cc-switch"].error = "数据库不可写 <unsafe>";
  const html = renderClientSetup(states, { pi: { available: true, exists: false, configured_entries: 0 }, "cc-switch": { available: true, configured_entries: 0 } }, models, L);
  assert.match(html, /同时配置 cc-switch 和 pi/);
  assert.match(html, /Claude Code/);
  assert.match(html, /Codex/);
  assert.match(html, /首次配置会创建 models.json/);
  assert.match(html, /数据库不可写 &lt;unsafe&gt;/);
  assert.match(html, /role="status"/);
  assert.match(html, /workbuddy\/&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});

test("batch configuration runs sequentially and retains partial success after a failure", async () => {
  const seen = [];
  const outcomes = await runClientBatch(["cc-switch", "pi"], async id => {
    seen.push(id);
    if (id === "cc-switch") throw new Error("locked");
    return { status: "updated" };
  });
  assert.deepEqual(seen, ["cc-switch", "pi"]);
  assert.equal(outcomes[0].error.message, "locked");
  assert.equal(outcomes[1].result.status, "updated");
});
