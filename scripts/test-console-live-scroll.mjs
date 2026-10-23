import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { createResourceLoader } from "../internal/web/static/resources.js";
import { liveUsageView } from "../internal/web/static/usage-view.js";
import { sampleLive } from "../internal/web/static/usage.js";
import { escapeHTML, number, compact, timestamp, duration, countdown } from "../internal/web/static/core.js";

const source = readFileSync(new URL("../internal/web/static/app.js", import.meta.url), "utf8");
const snapshot = (count = 12) => ({ sampled_at: new Date().toISOString(), in_flight: count,
  active: Array.from({ length: count }, (_, id) => ({ id: String(id), model: "test", platform: "fixture", phase: "streaming", output_characters: id, started_at: new Date().toISOString() })) });
function harness() {
  const s = { page: "analytics", cache: { metrics: {} }, errors: {}, refreshEvery: 10, usageInsights: { calendarExpanded: false },
    live: { data: snapshot(), error: "", points: [], busy: false, lastAttempt: 0, receivedAt: Date.now() } };
  const nodes = new Map(), timers = [], focusCalls = [];
  const document = { hidden: false, documentElement: { scrollTop: 640 }, body: { scrollTop: 0, closest: () => null }, activeElement: null,
    querySelectorAll: () => [], querySelector: () => null };
  const element = id => ({ id, dataset: {}, closest: () => null,
    focus(options) { focusCalls.push(options); document.activeElement = this; if (!options?.preventScroll) document.documentElement.scrollTop = 0; } });
  document.activeElement = document.body;
  const outside = element("outsideControl");
  nodes.set(outside.id, outside);
  let list, details, toggle, refreshButton, placeholder;
  const body = element("liveUsageBody");
  body.querySelector = selector => ({ ".live-request-list": list, ".usage-live-placeholder": placeholder, '[data-action="refresh-live"]': refreshButton })[selector] || null;
  Object.defineProperty(body, "innerHTML", { set(html) {
    if ([list, toggle, refreshButton].includes(document.activeElement)) document.activeElement = document.body;
    for (const id of ["liveRequestDetails", "liveRequestToggle"]) nodes.delete(id);
    list = details = toggle = refreshButton = placeholder = null;
    if (html.includes('class="usage-live-placeholder"')) placeholder = element("");
    if (html.includes('data-action="refresh-live"')) refreshButton = element("");
    if (!html.includes('id="liveRequestDetails"')) return;
    details = { ...element("liveRequestDetails"), open: false };
    toggle = element("liveRequestToggle");
    nodes.set(details.id, details); nodes.set(toggle.id, toggle);
    const count = [...html.matchAll(/class="live-request-row"/g)].length;
    let top = 0, left = 0;
    list = element("");
    // A new DOM scroller starts at zero and the browser clamps assigned offsets.
    // A closed details element has no scrolling area until it is reopened.
    Object.defineProperties(list, {
      scrollTop: { get: () => top, set: value => { top = details.open ? Math.max(0, Math.min(value, count * 40 - 220)) : 0; } },
      scrollLeft: { get: () => left, set: value => { left = details.open ? Math.max(0, Math.min(value, 300)) : 0; } },
    });
  } });
  nodes.set(body.id, body);
  const page = { ...element("pageContent"), dataset: { page: "analytics" }, querySelector: () => null, querySelectorAll: () => [] };
  Object.defineProperty(page, "innerHTML", { set(html) { body.innerHTML = html; } });
  nodes.set(page.id, page);
  nodes.set("modal", { open: false }); nodes.set("detailDialog", { open: false });
  let nextData = s.live.data, nextError;
  const context = vm.createContext({ s, document, HTMLInputElement: class {}, createResourceLoader, liveUsageView, sampleLive,
    $: id => nodes.get(id), e: escapeHTML, L: zh => zh, n: number, compact, timestamp, duration, countdown,
    api: async () => { if (nextError) throw nextError; return nextData; }, setInterval: callback => timers.push(callback),
    pageKeys: { analytics: ["metrics"] }, views: { analytics: () => liveUsageView(s.live.data, s.live.error, s.live.points, vm.runInContext("liveUI()", context)) },
    updateAnalyticsTable() {},
  });
  const begin = source.indexOf("const liveLoader =");
  vm.runInContext(source.slice(begin, source.indexOf("setInterval(() => {\n  if (document.hidden", begin)), context);
  vm.runInContext(source.slice(source.indexOf("function draw("), source.indexOf("\nfunction metricCard(")), context);
  vm.runInContext("renderLive()", context);
  return { s, document, timers, focusCalls, outside, context,
    get list() { return list; }, get details() { return details; }, get toggle() { return toggle; }, get refreshButton() { return refreshButton; },
    position(top = 150, left = 27) { details.open = true; list.scrollTop = top; list.scrollLeft = left; },
    data(value) { nextData = value; nextError = undefined; }, error(value = new Error("fixture offline")) { nextError = value; },
    refresh: () => vm.runInContext("refreshLive(true)", context), draw: () => vm.runInContext("draw()", context),
  };
}

