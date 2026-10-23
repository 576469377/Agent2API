import { test } from "node:test";
import assert from "node:assert/strict";
import { refreshResources, filterModelCatalog, compareModelIdentity, remainingCooldown, availabilityState, modelAvailability, loadResources, createResourceLoader, pageSnapshotTime } from "../internal/web/static/resources.js";

const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

test("resource reads are shared while a manual refresh supersedes stale results and errors", async () => {
  const old = deferred(), fresh = deferred(), calls = [], shown = [], failed = [], settled = [];
  const loader = createResourceLoader({
    request: (key, signal) => { calls.push({ key, signal }); return calls.length === 1 ? old.promise : fresh.promise; },
    success: (key, value) => shown.push([key, value]), failure: (key, error) => failed.push([key, error]), settled: key => settled.push(key),
  });
  const first = loader.load(["models"]), duplicate = loader.load(["models", "models"]);
  assert.equal(calls.length, 1);
  const replacement = loader.load(["models"], { replace: true });
  assert.equal(calls.length, 2);
  assert.equal(calls[0].signal.aborted, true);
  fresh.resolve({ revision: 2, status: "paid" });
  await replacement;
  old.resolve({ revision: 1, status: "free" });
  await Promise.all([first, duplicate]);
  assert.deepEqual(shown, [["models", { revision: 2, status: "paid" }]]);
  assert.deepEqual(settled, ["models"]);
  assert.deepEqual(failed, []);
});

test("canceling old authorization does not publish a late 401 or clear the new request's busy state", async () => {
  const old = deferred(), fresh = deferred(), failed = [], settled = [], signals = [];
  let calls = 0;
  const loader = createResourceLoader({
    request: (_, signal) => { signals.push(signal); return calls++ ? fresh.promise : old.promise; },
    success: () => {}, failure: (_, error) => failed.push(error), settled: key => settled.push(key),
  });
  const first = loader.load(["accounts"]);
  loader.cancel();
  assert.equal(signals[0].aborted, true);
  assert.deepEqual(settled, ["accounts"]);
  const second = loader.load(["accounts"]);
  old.reject(Object.assign(new Error("obsolete key"), { status: 401 }));
  await first;
  assert.deepEqual(failed, []);
  assert.deepEqual(settled, ["accounts"]);
  fresh.resolve({ revision: 2 });
  await second;
  assert.deepEqual(settled, ["accounts", "accounts"]);
});

test("fresh rate verification uses the same resource slot and independent resources finish immediately", async () => {
  const old = deferred(), fresh = deferred(), shown = [], errors = [];
  const loader = createResourceLoader({
    request: key => key === "models" ? old.promise : Promise.resolve({ total: 42 }),
    success: (key, data) => shown.push([key, data]), failure: (_, error) => errors.push(error), settled: () => {},
  });
  const original = loader.load(["models", "metrics"]);
  const verified = loader.load(["models"], { replace: true, request: () => fresh.promise, success: (key, data) => shown.push([key, { ...data, verified: true }]) });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(shown, [["metrics", { total: 42 }]]);
  old.reject(new Error("old request canceled"));
  await original;
  assert.deepEqual(errors, []);
  fresh.resolve({ revision: 2 });
  await verified;
  assert.deepEqual(shown[1], ["models", { revision: 2, verified: true }]);
  await loader.load(["metrics"], { replace: true, request: async () => { throw new Error("current failure"); } });
  assert.equal(errors[0].message, "current failure");
});

test("Matrix refresh and retry include account states; background polling refreshes the displayed matrix", () => {
  assert.deepEqual(refreshResources("models", true), ["models", "accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", true, true), ["accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", false, true), ["accounts"]);
});

test("platform polling updates connection health and counts alongside its catalogue", () => {
  assert.deepEqual(refreshResources("platforms", false, true), ["platforms", "accounts", "models"]);
  assert.ok(refreshResources("accounts").includes("platforms"));
});

