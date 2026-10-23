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
from unittest import mock
import importlib.util
import sqlite3


spec = importlib.util.spec_from_file_location('sync_zcode', Path(__file__).with_name('sync-zcode.py'))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


class ConfigWriteSafetyTest(unittest.TestCase):
    def test_console_sync_lock_is_respected_and_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            source = b'{"old":true}'
            path.write_bytes(source)
            lock = path.with_name(path.name + '.agent2api.lock')
            lock.write_bytes(b'console-writer')
            with self.assertRaisesRegex(ValueError, 'already being synchronized'):
                sync.write_config(path, {'new': True}, source)
            self.assertEqual(path.read_bytes(), source)
            self.assertEqual(lock.read_bytes(), b'console-writer')
            self.assertEqual(set(path.parent.iterdir()), {path, lock})

    def test_first_publish_does_not_replace_a_concurrently_created_config(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            calls = 0
            def read_before_creation(target):
                nonlocal calls
                self.assertEqual(target, path)
                calls += 1
                if calls == 3:
                    path.write_bytes(b'{"client_created":true}')
                return None
            with mock.patch.object(sync, 'read_source', side_effect=read_before_creation):
                with self.assertRaises(FileExistsError):
                    sync.write_config(path, {'new': True}, None)
            self.assertEqual(path.read_bytes(), b'{"client_created":true}')
            self.assertEqual(list(path.parent.iterdir()), [path])

    def test_unchanged_json_preserves_bytes_mtime_and_creates_no_backup(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            raw = b'\xef\xbb\xbf{ "keep": true, "models": [] }\r\n'
            path.write_bytes(raw)
            os.utime(path, ns=(1600000000000000000, 1600000000000000000))
            before = path.stat()
            self.assertEqual(sync.write_config(path, {'models': [], 'keep': True}, raw), (None, None))
            self.assertEqual(path.read_bytes(), raw)
            self.assertEqual(path.stat().st_mtime_ns, before.st_mtime_ns)
            self.assertEqual(list(path.parent.iterdir()), [path])

    def test_edit_during_catalogue_refresh_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            source = b'{"old":true}'
            edited = b'{"user_edit":true}'
            path.write_bytes(edited)
            with self.assertRaisesRegex(ValueError, 'changed during sync'):
                sync.write_config(path, {'old': False}, source)
            self.assertEqual(path.read_bytes(), edited)
            self.assertEqual(list(path.parent.iterdir()), [path])

    def test_edit_during_temporary_write_is_not_overwritten_and_temp_is_cleaned(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            source = b'{"old":true}'
            edited = b'{"user_edit":true}'
            path.write_bytes(source)
            with mock.patch.object(sync.os, 'fsync', side_effect=lambda _: path.write_bytes(edited)):
                with self.assertRaisesRegex(ValueError, 'changed during sync'):
                    sync.write_config(path, {'old': False}, source)
            self.assertEqual(path.read_bytes(), edited)
            self.assertEqual(list(path.parent.iterdir()), [path])

    def test_atomic_write_preserves_original_backup_and_does_not_reuse_a_foreign_temp(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            source = b'{"old":true}\r\n'
            path.write_bytes(source)
            foreign = path.with_name(path.name + '.tmp')
            foreign.write_bytes(b'other-sync-in-progress')
            output, backup = sync.write_config(path, {'new': '\u4e2d\u6587'}, source)
            self.assertEqual(output, path)
            self.assertEqual(backup.read_bytes(), source)
            self.assertEqual(json.loads(path.read_text(encoding='utf-8')), {'new': '\u4e2d\u6587'})
            self.assertEqual(foreign.read_bytes(), b'other-sync-in-progress')
            self.assertEqual(len(list(path.parent.iterdir())), 3)

    def test_replace_failure_leaves_valid_source_and_cleans_own_temp(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'provider.json'
            source = b'{"old":true}'
            path.write_bytes(source)
            with mock.patch.object(sync.os, 'replace', side_effect=PermissionError('locked')):
                with self.assertRaises(PermissionError):
                    sync.write_config(path, {'new': True}, source)
            self.assertEqual(path.read_bytes(), source)
            self.assertEqual(list(path.parent.glob('*.tmp')), [])


class UsageVerificationTest(unittest.TestCase):
    def test_wal_data_is_read_without_rewriting_storage_or_emitting_private_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'db.sqlite'
            connection = sqlite3.connect(path)
            try:
                connection.execute('pragma journal_mode=wal')
                connection.execute('pragma wal_autocheckpoint=0')
                connection.execute('create table model_usage(started_at integer, computed_total_tokens integer, raw_usage_json text)')
                connection.executemany('insert into model_usage values(?,?,?)', [(1000, 200, 'private-prompt-secret'), (2000, 300, 'private-identity'), (9000, 900, 'future-request')])
                connection.commit()
                wal = path.with_name(path.name + '-wal')
                original, original_wal = path.read_bytes(), wal.read_bytes()
                snapshot = sync.usage_snapshot(path, 3000)
                self.assertEqual(snapshot['status'], 'ready')
                self.assertEqual(snapshot['model_requests'], 2)
                self.assertEqual(snapshot['tokens'], 500)
                report = sync.usage_report(path, snapshot, 3000)
                self.assertEqual(report['comparison'], 'no_record_count_decrease')
                self.assertFalse(report['client_ui_verified'])
                text = json.dumps(report)
                self.assertNotIn('private-', text)
                self.assertNotIn('future-request', text)
                self.assertNotIn(str(path), text)
                self.assertEqual(path.read_bytes(), original)
                self.assertEqual(wal.read_bytes(), original_wal)
                connection.execute('delete from model_usage where started_at=1000'); connection.commit()
                self.assertEqual(sync.usage_report(path, snapshot, 3000)['comparison'], 'record_count_decreased')
            finally:
                connection.close()

    def test_missing_invalid_and_unsupported_database_remain_untouched(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'db.sqlite'
            self.assertEqual(sync.usage_snapshot(path, 10), {'status': 'missing'})
            self.assertFalse(path.exists())
            path.write_bytes(b'not-a-database-private-secret')
            self.assertEqual(sync.usage_snapshot(path, 10), {'status': 'unavailable'})
            self.assertEqual(path.read_bytes(), b'not-a-database-private-secret')
            path.unlink()
            with sqlite3.connect(path) as connection:
                connection.execute('create table unrelated(value text)')
            connection.close()
            before = path.read_bytes()
            self.assertEqual(sync.usage_snapshot(path, 10), {'status': 'unsupported_schema'})
            self.assertEqual(path.read_bytes(), before)


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
    def test_kiro_without_published_output_limit_syncs_and_preserves_preferences(self):
        model = self.trae_model()
        model.update(id='kiro-auto', routing_id='kiro/kiro-auto', platform='kiro', max_output_tokens=0,
                     supports_thinking=False)
        model['pricing']['source'] = 'kiro_cli_catalog'
        model['access']['source'] = 'kiro_cli_free_plan'
        self.models.append(model)
        result = self.run_sync('--models', model['routing_id'])
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        rule = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertEqual(rule['config']['optionSpecs']['maxOutputTokens'], {'max': 8192})
        self.assertEqual(rule['config']['optionSpecs']['reasoningLevel'], {'values': ['disabled'], 'map': '{}'})
        rule['config']['enabled'] = False
        rule['config']['optionSpecs']['maxOutputTokens']['max'] = 512
        self.doc['config'] = cfg
        self.save()
        result = self.run_sync('--models', 'free')
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        retained = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertFalse(retained['config']['enabled'])
        self.assertEqual(retained['config']['optionSpecs']['maxOutputTokens'], {'max': 512})
        original = self.path.read_bytes()
        result = self.run_sync('--models', 'free')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b'Unchanged:', result.stdout)
        self.assertEqual(self.path.read_bytes(), original)

    def test_kiro_preserves_real_credit_rates_and_unknown_output_metadata(self):
        model = self.trae_model()
        model.update(platform='kiro', max_output_tokens=0, supports_thinking=False)
        model['pricing']['source'] = 'kiro_cli_catalog'
        model['access']['source'] = 'kiro_cli_free_plan'
        self.assertTrue(sync.confirmed_free(model))
        self.assertEqual(sync.output_limit(model), 8192)
        self.assertEqual(model['max_output_tokens'], 0)
        for key, value in [('plan','pro'), ('source','trae_free_plan'), ('remaining_credits',0), ('overage_enabled',True)]:
            changed = json.loads(json.dumps(model))
            changed['access'][key] = value
            self.assertFalse(sync.confirmed_free(changed))

    def trae_model(self):
        model = self.model('deepseek-v4.1-flash', 'credits', 'trae-cn')
        model['supports_thinking'] = True
        model['pricing']['source'] = 'trae_account_catalog'
        model['access'] = {'source': 'trae_free_plan', 'basis': 'recurring_free_allowance',
                           'plan': 'free', 'overage_enabled': False, 'remaining_credits': 500,
                           'checked_at': model['pricing']['observed_at']}
        return model

    def test_trae_requires_fresh_recurring_free_account_evidence(self):
        model = self.trae_model()
        self.assertTrue(sync.confirmed_free(model))
        for access in ({'plan': 'pro'}, {'basis': 'trial'}, {'source': 'unknown'},
                       {'overage_enabled': True}, {'overage_enabled': None},
                       {'remaining_credits': 0}, {'remaining_credits': '500'},
                       {'checked_at': '2020-01-01T00:00:00Z'}):
            with self.subTest(access=access):
                self.assertFalse(sync.confirmed_free({**model, 'access': {**model['access'], **access}}))
        for patch in ({'access': {}}, {'pricing_from_snapshot': True}, {'pricing_sync_failed': True}):
            self.assertFalse(sync.confirmed_free({**model, **patch}))

    def test_trae_sync_preserves_price_and_does_not_advertise_thinking_control(self):
        model = self.trae_model()
        self.models.append(model)
        result = self.run_sync('--models', model['routing_id'])
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        rule = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertEqual(rule['config']['optionSpecs']['reasoningLevel'], {'values': ['enabled'], 'map': '{}'})
        self.assertEqual(model['pricing']['credit_multiplier'], 1)
        rule['config']['enabled'] = False
        rule['config']['optionSpecs']['reasoningLevel'] = {'values': ['disabled', 'enabled'], 'map': 'old'}
        self.doc['config'] = cfg
        self.save()
        result = self.run_sync('--models', 'free')
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        rule = next(r for r in cfg['modelConfigRules']['manualProviderModelRules'] if r['modelId'] == model['routing_id'])
        self.assertFalse(rule['config']['enabled'])
        self.assertEqual(rule['config']['optionSpecs']['reasoningLevel'], {'values': ['enabled'], 'map': '{}'})

    def test_trae_upgrades_legacy_windows_and_preserves_custom_settings(self):
        for model_id in ('deepseek-v4.1-flash', 'glm-5.3-flash'):
            model = self.trae_model()
            model.update(id=model_id, routing_id='trae-cn/' + model_id, context_tokens=1000000, max_output_tokens=64000)
            self.models.append(model)
            self.doc['config']['modelConfigRules']['manualProviderModelRules'].append({
                'providerId': 'agent2api', 'modelId': model['routing_id'],
                'config': {'enabled': False, 'keep': 'custom', 'properties': {'contextWindow': 200000},
                           'optionSpecs': {'maxOutputTokens': {'max': 32000}}},
            })
        self.save()
        result = self.run_sync()
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        for rule in cfg['modelConfigRules']['manualProviderModelRules']:
            if not rule['modelId'].startswith('trae-cn/'):
                continue
            self.assertFalse(rule['config']['enabled'])
            self.assertEqual(rule['config']['keep'], 'custom')
            self.assertEqual(rule['config']['properties']['contextWindow'], 1000000)
            self.assertEqual(rule['config']['optionSpecs']['maxOutputTokens']['max'], 64000)
            rule['config']['properties']['contextWindow'] = 123456
            rule['config']['optionSpecs']['maxOutputTokens']['max'] = 4096
        self.doc['config'] = cfg
        self.save()
        result = self.run_sync()
        self.assertEqual(result.returncode, 0, result.stderr)
        cfg = json.loads(self.path.read_text())['config']
        for rule in cfg['modelConfigRules']['manualProviderModelRules']:
            if rule['modelId'].startswith('trae-cn/'):
                self.assertEqual(rule['config']['properties']['contextWindow'], 123456)
                self.assertEqual(rule['config']['optionSpecs']['maxOutputTokens']['max'], 4096)

    def test_cursor_free_plan_requires_fresh_verified_metadata(self):
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
        self.catalog_hook = None
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
                if case.catalog_hook:
                    case.catalog_hook()
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

    def test_second_sync_does_not_rewrite_config_or_create_another_backup(self):
        first = self.run_sync(); self.assertEqual(first.returncode, 0, first.stderr)
        original = self.path.read_bytes()
        os.utime(self.path, ns=(1600000000000000000, 1600000000000000000))
        mtime = self.path.stat().st_mtime_ns
        result = self.run_sync(); self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b'Unchanged:', result.stdout)
        self.assertEqual(self.path.read_bytes(), original)
        self.assertEqual(self.path.stat().st_mtime_ns, mtime)
        self.assertEqual(len(list(self.path.parent.glob('zcode.json.bak-*'))), 1)

    def test_concurrent_client_edit_during_network_request_is_preserved(self):
        edited = {**self.doc, 'clientEdit': True}
        self.catalog_hook = lambda: self.path.write_text(json.dumps(edited), encoding='utf-8')
        result = self.run_sync()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'changed during sync', result.stderr)
        self.assertEqual(json.loads(self.path.read_text()), edited)
        self.assertEqual(list(self.path.parent.glob('*.bak-*')), [])

    def test_usage_check_does_not_write_database_or_include_private_details(self):
        path = self.path.parent / 'usage.sqlite'
        with sqlite3.connect(path) as connection:
            connection.execute('create table model_usage(started_at integer, computed_total_tokens integer, raw_usage_json text)')
            connection.execute('insert into model_usage values(1,123,?)', ('private-prompt-secret',))
        connection.close()
        before = path.read_bytes()
        result = self.run_sync('--verify-usage', '--usage-db', str(path))
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(next(line.removeprefix('ZCode usage check: ') for line in result.stdout.decode().splitlines() if line.startswith('ZCode usage check: ')))
        self.assertEqual(report['before']['tokens'], 123)
        self.assertEqual(report['after']['tokens'], 123)
        self.assertEqual(report['comparison'], 'no_record_count_decrease')
        self.assertNotIn(b'private-', result.stdout)
        self.assertEqual(path.read_bytes(), before)

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


class KiloFreeEvidenceTest(unittest.TestCase):
    def test_only_current_native_unexpired_kilo_evidence_is_free(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        price = {'status': 'free', 'source': 'kilo_models', 'observed_at': now.isoformat()}
        model = {'platform': 'kilo', 'pricing': price}
        self.assertTrue(sync.confirmed_free(model))
        for patch in [
            {'platform': 'openrouter'}, {'pricing_from_snapshot': True}, {'pricing_sync_failed': True},
            *[{'pricing': {**price, **value}} for value in [
                {'status': 'credits'}, {'source': 'unknown'},
                {'expires_at': now.isoformat()}, {'expires_at': 'later'}, {'expires_at': False},
                {'observed_at': (now - datetime.timedelta(minutes=6)).isoformat()},
            ]],
        ]:
            with self.subTest(patch=patch):
                self.assertFalse(sync.confirmed_free({**model, **patch}))


if __name__ == '__main__':
    unittest.main()
