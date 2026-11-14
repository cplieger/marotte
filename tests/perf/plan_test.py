"""The performance harness's own contract: the plan cplieger/ci's perf-ci.yaml
reads, the instance's refusal of the live port, and a seeded fixture.

run.sh runs this before it measures anything, so a broken plan fails the shard.
"""

import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
PERF = ROOT / 'tests' / 'perf'

BROWSER_SCENARIOS = {
    'chat-long',
    'chat-stream',
    'tabs',
    'run-dock',
    'run-page',
    'files',
    'git-changes',
    'git-prs',
    'settings',
    'settings-tools',
    'docs',
    'docs-workflows',
    'shell',
    'history',
    'history-runs',
}
SERVER_SCENARIOS = {'run-drive'}
BROWSER_WINDOWS = ['cold', 'rest', 'live', 'away', 'back']
STREAM_WINDOWS = {'live', 'away'}
SERVER_UNIT = {'engine': 'server', 'width': 'none'}
UNIT_FIELDS = ('scenario', 'scale', 'engine', 'width')


def load_plan() -> dict:
    return json.loads((PERF / 'plan.json').read_text())


def unit(cell: dict) -> tuple:
    return (*(cell[f] for f in UNIT_FIELDS), tuple(cell['windows']) if 'windows' in cell else None)


def browser(scenario: str, scale: str, engine: str, width: str, windows=None) -> tuple:
    return (scenario, scale, engine, width, windows)


def server(scale: str) -> tuple:
    return ('run-drive', scale, SERVER_UNIT['engine'], SERVER_UNIT['width'], None)


def harness(name: str) -> set[str]:
    """The names an export of static-src/perf/views.mjs holds: DRIVERS or VIEWS."""
    views = ROOT / 'static-src' / 'perf' / 'views.mjs'
    script = (
        f'import {{ {name} }} from {json.dumps(views.as_uri())};'
        f'console.log(JSON.stringify(Object.keys({name})));'
    )
    out = subprocess.run(
        ['node', '--input-type=module', '-e', script],
        capture_output=True,
        text=True,
        check=True,
        timeout=60,
    )
    return set(json.loads(out.stdout))


class Plan(unittest.TestCase):
    def setUp(self):
        self.plan = load_plan()
        self.scenarios = self.plan['scenarios']

    def test_node_is_the_package_that_pins_playwright(self):
        package = json.loads((ROOT / self.plan['node'] / 'package.json').read_text())
        self.assertIn('playwright', package.get('devDependencies', {}))

    def test_scenarios_are_the_measured_views_and_the_server_drive(self):
        self.assertEqual(set(self.scenarios), BROWSER_SCENARIOS | SERVER_SCENARIOS)

    def test_every_browser_scenario_is_a_view_the_harness_draws(self):
        self.assertEqual(harness('VIEWS'), BROWSER_SCENARIOS)

    def test_a_browser_scenario_has_every_window_and_a_driver_for_each_stream(self):
        drivers = harness('DRIVERS')
        for name in BROWSER_SCENARIOS:
            with self.subTest(scenario=name):
                s = self.scenarios[name]
                self.assertEqual(s['windows'], BROWSER_WINDOWS)
                self.assertEqual(set(s['drivers']), STREAM_WINDOWS)
                self.assertLessEqual(set(s['drivers'].values()), drivers)

    def test_the_server_drive_has_one_window_and_its_driver(self):
        s = self.scenarios['run-drive']
        self.assertEqual(s['windows'], ['drive'])
        self.assertEqual(set(s['drivers']), {'drive'})
        self.assertLessEqual(set(s['drivers'].values()), harness('DRIVERS'))

    def test_every_cell_names_a_scenario_and_its_seconds(self):
        for set_name, spec in self.plan['sets'].items():
            shards = spec['shards']
            self.assertIsInstance(shards, int, set_name)
            self.assertGreater(shards, 0, set_name)
            seen = set()
            for cell in spec['cells']:
                with self.subTest(set=set_name, cell=cell):
                    self.assertIn(cell['scenario'], self.scenarios)
                    self.assertIn(cell['scale'], {'x1', 'x10'})
                    seconds = cell['seconds']
                    self.assertIsInstance(seconds, int | float)
                    self.assertNotIsInstance(seconds, bool)
                    self.assertGreater(seconds, 0)
                    windows = self.scenarios[cell['scenario']]['windows']
                    self.assertLessEqual(set(cell.get('windows', windows)), set(windows))
                    key = tuple(cell[f] for f in UNIT_FIELDS)
                    self.assertNotIn(key, seen)
                    seen.add(key)

    def test_every_scenario_is_in_both_sets(self):
        for set_name in ('pr', 'full'):
            listed = {c['scenario'] for c in self.plan['sets'][set_name]['cells']}
            self.assertEqual(listed, set(self.scenarios), set_name)

    def test_the_pr_set_is_x10_chromium_every_window_webkit_cold_and_the_drive(self):
        want = {browser(s, 'x10', 'chromium', 'desktop') for s in BROWSER_SCENARIOS}
        want |= {browser(s, 'x10', 'webkit', 'desktop', ('cold',)) for s in BROWSER_SCENARIOS}
        want.add(server('x10'))
        cells = self.plan['sets']['pr']['cells']
        self.assertEqual({unit(c) for c in cells}, want)
        self.assertFalse([c for c in cells if c.get('profile')], 'the pr set profiles nothing')

    def test_the_full_set_is_both_scales_engines_and_widths_with_one_profiled_rep(self):
        want = {
            browser(s, scale, engine, width)
            for s in BROWSER_SCENARIOS
            for scale in ('x1', 'x10')
            for engine in ('chromium', 'webkit')
            for width in ('desktop', 'phone')
        }
        want |= {server('x1'), server('x10')}
        cells = self.plan['sets']['full']['cells']
        self.assertEqual({unit(c) for c in cells}, want)
        profiled = {c['scenario'] for c in cells if c.get('profile')}
        self.assertEqual(profiled, BROWSER_SCENARIOS)
        for c in cells:
            if c.get('profile'):
                self.assertEqual(
                    (c['scale'], c['engine'], c['width']), ('x10', 'chromium', 'desktop')
                )


