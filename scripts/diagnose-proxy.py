#!/usr/bin/env python3
"""Temporary loopback relay for structural request diagnostics. No raw request logging."""
import argparse
from collections import Counter
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
import threading
import time
from urllib.parse import urlsplit

HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
               "te", "trailer", "transfer-encoding", "upgrade"}
BODY_LIMIT = 32 * 1024 * 1024


def safe_enum(value, allowed):
    return value if isinstance(value, str) and value in allowed else "other"


def request_summary(raw):
    """Only aggregate structure and allowlisted scalar options; never copy user strings."""
    try:
        body = json.loads(raw)
    except (ValueError, UnicodeError, RecursionError):
        return {"body_bytes": len(raw), "valid_json": False}
    if not isinstance(body, dict):
        return {"body_bytes": len(raw), "valid_json_object": False}
    result = {"body_bytes": len(raw), "valid_json": True}
    for key in ["max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p", "top_k"]:
        value = body.get(key)
        if value is not None:
            result[key] = value if type(value) in (int, float) and abs(value) < 1e12 else "non_numeric"
    result["stream"] = body.get("stream") is True

    def count_images(items):
        if not isinstance(items, list):
            return 0
        total = 0
        for item in items:
            if not isinstance(item, dict):
                continue
            kind = item.get("type")
            if kind in ("image", "image_url", "input_image"):
                total += 1
            elif kind in ("tool_result", "message", None):
                total += count_images(item.get("content"))
            elif kind == "function_call_output":
                total += count_images(item.get("output"))
        return total

    result["image_count"] = count_images(body.get("messages")) + count_images(body.get("input"))
    thinking = body.get("thinking")
    if isinstance(thinking, dict):
        result["thinking_type"] = safe_enum(thinking.get("type"), {"enabled", "disabled", "adaptive"})
        budget = thinking.get("budget_tokens")
        if type(budget) is int and abs(budget) < 1e12:
            result["thinking_budget"] = budget
    effort = body.get("reasoning_effort")
    output = body.get("output_config")
    if effort is None and isinstance(output, dict):
        effort = output.get("effort")
    if effort is not None:
        result["reasoning_effort"] = safe_enum(effort, {"none", "minimal", "low", "medium", "high", "xhigh", "max"})
    choice = body.get("tool_choice")
    if choice is not None:
        value = choice.get("type") if isinstance(choice, dict) else choice
        result["tool_choice"] = safe_enum(value, {"auto", "none", "any", "required", "tool", "function"})
    result["stop_sequence_count"] = len(body.get("stop_sequences", [])) if isinstance(body.get("stop_sequences"), list) else 0
    tools = body.get("tools", [])
    tools = tools if isinstance(tools, list) else []
    result["tool_count"] = len(tools)
    result["tool_schema_bytes"] = sum(len(json.dumps(t, ensure_ascii=False).encode()) for t in tools)
    roles, blocks = Counter(), Counter()
    calls, answers = Counter(), Counter()
    pending = Counter()
    orphan_results = duplicate_results = missing_call_ids = mixed_user_messages = 0
    text_bytes = reasoning_bytes = tool_result_bytes = tool_argument_bytes = 0
    messages = body.get("messages", [])
    messages = messages if isinstance(messages, list) else []
    for message in messages:
        if not isinstance(message, dict):
            continue
        role = safe_enum(message.get("role"), {"system", "developer", "user", "assistant", "tool"})
        roles[role] += 1
        content = message.get("content", [])
        if isinstance(content, str):
            text_bytes += len(content.encode())
        reasoning = message.get("reasoning_content")
        if isinstance(reasoning, str):
            reasoning_bytes += len(reasoning.encode())
        items = content if isinstance(content, list) else []
        has_result = has_user_content = False
        tool_calls = message.get("tool_calls", [])
        tool_calls = tool_calls if isinstance(tool_calls, list) else []
        for call in tool_calls:
            if isinstance(call, dict):
                items = items + [{"type": "tool_use", "id": call.get("id"), "input": call.get("function", {}).get("arguments") if isinstance(call.get("function"), dict) else None}]
        if role == "tool":
            items = items + [{"type": "tool_result", "tool_use_id": message.get("tool_call_id"), "content": content}]
        for item in items:
            if not isinstance(item, dict):
                continue
            kind = safe_enum(item.get("type"), {"text", "thinking", "redacted_thinking", "image", "image_url", "tool_use", "tool_result"})
            blocks[kind] += 1
            if kind in {"text", "image", "image_url"}:
                has_user_content = True
            if kind == "text" and isinstance(item.get("text"), str):
                text_bytes += len(item["text"].encode())
            if kind == "thinking" and isinstance(item.get("thinking"), str):
                reasoning_bytes += len(item["thinking"].encode())
            if kind == "tool_use":
                call_id = item.get("id")
                if not isinstance(call_id, str) or not call_id:
                    missing_call_ids += 1
                else:
                    calls[call_id] += 1
                    pending[call_id] += 1
                tool_argument_bytes += len(json.dumps(item.get("input"), ensure_ascii=False).encode())
            if kind == "tool_result":
                has_result = True
                call_id = item.get("tool_use_id")
                if not isinstance(call_id, str) or not call_id:
                    missing_call_ids += 1
                else:
                    answers[call_id] += 1
                    if not pending[call_id]:
                        orphan_results += 1
                    else:
                        pending[call_id] -= 1
                    if answers[call_id] > calls[call_id] and calls[call_id]:
                        duplicate_results += 1
                tool_result_bytes += len(json.dumps(item.get("content"), ensure_ascii=False).encode())
        if role == "user" and has_result and has_user_content:
            mixed_user_messages += 1
    result.update(message_count=len(messages), roles=dict(roles), content_blocks=dict(blocks),
                  text_bytes=text_bytes, reasoning_bytes=reasoning_bytes,
                  tool_result_bytes=tool_result_bytes, tool_argument_bytes=tool_argument_bytes,
                  tool_call_count=blocks["tool_use"], tool_result_count=blocks["tool_result"],
                  duplicate_tool_calls=sum(max(0, count - 1) for count in calls.values()),
                  orphan_tool_results=orphan_results, unanswered_tool_calls=sum(pending.values()),
                  duplicate_tool_results=duplicate_results, missing_tool_ids=missing_call_ids,
                  mixed_tool_result_user_messages=mixed_user_messages)
    system = body.get("system")
    result["system_bytes"] = len(json.dumps(system, ensure_ascii=False).encode()) if system is not None else 0
    # Byte sizes are structural observations, not token estimates or a verdict on validity.
    return result


