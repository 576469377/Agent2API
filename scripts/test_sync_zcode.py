import json
import datetime
import os
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
                 'max_output_tokens': 100, 'supports_images': True,
                 'pricing': {'status': 'free', 'source': 'upstream_config', 'credit_multiplier': 0,
                             'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}}
                for p, n in [('workbuddy', 1000), ('workbuddy-global', 2000)]
            ]
            class Handler(BaseHTTPRequestHandler):
                def do_POST(self):
                    self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
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
                self.assertEqual(provider['config']['personalModelIds'], ['workbuddy/shared', 'workbuddy-global/shared'])
                self.assertEqual(provider['config']['modelOrder'], ['workbuddy/shared', 'workbuddy-global/shared'])
                self.assertEqual(cfg['providerConfigRules']['providerRules'][0], other)
                self.assertIn({**original_rule, 'modelId': 'workbuddy/shared'}, cfg['modelConfigRules']['manualProviderModelRules'])
                self.assertEqual(cfg['defaultModelSelection'], {**doc['config']['defaultModelSelection'], 'modelId': 'workbuddy/shared'})
                international = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == 'workbuddy-global/shared')
                self.assertEqual(international['config']['properties']['contextWindow'], 2000)
                subprocess.run(args, check=True, capture_output=True)
                self.assertEqual(json.loads(path.read_text()), merged)
            finally:
                server.shutdown(); server.server_close(); worker.join()


