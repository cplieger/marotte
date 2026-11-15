"""budgets.json against the signed calibrations it names, and the plan's seconds per cell and
shard count against the times those calibrations measured.

cplieger/ci's perf_gate.py checks the same rule, the bundles' signatures and the base's tiers in
the compare job; run.sh runs this first, so a budget file that breaks the rule fails the shard
before anything is measured.
"""

import json
import math
import pathlib
import re
import unittest

PERF = pathlib.Path(__file__).resolve().parent
KEY_FIELDS = ('scenario', 'scale', 'engine', 'width', 'window', 'metric')
UNIT_FIELDS = KEY_FIELDS[:4]
SOURCE_RE = re.compile(r'^https://github\.com/cplieger/ci/actions/runs/(\d+)$')
ROW_RE = re.compile(r'^(Q[1-8]|[A-Z]{1,2}\d+)$')
TIER_RUNS = 10
SERVER_CPU = 'server_cpu_ms'
Q7_SERVER_CPU_MS = 63
# Q8: the pull request's performance job finishes in about 25 minutes. Besides its slowest
# shard's cells it runs plan and build (82 s in pr calibration 43591500420) and the shard's own
# setup (at most 108 s there: checkout, Node, Playwright, the build artifact).
PR_JOB_SECONDS = 25 * 60
PR_SETUP_SECONDS = 82 + 108


def key(d: dict) -> tuple:
    return tuple(d[f] for f in KEY_FIELDS)


def unit(d: dict) -> tuple:
    return tuple(d[f] for f in UNIT_FIELDS)


def family(metric: str) -> str:
    return metric.split(':', 1)[0]


def longest_first(seconds: list[float], shards: int) -> list[float]:
    """The shard loads perf_gate.py's plan gives: each cell, longest first, onto the
    least-loaded shard."""
    load = [0.0] * min(shards, len(seconds))
    for s in sorted(seconds, reverse=True):
        load[load.index(min(load))] += s
    return load


class Budgets(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.budgets = json.loads((PERF / 'budgets.json').read_text())
        cls.plan = json.loads((PERF / 'plan.json').read_text())
        cls.files = {}
        calibrations = PERF / 'calibrations'
        if calibrations.is_dir():
            for p in sorted(calibrations.glob('*.json')):
                if not p.name.endswith('.sigstore.json'):
                    cls.files[p.stem] = json.loads(p.read_text())
        cls.entries = {}
        for e in cls.budgets['budgets']:
            cls.entries.setdefault(key(e), []).append(e)

    def test_every_planned_unit_has_entries(self):
        units = {k[:4] for k in self.entries}
        for set_name, s in self.plan['sets'].items():
            for c in s['cells']:
                with self.subTest(set=set_name, unit=unit(c)):
                    self.assertIn(unit(c), units)

    def test_each_cell_has_one_entry(self):
        for k, es in self.entries.items():
            with self.subTest(cell=k):
                self.assertEqual(len(es), 1)

    def test_every_entry_is_its_sources_cell_under_the_rule(self):
        for e in self.budgets['budgets']:
            with self.subTest(entry=key(e)):
                m = SOURCE_RE.match(e.get('source', ''))
                self.assertIsNotNone(m, 'source is a perf-calibrate run URL')
                doc = self.files.get(m.group(1))
                self.assertIsNotNone(doc, f'calibrations/{m.group(1)}.json is checked in')
                self.assertEqual(doc['run_url'], e['source'])
                c = {key(c): c for c in doc['cells']}.get(key(e))
                self.assertIsNotNone(c, 'its calibration measured the cell')
                self.assertEqual(e.get('baseline'), c['baseline'])
                if doc['runs'] >= TIER_RUNS:
                    self.assertEqual((e['tier'], e.get('headroom')), (c['tier'], c.get('headroom')))
                self.assertTrue(e['rows'] and all(ROW_RE.match(r) for r in e['rows']), e['rows'])
                fam = family(e['metric'])
                if e['tier'] == 'exact':
                    self.assertEqual(e['budget'], e['baseline'])
                elif e['tier'] == 'gated' and fam == SERVER_CPU:
                    self.assertEqual((e['target'], e['budget']), (Q7_SERVER_CPU_MS,) * 2)
                elif e['tier'] == 'gated':
                    h = e.get('headroom', self.budgets['headroom'][fam])
                    want = round(max(self.budgets['floor'].get(fam, 0), e['baseline'] * h), 2)
                    self.assertAlmostEqual(e['budget'], want, places=2)
                else:
                    self.assertNotIn('budget', e)
                    self.assertNotIn('headroom', e)

    def test_every_calibration_file_is_named_and_has_its_bundle(self):
        named = {SOURCE_RE.match(e['source']).group(1) for e in self.budgets['budgets']}
        self.assertEqual(set(self.files), named)
        for run_id, doc in self.files.items():
            with self.subTest(run=run_id):
                self.assertTrue((PERF / 'calibrations' / f'{run_id}.sigstore.json').is_file())
                self.assertEqual(
                    (doc['run_id'], doc['repo'], doc['runner']),
                    (run_id, 'cplieger/marotte', self.budgets['runner']),
                )

    def test_a_ten_run_calibrations_widened_and_demoted_cells_are_carried(self):
        for run_id, doc in self.files.items():
            if doc['runs'] < TIER_RUNS:
                continue
            for w in doc['whole_pr']['widened']:
                with self.subTest(run=run_id, widened=key(w)):
                    e = self.entry_from(w, run_id)
                    if e is not None:
                        self.assertEqual((e['tier'], e.get('headroom')), ('gated', w['headroom']))
            for d in doc['whole_pr']['demoted']:
                with self.subTest(run=run_id, demoted=key(d)):
                    e = self.entry_from(d, run_id)
                    if e is not None:
                        self.assertEqual(e['tier'], 'reported')

    def entry_from(self, cell: dict, run_id: str) -> dict | None:
        """The cell's entry when run_id is its source; None when another calibration is."""
        es = self.entries.get(key(cell))
        self.assertTrue(es, 'the cell has an entry')
        return es[0] if es[0]['source'].endswith(f'/{run_id}') else None

    def test_every_planned_cells_seconds_are_a_measured_time(self):
        measured = {}
        for doc in self.files.values():
            for s in doc['seconds']:
                measured.setdefault(unit(s), set()).add(math.ceil(s['seconds']))
        for set_name, s in self.plan['sets'].items():
            for c in s['cells']:
                with self.subTest(set=set_name, unit=unit(c)):
                    self.assertIn(c['seconds'], measured.get(unit(c), set()))

    def test_the_pr_sets_slowest_shard_fits_the_pull_requests_job(self):
        s = self.plan['sets']['pr']
        seconds = [c['seconds'] for c in s['cells']]
        loads = longest_first(seconds, s['shards'])
        self.assertLessEqual(max(loads) + PR_SETUP_SECONDS, PR_JOB_SECONDS, loads)
        if s['shards'] > 1:
            fewer = longest_first(seconds, s['shards'] - 1)
            self.assertGreater(max(fewer), max(loads), 'one shard fewer is as fast')


if __name__ == '__main__':
    unittest.main()
