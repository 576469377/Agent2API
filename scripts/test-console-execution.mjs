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

test("upload status shows body progress and distinguishes a blocked final write", () => {
  const row = {phase:"uploading",request_bytes:2097152,uploaded_bytes:1048576,upload_started_at_ms:1000,upload_last_progress_at_ms:2000};
  assert.equal(liveRequestStatus(row,L,3000),"正在上传请求 · 1.0 MB / 2.0 MB");
  assert.equal(liveRequestStatus(row,L,9000),"正在上传请求 · 1.0 MB / 2.0 MB · 7秒无进展");
  assert.equal(liveRequestStatus({...row,uploaded_bytes:2097152},L,3000),"正在上传请求 · 2.0 MB / 2.0 MB · 等待发送完成");
  assert.equal(liveRequestStatus({...row,phase:"waiting_headers",upload_completed_at_ms:3000},L,3000),"等待上游响应");
});
