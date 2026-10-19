import importlib.util
from contextlib import closing
import io
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("diagnose-cursor.py")
SPEC = importlib.util.spec_from_file_location("cursor_probe", SCRIPT)
PROBE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PROBE)


class CursorProbeTest(unittest.TestCase):
    def test_upstream_error_cannot_echo_credentials(self):
        error = PROBE.urllib.error.HTTPError("https://example.invalid", 401, "test-token", {}, io.BytesIO(b'test-token'))
        with mock.patch.object(PROBE.urllib.request, "urlopen", side_effect=error):
            with self.assertRaises(PROBE.ProbeError) as caught:
                PROBE.fetch("https://example.invalid", "/auth/full_stripe_profile", "test-token", "3.0.9")
        self.assertEqual(caught.exception.status, 401)
        self.assertEqual(caught.exception.kind, "http_error")
        self.assertNotIn("test-token", str(caught.exception))

    def test_membership_and_quota_do_not_claim_models_are_free(self):
        report = PROBE.summarize(
            {"membershipType": "free", "isTeamMember": False}, {"noUsageBasedAllowed": True},
            {"clientVersionStatus": {"minSupportedClientVersion": "3.12.0", "minAllowedClientVersion": "3.0.0"}},
            {"models": [{"name": "default"}, {"name": "a-model", "price": None}]},
            {"models": [{"modelId": "a-model"}]}, "3.0.9")
        self.assertTrue(report["account"]["individual_free_account_confirmed"])
        self.assertEqual(report["catalogue"]["confirmed_free_generation_models"], [])
        self.assertEqual(report["next_step"], "update_cursor")
        self.assertTrue(report["free_account_billing_guard_passed"])
        self.assertFalse(report["generation_verified"])
        for profile, limits in [
                ({"membershipType": "pro", "isTeamMember": False}, {"noUsageBasedAllowed": True}),
                ({"membershipType": "free", "isTeamMember": True}, {"noUsageBasedAllowed": True}),
                ({"membershipType": "free", "isTeamMember": False}, {}), ({}, {})]:
            self.assertFalse(PROBE.summarize(profile, limits, {}, {}, {}, "3.12.0")["free_account_billing_guard_passed"])

    def test_database_is_read_only_and_report_omits_identity_tokens_and_raw_errors(self):
        with tempfile.TemporaryDirectory() as folder:
            db = Path(folder) / "state.vscdb"
            with closing(sqlite3.connect(db)) as con:
                con.execute("CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)")
                con.execute("INSERT INTO ItemTable VALUES (?,?)", ("cursorAuth/accessToken", "test-token"))
                con.commit()
            original = db.read_bytes()
            paths = []
            case = self

            class Handler(BaseHTTPRequestHandler):
                def reply(self):
                    paths.append(self.path)
                    case.assertEqual(self.headers["Authorization"], "Bearer test-token")
                    if self.path.endswith("full_stripe_profile"):
                        data = {"membershipType": "free", "isTeamMember": False, "paymentId": "DO_NOT_EXPOSE", "email": "DO_NOT_EXPOSE"}
                    elif self.path.endswith("GetHardLimit"):
                        data = {"noUsageBasedAllowed": True}
                    elif self.path.endswith("GetServerConfig"):
                        data = {"clientVersionStatus": {"minSupportedClientVersion": "3.12.0"}}
                    else:
                        data = {"models": [{"name": "example"}]}
                    self.send_response(200); self.end_headers(); self.wfile.write(json.dumps(data).encode())
                do_POST = reply
                do_GET = reply
                def log_message(self, *args):
                    pass

            server = HTTPServer(("127.0.0.1", 0), Handler)
            worker = threading.Thread(target=server.serve_forever, daemon=True); worker.start()
            try:
                env = {k: v for k, v in os.environ.items() if k not in ("CURSOR_API_KEY", "CURSOR_ACCESS_TOKEN")}
                result = subprocess.run([sys.executable, str(SCRIPT), "--state-db", str(db), "--client-version", "3.0.9",
                                         "--base-url", "http://127.0.0.1:" + str(server.server_port)], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn("test-token", result.stdout + result.stderr)
                self.assertNotIn("DO_NOT_EXPOSE", result.stdout + result.stderr)
                self.assertEqual(json.loads(result.stdout)["catalogue"]["confirmed_free_generation_models"], [])
                self.assertEqual(len(paths), 5)
                self.assertFalse(any("Run" in p or "Stream" in p or "Set" in p for p in paths))
                self.assertEqual(db.read_bytes(), original)
            finally:
                server.shutdown(); server.server_close(); worker.join()


if __name__ == "__main__":
    unittest.main()