test("live polling preserves the active request list's vertical and horizontal scroll", async () => {
  const h = harness(); h.position();
  await h.refresh();
  assert.equal(h.details.open, true);
  assert.equal(h.list.scrollTop, 150);
  assert.equal(h.list.scrollLeft, 27);
  assert.equal(h.document.documentElement.scrollTop, 640);
});

test("full analytics redraws preserve the current list position between live updates", async () => {
  const h = harness(); h.position();
  h.draw();
  assert.equal(h.list.scrollTop, 150);
  assert.equal(h.list.scrollLeft, 27);
  h.position(190, 41);
  await h.refresh();
  h.draw();
  assert.equal(h.list.scrollTop, 190);
  assert.equal(h.list.scrollLeft, 41);
});

test("temporary live errors and empty active lists retain position for recovery", async () => {
  for (const absent of ["error", "empty"]) {
    const h = harness(); h.position();
    if (absent === "error") h.error(); else h.data(snapshot(0));
    await h.refresh();
    assert.equal(h.list, null);
    h.draw();
    h.document.activeElement = h.outside;
    h.data(snapshot());
    await h.refresh();
    assert.equal(h.details.open, true, absent);
    assert.equal(h.list.scrollTop, 150, absent);
    assert.equal(h.list.scrollLeft, 27, absent);
    assert.equal(h.document.activeElement, h.outside, absent);
  }
});

test("the paused stale-data timer retains the list viewport through full redraw and recovery", async () => {
  const h = harness(); h.position();
  h.s.refreshEvery = 0;
  h.s.live.receivedAt = Date.now() - 6000;
  h.timers[0]();
  assert.equal(h.list, null);
  h.draw();
  h.data(snapshot());
  await h.refresh();
  assert.equal(h.details.open, true);
  assert.equal(h.list.scrollTop, 150);
  assert.equal(h.list.scrollLeft, 27);
});

test("list and summary keyboard focus survive refresh and redraw without scrolling the page", async () => {
  for (const target of ["list", "toggle"]) {
    const h = harness(); h.position();
    h.document.activeElement = h[target];
    await h.refresh();
    assert.equal(h.document.activeElement, h[target], target);
    h.draw();
    assert.equal(h.document.activeElement, h[target], target);
    assert.ok(h.focusCalls.length > 0);
    assert.ok(h.focusCalls.every(options => options?.preventScroll === true));
    assert.equal(h.document.documentElement.scrollTop, 640);
  }
});

test("a shorter list clamps its saved offset naturally and does not jump back when it grows", async () => {
  const h = harness(); h.position();
  h.data(snapshot(6));
  await h.refresh();
  assert.equal(h.details.open, true);
  assert.equal(h.list.scrollTop, 20);
  h.data(snapshot());
  await h.refresh();
  assert.equal(h.list.scrollTop, 20);
});

test("closed request details remain closed through errors and recovery", async () => {
  const h = harness();
  assert.equal(h.details.open, false);
  await h.refresh();
  h.error(); await h.refresh();
  h.draw();
  h.data(snapshot()); await h.refresh();
  assert.equal(h.details.open, false);
  assert.equal(h.focusCalls.length, 0);
});

test("retry-button focus survives another error but does not move focus after recovery", async () => {
  const h = harness(); h.position();
  h.error(); await h.refresh();
  h.document.activeElement = h.refreshButton;
  await h.refresh();
  assert.equal(h.document.activeElement, h.refreshButton);
  h.draw();
  assert.equal(h.document.activeElement, h.refreshButton);
  h.document.activeElement = h.outside;
  h.data(snapshot()); await h.refresh();
  assert.equal(h.document.activeElement, h.outside);
  assert.equal(h.document.documentElement.scrollTop, 640);
  assert.ok(h.focusCalls.every(options => options?.preventScroll === true));
});