class FreeOnlySyncTest(unittest.TestCase):
    def test_cursor_free_plan_requires_fresh_verified_metadata(self):
        import importlib.util
        spec = importlib.util.spec_from_file_location('sync_zcode', Path(__file__).with_name('sync-zcode.py'))
        sync = importlib.util.module_from_spec(spec); spec.loader.exec_module(sync)
        model = self.model('composer-2.5', 'free', 'cursor')
        model['pricing']['source'] = 'cursor_free_plan'
        model['pricing'].pop('credit_multiplier')
        self.assertTrue(sync.confirmed_free(model))
        model['pricing']['observed_at'] = '2020-01-01T00:00:00Z'
        self.assertFalse(sync.confirmed_free(model))

    def setUp(self):
        self.folder = tempfile.TemporaryDirectory()
        self.addCleanup(self.folder.cleanup)
        root = Path(self.folder.name)
        self.path = root / 'zcode.json'
        self.config = root / 'config.json'
        self.config.write_text('{"auth":{"api_key":""}}', encoding='utf-8')
        self.models = [self.model('free', 'free'), self.model('paid', 'credits'),
                       self.model('auto', 'dynamic'), self.model('unknown', 'unknown'),
                       self.model('author/model:free', 'free', 'openrouter')]
        self.catalog_status = []
        self.doc = {'schemaVersion': 1, 'config': {
            'providerOrder': ['other', 'agent2api'],
            'providerConfigRules': {'providerRules': [
                {'providerId': 'other', 'config': {'personalModelIds': ['other/paid']}},
                {'providerId': 'agent2api', 'enabled': True, 'config': {
                    'personalModelIds': ['paid', 'free', 'auto', 'unknown', 'gone'],
                    'modelOrder': ['paid', 'free', 'auto', 'gone']}}]},
            'modelConfigRules': {
                'providerModelRules': [{'providerId': 'agent2api', 'modelId': 'paid', 'config': {'enabled': True}}],
                'manualProviderModelRules': [{'providerId': 'agent2api', 'modelId': m, 'config': {'enabled': True}}
                                             for m in ['free', 'auto', 'gone']]},
            'defaultModelSelection': {'providerId': 'agent2api', 'modelId': 'paid', 'options': {'reasoningLevel': 'enabled'}},
        }}
        self.save()
        case = self
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                case.assertEqual(self.path, '/api/config')
                case.assertEqual(json.loads(self.rfile.read(int(self.headers['Content-Length']))), {'refresh_models': True})
                self.send_response(200); self.end_headers(); self.wfile.write(b'{}')
            def do_GET(self):
                case.assertEqual(self.path, '/api/models?refresh=1')
                self.send_response(200); self.end_headers()
                self.wfile.write(json.dumps({'models': case.models, 'catalog_status': case.catalog_status}).encode())
            def log_message(self, *args):
                pass
        self.server = HTTPServer(('127.0.0.1', 0), Handler)
        self.worker = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.worker.start()
        self.addCleanup(self.close_server)

    def close_server(self):
        self.server.shutdown(); self.server.server_close(); self.worker.join()

    def model(self, name, status, platform='workbuddy'):
        return {'id': name, 'routing_id': platform + '/' + name, 'default_route': True, 'platform': platform,
                'context_tokens': 10000, 'max_output_tokens': 1000,
                'pricing': {'status': status, 'source': 'openrouter_models' if platform == 'openrouter' else 'upstream_config',
                            'credit_multiplier': 0 if status == 'free' else 1,
                            'observed_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}}

    def save(self):
        self.path.write_text(json.dumps(self.doc), encoding='utf-8')

    def run_sync(self, *extra):
        env = {k: v for k, v in os.environ.items() if k != 'AGENT2API_API_KEY'}
        return subprocess.run([sys.executable, str(Path(__file__).with_name('sync-zcode.py')),
                               '--config', str(self.config), '--zcode-config', str(self.path),
                               '--base-url', 'http://127.0.0.1:' + str(self.server.server_port), *extra],
                              env=env, capture_output=True)

    def test_prunes_paid_unknown_missing_rules_and_replaces_paid_default(self):
        result = self.run_sync(); self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        self.assertEqual(cfg['providerConfigRules']['providerRules'][1]['config']['personalModelIds'],
                         ['workbuddy/free', 'openrouter/author/model:free'])
        self.assertEqual(cfg['defaultModelSelection'], {'providerId': 'agent2api', 'modelId': 'workbuddy/free'})
        self.assertEqual(cfg['modelConfigRules']['providerModelRules'], [])
        self.assertEqual([r['modelId'] for r in cfg['modelConfigRules']['manualProviderModelRules']],
                         ['workbuddy/free', 'openrouter/author/model:free'])
        self.assertEqual(cfg['providerConfigRules']['providerRules'][0], self.doc['config']['providerConfigRules']['providerRules'][0])
        self.assertEqual(len(list(self.path.parent.glob('zcode.json.bak-*'))), 1)

    def test_explicit_paid_unknown_and_incomplete_models_refused_without_writes(self):
        self.models.append({**self.model('incomplete', 'free'), 'max_output_tokens': 0})
        for model in ['paid', 'auto', 'unknown', 'incomplete']:
            with self.subTest(model=model):
                self.assertNotEqual(self.run_sync('--models', model).returncode, 0)
                self.assertEqual(json.loads(self.path.read_text()), self.doc)
        self.assertEqual(list(self.path.parent.glob('*.bak-*')), [])

    def test_stale_or_failed_catalogue_does_not_change_file(self):
        for m in self.models:
            m['pricing']['observed_at'] = '2020-01-01T00:00:00Z'
        self.assertNotEqual(self.run_sync().returncode, 0)
        self.assertEqual(json.loads(self.path.read_text()), self.doc)
        self.catalog_status = [{'platform': 'workbuddy', 'status': 'error'}]
        self.models = [self.model('free', 'free')]
        self.assertNotEqual(self.run_sync().returncode, 0)
        self.assertEqual(json.loads(self.path.read_text()), self.doc)

    def test_subset_still_prunes_and_null_default_is_valid(self):
        self.doc['config']['defaultModelSelection'] = None; self.save()
        result = self.run_sync('--models', 'free'); self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        self.assertEqual(cfg['providerConfigRules']['providerRules'][1]['config']['personalModelIds'], ['workbuddy/free'])
        self.assertIsNone(cfg['defaultModelSelection'])

    def test_default_never_selects_disabled_free_models(self):
        self.models = self.models[:2]
        self.doc['config']['modelConfigRules']['manualProviderModelRules'][0]['config']['enabled'] = False
        self.save()
        result = self.run_sync(); self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('defaultModelSelection', json.loads(self.path.read_text())['config'])

    def test_openrouter_output_budget_repairs_retained_models_and_preserves_context(self):
        model = self.models[-1]
        model.update(context_tokens=512000, max_output_tokens=460800)
        result = self.run_sync('--models', model['routing_id'])
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        rule = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertEqual(rule['config']['properties']['contextWindow'], 512000)
        self.assertEqual(rule['config']['optionSpecs']['maxOutputTokens'], {'max': 8192})
        rule['config']['enabled'] = False
        rule['config']['optionSpecs']['maxOutputTokens']['max'] = 460800
        self.doc = {'schemaVersion': 1, 'config': cfg}; self.save()
        # Retained rules must be repaired even when --models selects another model.
        result = self.run_sync('--models', 'free')
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        repaired = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertFalse(repaired['config']['enabled'])
        self.assertEqual(repaired['config']['optionSpecs']['maxOutputTokens']['max'], 8192)
        repaired['config']['optionSpecs']['maxOutputTokens']['max'] = 512
        self.doc['config'] = cfg; self.save()
        result = self.run_sync('--models', 'free')
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        preserved = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertEqual(preserved['config']['optionSpecs']['maxOutputTokens']['max'], 512)


if __name__ == '__main__':
    unittest.main()
