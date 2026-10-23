import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('check-secrets.py').resolve()
spec = importlib.util.spec_from_file_location('secretcheck', SCRIPT)
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class SecretCheckTest(unittest.TestCase):
    def test_paths_include_client_backups_and_local_configs(self):
        for path in ('config.json', 'config.local.json', '.env.dev', 'auths/cn/a.json',
                     'provider_config.json.bak-20261012', 'provider_config.json.candidate',
                     'credentials.json', 'credential.json.tmp', 'config.json.tmp',
                     'metrics.json.tmp', '.zcode/v2/data/settings.json',
                     '.cc-switch/cc-switch.db', 'cc-switch.db-wal',
                     '.pi/agent/models.json', '.pi/agent/sessions/a.jsonl',
                     '.pi/auth.json', '.pi/models.json.bak-20261023',
                     'workbuddy-desktop-ai.info'):
            self.assertTrue(checker.forbidden_path(path), path)
        self.assertFalse(checker.forbidden_path('config.example.json'))
        self.assertFalse(checker.forbidden_path('internal/adapter/workbuddy/credential_crypto.go'))
        self.assertFalse(checker.forbidden_path('.pi/settings.json'))

    def test_patterns_do_not_print_secret_values(self):
        samples = ['ghp_' + 'A' * 36, 'sk-' + 'B' * 40,
                   '-----BEGIN ' + 'PRIVATE KEY-----',
                   '"apiKey": "' + 'C' * 32 + '"',
                   '"refresh_token": "' + 'D' * 40 + '"']
        for sample in samples:
            findings = checker.content_findings(sample.encode())
            self.assertTrue(findings)
            self.assertNotIn(sample, str(findings))
        self.assertFalse(checker.content_findings(b'{"api_key":"<YOUR_GATEWAY_KEY>"}'))

    def test_scans_staged_bytes_even_if_working_tree_was_cleaned(self):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                subprocess.run(['git', *args], cwd=folder, check=True, capture_output=True)
            git('init', '-q')
            path = Path(folder) / 'example.txt'
            path.write_text('ghp_' + 'A' * 36)
            git('add', 'example.txt')
            path.write_text('sanitized working tree')
            result = subprocess.run([sys.executable, str(SCRIPT), '--staged'], cwd=folder,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn('A' * 36, result.stdout)
            git('add', 'example.txt')
            result = subprocess.run([sys.executable, str(SCRIPT), '--staged'], cwd=folder,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0)

    def test_encoded_content_and_unquoted_env_values(self):
        token = 'ghp_' + 'A' * 36
        for data in (token.encode('utf-16'), token.encode('utf-16-be'),
                     token.encode('utf-32'), b'header\0' + token.encode(),
                     b'\0' * 100 + token.encode(),
                     ('AGENT2API_API_KEY=' + 'B' * 32).encode(),
                     ('api_key: "test-' + 'C' * 32 + '"').encode()):
            self.assertTrue(checker.content_findings(data))
        for text in ('api_key: "<YOUR_GATEWAY_KEY>"',
                     'AGENT2API_API_KEY=${LOCAL_GATEWAY_KEY}'):
            self.assertFalse(checker.content_findings(text.encode()))

    def test_structured_credentials_under_custom_filenames(self):
        for data in ('{"auth":{"accessToken":\n"' + 'A' * 32 + '"}}',
                     '{"auth":{"accessToken":{"$wbEncrypted":1,"envelope":"' + 'B' * 48 + '"}}}'):
            self.assertTrue(checker.content_findings(data.encode()))
        self.assertFalse(checker.content_findings(b'{"accessToken":{"$wbEncrypted":1,"envelope":"..."}}'))

    def test_staged_rename_to_sensitive_path_is_blocked(self):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                subprocess.run(['git', '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                                *args], cwd=folder, check=True, capture_output=True)
            git('init', '-q')
            (Path(folder) / 'notes.txt').write_text('ordinary content')
            git('add', '.'); git('commit', '-qm', 'initial')
            git('mv', 'notes.txt', 'config.local.json')
            result = subprocess.run([sys.executable, str(SCRIPT), '--staged'], cwd=folder,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertIn('config.local.json', result.stdout)

    def test_history_checks_every_path_for_reused_blobs(self):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                subprocess.run(['git', '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                                *args], cwd=folder, check=True, capture_output=True)
            git('init', '-q')
            path = Path(folder) / 'notes.txt'
            path.write_text('same blob at both paths')
            git('add', '.'); git('commit', '-qm', 'initial')
            git('mv', 'notes.txt', 'provider_config.json')
            git('commit', '-qm', 'sensitive path')
            git('mv', 'provider_config.json', 'notes.txt')
            git('commit', '-qm', 'restored path')
            result = subprocess.run([sys.executable, str(SCRIPT), '--history'], cwd=folder,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertIn('provider_config.json', result.stdout)

    def test_history_detects_secret_removed_from_latest_commit(self):
        with tempfile.TemporaryDirectory() as folder:
            def git(*args):
                subprocess.run(['git', '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                                *args], cwd=folder, check=True, capture_output=True)
            git('init', '-q')
            path = Path(folder) / 'example.txt'
            path.write_text('ghp_' + 'A' * 36)
            git('add', 'example.txt'); git('commit', '-qm', 'test fixture')
            path.write_text('removed secret')
            git('add', 'example.txt'); git('commit', '-qm', 'clean fixture')
            result = subprocess.run([sys.executable, str(SCRIPT), '--history'], cwd=folder,
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn('A' * 36, result.stdout)


if __name__ == '__main__':
    unittest.main()
