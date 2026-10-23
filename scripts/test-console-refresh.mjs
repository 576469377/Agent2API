import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { createResourceLoader } from "../internal/web/static/resources.js";
import { responseError } from "../internal/web/static/core.js";
import { sampleLive } from "../internal/web/static/usage.js";
import { verifyRateObservations, pricingChanges, modelPricing } from "../internal/web/static/pricing.js";

const source = readFileSync(new URL("../internal/web/static/app.js", import.meta.url), "utf8");
const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes; }); return { promise, resolve }; };
const response = value => new Response(JSON.stringify(value), { headers: { "content-type": "application/json" } });
function harness(fetch) {
  const s = { cache: {}, errors: {}, receivedAt: {}, busy: new Set(), page: "overview", refreshEvery: 10,
    live: { data: null, points: [], busy: false, error: "", lastAttempt: 0 },
    rateSync: { busy: false, lastAttempt: 0, confirmed: new Map(), changes: [], error: "" } };
  const context = vm.createContext({ s, fetch, AbortSignal, createResourceLoader, responseError, sampleLive,
    endpoints: { models: "/api/models", status: "/api/status", accounts: "/api/accounts", platforms: "/api/platforms" },
    authHeaders: () => ({}), $: () => ({ classList: { toggle() {} }, open: false }),
    document: { hidden: false }, chrome() {}, draw() {}, refreshLive() {}, currentResources: () => [],
    models: () => s.cache.models?.models || [], L: zh => zh, platformName: id => id,
    verifyRateObservations, pricingChanges, modelPricing,
    rateRefreshDue: (_, __, manual) => manual,
  });
  vm.runInContext(source.slice(source.indexOf("async function api("), source.indexOf("\nfunction chrome()")), context);
  return { s, context };
}

test("actual console refresh keeps fresh verified prices when the older catalogue settles last", async () => {
  const old = deferred(), calls = [];
  const { s, context } = harness((url, options) => {
    calls.push({ url, signal: options.signal });
    if (url === "/api/models") return old.promise;
    return Promise.resolve(response(url === "/api/config" ? { applied: { refresh_models: 1 } } : url.includes("refresh=1") ? { models: [], revision: 2 } : {}));
  });
  const background = vm.runInContext('refresh(["models"])', context);
  await vm.runInContext("syncRates(true)", context);
  assert.equal(calls.find(call => call.url === "/api/models").signal.aborted, true);
  assert.equal(s.cache.models.revision, 2);
  old.resolve(response({ models: [], revision: 1 }));
  await background;
  assert.equal(s.cache.models.revision, 2);
  assert.equal(s.busy.size, 0);
  assert.equal(s.rateSync.busy, false);
});

const rateModel = (platform, observedAt) => ({ platform, id: "test-model", pricing: {
  source: "upstream_config", status: "free", credit_multiplier: 0, observed_at: new Date(observedAt).toISOString(),
} });
function publishModels(context, data, checked = false) {
  context.catalogue = data;
  vm.runInContext(`publishResource("models", catalogue, ${checked})`, context);
}

test("a recovered catalogue clears the previous rate failure on an ordinary read", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  const now = Date.now();
  s.rateSync.lastAttempt = now;
  publishModels(context, { models: [rateModel("workbuddy-global", now - 60000)], catalog_status: [{ platform: "workbuddy-global", status: "error" }] }, true);
  assert.match(s.rateSync.error, /workbuddy-global/);
  publishModels(context, { models: [rateModel("workbuddy-global", now + 1000)], catalog_status: [{ platform: "workbuddy-global", status: "ready" }] });
  assert.equal(s.rateSync.error, "");
  assert.equal(modelPricing(s.cache.models.models[0]).status, "free");
});

test("partial recovery keeps failed platforms and never restores their cached free rates", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  const now = Date.now();
  s.rateSync.lastAttempt = now;
  publishModels(context, { models: [rateModel("workbuddy-global", now - 60000), rateModel("other", now - 60000)],
    catalog_status: [{ platform: "workbuddy-global", status: "error" }, { platform: "other", status: "error" }] }, true);
  publishModels(context, { models: [rateModel("workbuddy-global", now + 1000), rateModel("other", now - 60000)],
    catalog_status: [{ platform: "workbuddy-global", status: "ready" }, { platform: "other", status: "error" }] });
  assert.doesNotMatch(s.rateSync.error, /workbuddy-global/);
  assert.match(s.rateSync.error, /other/);
  assert.equal(modelPricing(s.cache.models.models[0]).status, "free");
  assert.equal(modelPricing(s.cache.models.models[1]).status, "unknown");
});

test("a failed platform without any cached models still produces a rate failure", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  publishModels(context, { models: [], catalog_status: [{ platform: "workbuddy-global", status: "error" }] }, true);
  assert.match(s.rateSync.error, /workbuddy-global/);
});

test("an ordinary read cannot revalidate observations from before the failed check", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  const now = Date.now();
  s.rateSync.lastAttempt = now;
  const catalogue = () => ({ models: [rateModel("workbuddy-global", now - 60000)], catalog_status: [{ platform: "workbuddy-global", status: "ready" }] });
  publishModels(context, catalogue(), true);
  publishModels(context, catalogue());
  assert.match(s.rateSync.error, /workbuddy-global/);
  assert.equal(modelPricing(s.cache.models.models[0]).status, "unknown");
});

