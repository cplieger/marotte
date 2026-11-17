"""budgets.json against the signed calibrations it names, and the plan's seconds per cell and
shard count against the times those calibrations measured.

cplieger/ci's perf_gate.py checks the same rule, the bundles' signatures and the base's tiers in
the compare job; run.sh runs this first, so a budget file that breaks the rule fails the shard
before anything is measured.

The tier and headroom of an entry from a file of fewer than ten runs are not checked here: that
entry takes its base entry's tier, or is reported when the base has none, and perf_gate.py
--check-file checks it against the base, which this test does not read.
"""

import json
import math
import pathlib
import re
import unittest
from typing import ClassVar

PERF = pathlib.Path(__file__).resolve().parent
KEY_FIELDS = ('scenario', 'scale', 'engine', 'width', 'window', 'metric')
UNIT_FIELDS = KEY_FIELDS[:4]
SOURCE_RE = re.compile(r'^https://github\.com/cplieger/ci/actions/runs/(\d+)$')
ROW_RE = re.compile(r'^(Q[1-8]|[A-Z]{1,2}\d+)$')
TIER_RUNS = 10
SERVER_CPU = 'server_cpu_ms'
Q7_SERVER_CPU_MS = 63
# Q8: the pull request's performance job finishes in about 25 minutes. Besides its slowest
# shard's cells it runs plan and build (104 s in pr calibration 43683563629) and the shard's own
# setup (at most 93 s there: checkout, Node, Playwright, the build artifact).
PR_JOB_SECONDS = 25 * 60
PR_SETUP_SECONDS = 104 + 93
# An answer whose size carries a clock value is not a count: GET /api/engine's `since` is a
# time.Time, whose RFC 3339 text drops trailing zero nanoseconds, and its seq can gain a digit,
# so its size moves rep to rep on one runner. perf_gate.py reports a measured cell that has no
# entry.
CLOCK_SIZED = frozenset({'max_bytes:GET /api/engine'})


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


def exact_entry_problems(e: dict, files: dict, cells: dict) -> list[str]:
    """Why exact entry e does not hold against the checked-in calibrations (files and cells by
    run id), or [] when it holds.

    Its source must be a file of the newest commit that measured the cell, since an older
    commit measured other code. Newest is by run id, a dispatch-order pre-check: a shard's
    shallow checkout has no history, so perf_gate.py's provenance check owns the rule by git
    ancestry. Every rep
    of that commit's files must be the baseline: perf_gate.py --calibrate makes a count exact
    only when every rep agrees, and one that moved holds its middle reps off on unchanged code.
    """
    m = SOURCE_RE.match(e.get('source', ''))
    if m is None or m.group(1) not in files:
        return [f'source {e.get("source")!r} is not a checked-in calibration']
    measured = [run_id for run_id, cs in cells.items() if key(e) in cs]
    if not measured:
        return ['no checked-in calibration measured the cell']
    newest = files[max(measured, key=int)]['commit']
    if files[m.group(1)]['commit'] != newest:
        return [f'source measured {files[m.group(1)]["commit"]}, not the newest commit {newest}']
    out = []
    for run_id in measured:
        if files[run_id]['commit'] == newest:
            reps = {r for run in cells[run_id][key(e)]['reps_by_run'] for r in run}
            if reps - {e['baseline']}:
                out.append(f'run {run_id} measured {sorted(reps)}, not only {e["baseline"]}')
    return out


class ExactEntryRule(unittest.TestCase):
    """exact_entry_problems on two commits' files that measured one cell differently."""

    CELL: ClassVar[dict] = dict(
        zip(KEY_FIELDS, ('chat-long', 'x10', 'chromium', 'desktop', 'cold', 'dom'), strict=True)
    )

    def calibration(self, run_id: str, commit: str, reps: list[int]) -> dict:
        cell = {**self.CELL, 'tier': 'exact', 'baseline': reps[0], 'reps_by_run': [reps] * 10}
        return {'run_id': run_id, 'commit': commit, 'cells': [cell]}

    def check(self, run_id: str, baseline: float) -> list[str]:
        files = {
            '99': self.calibration('99', 'old', [4384] * 3),
            '100': self.calibration('100', 'new', [4400] * 3),
        }
        cells = {r: {key(c): c for c in d['cells']} for r, d in files.items()}
        url = f'https://github.com/cplieger/ci/actions/runs/{run_id}'
        return exact_entry_problems(
            {**self.CELL, 'baseline': baseline, 'source': url}, files, cells
        )

    def test_an_entry_of_the_newest_commit_holds(self):
        self.assertEqual(self.check('100', 4400.0), [])

    def test_an_entry_re_sourced_to_an_older_commit_fails(self):
        self.assertTrue(self.check('99', 4384.0))

    def test_an_entry_off_its_commits_reps_fails(self):
        self.assertTrue(self.check('100', 4384.0))

    def test_an_unchecked_in_source_fails(self):
        self.assertTrue(self.check('300', 4400.0))


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
        cls.cells = {run_id: {key(c): c for c in doc['cells']} for run_id, doc in cls.files.items()}
        cls.entries = {}
        for e in cls.budgets['budgets']:
            cls.entries.setdefault(key(e), []).append(e)

    def test_every_planned_window_has_entries(self):
        units = {k[:4] for k in self.entries}
        windows = {k[:5] for k in self.entries}
        for set_name, s in self.plan['sets'].items():
            for c in s['cells']:
                with self.subTest(set=set_name, unit=unit(c)):
                    self.assertIn(unit(c), units)
                for w in c.get('windows', self.plan['scenarios'][c['scenario']]['windows']):
                    with self.subTest(set=set_name, unit=unit(c), window=w):
                        self.assertIn((*unit(c), w), windows)

    def test_every_exact_entry_is_every_rep_its_newest_commit_measured(self):
        for e in self.budgets['budgets']:
            if e['tier'] == 'exact':
                with self.subTest(entry=key(e)):
                    self.assertEqual(exact_entry_problems(e, self.files, self.cells), [])

    def test_no_entry_holds_an_answer_size_that_carries_a_clock(self):
        held = [key(e) for e in self.budgets['budgets'] if e['metric'] in CLOCK_SIZED]
        self.assertEqual(held, [])

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
                c = self.cells[m.group(1)].get(key(e))
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
