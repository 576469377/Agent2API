import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { readEvents, responseError, escapeHTML, number, duration, markdown } from "../internal/web/static/core.js";

const source = readFileSync(new URL("../internal/web/static/app.js", import.meta.url), "utf8");
const model = (id = "workbuddy/hy3", max = 64000) => ({ routing_id: id, platform: id.split("/")[0], max_output_tokens: max });
const choice = (delta = {}, finish_reason = null) => ({ choices: [{ index: 0, delta, finish_reason }] });
const stream = events => events.map(event => `data: ${event === "[DONE]" ? event : JSON.stringify(event)}\n\n`).join("");
const finished = [choice({ content: "Answer" }, "stop"), "[DONE]"];
function defaults() {
  const context = vm.createContext({ saved: (_, fallback = "") => fallback });
  vm.runInContext(source.slice(source.indexOf("const s = {"), source.indexOf("\nconst L =")), context);
  return { ...vm.runInContext("s.chat.params", context) };
}
function harness({ models = [model()], params = {}, events = finished, respond } = {}) {
  const s = { key: "", chat: { model: models[0].routing_id, draft: "test", controller: null, messages: [], params: { ...defaults(), ...params } } };
  const calls = [], fields = new Map();
  const host = { scrollHeight: 100, scrollTop: 0, clientHeight: 100 };
  const nodes = { ".message-body": {}, ".message-reasoning": { querySelector: () => ({}) }, ".message-error": {}, ".message-tools": {}, ".message-footer": {} };
  let modalHTML = "";
  const context = vm.createContext({ s, performance, AbortController, readEvents, responseError,
    models: () => models, confirmedFreeModels: rows => rows, routeId: row => row.routing_id, authHeaders: () => ({}),
    L: zh => zh, e: escapeHTML, n: number, duration, markdown, renderMath() {}, iconButton: () => "",
    renderMessages() {}, syncChatControls() {}, chrome() {}, button: () => "", options: () => "",
    modal: (_, html) => { modalHTML = html; }, toast() {},
    $: id => id === "chatMessages" ? host : id.startsWith("message-") ? { querySelector: selector => nodes[selector] }
      : fields.get(id) || { focus() {}, close() {} },
    fetch: async (_, options) => {
      calls.push(JSON.parse(options.body));
      return respond ? respond(options) : new Response(stream(events), { headers: { "Content-Type": "text/event-stream" } });
    },
  });
  vm.runInContext(source.slice(source.indexOf("function updateMessage("), source.indexOf("\nfunction syncChatControls()")), context);
  vm.runInContext(source.slice(source.indexOf("async function sendChat("), source.indexOf("\nfunction exportRequests()")), context);
  return { s, context, calls, nodes, fields, get modalHTML() { return modalHTML; },
    send: () => vm.runInContext("sendChat()", context), result: () => s.chat.messages.at(-1) };
}

test("chat auto budget follows the selected model and changes with the selection", async () => {
  assert.equal(defaults().max_tokens, null);
  const h = harness({ models: [model(), model("workbuddy/second", 32000)] });
  await h.send();
  assert.equal(h.calls[0].max_tokens, 64000);
  h.s.chat.model = "workbuddy/second";
  h.s.chat.draft = "next";
  await h.send();
  assert.equal(h.calls[1].max_tokens, 32000);
});

test("chat omits an unknown auto budget and preserves an explicit manual limit", async () => {
  const automatic = harness({ models: [model("workbuddy/unknown", 0)], params: { max_tokens: null } });
  await automatic.send();
  assert.equal(Object.hasOwn(automatic.calls[0], "max_tokens"), false);
  const manual = harness({ params: { max_tokens: 4096 } });
  await manual.send();
  assert.equal(manual.calls[0].max_tokens, 4096);
});

test("OpenRouter auto budgets use the shared 8192 cap while manual budgets remain explicit", async () => {
  for (const [limit, manual, expected] of [[460800, null, 8192], [4096, null, 4096], [460800, 16384, 16384], [0, null, undefined]]) {
    const h = harness({ models: [model("openrouter/free", limit)], params: { max_tokens: manual } });
    await h.send();
    assert.equal(h.calls[0].max_tokens, expected);
    if (expected === undefined) assert.equal(Object.hasOwn(h.calls[0], "max_tokens"), false);
  }
});

test("OpenRouter settings show the actual automatic budget without calling it a model capacity", () => {
  for (const [limit, displayed] of [[460800, "8,192"], [4096, "4,096"]]) {
    const h = harness({ models: [model("openrouter/free", limit)] });
    vm.runInContext("chatSettings()", h.context);
    const input = h.modalHTML.match(/<input id="chatMaxTokens"[^>]*>/)?.[0];
    assert.ok(input?.includes(displayed));
    assert.match(input, /自动/);
    assert.doesNotMatch(input, /模型上限/);
    assert.doesNotMatch(h.modalHTML, /留空自动选择当前模型的上限/);
  }
});