test("a legacy pricing snapshot does not clear failures from other platforms", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  s.rateSync.lastAttempt = Date.now();
  publishModels(context, { models: [{ ...rateModel("legacy", Date.now()), pricing_from_snapshot: true }], pricing_snapshot: true,
    catalog_status: [{ platform: "workbuddy-global", status: "error" }] });
  assert.match(s.rateSync.error, /历史费率快照/);
  assert.match(s.rateSync.error, /workbuddy-global/);
  assert.equal(modelPricing(s.cache.models.models[0]).status, "unknown");
});

test("fresh metadata without a fixed rate stays unknown without inventing a refresh failure", () => {
  const { s, context } = harness(() => { throw new Error("unexpected fetch"); });
  s.rateSync.lastAttempt = Date.now();
  const model = rateModel("workbuddy-global", Date.now() + 1000);
  delete model.pricing.credit_multiplier;
  model.pricing.status = "unknown";
  publishModels(context, { models: [model], catalog_status: [{ platform: "workbuddy-global", status: "ready" }] }, true);
  assert.equal(s.rateSync.error, "");
  assert.equal(modelPricing(s.cache.models.models[0]).status, "unknown");
});

test("rate refresh uses the catalogue refresh contract without a separate config mutation", async () => {
  const calls = [];
  const { context } = harness((url, options) => {
    calls.push({ url, method: options.method });
    return Promise.resolve(response(url === "/api/config" ? { applied: { refresh_models: 1 } } : { models: [] }));
  });
  await vm.runInContext("syncRates(true)", context);
  assert.equal(calls.filter(call => call.url.startsWith("/api/models")).length, 1);
  assert.equal(calls.find(call => call.url.startsWith("/api/models")).url, "/api/models?refresh=1");
  assert.equal(calls.some(call => call.method !== "GET"), false);
});

test("an older backend ignoring refresh does not verify its cached free model", async () => {
  const old = rateModel("workbuddy-global", Date.now() - 60000);
  const { s, context } = harness(url => Promise.resolve(response(url.startsWith("/api/models") ? { models: [old] } : {})));
  await vm.runInContext("syncRates(true)", context);
  assert.match(s.rateSync.error, /workbuddy-global/);
  assert.equal(modelPricing(s.cache.models.models[0]).status, "unknown");
});

test("actual post-action refresh replaces an in-flight pre-action account snapshot", async () => {
  const old = deferred();
  let accountCalls = 0;
  const { s, context } = harness(url => url === "/api/accounts" && ++accountCalls === 1 ? old.promise : Promise.resolve(response({ revision: 2 })));
  const beforeAction = vm.runInContext('refresh(["accounts"])', context);
  await vm.runInContext('refresh(["accounts"], false, true)', context);
  assert.equal(accountCalls, 2);
  old.resolve(response({ revision: 1 }));
  await beforeAction;
  assert.equal(s.cache.accounts.revision, 2);
});

test("actual live polling releases canceled page reads and ignores their late samples", async () => {
  const old = deferred(), calls = [];
  const { s, context } = harness((url, options) => {
    calls.push({ url, signal: options.signal });
    return calls.length === 1 ? old.promise : Promise.resolve(response({ in_flight: 0, active: [], sampled_at: "2026-10-24T12:00:02Z" }));
  });
  s.page = "analytics";
  vm.runInContext(source.slice(source.indexOf("const liveLoader ="), source.indexOf("\nfunction liveUI()")), context);
  vm.runInContext("renderLive = () => {}", context);
  const leaving = vm.runInContext("refreshLive(true)", context);
  vm.runInContext("liveLoader.cancel()", context);
  assert.equal(s.live.busy, false);
  assert.equal(calls[0].signal.aborted, true);
  await vm.runInContext("refreshLive(true)", context);
  old.resolve(response({ in_flight: 9, active: [], sampled_at: "2026-10-24T12:00:01Z" }));
  await leaving;
  assert.equal(s.live.data.in_flight, 0);
  assert.equal(s.live.points.length, 1);
  assert.equal(s.live.busy, false);
});

test("an old chat 401 cannot invalidate a newly verified connection", async () => {
  for (const changed of [true, false]) {
    const pending = deferred(), { s, context } = harness(() => pending.promise);
    s.key = "test-old-key";
    s.authRequired = false;
    s.chat = { model: "test/free", draft: "test message", controller: null, messages: [], params: {} };
    s.cache.models = { models: [{ routing_id: "test/free" }] };
    Object.assign(context, { performance, AbortController, confirmedFreeModels: models => models,
      routeId: model => model.routing_id, renderMessages() {}, syncChatControls() {}, updateMessage() {},
      $: () => ({ focus() {} }),
    });
    vm.runInContext(source.slice(source.indexOf("async function sendChat("), source.indexOf("\nfunction chatSettings()")), context);
    const request = vm.runInContext("sendChat()", context);
    if (changed) s.key = "test-verified-key";
    pending.resolve(new Response('{"error":{"message":"old request unauthorized"}}', { status: 401 }));
    await request;
    assert.equal(s.authRequired, !changed);
    assert.equal(s.chat.messages.at(-1).error, "old request unauthorized");
    assert.equal(s.chat.controller, null);
  }
});
