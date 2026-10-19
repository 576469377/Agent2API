import { test } from "node:test";
import assert from "node:assert/strict";
import { requestIssue, imageRecordSummary, activeFilterKeys, credentialState, accountConnection } from "../internal/web/static/operations.js";

test("generic provider 400 wording must not become an image-overflow diagnosis", () => {
  for (const error of ["Invalid request parameters", "http_400: the request parameters were rejected by the model provider"]) {
    assert.equal(requestIssue({ ok: false, status: 400, error, image_count: 61, max_images: 60 }).kind, "unknown");
  }
  assert.equal(requestIssue({ ok: false, status: 400, error: "image_count_exceeded: known guard" }).kind, "images");
  assert.equal(requestIssue({ ok: false, status: 400, error: "image_cleanup_unavailable: mismatched structure" }).kind, "image_structure");
  assert.equal(requestIssue({ ok: true, status: 200 }), null);
});

test("image inspection preserves unknown versus measured zero on older gateways", () => {
  assert.deepEqual(imageRecordSummary({}), { received: null, forwarded: null, removed: 0, limit: null });
  assert.deepEqual(imageRecordSummary({ image_count: 0, forwarded_image_count: 0, max_images: 0 }), { received: 0, forwarded: 0, removed: 0, limit: 0 });
  assert.deepEqual(imageRecordSummary({ image_count: 61, forwarded_image_count: 60, removed_image_count: 1, max_images: 60 }), { received: 61, forwarded: 60, removed: 1, limit: 60 });
});

test("filter feedback covers hidden advanced filters without including sorting preferences", () => {
  assert.deepEqual(activeFilterKeys({ query: " ", platform: "all", account: '["global","same-label"]', model: "hy3", sort: "time" }, ["query", "platform", "account", "model"]), ["account", "model"]);
  assert.deepEqual(activeFilterKeys({ status: "attention", query: "demo" }, ["status", "query", "platform"]), ["status", "query"]);
  assert.deepEqual(activeFilterKeys({ query: "all", platform: "all" }, ["query", "platform"]), ["query"]);
});

test("an upstream authentication failure overrides stale signed-in identity metadata", () => {
  assert.equal(credentialState({ identity: { nickname: "demo" }, reason: "unauthorized", healthy: true }), "sign_in");
  assert.equal(credentialState({ identity: { refresh_expired: true } }), "sign_in");
  assert.equal(credentialState({ identity: { needs_refresh: true } }), "refresh");
  assert.equal(credentialState({}), "unknown");
});

test("direct API-key and desktop integrations never expose WorkBuddy pool controls", () => {
  assert.deepEqual(accountConnection({ platform: "openrouter" }), { source: "api_key", pooled: false });
  assert.deepEqual(accountConnection({ platform: "cursor" }), { source: "desktop", pooled: false });
  assert.deepEqual(accountConnection({ platform: "future" }), { source: "unknown", pooled: false });
  assert.deepEqual(accountConnection(null), { source: "unknown", pooled: false });
});

test("WorkBuddy retains pool controls and the actual credential source", () => {
  assert.deepEqual(accountConnection({ platform: "workbuddy", identity: { source: "desktop", credential_path: "/desktop.json" } }), { source: "desktop", pooled: true });
  assert.deepEqual(accountConnection({ platform: "workbuddy-global", identity: { source: "file", credential_path: "/pool.json" } }), { source: "file", pooled: true });
  assert.deepEqual(accountConnection({ platform: "workbuddy" }), { source: "unknown", pooled: true });
});
