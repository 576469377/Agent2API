import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest


class SyncPreservationTest(unittest.TestCase):
    def test_merge_preserves_existing_models_preferences_and_other_providers(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            models = [
                {'id': 'shared', 'platform': p, 'routing_id': p + '/shared',
                 'default_route': p == 'workbuddy', 'context_tokens': n,
                 'max_output_tokens': 100, 'supports_images': True}
                for p, n in [('workbuddy', 1000), ('workbuddy-global', 2000)]
            ]
            class Handler(BaseHTTPRequestHandler):
                def do_GET(self):
                    self.send_response(200); self.end_headers()
                    self.wfile.write(json.dumps({'models': models}).encode())
                def log_message(self, *args):
                    pass
            server = HTTPServer(('127.0.0.1', 0), Handler)
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            try:
                other = {'providerId': 'other', 'enabled': False, 'config': {'personalModelIds': ['other-model']}}
                original_rule = {'providerId': 'agent2api', 'modelId': 'shared', 'config': {'enabled': False, 'properties': {'contextWindow': 777}}}
                doc = {'schemaVersion': 1, 'config': {
                    'providerOrder': ['other', 'agent2api'],
                    'providerConfigRules': {'providerRules': [other, {'providerId': 'agent2api', 'enabled': True,
                        'config': {'personalModelIds': ['old', 'shared'], 'modelOrder': ['shared', 'old']}}]},
                    'modelConfigRules': {'providerModelRules': [], 'manualProviderModelRules': [original_rule]},
                    'defaultModelSelection': {'providerId': 'agent2api', 'modelId': 'shared', 'options': {'reasoningLevel': 'enabled'}},
                }}
                path = root / 'zcode.json'; path.write_text(json.dumps(doc), encoding='utf-8')
                config = root / 'gateway.json'; config.write_text('{"auth":{"api_key":""}}', encoding='utf-8')
                args = [sys.executable, str(Path(__file__).with_name('sync-zcode.py')),
                        '--zcode-config', str(path), '--config', str(config),
                        '--base-url', 'http://127.0.0.1:' + str(server.server_port)]
                subprocess.run(args + ['--dry-run'], check=True, capture_output=True)
                self.assertEqual(json.loads(path.read_text()), doc)
                subprocess.run(args, check=True, capture_output=True)
                merged = json.loads(path.read_text())
                cfg = merged['config']; provider = cfg['providerConfigRules']['providerRules'][1]
                self.assertEqual(provider['config']['personalModelIds'], ['old', 'workbuddy/shared', 'workbuddy-global/shared'])
                self.assertEqual(provider['config']['modelOrder'], ['workbuddy/shared', 'old', 'workbuddy-global/shared'])
                self.assertEqual(cfg['providerConfigRules']['providerRules'][0], other)
                self.assertIn({**original_rule, 'modelId': 'workbuddy/shared'}, cfg['modelConfigRules']['manualProviderModelRules'])
                self.assertEqual(cfg['defaultModelSelection'], {**doc['config']['defaultModelSelection'], 'modelId': 'workbuddy/shared'})
                international = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == 'workbuddy-global/shared')
                self.assertEqual(international['config']['properties']['contextWindow'], 2000)
                subprocess.run(args, check=True, capture_output=True)
                self.assertEqual(json.loads(path.read_text()), merged)
            finally:
                server.shutdown(); server.server_close(); worker.join()


if __name__ == '__main__':
    unittest.main()
