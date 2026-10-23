import { test } from "node:test";
import assert from "node:assert/strict";
import { checkinLabel, renderTraeCheckin } from "../internal/web/static/trae-checkin.js";

test("check-in status keeps unknown separate from an actual unsigned account", () => {
  assert.equal(checkinLabel({}), "签到状态待查询");
  assert.equal(checkinLabel({ checked_in: false, available: true }), "今日未签到");
  assert.equal(checkinLabel({ checked_in: true, available: false }), "今日已签到");
  assert.equal(checkinLabel({ available: false }), "签到未开放");
});

test("balance retains measured zero and fractions while missing data stays unknown", () => {
  const html = renderTraeCheckin({ enabled: true, time: "09:00", balance: { remaining: 0, used: 16.55, total: 500 }, can_manual: false });
  assert.match(html, /<dd>0<\/dd>/);
  assert.match(html, /<dd>16\.55<\/dd>/);
  assert.match(html, /每天 09:00 自动签到/);
  assert.match(renderTraeCheckin({ balance: {} }), /<dd>—<\/dd>/);
  assert.doesNotMatch(renderTraeCheckin({}), /trae-credit-metrics/);
});

test("manual action is a fallback and is disabled during a running check", () => {
  assert.match(renderTraeCheckin({ can_manual: false }), /data-action="trae-checkin-manual"[^>]* disabled/);
  assert.doesNotMatch(renderTraeCheckin({ can_manual: true }), /data-action="trae-checkin-manual"[^>]* disabled/);
  assert.match(renderTraeCheckin({ can_manual: true, busy: true }), /data-action="trae-checkin-manual"[^>]* disabled/);
  assert.match(renderTraeCheckin({ last_error: '<script>secret<\/script>' }), /&lt;script&gt;/);
});
