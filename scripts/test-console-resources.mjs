import { test } from "node:test";
import assert from "node:assert/strict";
import { refreshResources, filterModelCatalog, compareModelIdentity, remainingCooldown, availabilityState, pageSnapshotTime } from "../internal/web/static/resources.js";

test("Matrix refresh and retry include account states; background polling refreshes the displayed matrix", () => {
  assert.deepEqual(refreshResources("models", true), ["models", "accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", true, true), ["accounts", "matrix"]);
  assert.deepEqual(refreshResources("models", false, true), ["status"]);
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
