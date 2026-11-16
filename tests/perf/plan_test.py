"""The performance harness's own contract: the plan cplieger/ci's perf-ci.yaml
reads, the instance's refusal of the live port, a seeded fixture, the git shim
that counts fetches, the fake engine's steer answer, the window failures that fail a rep, the
line that ends a shell drive, and the stream frames a window counts.

run.sh runs this before it measures anything, so a broken plan fails the shard.
"""

import hashlib
import http.server
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import threading
import time
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


def node(script: str) -> str:
    out = subprocess.run(
        ['node', '--input-type=module', '-e', script],
        capture_output=True,
        text=True,
        check=True,
        timeout=60,
    )
    return out.stdout


def harness(name: str) -> set[str]:
    """The names an export of static-src/perf/views.mjs holds: DRIVERS or VIEWS."""
    views = ROOT / 'static-src' / 'perf' / 'views.mjs'
    script = (
        f'import {{ {name} }} from {json.dumps(views.as_uri())};'
        f'console.log(JSON.stringify(Object.keys({name})));'
    )
    return set(json.loads(node(script)))


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


class View(unittest.TestCase):
    """static-src/perf/view.mjs: the instance it refuses before it opens a browser."""

    def run_view(self, port) -> subprocess.CompletedProcess:
        with tempfile.TemporaryDirectory() as tmp:
            instance = pathlib.Path(tmp)
            (instance / 'instance.json').write_text(json.dumps({'port': port}))
            return subprocess.run(
                [
                    'node',
                    str(ROOT / 'static-src' / 'perf' / 'view.mjs'),
                    *('--instance', str(instance), '--plan', str(PERF / 'plan.json')),
                    *('--scenario', 'settings', '--engine', 'chromium', '--width', 'desktop'),
                    *('--out', str(instance / 'out.json')),
                ],
                capture_output=True,
                text=True,
                timeout=60,
                check=False,
            )

    def test_refuses_an_instance_port_that_is_not_one_to_65535(self):
        for port in ('x', 0, 65536, '080', '1e3', None):
            with self.subTest(port=port):
                done = self.run_view(port)
                self.assertNotEqual(done.returncode, 0, done.stderr)
                self.assertIn('has no valid port', done.stderr)


READY_SEEDED = {
    'tabs': {
        'c-a': 'b1',
        'c-b': 'b2',
        'c-c': 'b3',
        'c-long': 'b4',
        'files': 'bf',
        'git': 'bg',
        'settings': 'bs',
        'docs': 'bd',
        'run': 'br',
    },
    'long_chat': 'c-long',
    'stream_chat': 'c-long',
    'run_parent': 'c-long',
    'run': 'wf_1',
    'work': '/w',
}


