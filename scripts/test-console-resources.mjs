import { test } from "node:test";
import assert from "node:assert/strict";
import { refreshResources, filterModelCatalog, compareModelIdentity, remainingCooldown, availabilityState, modelAvailability, loadResources, pageSnapshotTime } from "../internal/web/static/resources.js";

test("Matrix refresh and retry include account states; background polling refreshes the displayed matrix", () => {
  assert.deepEqual(refreshResources("models", true), ["models", "accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", true, true), ["accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", false, true), ["accounts"]);
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
