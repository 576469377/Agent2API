import { test } from "node:test";
import assert from "node:assert/strict";
import { readEvents, markdown, responseError } from "../internal/web/static/core.js";

test("HTML failure pages produce a readable status while JSON provider errors retain their message", async () => {
  const html = new Response('<!DOCTYPE HTML><html><body>503 failed<script>unsafe</script></body></html>', { status: 503, headers: { "Content-Type": "text/html" } });
  const message = await responseError(html, "zh");
  assert.ok(message.includes("503") && message.includes("检查连接"));
  assert.ok(!message.includes("<html") && !message.includes("script"));
  assert.equal(await responseError(new Response('{"error":{"message":"Invalid request parameters"}}', { status: 400 })), "Invalid request parameters");
  assert.equal(await responseError(new Response('plain provider error', { status: 400 })), "plain provider error");
});

test("SSE: split UTF-8 and CRLF, multiline data, usage-only frames and DONE", async () => {
  const source =
    ': heartbeat\r\ndata: {"choices":[{"delta":{"content":"你好"}}]}\r\n\r\ndata: {"choices":[],\r\ndata: "usage":{"completion_tokens":2}}\r\n\r\ndata: [DONE]\r\n\r\n';
  const bytes = new TextEncoder().encode(source);
  let cancelled = false;
  const stream = new ReadableStream({
    start(controller) {
      for (const byte of bytes) controller.enqueue(Uint8Array.of(byte));
    },
    cancel() {
      cancelled = true;
    },
  });
  const events = [];
  for await (const event of readEvents(stream)) events.push(event);
  assert.equal(events[0].choices[0].delta.content, "你好");
  assert.equal(events[1].usage.completion_tokens, 2);
  assert.deepEqual(events[2], { done: true });
  assert.equal(cancelled, true);
});
test("SSE: early cancellation releases the upstream reader", async () => {
  let cancelled = false;
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode('data: {"choices":[]}\n\n'));
    },
    cancel() {
      cancelled = true;
    },
  });
  for await (const event of readEvents(stream)) {
    assert.deepEqual(event, { choices: [] });
    break;
  }
  assert.equal(cancelled, true);
  assert.equal(stream.locked, false);
});
test("SSE: parses final EOF frame and rejects malformed JSON", async () => {
  const stream = (text) =>
    new ReadableStream({
      start(controller) {
        controller.enqueue(new TextEncoder().encode(text));
        controller.close();
      },
    });
  const events = [];
  for await (const event of readEvents(stream('data: {"choices":[]}')))
    events.push(event);
  assert.deepEqual(events, [{ choices: [] }]);
  await assert.rejects(async () => {
    for await (const event of readEvents(stream("data: broken\n\n")))
      void event;
  }, SyntaxError);
});
test("SSE: CR-only delimiters and a trailing CR dispatch every final frame", async () => {
  for (const source of ['data: {"usage":{"completion_tokens":2}}\n\r', 'data: {"choices":[]}\r\rdata: [DONE]\r\r']) {
    const bytes = new TextEncoder().encode(source);
    const stream = new ReadableStream({ start(controller) {
      for (const byte of bytes) controller.enqueue(Uint8Array.of(byte));
      controller.close();
    }});
    const events = [];
    for await (const event of readEvents(stream)) events.push(event);
    assert.deepEqual(events, source.includes('[DONE]') ? [{ choices: [] }, { done: true }] : [{ usage: { completion_tokens: 2 } }]);
    assert.equal(stream.locked, false);
  }
});
test("SSE: a CR-terminated DONE cancels without waiting for another network chunk", async () => {
  let controller, cancelled = false;
  const stream = new ReadableStream({
    start(value) { controller = value; controller.enqueue(new TextEncoder().encode('data: [DONE]\r\r')); },
    cancel() { cancelled = true; },
  });
  const events = [];
  const reading = (async () => { for await (const event of readEvents(stream)) events.push(event); })();
  await new Promise(resolve => setImmediate(resolve));
  try {
    assert.equal(cancelled, true);
    assert.deepEqual(events, [{ done: true }]);
  } finally {
    if (!cancelled) controller.close();
    await reading;
  }
});
test("Markdown: render code and lists without executing HTML or unsafe URLs", () => {
  const rendered = markdown(
    "<img src=x onerror=alert(1)>\n\n[bad](javascript:alert(1))\n\n```html\n<script>alert(1)</script>\n```\n\n- **one**\n- two\n\n[docs](https://example.com)",
  );
  assert.ok(!rendered.includes("<img"));
  assert.ok(!rendered.includes("<script>"));
  assert.ok(!rendered.includes('href="javascript:'));
  assert.ok(rendered.includes("<code>&lt;script&gt;"));
  assert.ok(rendered.includes("<ul><li><strong>one</strong></li>"));
  assert.ok(rendered.includes('rel="noopener noreferrer"'));
});
test("Markdown: tables, math fallback and code copy preserve escaped source", () => {
  const rendered = markdown(
    "| Name | Value |\n| --- | --- |\n| **a** | 1 |\n\n\\(x^2\\)\n\n```text\n<unsafe> $x^2$\n```",
  );
  assert.ok(rendered.includes('class="markdown-table"'));
  assert.ok(rendered.includes("<td><strong>a</strong></td>"));
  assert.ok(rendered.includes('data-tex="x^2"'));
  assert.ok(rendered.includes('data-code="&lt;unsafe&gt; $x^2$"'));
  assert.equal((rendered.match(/data-tex=/g) || []).length, 1);
});