class ReadyScripts(unittest.TestCase):
    """The ready predicates views.mjs splices for page.evaluate."""

    def ready(self, body: str):
        views = ROOT / 'static-src' / 'perf' / 'views.mjs'
        script = (
            f'import {{ VIEWS, instanceFacts }} from {json.dumps(views.as_uri())};'
            f'const f = instanceFacts({json.dumps(READY_SEEDED)});'
            f'const ready = Object.fromEntries(Object.entries(VIEWS).map(([k, v]) => [k, v(f).ready]));'
            f'{body}'
        )
        return json.loads(node(script))

    def test_a_spliced_path_literal_has_its_slashes_escaped(self):
        for name, src in self.ready('console.log(JSON.stringify(ready));').items():
            with self.subTest(view=name):
                self.assertNotIn('=== "/', src)
                self.assertNotIn('</', src)

    def test_an_escaped_path_literal_still_matches_its_path(self):
        got = self.ready(
            'const run = (src, pathname) => new Function("location", `return ${src}`)({ pathname });'
            'console.log(JSON.stringify([run(ready["git-prs"].split(" && ")[0], "/git/prs"),'
            ' run(ready["git-prs"].split(" && ")[0], "/git")]));'
        )
        self.assertEqual(got, [True, False])


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

    def refuse(self, script: pathlib.Path, out: str, cwd: pathlib.Path) -> None:
        done = subprocess.run(
            [sys.executable, str(script), '--out', out],
            capture_output=True,
            text=True,
            timeout=120,
            check=False,
            cwd=cwd,
        )
        self.assertEqual(done.returncode, 2, done.stderr)
        self.assertIn(os.path.abspath(cwd / out), done.stderr)

    def test_refuses_a_directory_it_did_not_write(self):
        for names in (['keep.txt'], ['manifest.json'], ['keep.txt', 'manifest.json']):
            with self.subTest(names=names), tempfile.TemporaryDirectory() as tmp:
                out = pathlib.Path(tmp) / 'notes'
                out.mkdir()
                for n in names:
                    (out / n).write_text(n)
                self.refuse(PERF / 'synth' / 'fixtures.py', 'notes', pathlib.Path(tmp))
                self.assertEqual(sorted(p.name for p in out.iterdir()), names)
                for n in names:
                    self.assertEqual((out / n).read_text(), n)

    def test_refuses_the_repositorys_static_and_internal_trees(self):
        with tempfile.TemporaryDirectory() as tmp:
            repo = pathlib.Path(tmp)
            shutil.copytree(
                PERF / 'synth',
                repo / 'tests' / 'perf' / 'synth',
                ignore=shutil.ignore_patterns('__pycache__'),
            )
            script = repo / 'tests' / 'perf' / 'synth' / 'fixtures.py'
            (repo / 'static').mkdir()
            (repo / 'static' / 'index.html').write_text('index')
            (repo / 'static' / 'manifest.json').write_text('{}')
            (repo / 'static' / '.perf-fixture').touch()
            (repo / 'internal').mkdir()
            trees = [repo / 'static', repo / 'internal']
            before = [snapshot(t) for t in trees]
            for out in ('static', 'internal/perf', 'static/../internal'):
                with self.subTest(out=out):
                    self.refuse(script, out, repo)
                    self.assertEqual([snapshot(t) for t in trees], before)

    def test_one_seed_writes_byte_identical_instances(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp) / 'x1'
            first = self.write(out, '11')
            self.assertIn('.perf-fixture', first)
            self.assertIn('manifest.json', first)
            self.assertTrue(any(p.startswith('config/chats/') for p in first))
            self.assertTrue(any(p.endswith('.git/index') for p in first))
            second = self.write(out, '11')
            self.assertEqual(first, second)
            other = self.write(out, '12')
            self.assertNotEqual(first, other)

    def test_emit_py_ends_on_a_done_line_naming_its_pid(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = pathlib.Path(tmp) / 'x1'
            self.write(out, '11')
            done = subprocess.run(
                [str(out / 'work' / 'emit.py'), '3'],
                capture_output=True,
                text=True,
                check=True,
                timeout=30,
            )
            lines = done.stdout.splitlines()
            self.assertEqual(len(lines), 4, lines)
            self.assertIn('000002', lines[2])
            self.assertRegex(lines[3], r'^emit\.py done \d+$')


class Shim(unittest.TestCase):
    """tests/perf/instance/git, the instance's git, behind marotte's own argv prefix."""

    def run_shim(self, cwd: pathlib.Path, tmp: pathlib.Path, *args: str):
        argv = tmp / 'argv'
        stub = tmp / 'real-git'
        stub.write_text('#!/usr/bin/env bash\nprintf "%s\\n" "$@" >"$STUB_ARGV"\n')
        stub.chmod(0o755)
        log = tmp / 'git.jsonl'
        env = {**os.environ, 'PERF_GIT_REAL': str(stub), 'PERF_GIT_LOG': str(log)}
        env['STUB_ARGV'] = str(argv)
        subprocess.run(
            [str(PERF / 'instance' / 'git'), *args], cwd=cwd, env=env, check=True, timeout=30
        )
        lines = log.read_text().splitlines() if log.exists() else []
        return lines, argv.read_text().splitlines()

    def test_logs_a_fetch_behind_the_config_prefix_and_runs_git_unchanged(self):
        args = ('-c', 'core.fsmonitor=', '-c', 'core.quotePath=false', 'fetch', '--quiet')
        with tempfile.TemporaryDirectory() as tmp:
            repo = pathlib.Path(os.path.realpath(tmp)) / 'repo "q" \\b'
            repo.mkdir()
            lines, argv = self.run_shim(repo, pathlib.Path(tmp), *args)
            self.assertEqual(len(lines), 1, lines)
            entry = json.loads(lines[0])
            self.assertEqual((entry['op'], entry['dir']), ('fetch', str(repo)))
            self.assertIsInstance(entry['t'], float)
            self.assertEqual(argv, list(args))

    def test_logs_nothing_for_another_subcommand(self):
        args = ('-c', 'fetch', 'status', '--porcelain')
        with tempfile.TemporaryDirectory() as tmp:
            lines, argv = self.run_shim(pathlib.Path(tmp), pathlib.Path(tmp), *args)
            self.assertEqual(lines, [])
            self.assertEqual(argv, list(args))


class FakeEngine(unittest.TestCase):
    """tests/perf/synth/fake-kiro-cli.py, the instance's kiro-cli, on one ACP request."""

    def frames(self, request: dict, count: int) -> list[dict]:
        proc = subprocess.run(
            [sys.executable, str(PERF / 'synth' / 'fake-kiro-cli.py'), 'acp'],
            input=json.dumps(request) + '\n',
            capture_output=True,
            text=True,
            check=True,
            timeout=30,
        )
        lines = proc.stdout.splitlines()
        self.assertEqual(len(lines), count, lines)
        return [json.loads(line) for line in lines]

    def test_a_steer_is_held_as_kas_holds_one_mid_turn(self):
        params = {'sessionId': 'sess_1', 'messageId': 'm-1', 'message': 'also check the spacing'}
        update, answer = self.frames(
            {'jsonrpc': '2.0', 'id': 7, 'method': '_session/steer', 'params': params}, 2
        )
        self.assertEqual(update['method'], 'session/update')
        self.assertEqual(update['params']['sessionId'], 'sess_1')
        info = update['params']['update']
        self.assertEqual(info['sessionUpdate'], 'session_info_update')
        kiro = info['_meta']['kiro']
        self.assertEqual(
            (kiro['kind'], kiro['messageId'], kiro['content']),
            ('steering_queued', 'steer-m-1', 'also check the spacing'),
        )
        self.assertEqual(answer, {'jsonrpc': '2.0', 'id': 7, 'result': {'queued': True}})


UNSETTLED = {'settled': False, 'ms': 60000, 'inflight': 2, 'path': '/files', 'ready': True}


class Failures(unittest.TestCase):
    """static-src/perf/failures.mjs: what makes view.mjs exit 1 for one window."""

    def failures(self, name: str, window: dict) -> list[str]:
        module = ROOT / 'static-src' / 'perf' / 'failures.mjs'
        script = (
            f'import {{ windowFailures }} from {json.dumps(module.as_uri())};'
            f'console.log(JSON.stringify(windowFailures({json.dumps(name)}, '
            f'{json.dumps(window)})));'
        )
        return json.loads(node(script))

    def test_an_unsettled_tab_switch_fails_the_away_window(self):
        got = self.failures(
            'away',
            {'tabSwitch': UNSETTLED, 'settle': {'settled': True}, 'drive': {'done': True}},
        )
        self.assertEqual(len(got), 1, got)
        self.assertTrue(got[0].startswith('away: the tab switch did not settle'), got)
        self.assertIn('/files', got[0])
        self.assertIn('2 reads in flight', got[0])

    def test_a_window_that_settled_and_finished_its_drive_has_no_failure(self):
        settled = {'settled': True, 'ms': 2600}
        window = {'tabSwitch': settled, 'settle': settled, 'drive': {'done': True}}
        self.assertEqual(self.failures('away', window), [])

    def test_an_unsettled_window_and_an_unfinished_drive_are_each_named(self):
        got = self.failures('live', {'settle': UNSETTLED, 'drive': {'done': False}})
        self.assertEqual(len(got), 2, got)
        self.assertTrue(got[0].startswith('live: did not settle'), got)
        self.assertEqual(got[1], 'live: the driver did not finish')

    def test_an_unfinished_drive_names_why(self):
        drive = {'done': False, 'error': 'no emit.py done line in 300 s'}
        got = self.failures('away', {'settle': {'settled': True}, 'drive': drive})
        self.assertEqual(got, ['away: the driver did not finish (no emit.py done line in 300 s)'])


def scroll_frame(lines: list[str]) -> bytes:
    """A web-terminal-engine scroll frame (docs/wire-protocol.md): type 1, inputAck, first_index,
    then each line as one default-coloured run."""
    out = bytearray([1]) + (0).to_bytes(8, 'little') + (49990).to_bytes(8, 'little')
    out += len(lines).to_bytes(2, 'little')
    for line in lines:
        text = line.encode()
        out += (1).to_bytes(2, 'little') + len(text).to_bytes(2, 'little') + text
        out += (-1).to_bytes(4, 'little', signed=True) * 2 + (0).to_bytes(2, 'little')
        out += (-1).to_bytes(4, 'little', signed=True) + (0).to_bytes(2, 'little')
    return bytes(out)


class ShellEnd(unittest.TestCase):
    """static-src/perf/shell-end.mjs: the line that ends a shell driver's command."""

    def pids(self, frame: bytes | str) -> list[str]:
        module = ROOT / 'static-src' / 'perf' / 'shell-end.mjs'
        payload = (
            json.dumps(frame)
            if isinstance(frame, str)
            else f'Buffer.from({json.dumps(frame.hex())}, "hex")'
        )
        script = (
            f'import {{ donePids }} from {json.dumps(module.as_uri())};'
            f'console.log(JSON.stringify(donePids({payload})));'
        )
        return json.loads(node(script))

    def test_a_resumed_sockets_tail_frame_names_the_done_pid(self):
        frame = scroll_frame(['049998 INFO request handled', '049999 INFO', 'emit.py done 4242'])
        self.assertEqual(self.pids(frame), ['4242'])

    def test_the_typed_commands_echo_names_no_pid(self):
        self.assertEqual(self.pids(scroll_frame(['$ sleep 3 && ./emit.py 50000'])), [])
        self.assertEqual(self.pids('sleep 3 && ./emit.py 50000'), [])


def hello(keepalive_event: str) -> str:
    """go/sse/v2's handshake frame (Hello, verdict.go) naming the hub's keepalive event."""
    data = {'epoch': 'e1', 'verdict': 'fresh', 'keepalive_event': keepalive_event, 'wire': 1}
    return f'event: sse:hello\ndata: {json.dumps(data)}\n\n'


class SseCapture(unittest.TestCase):
    """tests/perf/measure/sse-capture.py: the records it writes for a chunked event stream."""

    def capture(self, chunks: list[str], records: int, close: bool = False) -> list[dict]:
        held = threading.Event()

        class Stream(http.server.BaseHTTPRequestHandler):
            # HTTP/1.1 chunked, as net/http serves a stream with no Content-Length.
            protocol_version = 'HTTP/1.1'

            def do_GET(self):
                self.send_response(200)
                self.send_header('Content-Type', 'text/event-stream')
                self.send_header('Transfer-Encoding', 'chunked')
                if close:
                    self.send_header('Connection', 'close')
                self.end_headers()
                for c in chunks:
                    data = c.encode()
                    self.wfile.write(b'%x\r\n%s\r\n' % (len(data), data))
                    self.wfile.flush()
                held.wait(30)

            def log_message(self, *args):
                pass

        srv = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Stream)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                out, stop, scope = (pathlib.Path(tmp, n) for n in ('sse.jsonl', 'stop', 'scope'))
                scope.write_text('null')
                proc = subprocess.Popen(
                    [
                        sys.executable,
                        str(PERF / 'measure' / 'sse-capture.py'),
                        *('--port', str(srv.server_address[1]), '--out', str(out)),
                        *('--scope-file', str(scope), '--stop-file', str(stop)),
                    ]
                )
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline and proc.poll() is None:
                    if out.exists() and out.read_text().count('\n') >= records:
                        break
                    time.sleep(0.05)
                stop.touch()
                try:
                    rc = proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                    self.fail('the stop file did not end the capture')
                self.assertEqual(rc, 0)
                return [json.loads(line) for line in out.read_text().splitlines()]
        finally:
            held.set()
            srv.shutdown()
            srv.server_close()

    def marks(self, chunks: list[str], records: int) -> list[tuple[str, bool]]:
        return [(r['event'], r.get('keepalive', False)) for r in self.capture(chunks, records)]

    def test_marks_the_keepalive_the_hello_names_and_no_other_frame(self):
        chunks = [
            hello('beat'),
            'event: beat\ndata: {}\n\n',
            'event: heartbeat\ndata: {}\n\n',
            'data: {"type":"chat"}\n\n',
        ]
        self.assertEqual(
            self.marks(chunks, 4),
            [('sse:hello', False), ('beat', True), ('heartbeat', False), ('message', False)],
        )

    def test_a_hello_naming_the_comment_form_marks_no_frame(self):
        chunks = [hello(''), ': keepalive\n\n', 'data: {"type":"chat"}\n\n']
        self.assertEqual(self.marks(chunks, 2), [('sse:hello', False), ('message', False)])

    def test_a_frame_split_across_chunks_is_one_record(self):
        chunks = [hello('heartbeat'), 'event: chat\n', 'data: {"type":"turn","chat_id":"c1"}\n\n']
        got = self.capture(chunks, 2)
        self.assertEqual(len(got), 2, got)
        self.assertEqual(
            (got[1]['event'], got[1].get('type'), got[1].get('chat_id')), ('chat', 'turn', 'c1')
        )

    def test_the_stop_file_ends_a_stream_that_closes_its_connection(self):
        # http.client hands such a connection to the response and clears conn.sock.
        got = self.capture([hello('heartbeat'), 'data: {}\n\n'], 2, close=True)
        self.assertEqual([r['event'] for r in got], ['sse:hello', 'message'])


class StreamFrames(unittest.TestCase):
    """static-src/perf/stream-frames.mjs: the sse_frames count of one window."""

    def count(self, records: list[dict]) -> int:
        module = ROOT / 'static-src' / 'perf' / 'stream-frames.mjs'
        script = (
            f'import {{ streamFrames }} from {json.dumps(module.as_uri())};'
            f'console.log(JSON.stringify(streamFrames({json.dumps(records)})));'
        )
        return json.loads(node(script))

    def test_counts_no_keepalive_and_no_handshake(self):
        records = [
            {'event': 'sse:hello'},
            {'event': 'heartbeat', 'keepalive': True},
            {'event': 'message', 'type': 'chat'},
            {'event': 'sse:keepalive', 'keepalive': True},
        ]
        self.assertEqual(self.count(records), 1)

    def test_counts_an_event_the_hello_did_not_name_as_its_keepalive(self):
        self.assertEqual(self.count([{'event': 'heartbeat'}]), 1)


if __name__ == '__main__':
    unittest.main()