class Serve(unittest.TestCase):
    def refuse(self, port: str) -> subprocess.CompletedProcess:
        with tempfile.TemporaryDirectory() as tmp:
            instance = pathlib.Path(tmp) / 'instance'
            instance.mkdir()
            env = {**os.environ, 'PERF_PORT': port, 'PERF_BUILD': str(pathlib.Path(tmp) / 'none')}
            done = subprocess.run(
                [str(PERF / 'instance' / 'serve.sh'), str(instance)],
                env=env,
                capture_output=True,
                text=True,
                timeout=30,
                check=False,
            )
            self.assertEqual(list(instance.iterdir()), [], 'serve.sh wrote before refusing')
            return done

    def test_refuses_the_live_port(self):
        done = self.refuse('9847')
        self.assertEqual(done.returncode, 2, done.stderr)
        self.assertIn('9847', done.stderr)

    def test_refuses_a_port_whose_forge_lands_on_the_live_port(self):
        done = self.refuse('9838')
        self.assertEqual(done.returncode, 2, done.stderr)
        self.assertIn('9847', done.stderr)

    def test_refuses_a_port_that_is_not_a_number(self):
        done = self.refuse('19951x')
        self.assertEqual(done.returncode, 2, done.stderr)
        self.assertIn('PERF_PORT', done.stderr)


def snapshot(root: pathlib.Path) -> dict[str, str]:
    """Each file's sha256 by path. A git index records each file's stat
    times, so a repository's staged entries stand in for its index bytes."""
    out = {}
    for path in sorted(root.rglob('*')):
        rel = path.relative_to(root).as_posix()
        if path.is_symlink():
            out[rel] = 'link:' + os.readlink(path)
        elif path.name == 'index' and path.parent.name == '.git':
            staged = subprocess.run(
                ['git', '-C', str(path.parent.parent), 'ls-files', '--stage'],
                capture_output=True,
                check=True,
            ).stdout
            out[rel] = 'staged:' + hashlib.sha256(staged).hexdigest()
        elif path.is_file():
            out[rel] = hashlib.sha256(path.read_bytes()).hexdigest()
    return out


class Fixtures(unittest.TestCase):
    NOW = '1790000000'

    def write(self, out: pathlib.Path, seed: str) -> dict[str, str]:
        subprocess.run(
            [
                sys.executable,
                str(PERF / 'synth' / 'fixtures.py'),
                '--out',
                str(out),
                '--scale',
                '1',
                '--seed',
                seed,
                '--now',
                self.NOW,
            ],
            capture_output=True,
            check=True,
            timeout=120,
        )
        return snapshot(out)

    def test_one_seed_writes_byte_identical_instances(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp) / 'x1'
            first = self.write(out, '11')
            self.assertIn('manifest.json', first)
            self.assertTrue(any(p.startswith('config/chats/') for p in first))
            self.assertTrue(any(p.endswith('.git/index') for p in first))
            second = self.write(out, '11')
            self.assertEqual(first, second)
            other = self.write(out, '12')
            self.assertNotEqual(first, other)


if __name__ == '__main__':
    unittest.main()
