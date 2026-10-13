import importlib.util
import json
import pathlib
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

spec = importlib.util.spec_from_file_location("diagnose_proxy", pathlib.Path(__file__).with_name("diagnose-proxy.py"))
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)


class DiagnosticTest(unittest.TestCase):
    def test_summary_identifies_tool_history_without_disclosing_content(self):
        secret = "private-conversation-sentinel"
        body = {"model": secret, "system": secret, "max_tokens": 64000,
                "thinking": {"type": "adaptive"}, "output_config": {"effort": "high"},
                "tools": [{"name": secret, "input_schema": {"type": "object"}}],
                "messages": [
                    {"role": "user", "content": secret},
                    {"role": "assistant", "content": [{"type": "thinking", "thinking": secret, "signature": secret},
                        {"type": "tool_use", "id": "call-1", "name": secret, "input": {"text": secret}},
                        {"type": "tool_use", "id": "call-2", "name": secret, "input": {}}]},
                    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call-1", "content": [{"type":"image","source":{"data":secret}},{"type":"text","text":secret}]},
                        {"type": "tool_result", "tool_use_id": "call-1", "content": secret},
                        {"type": "tool_result", "tool_use_id": "call-orphan", "content": secret},
                        {"type": "text", "text": secret}, {"type": "image", "source": {"data": secret}}]}]}
        summary = relay.request_summary(json.dumps(body).encode())
        self.assertEqual(summary["tool_call_count"], 2)
        self.assertEqual(summary["image_count"], 2)
        self.assertEqual(summary["unanswered_tool_calls"], 1)
        self.assertEqual(summary["orphan_tool_results"], 2)
        self.assertEqual(summary["duplicate_tool_results"], 1)
        self.assertEqual(summary["mixed_tool_result_user_messages"], 1)
        self.assertEqual(summary["thinking_type"], "adaptive")
        self.assertEqual(summary["reasoning_effort"], "high")
        self.assertNotIn(secret, json.dumps(summary))
        self.assertNotIn("call-", json.dumps(summary))

    def test_unknown_scalars_and_roles_are_never_echoed(self):
        marker = "private-marker"
        summary = relay.request_summary(json.dumps({"temperature": marker, "tool_choice": marker,
            "thinking": {"type": marker}, "messages": [{"role": marker, "content": [{"type": marker}]}]}).encode())
        self.assertNotIn(marker, json.dumps(summary))
        self.assertEqual(summary["roles"], {"other": 1})
        self.assertEqual(relay.request_summary(b'[]')["valid_json_object"], False)
        self.assertEqual(relay.request_summary(b'not json')["valid_json"], False)

    def test_only_loopback_origins_are_accepted(self):
        for value in ["http://127.0.0.1:8787", "https://localhost", "http://[::1]:8787"]:
            relay.parse_upstream(value)
        for value in ["http://example.com", "file:///tmp", "http://user:pass@localhost", "http://localhost/v1", "http://localhost?key=test", "http://localhost#fragment"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                relay.parse_upstream(value)

    def test_failed_request_is_forwarded_once_and_streaming_is_not_buffered(self):
        requests, emitted = [], []
        first_sent, release_second = threading.Event(), threading.Event()

        class Upstream(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((raw, self.headers.get("x-api-key"), self.headers.get("anthropic-version")))
                body = json.loads(raw)
                self.send_response(400 if body.get("fail") else 200)
                self.send_header("Content-Type", "application/json" if body.get("fail") else "text/event-stream")
                self.end_headers()
                if body.get("fail"):
                    self.wfile.write(b'{"error":"rejected"}')
                else:
                    self.wfile.write(b'data: first\n\n')
                    self.wfile.flush()
                    first_sent.set()
                    release_second.wait(3)
                    self.wfile.write(b'data: second\n\n')

        upstream = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
        diagnostic = ThreadingHTTPServer(("127.0.0.1", 0), relay.make_handler(relay.parse_upstream(f"http://127.0.0.1:{upstream.server_port}"), emitted.append))
        workers = [threading.Thread(target=server.serve_forever, daemon=True) for server in [upstream, diagnostic]]
        for worker in workers:
            worker.start()
        try:
            url = f"http://127.0.0.1:{diagnostic.server_port}/v1/messages"
            headers = {"Content-Type": "application/json", "x-api-key": "test-token", "anthropic-version": "2023-06-01"}
            raw = b'{"fail":true,"messages":[{"role":"user","content":"private-prose"}]}'
            with self.assertRaises(urllib.error.HTTPError) as caught:
                urllib.request.urlopen(urllib.request.Request(url, raw, headers), timeout=3)
            with caught.exception as response:
                self.assertEqual(response.code, 400)
                self.assertEqual(response.read(), b'{"error":"rejected"}')
            self.assertEqual(requests, [(raw, "test-token", "2023-06-01")])
            self.assertEqual(len(emitted), 1)
            self.assertEqual(emitted[0]["status"], 400)
            self.assertNotIn("private-prose", json.dumps(emitted))
            self.assertNotIn("test-token", json.dumps(emitted))
            with urllib.request.urlopen(urllib.request.Request(url, b'{"stream":true}', headers), timeout=2) as response:
                self.assertTrue(first_sent.wait(1))
                self.assertEqual(response.readline(), b'data: first\n')
                self.assertFalse(release_second.is_set())
                release_second.set()
                self.assertEqual(response.read(), b'\ndata: second\n\n')
            self.assertEqual(len(requests), 2)
        finally:
            release_second.set()
            for server in [diagnostic, upstream]:
                server.shutdown()
                server.server_close()
            for worker in workers:
                worker.join()


if __name__ == "__main__":
    unittest.main()
