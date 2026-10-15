import importlib.util
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("diagnose_console", Path(__file__).with_name("diagnose-console.py"))
diagnose = importlib.util.module_from_spec(spec)
spec.loader.exec_module(diagnose)


class ConsoleDiagnosticsTest(unittest.TestCase):
    def test_summary_keeps_all_dates_without_emitting_request_or_identity(self):
        data = {"total": 999, "input_tokens": 12, "output_tokens": 4,
                "recent": [{"time": "2026-10-15T12:00:00Z", "account": "private-account", "error": "private-key"}],
                "usage_history": {"days": [{"date": "2026-10-03"}, {"date": "2026-10-14"}]}, "api_key": "private-key"}
        summary = diagnose.usage_summary(data)
        self.assertEqual(summary["dates"], ["2026-10-03", "2026-10-14"])
        self.assertEqual(summary["recent_dates"], ["2026-10-15"])
        self.assertEqual(summary["tokens"], 16)
        self.assertNotIn("private-", json.dumps(summary))

    def test_regions_and_failed_catalog_do_not_become_free(self):
        now = datetime(2026, 10, 15, tzinfo=timezone.utc)
        rate = {"source": "upstream_config", "observed_at": now.isoformat(), "credit_multiplier": 0}
        data = {"models": [{"platform": "cn", "pricing": rate}, {"platform": "global", "pricing": rate}],
                "catalog_status": [{"platform": "global", "status": "error", "error": "private-key"}]}
        summary = diagnose.model_summary(data, now)
        self.assertEqual(summary["cn"]["zero_credits"], 1)
        self.assertEqual(summary["global"]["unknown_or_variable"], 1)
        self.assertNotIn("private-key", json.dumps(summary))

    def test_local_file_read_is_read_only_and_omits_errors_and_names(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "metrics.json"
            raw = b'{"total":123,"recent":[{"error":"private-key"}]}'
            path.write_bytes(raw)
            summary = diagnose.file_summary(path)
            self.assertEqual(summary["requests"], 123)
            self.assertNotIn("private-key", json.dumps(summary))
            self.assertEqual(path.read_bytes(), raw)
            path.write_bytes(b'{"broken":')
            self.assertEqual(diagnose.file_summary(path)["read_status"], "unreadable_or_invalid")

    def test_legacy_coverage_is_separate_from_lifetime_and_recent(self):
        summary = diagnose.usage_summary({"total": 22000, "usage_history": {"legacy_requests": 21000, "tracking_since": "2026-10-15T01:00:00Z", "days": [{"date": "2026-10-15", "models": [{"total": 1200}]}]}})
        self.assertEqual(summary["requests"], 22000)
        self.assertEqual(summary["legacy_requests"], 21000)
        self.assertEqual(summary["daily_recorded_requests"], 1200)

    def test_history_scan_stays_in_named_data_directories_and_omits_log_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "bin").mkdir()
            (root / "auths").mkdir()
            archive = root / "bin" / "old-metrics.json.backup"
            archive.write_text('{"total":123}', encoding="utf-8")
            (root / "auths" / "metrics.json").write_text('{}', encoding="utf-8")
            (root / "bin" / "metrics-credentials.json").write_text('{}', encoding="utf-8")
            log = root / "bin" / "proxy.stdout.log"
            log.write_text('[agent2api] 2026/10/13 12:00:00.123456 protocol=chat model=m status=200 account=private-account error=private-key\n[agent2api] 2026/10/12 12:00:00 protocol=chat model=m ok=true dur_ms=42\nnot a completion private-key\n', encoding="utf-8")
            self.assertEqual(diagnose.history_candidates(root), sorted([archive.resolve(), log.resolve()]))
            summary = diagnose.log_summary(log)
            self.assertEqual(summary["dates_in_log_timezone"], {"2026-10-12": 1, "2026-10-13": 1})
            self.assertEqual(summary["completion_lines"], 2)
            self.assertNotIn("private-", json.dumps(summary))


if __name__ == "__main__":
    unittest.main()
