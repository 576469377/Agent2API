import test from "node:test";
import assert from "node:assert/strict";
import { requestKey, filterRequests } from "../internal/web/static/monitor.js";
import { liveRequestStatus } from "../internal/web/static/usage-view.js";

const L = (cn) => cn;
test("live status distinguishes queued, connecting, and accepted requests with no output", () => {
  assert.equal(liveRequestStatus({phase:"queued",queue_ms:2500},L,0),"排队等待 · 2.5s");
  assert.equal(liveRequestStatus({phase:"connecting"},L,0),"正在连接上游");
  assert.equal(liveRequestStatus({phase:"waiting_output"},L,0),"等待首段输出");
  assert.equal(liveRequestStatus({phase:"streaming",last_output_at:new Date(1000).toISOString()},L,5000),"等待后续输出");
  assert.equal(liveRequestStatus({},L,0),"等待首段输出");
});

test("request drill-down and search use the server request ID without breaking legacy history", () => {
  const row={request_id:"stable-request-id",time:new Date().toISOString(),model:"m",ok:true};
  assert.equal(requestKey(row),requestKey({...row,duration_ms:1000,platform:"changed"}));
  assert.deepEqual(filterRequests([row],{query:"stable-request"}),[row]);
  assert.notEqual(requestKey({time:"first",model:"m"}),requestKey({time:"second",model:"m"}));
});