def parse_upstream(value):
    target = urlsplit(value)
    if target.scheme not in {"http", "https"} or not target.hostname or target.username or target.password or target.query or target.fragment or target.path not in {"", "/"}:
        raise ValueError("upstream must be an HTTP(S) loopback origin without credentials, path or query")
    if target.hostname != "localhost":
        try:
            if not ipaddress.ip_address(target.hostname).is_loopback:
                raise ValueError("upstream must be on this machine's loopback address")
        except ValueError as error:
            raise ValueError("upstream must be on this machine's loopback address") from error
    return target


def make_handler(target, emit):
    class Relay(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def do_GET(self):
            self.relay()

        def do_POST(self):
            self.relay()

        def relay(self):
            if not urlsplit(self.path).path.startswith("/v1/"):
                self.send_error(404, "Only /v1/ API routes are forwarded")
                return
            if self.headers.get("Transfer-Encoding"):
                self.send_error(400, "Use Content-Length for diagnostic requests")
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                self.send_error(400, "Invalid Content-Length")
                return
            if length < 0 or length > BODY_LIMIT:
                self.send_error(413, "Request exceeds the diagnostic relay body limit")
                return
            raw = self.rfile.read(length)
            if len(raw) != length:
                self.send_error(400, "Incomplete request body")
                return
            connection_type = http.client.HTTPSConnection if target.scheme == "https" else http.client.HTTPConnection
            connection = connection_type(target.hostname, target.port, timeout=180)
            connection_tokens = {v.strip().lower() for v in self.headers.get("Connection", "").split(",")}
            headers = {k: v for k, v in self.headers.items() if k.lower() not in HOP_HEADERS | connection_tokens | {"host"}}
            started = time.monotonic()
            responded = False
            try:
                # Exactly one upstream request. The original body and end-to-end headers are untouched.
                connection.request(self.command, self.path, body=raw if length else None, headers=headers)
                response = connection.getresponse()
                if response.status >= 400:
                    try:
                        summary = request_summary(raw)
                    except (ValueError, TypeError, UnicodeError, RecursionError):
                        summary = {"body_bytes": len(raw), "summary_unavailable": True}
                    emit({"status": response.status, "seconds": round(time.monotonic() - started, 3),
                          "protocol": "anthropic-messages" if urlsplit(self.path).path.endswith("/messages") else "other",
                          "request": summary})
                self.send_response(response.status)
                response_tokens = {v.strip().lower() for v in (response.getheader("Connection") or "").split(",")}
                for name, value in response.getheaders():
                    if name.lower() not in HOP_HEADERS | response_tokens:
                        self.send_header(name, value)
                self.send_header("Connection", "close")
                self.end_headers()
                responded = True
                while True:
                    chunk = response.read1(65536)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    self.wfile.flush()
            except (OSError, http.client.HTTPException):
                if not responded:
                    self.send_error(502, "Diagnostic relay could not connect to the running gateway")
            finally:
                connection.close()
                self.close_connection = True
    return Relay


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--upstream", default="http://127.0.0.1:8787")
    parser.add_argument("--port", type=int, default=8790)
    args = parser.parse_args()
    try:
        target = parse_upstream(args.upstream)
    except ValueError as error:
        parser.error(str(error))
    output_lock = threading.Lock()

    def emit(value):
        with output_lock:
            print(json.dumps(value, ensure_ascii=True), flush=True)

    server = ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(target, emit))
    print(f"Temporary diagnostic Base URL: http://127.0.0.1:{server.server_port}/v1", flush=True)
    print("Failed HTTP requests emit structure counts only. No prompts, tool arguments, identifiers, headers or credentials are logged. No retries.", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