test("client setup loads local ZCode, cc-switch and pi availability before enabling sync", () => {
  assert.deepEqual(refreshResources("connection"), ["config", "models", "zcode", "cc-switch", "pi"]);
});
test("Local statistics render before a slow catalogue, including when the catalogue fails", async () => {
  let release;
  const slow = new Promise((_, reject) => { release = reject; });
  const shown = [], failed = [], done = [];
  const pending = loadResources(["models", "metrics"], {
    request: key => key === "models" ? slow : Promise.resolve({ total: 123 }),
    success: (key, data) => shown.push([key, data.total]),
    failure: key => failed.push(key), settled: key => done.push(key),
  });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(shown, [["metrics", 123]]);
  assert.deepEqual(done, ["metrics"]);
  release(new Error("upstream unavailable"));
  await pending;
  assert.deepEqual(failed, ["models"]);
  assert.deepEqual(done, ["metrics", "models"]);
});
test("Model scheduling separates regions, partial cooldowns, account cooldowns and credentials", () => {
  const received = 100000, now = 110000, model = { platform: "global", id: "same" };
  const cooling = { platform: "global", healthy: true, model_cooldowns: { same: 60 } };
  const domestic = { platform: "cn", healthy: true };
  const state = modelAvailability(model, [cooling, domestic], received, now);
  assert.equal(state.kind, "limited");
  assert.equal(state.ready, 0);
  assert.equal(state.nextReset, 160000);
  assert.equal(state.limited[0].remaining, 50);
  const ready = { platform: "global", healthy: true };
  assert.equal(modelAvailability(model, [cooling, ready], received, now).kind, "partial");
  assert.equal(modelAvailability(model, [cooling], received, 170000).kind, "awaiting_update");
  assert.equal(modelAvailability(model, [cooling, ready], received, 170000).kind, "ready");
  assert.equal(modelAvailability(model, [{ ...cooling, state: "blocked" }], received, now).kind, "unavailable");
  assert.equal(modelAvailability(model, [{ ...cooling, state: "disabled" }], received, now).kind, "unknown");
  assert.equal(modelAvailability(model, [{ ...ready, healthy: false, cooldown_secs: 80 }], received, now).kind, "limited");
  assert.equal(modelAvailability(model, [ready], undefined, now).kind, "unknown");
});
test("A redraw of old data cannot restart cooldown or assume the account recovered", () => {
  const received = 100000, now = received + 20000;
  assert.equal(remainingCooldown(60, received, now), 40);
  assert.equal(remainingCooldown(60, received, now + 10000), 30);
  assert.equal(remainingCooldown(60, received, now + 60000), 0);
  assert.deepEqual(availabilityState({ healthy: true }, true, 60, received, now), { kind: "cooling", remaining: 40 });
  assert.equal(availabilityState({ healthy: true }, true, 60, received, now + 60000).kind, "awaiting_update");
  assert.equal(remainingCooldown(60, undefined, now), 0);
});
test("Catalog support cannot override disabled, invalid or missing account state", () => {
  assert.equal(availabilityState({ state: "disabled", healthy: true }, true, 0, 1).kind, "disabled");
  assert.equal(availabilityState({ state: "blocked", healthy: false }, true, 0, 1).kind, "blocked");
  assert.equal(availabilityState({ healthy: false }, true, 0, 1).kind, "account_unavailable");
  assert.equal(availabilityState(undefined, true, 0, 1).kind, "unknown");
  assert.equal(availabilityState({ healthy: true }, false, 0, 1).kind, "unsupported");
});
test("Model search/capabilities preserve each platform's own facts", () => {
  const rows = [{ id: "same", platform: "cn", supports_images: false }, { id: "same", platform: "global", supports_images: true }, { id: "other", platform: "cn" }];
  assert.deepEqual(filterModelCatalog(rows, { capability: "supports_images", query: " SAME " }), [rows[1]]);
  assert.deepEqual(filterModelCatalog(rows, { platform: "cn", query: "cn/same" }), [rows[0]]);
  assert.deepEqual([...rows].sort((a,b) => compareModelIdentity(a,b,["cn","global"])), [rows[2], rows[0], rows[1]]);
});
test("A fresh gateway status must not make stale page data look newly refreshed", () => {
  const received = { status: 999, models: 100, accounts: 300, matrix: 250, metrics: 280 };
  assert.equal(pageSnapshotTime("models", received), 100);
  assert.equal(pageSnapshotTime("models", received, true), 250);
  assert.equal(pageSnapshotTime("overview", received), 280);
  assert.equal(pageSnapshotTime("models", { status: 999 }), null);
});