test("TRAE and Kiro continue omitting parameters controlled by the provider", async () => {
  for (const platform of ["trae-cn", "kiro"]) {
    const h = harness({ models: [model(`${platform}/free`)], params: { max_tokens: 8192, temperature: 0.5, reasoning_effort: "high" } });
    await h.send();
    for (const key of ["max_tokens", "temperature", "reasoning_effort"]) assert.equal(Object.hasOwn(h.calls[0], key), false, key);
  }
});

test("chat retains reasoning, final text and the normal stop reason", async () => {
  const h = harness({ events: [choice({ reasoning_content: "Thinking" }), choice({ content: "Answer" }), choice({}, "stop"), "[DONE]"] });
  await h.send();
  assert.equal(h.result().reasoning, "Thinking");
  assert.equal(h.result().content, "Answer");
  assert.equal(h.result().finishReason, "stop");
  assert.equal(h.result().error, undefined);
  assert.match(h.nodes[".message-footer"].innerHTML, /已完成/);
});

test("length-truncated thinking is incomplete and consumes trailing usage frames", async () => {
  const h = harness({ events: [choice({ reasoning_content: "Thinking" }), choice({}, "length"),
    { choices: [], usage: { prompt_tokens: 169, completion_tokens: 4096, completion_tokens_details: { reasoning_tokens: 4096 } } }, "[DONE]"] });
  await h.send();
  assert.equal(h.result().finishReason, "length");
  assert.equal(h.result().content, "");
  assert.equal(h.result().reasoning, "Thinking");
  assert.match(h.result().error, /输出上限/);
  assert.equal(h.result().usage.completion_tokens, 4096);
  assert.doesNotMatch(h.nodes[".message-footer"].innerHTML, /已完成/);
  assert.match(h.nodes[".message-footer"].innerHTML, /输入.*169.*输出.*4,096/);
});

test("content filtering is incomplete while tool calls remain a valid completion", async () => {
  const filtered = harness({ events: [choice({}, "content_filter"), "[DONE]"] });
  await filtered.send();
  assert.match(filtered.result().error, /过滤/);
  assert.doesNotMatch(filtered.nodes[".message-footer"].innerHTML, /已完成/);
  const tool = harness({ events: [choice({ tool_calls: [{ index: 0, id: "call-test", function: { name: "read", arguments: "{}" } }] }, "tool_calls"), "[DONE]"] });
  await tool.send();
  assert.equal(tool.result().finishReason, "tool_calls");
  assert.equal(tool.result().error, undefined);
  assert.equal(tool.result().tools[0].function.name, "read");
  assert.match(tool.nodes[".message-footer"].innerHTML, /已完成/);
});

test("a normal stop containing only reasoning does not claim a finished answer", async () => {
  const h = harness({ events: [choice({ reasoning_content: "Thinking" }, "stop"), "[DONE]"] });
  await h.send();
  assert.equal(h.result().content, "");
  assert.match(h.result().error, /未生成正文/);
  assert.doesNotMatch(h.nodes[".message-footer"].innerHTML, /已完成/);
});

test("EOF and upstream errors preserve partial output without a completed footer", async () => {
  for (const [events, error] of [
    [[choice({ content: "Partial" })], /连接提前结束/],
    [[choice({ content: "Partial" }, "length"), { error: { message: "upstream test error" } }], /upstream test error/],
  ]) {
    const h = harness({ events });
    await h.send();
    assert.equal(h.result().content, "Partial");
    assert.match(h.result().error, error);
    assert.doesNotMatch(h.nodes[".message-footer"].innerHTML, /已完成/);
  }
});

test("stopping the request takes precedence over a stream error", async () => {
  const h = harness({ respond: () => { h.s.chat.controller.abort(); throw new Error("aborted fixture"); } });
  await h.send();
  assert.equal(h.result().stopped, true);
  assert.equal(h.result().error, undefined);
  assert.match(h.nodes[".message-footer"].innerHTML, /已停止/);
});

test("chat settings show automatic model capacity and accept a blank limit", () => {
  const h = harness({ params: { max_tokens: null } });
  vm.runInContext("chatSettings()", h.context);
  const input = h.modalHTML.match(/<input id="chatMaxTokens"[^>]*>/)?.[0];
  assert.ok(input);
  assert.match(input, /value=""/);
  assert.doesNotMatch(input, /required/);
  assert.match(h.modalHTML, /64,000/);
  assert.match(h.modalHTML, /思考.*正文/);
  for (const [value, expected] of [["", null], ["8192", 8192]]) {
    h.fields.set("chatTemperature", { value: "0.7" });
    h.fields.set("chatMaxTokens", { value });
    h.fields.set("chatEffort", { value: "" });
    h.fields.set("chatSystem", { value: "" });
    h.context.event = { target: { id: "chatSettingsForm" }, preventDefault() {} };
    const begin = source.indexOf('  if (event.target.id === "chatSettingsForm")');
    vm.runInContext(source.slice(begin, source.indexOf("\n});", begin)), h.context);
    assert.equal(h.s.chat.params.max_tokens, expected);
  }
});
