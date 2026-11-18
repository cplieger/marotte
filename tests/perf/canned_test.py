"""canned.py: a recording becomes static-src/perf/canned/, and a set it cannot vouch for is refused."""

import importlib.util
import json
import pathlib
import tempfile
import unittest

PERF = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('canned', PERF / 'measure' / 'canned.py')
canned = importlib.util.module_from_spec(spec)
spec.loader.exec_module(canned)

INST = '/tmp/perf-canned.abc/instance'
EPOCH = '1993890b717eea9e'
ACCOUNT = 'XJPAHSJGNEFJP7HJ436KXID4S4'
FIX = canned.rewriter(INST, {EPOCH: canned.EPOCH, ACCOUNT: canned.ACCOUNT})
ROUTES = {
    'GET /api/tabs': 'TabList',
    'GET /api/forges': None,
    'GET /api/forges/inventory': None,
    'GET /api/forges/{forge}/repos': 'RepoList',
}


def answer(route, url, body, status=200, method='GET', request=None):
    return {
        'route': route,
        'method': method,
        'url': url,
        'request': request,
        'status': status,
        'body': json.dumps(body),
    }


class Canned(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = pathlib.Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def record(self, scenario, answers):
        lines = '\n'.join(json.dumps(a) for a in answers) + '\n'
        (self.dir / f'{scenario}.answers.jsonl').write_text(lines)

    def docs(self, out):
        return [json.loads(t) for p, t in out.items() if p.startswith('answers/')]

    def test_keeps_the_first_ok_answer_per_url_with_its_decoder(self):
        self.record(
            'tabs',
            [
                answer('GET /api/tabs', 'http://127.0.0.1:1/api/tabs', {'tabs': []}, status=503),
                answer('GET /api/tabs', 'http://127.0.0.1:1/api/tabs', {'tabs': [1]}),
                answer('GET /api/tabs', 'http://127.0.0.1:1/api/tabs', {'tabs': [2]}),
            ],
        )
        docs = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual(
            docs,
            [
                {
                    'route': 'GET /api/tabs',
                    'url': '/api/tabs',
                    'decoder': 'TabList',
                    'body': {'tabs': [1]},
                }
            ],
        )

    def test_keeps_one_answer_per_request_body_without_its_op_id(self):
        route = 'POST /api/command'
        sent = [
            {'type': 'open_tab', 'payload': {'kind': kind, 'op_id': op}}
            for kind, op in (('git', 'o1'), ('docs', 'o2'), ('git', 'o3'))
        ]
        self.record(
            'tabs',
            [
                answer(
                    route, 'http://h/api/command', {'n': i}, method='POST', request=json.dumps(r)
                )
                for i, r in enumerate(sent)
            ],
        )
        routes = ROUTES | {route: None}
        docs = self.docs(canned.assemble(self.dir, FIX, ['tabs'], routes))
        self.assertEqual(
            sorted((d['request']['payload']['kind'], d['body']['n']) for d in docs),
            [('docs', 1), ('git', 0)],
        )
        self.assertNotIn('op_id', docs[0]['request']['payload'])

    def test_keeps_a_bodyless_answer_with_its_status(self):
        a = answer('GET /api/tabs', 'http://h/api/tabs', None, status=204)
        a['body'] = None
        self.record('tabs', [a])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual((doc['status'], doc['body']), (204, None))

    def test_rewrites_the_instance_directory(self):
        self.record('tabs', [answer('GET /api/tabs', 'http://h/api/tabs', {'dir': f'{INST}/ws'})])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual(doc['body'], {'dir': '/perf/ws'})

    def test_rewrites_the_instance_directory_in_an_encoded_query(self):
        url = 'http://h/api/tabs?paths=%2Ftmp%2Fperf-canned.abc%2Finstance%2Fa.go'
        self.record('tabs', [answer('GET /api/tabs', url, {})])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual(doc['url'], '/api/tabs?paths=%2Fperf%2Fa.go')

    def test_rewrites_the_instance_directory_relative_to_the_root(self):
        body = {'docs': [{'path': 'tmp/perf-canned.abc/instance/work/.kiro/a.md'}]}
        self.record('tabs', [answer('GET /api/tabs', 'http://h/api/tabs', body)])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual(doc['body']['docs'][0]['path'], 'perf/work/.kiro/a.md')

    def test_rewrites_each_recorded_epoch_as_the_canned_servers(self):
        body = {'subject': {'epoch': EPOCH, 'version': '3'}}
        self.record('tabs', [answer('GET /api/tabs', 'http://h/api/tabs', body)])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['tabs'], ROUTES))
        self.assertEqual(doc['body']['subject']['epoch'], canned.EPOCH)

    def test_epochs_are_read_from_each_scenarios_hello(self):
        self.stream_recording('a', [])
        self.assertEqual(canned.epochs_of(self.dir, ['a']), {EPOCH})

    def test_rewrites_each_scenarios_forge_account_as_the_canned_one(self):
        forges = {'forges': [{'id': 'gitea:h', 'account': ACCOUNT}]}
        inventory = {'entries': [{'forge_id': 'gitea:h', 'account': ACCOUNT}]}
        self.record('git-changes', [answer('GET /api/forges', 'http://h/api/forges', forges)])
        self.record(
            'git-prs',
            [answer('GET /api/forges/inventory', 'http://h/api/forges/inventory', inventory)],
        )
        self.assertEqual(canned.accounts_of(self.dir, ['git-changes', 'git-prs']), {ACCOUNT})
        docs = self.docs(canned.assemble(self.dir, FIX, ['git-changes', 'git-prs'], ROUTES))
        self.assertEqual(
            {d['route']: d['body'][k][0]['account'] for d in docs for k in d['body']},
            {'GET /api/forges': canned.ACCOUNT, 'GET /api/forges/inventory': canned.ACCOUNT},
        )

    def test_names_a_forge_route_without_its_port(self):
        route = 'GET /api/forges/gitea%3A127.0.0.1%5C%3A19980/repos'
        self.record('git-prs', [answer(route, 'http://h/api/forges/x/repos', {'repos': []})])
        [doc] = self.docs(canned.assemble(self.dir, FIX, ['git-prs'], ROUTES))
        self.assertEqual(doc['route'], 'GET /api/forges/{forge}/repos')
        self.assertEqual(canned.route_key('GET /api/forges/inventory'), 'GET /api/forges/inventory')

    def test_refuses_a_route_with_no_decoder_entry(self):
        self.record('tabs', [answer('GET /api/unknown', 'http://h/api/unknown', {})])
        with self.assertRaisesRegex(canned.CannedError, 'GET /api/unknown'):
            canned.assemble(self.dir, FIX, ['tabs'], ROUTES)

    def test_live_frames_drop_hub_frames_and_keepalives_and_start_at_zero(self):
        doc = {'windows': {'live': {'start': 100, 'end': 200}}}
        records = [
            {'t': 90, 'event': 'entry_delta', 'type': 'entry_delta', 'bytes': 5},
            {'t': 110, 'event': 'sse:scope', 'type': 'sse:scope', 'bytes': 9},
            {'t': 120, 'event': 'entry_delta', 'type': 'entry_delta', 'bytes': 7},
            {'t': 130, 'event': 'message', 'keepalive': True, 'bytes': 1},
            {'t': 150, 'event': 'turn_closed', 'type': 'turn_closed', 'bytes': 30},
            {'t': 210, 'event': 'entry_delta', 'type': 'entry_delta', 'bytes': 5},
        ]
        self.assertEqual(
            canned.live_frames(doc, records),
            [
                {'at': 0, 'event': 'entry_delta', 'bytes': 7},
                {'at': 30, 'event': 'turn_closed', 'bytes': 30},
            ],
        )

    def test_refuses_a_live_window_with_no_frame(self):
        doc = {'windows': {'live': {'start': 0, 'end': 10}}}
        with self.assertRaises(canned.CannedError):
            canned.live_frames(doc, [{'t': 50, 'event': 'x', 'type': 'x', 'bytes': 1}])

    def test_stream_frames_keep_ids_and_payloads_from_the_window_start(self):
        records = [
            frame(90, 'entry_delta'),
            frame(110, 'entry_delta', fid='4'),
            {'t': 120, 'event': 'sse:hello', 'bytes': 2, 'type': '', 'data': '{}'},
            {'t': 125, 'event': 'heartbeat', 'keepalive': True, 'bytes': 2, 'data': '{}'},
            frame(130, 'forge_inventory'),
            frame(140, 'turn_closed'),
        ]
        got = canned.stream_frames(records, 100, 200, keep_poll=False)
        self.assertEqual(
            [(f['at'], f['envelope']['type'], f.get('id')) for f in got],
            [
                (10, 'entry_delta', '4'),
                (40, 'turn_closed', None),
            ],
        )
        self.assertEqual(got[0]['envelope'], {'type': 'entry_delta'})
        kept = canned.stream_frames(records, 100, 200, keep_poll=True)
        self.assertIn('forge_inventory', [f['envelope']['type'] for f in kept])

    def test_scope_at_reads_the_last_change_before_the_instant(self):
        doc = {'scopes': [{'t': 10, 'topics': []}, {'t': 20, 'topics': ['run:r']}]}
        self.assertEqual(canned.scope_at(doc, 15), [])
        self.assertEqual(canned.scope_at(doc, 25), ['run:r'])

    def test_streams_keep_the_first_of_each_window_driver_and_scope(self):
        plan = {'scenarios': {s: {'drivers': {'live': 'steps', 'away': 'steps'}} for s in 'abc'}}
        for s, live_topics in (('a', []), ('b', ['run:r']), ('c', [])):
            self.stream_recording(s, live_topics)
        out = canned.streams(self.dir, FIX, plan, ['a', 'b', 'c'])
        self.assertEqual(
            sorted(out),
            [
                'frames/connect.json',
                'frames/stream.steps.a-away.json',
                'frames/stream.steps.a-live.json',
                'frames/stream.steps.b-away.json',
                'frames/stream.steps.b-live.json',
            ],
        )
        b = json.loads(out['frames/stream.steps.b-live.json'])
        self.assertEqual((b['topics'], [f['at'] for f in b['frames']]), (['run:r'], [5]))
        hello = json.loads(out['frames/connect.json'])
        self.assertEqual(
            (hello['scenario'], hello['at'], hello['hello']), ('a', 2, {'epoch': canned.EPOCH})
        )
        self.assertEqual([f['envelope']['type'] for f in hello['frames']], ['connected'])

    def test_streams_keep_one_window_per_chat_a_driver_streamed(self):
        plan = {'scenarios': {s: {'drivers': {'live': 'shown'}} for s in 'ab'}}
        for s in 'ab':
            self.stream_recording(s, [], chat=f'c-{s}')
        out = canned.streams(self.dir, FIX, plan, ['a', 'b'])
        self.assertIn('frames/stream.shown.b-live.json', out)

    def test_streams_refuse_a_recording_with_no_hello_payload(self):
        plan = {'scenarios': {'a': {'drivers': {}}}}
        self.stream_recording('a', [])
        lines = (self.dir / 'a.json.sse.jsonl').read_text().splitlines()[1:]
        (self.dir / 'a.json.sse.jsonl').write_text('\n'.join(lines) + '\n')
        with self.assertRaisesRegex(canned.CannedError, 'no hello'):
            canned.streams(self.dir, FIX, plan, ['a'])

    def stream_recording(self, s, live_topics, chat=''):
        doc = {
            'windows': {
                'cold': {'start': 0, 'end': 50},
                'live': {'start': 100, 'end': 200},
                'away': {'start': 300, 'end': 400},
            },
            'scopes': [{'t': 1, 'topics': []}, {'t': 90, 'topics': live_topics}],
        }
        (self.dir / f'{s}.json').write_text(json.dumps(doc))
        records = [
            {
                't': 2,
                'event': 'sse:hello',
                'bytes': 2,
                'type': '',
                'data': json.dumps({'epoch': EPOCH}),
            },
            frame(3, 'connected'),
            frame(105, 'run_progress', chat=chat),
            frame(305, 'run_progress', chat=chat),
        ]
        (self.dir / f'{s}.json.sse.jsonl').write_text(
            '\n'.join(json.dumps(r) for r in records) + '\n'
        )

    def test_refuses_a_set_over_the_cap(self):
        (self.dir / 'tabs.answers.jsonl').write_text(
            json.dumps(
                answer('GET /api/tabs', 'http://h/api/tabs', {'pad': 'x' * canned.MAX_BYTES})
            )
            + '\n'
        )
        (self.dir / 'chat-stream.answers.jsonl').write_text('')
        self.stream_recording('chat-stream', [])
        self.stream_recording('tabs', [])
        inst = self.dir / 'instance'
        inst.mkdir()
        (inst / 'seeded.json').write_text(json.dumps(dict.fromkeys(canned.FACTS, 'v')))
        drivers = {'drivers': {'live': 'prompt', 'away': 'prompt'}}
        plan = {'scenarios': {'tabs': drivers, 'chat-stream': drivers, 'run-drive': {}}}
        with self.assertRaisesRegex(canned.CannedError, 'over the'):
            canned.build(self.dir, str(inst), plan, ROUTES)


def frame(t, kind, fid=None, chat=''):
    data = json.dumps({'type': kind} | ({'chat_id': chat} if chat else {}))
    rec = {'t': t, 'event': 'message', 'bytes': len(data), 'type': kind, 'data': data}
    return rec | ({'id': fid} if fid else {})


if __name__ == '__main__':
    unittest.main()
