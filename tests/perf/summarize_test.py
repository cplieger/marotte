"""summarize.py: the raw reps of a shard become the results cells perf_gate.py judges."""

import importlib.util
import json
import pathlib
import tempfile
import unittest

PERF = pathlib.Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('summarize', PERF / 'measure' / 'summarize.py')
summarize = importlib.util.module_from_spec(spec)
spec.loader.exec_module(summarize)

UNIT = {'scenario': 'files', 'scale': 'x10', 'engine': 'chromium', 'width': 'desktop'}
STEM = 'files.x10.chromium.desktop'


def window(cpu=100, **extra):
    w = {'cpuMs': cpu, 'routes': {}, 'kiroCli': 0, 'gitFetch': 0, 'sseFrames': 3, 'longTaskMs': 40}
    w.update(extra)
    return w


def rep(**windows):
    return {'schema': 1, 'failures': [], 'windows': windows}


class Summarize(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.raw = pathlib.Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def put(self, name, doc):
        (self.raw / name).write_text(json.dumps(doc))

    def run_units(self, *units):
        for u in units:
            (self.raw / f'{summarize.stem(u)}.seconds').write_text('81.25')
        return summarize.results(list(units), self.raw)

    def cell(self, doc, window, metric):
        found = [c for c in doc['cells'] if (c['window'], c['metric']) == (window, metric)]
        self.assertEqual(len(found), 1, (window, metric))
        return found[0]

    def test_each_metric_is_a_cell_with_its_reps_in_rep_order(self):
        chats = {'GET /api/chats': {'count': 1, 'maxBytes': 900, 'url': 'http://h/api/chats'}}
        self.put(f'{STEM}.r1.json', rep(cold=window(110, viewDom=500, heapMB=11.5, routes=chats)))
        self.put(f'{STEM}.r2.json', rep(cold=window(130, viewDom=500, heapMB=11.0)))
        doc = self.run_units(UNIT)
        self.assertEqual(self.cell(doc, 'cold', 'cpu_ms')['reps'], [110, 130])
        self.assertEqual(self.cell(doc, 'cold', 'cpu_ms')['kind'], 'time')
        self.assertEqual(self.cell(doc, 'cold', 'heap_mb')['reps'], [11.5, 11.0])
        self.assertEqual(self.cell(doc, 'cold', 'dom')['kind'], 'count')
        reads = self.cell(doc, 'cold', 'reads:GET /api/chats')
        self.assertEqual((reads['kind'], reads['reps']), ('count', [1, 0]))
        self.assertEqual(doc['seconds'], [{**UNIT, 'seconds': 81.2}])

    def test_reads_in_a_stream_window_are_reported(self):
        route = {'GET /api/files': {'count': 4, 'maxBytes': 10, 'url': 'http://h/api/files'}}
        self.put(f'{STEM}.r1.json', rep(live=window(routes=route, frameP95Ms=16.7)))
        doc = self.run_units({**UNIT, 'windows': ['live']})
        self.assertEqual(self.cell(doc, 'live', 'reads:GET /api/files')['kind'], 'report')
        self.assertEqual(self.cell(doc, 'live', 'frame_p95_ms')['reps'], [16.7])

    def test_a_time_a_rep_did_not_measure_fails(self):
        self.put(f'{STEM}.r1.json', rep(rest=window(90)))
        self.put(f'{STEM}.r2.json', rep(rest=window(None)))
        with self.assertRaisesRegex(
            summarize.SummaryError, r'rest cpu_ms not measured in rep \[2\]'
        ):
            self.run_units(UNIT)

    def test_a_failed_rep_or_a_missing_window_fails(self):
        self.put(f'{STEM}.r1.json', {**rep(rest=window()), 'failures': ['rest: did not settle']})
        with self.assertRaisesRegex(summarize.SummaryError, 'did not settle'):
            self.run_units(UNIT)
        self.put(f'{STEM}.r1.json', rep(rest=window()))
        with self.assertRaisesRegex(summarize.SummaryError, 'no cold'):
            self.run_units({**UNIT, 'windows': ['cold', 'rest']})

    def test_the_replay_gates_every_route_but_the_whole_answers(self):
        self.put(f'{STEM}.r1.json', rep(rest=window()))
        routes = {
            'GET /api/runs/{run}?view=glance': {'cpu_ms': 4.5},
            'GET /api/runs/{run}': {'cpu_ms': 280.0},
            'GET /api/chats/{chat}/export?format': {'cpu_ms': 180.5},
        }
        self.put(f'{STEM}.r1.servercpu.json', {'n': 5, 'routes': routes})
        doc = self.run_units(UNIT)
        kinds = {c['metric']: c['kind'] for c in doc['cells'] if c['window'] == 'replay'}
        self.assertEqual(
            kinds,
            {
                'server_cpu_ms:GET /api/runs/{run}?view=glance': 'server_cpu',
                'server_cpu_ms:GET /api/runs/{run}': 'report',
                'server_cpu_ms:GET /api/chats/{chat}/export?format': 'report',
            },
        )

    def test_the_server_drive_and_the_profiled_rep(self):
        drive = {'scenario': 'run-drive', 'scale': 'x10', 'engine': 'server', 'width': 'none'}
        self.put('run-drive.x10.server.none.r1.json', rep(drive={'serverCpuMs': 1780}))
        profiled = {**UNIT, 'profile': True}
        self.put(f'{STEM}.r1.json', rep(live=window()))
        shares = {'busy': 900, 'unmapped': 0, 'shares': {'markdown': 0.05}}
        self.put(f'{STEM}.rp.json', rep(live={'layouts': 12, 'styleRecalcs': 30, 'decode': shares}))
        doc = self.run_units(drive, profiled)
        self.assertEqual(self.cell(doc, 'drive', 'cpu_ms:server')['reps'], [1780])
        self.assertEqual(
            self.cell(doc, 'live', 'cpu_ms')['reps'], [100], 'the profiled rep times nothing'
        )
        self.assertEqual(self.cell(doc, 'live', 'decode_share:markdown')['kind'], 'report')
        self.assertEqual(self.cell(doc, 'live', 'layouts')['reps'], [12])

    def test_a_unit_with_no_seconds_fails(self):
        self.put(f'{STEM}.r1.json', rep(rest=window()))
        with self.assertRaisesRegex(summarize.SummaryError, 'seconds'):
            summarize.results([UNIT], self.raw)


if __name__ == '__main__':
    unittest.main()
