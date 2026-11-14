#!/usr/bin/env python3
"""Write one whole synthetic marotte instance, every byte generated here.

--scale 1 models the documented real sizes; --scale 10 multiplies each view's own axis. Records
are dated back from --now, so one seed and anchor write the same bytes while a run's records stay
inside the chat and run retention windows.

Layout under --out:
  config/        KIRO_CONFIG_DIR: chats/<id>/{chat.json,entries.jsonl}, tabs.json, runs.json,
                 runs/<wid>/, mcp.json, launch-rules.json
  home/.kiro/    KIRO_HOME (empty)
  work/          KIRO_WORK_DIR: tree/ (files), repo-NN/ (git Changes), .kiro/ (docs), emit.py (shell)
  engine/        the fake engine's data: fake-runs.json, recipes.json, inspect/<wid>.json,
                 sessions.json
  forge/         forge.json, the fake Gitea's repositories and pull requests
  manifest.json  what was written, with counts and bytes
  .perf-fixture  the empty marker that lets a re-run empty --out

An existing --out is emptied first only when it holds .perf-fixture; any other non-empty
directory, and any --out inside the repository's static/ or internal/ trees, is refused with
exit 2.

Usage: fixtures.py --out DIR [--scale N] [--seed S] [--now UNIX_SECONDS] [--forge-base URL]
"""

import argparse
import base64
import hashlib
import json
import os
import random
import shutil
import subprocess
import sys
import time

import recipes

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.realpath(os.path.join(HERE, '..', '..', '..'))
MARKER = '.perf-fixture'
# Three days back keeps every record inside the 7-day chat and run retention default.
BACKDATE_MS = 3 * 86400 * 1000
MODELS = ['model-a', 'model-b', 'model-c']

WORDS = [
    'the',
    'server',
    'reads',
    'each',
    'chat',
    'log',
    'once',
    'and',
    'the',
    'client',
    'renders',
    'every',
    'row',
    'it',
    'receives',
    'so',
    'a',
    'long',
    'transcript',
    'costs',
    'what',
    'the',
    'log',
    'holds',
    'rather',
    'than',
    'what',
    'the',
    'screen',
    'shows',
    'today',
    'state',
    'store',
    'event',
    'frame',
    'budget',
    'cursor',
    'index',
    'patch',
    'review',
    'gate',
    'lane',
    'step',
    'build',
    'test',
    'lint',
    'deadset',
    'coverage',
    'branch',
]


class R:
    def __init__(self, seed, t0):
        self.r = random.Random(seed)
        self.t0 = t0

    def words(self, n):
        return ' '.join(self.r.choice(WORDS) for _ in range(n))

    def para(self, lo, hi):
        s = self.words(self.r.randint(lo, hi))
        return s[0].upper() + s[1:] + '.'

    def hexid(self, n=32):
        return f'{self.r.getrandbits(n * 4):0{n}x}'

    def uuid(self):
        h = self.hexid(32)
        return f'{h[:8]}-{h[8:12]}-4{h[13:16]}-8{h[17:20]}-{h[20:32]}'


def dump(path, obj, mode=None):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, 'w') as f:
        json.dump(obj, f, separators=(',', ':'))
    if mode:
        os.chmod(path, mode)


def load(path):
    with open(path) as f:
        return json.load(f)


# ---- chats ---------------------------------------------------------------------------------


def markdown_answer(rg):
    """A text entry with the shapes the markdown renderer handles: heading, list, code fence."""
    parts = ['## ' + rg.words(4).capitalize(), rg.para(30, 60)]
    parts.append('\n'.join('- ' + rg.para(6, 14) for _ in range(rg.r.randint(2, 5))))
    if rg.r.random() < 0.6:
        handler = rg.r.randrange(1000)
        body = ''.join(f'\tx{k} := read(r)\n' for k in range(rg.r.randint(3, 12)))
        parts.append(
            f'```go\nfunc handle{handler}(w http.ResponseWriter, r *http.Request) {{\n{body}}}\n```'
        )
    parts.append(rg.para(20, 50))
    return '\n\n'.join(parts)


TOOLS = [
    ('read', 'Read File', 0.45),
    ('execute', 'Shell', 0.25),
    ('search', 'Grep', 0.15),
    ('edit', 'Edit File', 0.15),
]


def tool_shape(rg, k):
    x = rg.r.random()
    acc = 0.0
    for kind, title, p in TOOLS:
        acc += p
        if x <= acc:
            break
    path = f'/workspace/app/internal/pkg{rg.r.randrange(40):02d}/file{k % 997:03d}.go'
    if kind == 'read':
        inp = {'path': path}
        out = 'package pkg\n\n' + ''.join(
            f'// {rg.words(8)}\nfunc f{i}() int {{ return {i} }}\n'
            for i in range(rg.r.randint(20, 60))
        )
    elif kind == 'execute':
        inp = {
            'command': f'cd /workspace/app && go test ./internal/pkg{rg.r.randrange(40):02d}/...'
        }
        out = (
            ''.join(
                f'=== RUN   TestCase{i}\n--- PASS: TestCase{i} (0.0{i % 9}s)\n'
                for i in range(rg.r.randint(10, 40))
            )
            + 'PASS\nok  \tapp/internal\t0.412s\n'
        )
    elif kind == 'search':
        inp = {'pattern': rg.words(2), 'path': '/workspace/app'}
        out = ''.join(f'{path}:{i * 7}: {rg.words(9)}\n' for i in range(rg.r.randint(5, 30)))
    else:
        inp = {'path': path, 'oldStr': rg.words(12), 'newStr': rg.words(14)}
        out = 'Edited ' + path
    return kind, title, inp, out, [{'path': path}]


def chat_log(rg, cid, turns, per_turn, live_open=False):
    """Entries for a chat of `turns` turns of about `per_turn` entries each, in the documented
    kind mix (tool_call 55%, tool_result 23%, thinking 14%, text 2%, steer 3%, plus the turn's
    open and close)."""
    log = []
    clock = rg.t0
    for t in range(1, turns + 1):
        tid = f't-{cid[2:10]}-{t:05d}'
        seq = 0
        clock += 60000

        def add(kind, payload, eid=None, tid=tid):
            nonlocal seq, clock
            clock += rg.r.randint(200, 4000)
            log.append(
                {
                    'id': eid or f'{tid}-{seq}',
                    'turn': tid,
                    'kind': kind,
                    'payload': payload,
                    'seq': seq,
                    'ts': clock,
                }
            )
            seq += 1

        prompt = {'id': f'm-{rg.hexid(16)}', 'text': rg.para(20, 120)}
        add('turn_open', {'prompt': prompt, 'source': 'prompt', 'n': t})
        calls = []
        for _ in range(max(1, per_turn - 2)):
            x = rg.r.random()
            if x < 0.55:
                kind, title, inp, out, locs = tool_shape(rg, len(log))
                call = f'toolu_{rg.hexid(8)}_{len(log):05d}'
                inline = rg.r.random() >= 0.42  # 23 of 55 calls carry a separate result entry
                p = {
                    'id': call,
                    'title': title,
                    'kind': kind,
                    'status': 'completed' if inline else 'pending',
                    'input': inp,
                    'locations': locs,
                    'ts': clock,
                }
                if inline:
                    p['output'] = out[:1500]
                    p['duration_ms'] = rg.r.randint(20, 9000)
                add('tool_call', p, eid=call)
                if not inline:
                    calls.append((call, title, kind, out))
            elif x < 0.78 and calls:
                call, title, kind, out = calls.pop(0)
                result = {
                    'title': title,
                    'kind': kind,
                    'status': 'completed',
                    'output': out,
                    'output_bytes': len(out),
                    'duration_ms': rg.r.randint(20, 9000),
                }
                add('tool_result', result, eid=call + ':result')
            elif x < 0.92:
                text = '\n\n'.join(rg.para(20, 70) for _ in range(rg.r.randint(1, 3)))
                add('thinking', {'text': text})
            elif x < 0.95:
                steer = {
                    'text': rg.para(6, 30),
                    'origin': 'user',
                    'state': 'read',
                    'read_ts': clock,
                    'produced_ts': clock - 900,
                }
                add('steer', steer)
            else:
                add('text', {'text': markdown_answer(rg)})
        for call, title, kind, out in calls:
            result = {
                'title': title,
                'kind': kind,
                'status': 'completed',
                'output': out,
                'output_bytes': len(out),
            }
            add('tool_result', result, eid=call + ':result')
        add('text', {'text': markdown_answer(rg)})
        if live_open and t == turns:
            break  # the stream chat's last turn stays open; the live stream continues it
        close = {
            'outcome': 'completed',
            'stop_reason_raw': 'end_turn',
            'model': MODELS[0],
            'credits': round(rg.r.uniform(0.5, 12), 2),
            'elapsed_ms': rg.r.randint(20000, 900000),
        }
        add('turn_close', close, eid=tid + ':close')
    return log, clock


def write_chat(cfg, rg, cid, name, sid, turns, per_turn, live_open=False):
    log, clock = chat_log(rg, cid, turns, per_turn, live_open)
    d = os.path.join(cfg, 'chats', cid)
    os.makedirs(d, exist_ok=True)
    with open(os.path.join(d, 'entries.jsonl'), 'w') as f:
        f.writelines(json.dumps(e, separators=(',', ':')) + '\n' for e in log)
    hdr = {
        'name': name,
        'model': MODELS[0],
        'acp_session_id': sid,
        'current_mode_id': 'default-v2',
        'id': cid,
        'usage': {
            'credits': round(turns * 3.1, 2),
            'has_real_data': True,
            'context_pct': 41.0,
            'context_size': 200000,
            'last_turn_ms': 120000,
        },
        'created_at': rg.t0,
        'updated_at': clock,
        'turn_count': turns,
        'last_turn_outcome': 'completed',
    }
    dump(os.path.join(d, 'chat.json'), hdr)
    return {
        'id': cid,
        'name': name,
        'session': sid,
        'turns': turns,
        'entries': len(log),
        'bytes': os.path.getsize(os.path.join(d, 'entries.jsonl')),
    }


# ---- files, git, docs, shell ---------------------------------------------------------------


def write_tree(work, rg, files, folders, listed):
    """tree/big holds `listed` entries (the listed folder); the rest spread over `folders`."""
    base = os.path.join(work, 'tree')
    big = os.path.join(base, 'big')
    os.makedirs(big, exist_ok=True)
    for i in range(listed):
        ext = ('go', 'ts', 'md', 'json')[i % 4]
        with open(os.path.join(big, f'entry-{i:05d}.{ext}'), 'w') as f:
            f.write(f'// {rg.words(20)}\n')
    for i in range(files - listed):
        d = os.path.join(base, f'dir-{i % folders:04d}')
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, f'file-{i:05d}.go'), 'w') as f:
            f.write(f'package dir\n\n// {rg.words(25)}\n')
    with open(os.path.join(base, 'large.log'), 'w') as f:
        for i in range(12000):
            f.write(
                f'2026-11-02T18:{i // 60 % 60:02d}:{i % 60:02d} INFO request id={i} {rg.words(8)}\n'
            )


def git(cwd, *args):
    env = {
        'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
        'HOME': cwd,
        'GIT_CONFIG_NOSYSTEM': '1',
        'GIT_AUTHOR_NAME': 'fixture',
        'GIT_AUTHOR_EMAIL': 'fixture@example.invalid',
        'GIT_COMMITTER_NAME': 'fixture',
        'GIT_COMMITTER_EMAIL': 'fixture@example.invalid',
        'GIT_AUTHOR_DATE': '2026-11-02T18:00:00+01:00',
        'GIT_COMMITTER_DATE': '2026-11-02T18:00:00+01:00',
    }
    subprocess.run(
        ['git', '-c', 'init.defaultBranch=main', '-c', 'commit.gpgsign=false', *args],
        cwd=cwd,
        env=env,
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def write_repos(work, rg, repos, changed_per, tracked, forge_base):
    names = []
    for n in range(repos):
        name = f'repo-{n:02d}'
        d = os.path.join(work, name)
        os.makedirs(os.path.join(d, 'internal'), exist_ok=True)
        for i in range(tracked):
            with open(os.path.join(d, 'internal', f'f{i:03d}.go'), 'w') as f:
                f.write('package internal\n\n' + ''.join(f'// {rg.words(10)}\n' for _ in range(30)))
        git(d, 'init', '-q')
        git(d, 'add', 'internal')
        git(d, 'commit', '-q', '-m', 'chore: initial')
        git(d, 'remote', 'add', 'origin', f'{forge_base}/fixture/{name}.git')
        for i in range(changed_per):
            m = i % 4
            if m in {0, 1}:  # modified, half of them staged
                p = os.path.join(d, 'internal', f'f{i:03d}.go')
                with open(p, 'a') as f:
                    lines = rg.r.randint(3, 40)
                    f.write(''.join(f'// change {rg.words(8)}\n' for _ in range(lines)))
                if m == 1:
                    git(d, 'add', os.path.relpath(p, d))
            elif m == 2:  # untracked
                with open(os.path.join(d, f'new-{i:03d}.ts'), 'w') as f:
                    f.write(f'export const x = {i};\n')
            else:  # deleted
                os.remove(os.path.join(d, 'internal', f'f{tracked - 1 - i:03d}.go'))
        names.append(name)
    return names


def frontmatter(**kv):
    return '---\n' + ''.join(f'{k}: {v}\n' for k, v in kv.items()) + '---\n\n'


def write_text(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, 'w') as f:
        f.write(text)


def write_docs(work, rg, steering, skills, hooks, specs, agents, prompts):
    k = os.path.join(work, '.kiro')
    for i in range(steering):
        inc = ('always', 'fileMatch', 'manual')[i % 3]
        extra = {'fileMatchPattern': f'"internal/pkg{i:02d}/**"'} if inc == 'fileMatch' else {}
        head = frontmatter(inclusion=inc, description=f'"{rg.para(10, 25)}"', **extra)
        body = f'# Doc {i}\n\n' + '\n\n'.join(rg.para(40, 90) for _ in range(6))
        write_text(os.path.join(k, 'steering', f'doc-{i:03d}.md'), head + body)
    for i in range(skills):
        head = frontmatter(name=f'skill-{i:03d}', description=f'"{rg.para(15, 30)}"')
        body = f'# Skill {i}\n\n' + '\n\n'.join(rg.para(40, 90) for _ in range(8))
        write_text(os.path.join(k, 'skills', f'skill-{i:03d}', 'SKILL.md'), head + body)
    for i in range(hooks):
        hook = {
            'name': f'hook {i}',
            'trigger': 'PostFileSave',
            'matcher': '.*\\.go$',
            'action': {'type': 'command', 'command': f'go vet ./internal/pkg{i:02d}/...'},
            'timeout': 60,
        }
        dump(os.path.join(k, 'hooks', f'hook-{i:03d}.json'), {'version': 'v1', 'hooks': [hook]})
    for i in range(specs):
        for fn in ('requirements.md', 'design.md', 'tasks.md'):
            body = f'# {fn[:-3].capitalize()} {i}\n\n'
            body += '\n\n'.join(rg.para(40, 90) for _ in range(10))
            write_text(os.path.join(k, 'specs', f'feature-{i:03d}', fn), body)
    for i in range(agents):
        head = frontmatter(
            name=f'agent-{i:03d}', description=f'"{rg.para(8, 20)}"', model=MODELS[i % 3]
        )
        write_text(os.path.join(k, 'agents', f'agent-{i:03d}.md'), head + rg.para(60, 120))
    for i in range(prompts):
        body = f'# Prompt {i}\n\n{rg.para(20, 60)}\n'
        write_text(os.path.join(k, 'prompts', f'prompt-{i:03d}.md'), body)


EMIT = r'''#!/usr/bin/env python3
"""Print N numbered lines at R lines per second (R 0: as fast as possible)."""
import sys, time
n, rate = int(sys.argv[1]), float(sys.argv[2]) if len(sys.argv) > 2 else 0.0
gap = 1.0 / rate if rate > 0 else 0.0
for i in range(n):
    sys.stdout.write("\x1b[32m%06d\x1b[0m 2026-11-02T18:00:00 INFO worker=%02d request handled path=/api/x/%d bytes=%d\n" % (i, i % 16, i, i * 37 % 9973))
    if gap:
        sys.stdout.flush(); time.sleep(gap)
sys.stdout.flush()
'''


# ---- settings ------------------------------------------------------------------------------


def write_settings(cfg, rg, mcp, rules):
    servers = []
    for i in range(mcp):
        s = {
            'id': f'srv-{i:03d}',
            'name': f'server-{i:03d}',
            'transport': 'stdio' if i % 2 == 0 else 'http',
            'created_at': rg.t0,
            'updated_at': rg.t0,
        }
        if i % 2 == 0:
            s.update({'command': '/bin/true', 'args': ['--port', str(9000 + i)]})
        else:
            s['url'] = f'https://mcp-{i:03d}.example.invalid/mcp'
        servers.append(s)
    dump(os.path.join(cfg, 'mcp.json'), {'servers': servers, 'version': 1}, mode=0o600)
    out = []
    for i in range(rules):
        digest = hashlib.sha256(b'rule%d' % i).digest()[:16]
        rid = base64.urlsafe_b64encode(digest).rstrip(b'=').decode()
        clock = {
            'freq': ('daily', 'weekly', 'hourly')[i % 3],
            'weekdays': [1, 3, 5],
            'interval': 2,
            'hour': 3 + i % 18,
            'minute': (i * 7) % 60,
        }
        out.append(
            {
                'id': rid,
                'source': f'/workspace/.kiro/workflows/recipe-{i % 7:02d}.yaml',
                'name': f'recipe-{i % 7:02d}',
                'enabled': i % 3 != 0,
                'when': {'clock': clock},
            }
        )
    dump(os.path.join(cfg, 'launch-rules.json'), out)


# ---- engine data and runs ------------------------------------------------------------------


def tiny_run(t0, wid, parent, i):
    started = time.strftime('%Y-%m-%dT%H:%M:%S.000Z', time.gmtime(t0 / 1000 + i * 60))
    step = {
        'nodeId': 'only',
        'type': 'step',
        'status': 'completed',
        'agentName': 'builder',
        'modelId': MODELS[0],
        'startedAt': started,
        'endedAt': started,
        'capturedOutput': 'done',
    }
    root = {
        'nodeId': wid,
        'type': 'sequence',
        'status': 'completed',
        'startedAt': started,
        'endedAt': started,
        'children': [step],
    }
    state = {
        'workflowId': wid,
        'workflowName': f'recipe-{i % 7:02d}',
        'status': 'completed',
        'inputs': {},
        'artifacts': {},
        'capturedOutputs': {},
        'root': root,
        'parentSessionId': parent,
        'createdAt': started,
        'runLabel': f'history run {i}',
        'planRevision': 0,
    }
    row = {
        'workflowId': wid,
        'name': f'history run {i}',
        'workflowName': f'recipe-{i % 7:02d}',
        'status': ('completed', 'failed')[i % 5 == 0],
        'createdAt': started,
        'updatedAt': started,
        'startedAt': started,
        'parentSessionId': parent,
    }
    return {'workflowId': wid, 'state': state, 'nodePlan': []}, row


def write_run(out, cfg, eng, s, now, parent):
    """gen.py's run at this scale, parented by `parent`; returns its summary and lease."""
    gen_out = os.path.join(out, '.gen')
    res = subprocess.run(
        [
            sys.executable,
            os.path.join(HERE, 'gen.py'),
            '--out',
            gen_out,
            '--scale',
            str(s),
            '--now',
            str(now),
            '--parent-session',
            parent['session'],
            '--chat',
            parent['id'],
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    run = json.loads(res.stdout)
    wid = run['workflowId']
    shutil.copy(
        os.path.join(gen_out, 'inspect', wid + '.json'),
        os.path.join(eng, 'inspect', wid + '.json'),
    )
    os.makedirs(os.path.join(cfg, 'runs', wid), exist_ok=True)
    shutil.copy(
        os.path.join(gen_out, 'runs', wid, 'entries.jsonl'),
        os.path.join(cfg, 'runs', wid, 'entries.jsonl'),
    )
    lease = load(os.path.join(gen_out, 'lease.json'))
    row = load(os.path.join(gen_out, 'row.json'))
    shutil.rmtree(gen_out)
    return run, lease, row


def session_rows(rg, chats, total):
    """session/list rows: every chat's session, then unclaimed ones up to `total`."""
    rows = []
    for i, c in enumerate(chats):
        rows.append(
            {
                'sessionId': c['session'],
                'title': c['name'],
                'updatedAt': f'2026-11-{1 + i % 9:02d}T10:00:00Z',
                '_meta': {
                    'kiro': {
                        'createdAt': '2026-10-01T10:00:00Z',
                        'agentMode': 'default-v2',
                        'status': 'idle',
                    }
                },
            }
        )
    for i in range(total - len(chats)):
        sid = 'sess_' + rg.uuid()
        title = f'Old session {i}: {rg.words(5)}'
        kiro = {
            'createdAt': '2026-01-01T10:00:00Z',
            'agentMode': 'default-v2',
            'status': ('idle', 'failed')[i % 9 == 0],
            'description': rg.para(5, 15) if i % 4 == 0 else '',
        }
        rows.append(
            {
                'sessionId': sid,
                'title': title,
                'updatedAt': f'2026-{1 + i % 10:02d}-{1 + i % 28:02d}T10:00:00Z',
                '_meta': {'kiro': kiro},
            }
        )
    return rows


def forge_data(rg, s):
    """30 pull requests over 6 repositories per x1, named like the work tree's repositories."""
    frepos = [f'repo-{i:02d}' for i in range(6 * s)]
    prs = []
    for i in range(30 * s):
        prs.append(
            {
                'repo': frepos[i % len(frepos)],
                'number': 1 + i // len(frepos),
                'title': rg.para(4, 10).rstrip('.'),
                'body': '\n\n'.join(rg.para(20, 60) for _ in range(3)),
                'draft': i % 7 == 0,
                'author': ('fixture', 'renovate[bot]', 'someone')[i % 3],
                'head': f'feat/branch-{i:03d}',
                'sha': hashlib.sha1(b'pr%d' % i, usedforsecurity=False).hexdigest(),
                'updated': f'2026-11-{1 + i % 9:02d}T10:{i % 60:02d}:00Z',
                'labels': [['dependencies'], ['feature'], []][i % 3],
            }
        )
    return {'owner': 'fixture', 'login': 'fixture', 'repos': frepos, 'prs': prs}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--out', required=True)
    ap.add_argument('--scale', type=int, default=1)
    ap.add_argument('--seed', type=int, default=11)
    ap.add_argument(
        '--now', type=int, default=None, help='the Unix seconds records are dated back from'
    )
    ap.add_argument(
        '--forge-base',
        default='http://127.0.0.1:19960',
        help="the fake forge's URL the repositories' remotes name; seed-fixture.sh re-points it",
    )
    a = ap.parse_args()
    s = a.scale
    now = a.now if a.now is not None else int(time.time())
    rg = R(a.seed * 1000 + s, now * 1000 - BACKDATE_MS)
    out = os.path.abspath(a.out)
    real = os.path.realpath(out)
    for tree in ('static', 'internal'):
        top = os.path.join(REPO, tree)
        if os.path.commonpath([real, top]) == top:
            print(f"fixtures.py: {out} is inside the repository's {tree}/ tree", file=sys.stderr)
            return 2
    if os.path.lexists(out):
        if not os.path.isdir(out) or os.path.islink(out):
            print(f'fixtures.py: {out} is not a directory', file=sys.stderr)
            return 2
        if os.listdir(out) and not os.path.isfile(os.path.join(out, MARKER)):
            print(f'fixtures.py: {out} is not empty and holds no {MARKER}', file=sys.stderr)
            return 2
        shutil.rmtree(out)
    os.makedirs(out)
    open(os.path.join(out, MARKER), 'x').close()
    cfg, home, work, eng, forge = (
        os.path.join(out, p) for p in ('config', 'home/.kiro', 'work', 'engine', 'forge')
    )
    for p in (cfg, home, work, os.path.join(eng, 'inspect'), forge):
        os.makedirs(p, exist_ok=True)
    man = {'scale': s, 'seed': a.seed, 'now': now, 'work_path': work, 'forge_base': a.forge_base}

    # One long transcript, one streaming chat with its last turn left open, the rest small.
    # Open tabs at x10 reach MaxOpenTabs 48 (internal/tabs): 42 chats, the run, 5 singletons.
    open_chats = 12 if s == 1 else 42
    chats = [
        write_chat(
            cfg, rg, 'c-' + rg.hexid(), f'Long transcript x{s}', 'sess_' + rg.uuid(), 19 * s, 147
        ),
        write_chat(
            cfg,
            rg,
            'c-' + rg.hexid(),
            f'Streaming chat x{s}',
            'sess_' + rg.uuid(),
            2 * s,
            150,
            live_open=True,
        ),
        write_chat(cfg, rg, 'c-' + rg.hexid(), f'Run parent x{s}', 'sess_' + rg.uuid(), 4, 40),
    ]
    for i in range(30 * s - 3):
        cid = 'c-' + rg.hexid()
        name = f'Chat {i:03d} {rg.words(3)}'
        sid = 'sess_' + rg.uuid()
        turns, per_turn = rg.r.randint(2, 6), rg.r.randint(15, 30)
        chats.append(write_chat(cfg, rg, cid, name, sid, turns, per_turn))
    man['chats'] = {
        'on_disk': len(chats),
        'open': open_chats,
        'long': chats[0],
        'stream': chats[1],
        'run_parent': chats[2],
        'small_entries_total': sum(c['entries'] for c in chats[3:]),
        'bytes_total': sum(c['bytes'] for c in chats),
    }

    parent = chats[2]
    run, lease, row = write_run(out, cfg, eng, s, now, parent)
    wid = run['workflowId']
    dump(os.path.join(cfg, 'runs.json'), {'leases': [lease], 'version': 1})
    rows = [row]
    hist_runs = 20 * s
    for i in range(hist_runs):
        hw = f'wf_h{s * 100000 + i:015x}'
        reply, hrow = tiny_run(rg.t0, hw, chats[3 + i % (len(chats) - 3)]['session'], i)
        dump(os.path.join(eng, 'inspect', hw + '.json'), reply)
        rows.append(hrow)
    dump(os.path.join(eng, 'fake-runs.json'), rows)
    man['run'] = run
    man['history_runs'] = hist_runs
    man['recipes'] = recipes.write(os.path.join(eng, 'recipes.json'), s)

    sessions = session_rows(rg, chats, 150 * s)
    dump(os.path.join(eng, 'sessions.json'), sessions)
    man['history_sessions'] = len(sessions)

    # The long chat first, then the stream chat, the run parent and its run, the singletons.
    tid = iter(f'b{n + 1:031x}' for n in range(1000))
    tabs = []
    for c in chats[:open_chats]:
        t = {
            'id': next(tid),
            'kind': 'chat',
            'ref': c['id'],
            'parent': '',
            'pinned': False,
            'owns': False,
        }
        tabs.append(t)
        if c is parent:
            tabs.append(
                {
                    'id': next(tid),
                    'kind': 'run',
                    'ref': wid,
                    'parent': t['id'],
                    'pinned': False,
                    'owns': False,
                }
            )
    for kind in ('git', 'files', 'docs', 'settings'):
        ref = work + '/tree/big' if kind == 'files' else ''  # a files tab's ref is its folder
        tabs.append(
            {
                'id': next(tid),
                'kind': kind,
                'ref': ref,
                'parent': '',
                'pinned': False,
                'owns': False,
            }
        )
    dump(os.path.join(cfg, 'tabs.json'), {'tabs': tabs, 'version': 1})
    ids = {t['kind'] if t['kind'] != 'chat' else t['ref']: t['id'] for t in tabs}
    man['tabs'] = {'count': len(tabs), 'ids': ids}

    write_tree(work, rg, 2000 * s, 120 * s, 300 * s)
    repos = write_repos(work, rg, 3 * s, 20, 40, a.forge_base)
    write_docs(work, rg, 40 * s, 30 * s, 10 * s, 5 * s, 10 * s, 5 * s)
    with open(os.path.join(work, 'emit.py'), 'w') as f:
        f.write(EMIT)
    os.chmod(os.path.join(work, 'emit.py'), 0o700)
    man['files'] = {
        'total': 2000 * s,
        'folders': 120 * s,
        'listed_folder': 'tree/big',
        'listed_entries': 300 * s,
    }
    man['git'] = {'repos': repos, 'changed_per_repo': 20, 'changed_total': 20 * len(repos)}
    man['docs'] = {
        'steering': 40 * s,
        'skills': 30 * s,
        'hooks': 10 * s,
        'spec_features': 5 * s,
        'agents': 10 * s,
        'prompts': 5 * s,
    }
    man['shell'] = {'lines': 5000 * s, 'command': f'./emit.py {5000 * s}'}

    write_settings(cfg, rg, 5 * s, 5 * s)
    man['settings'] = {'mcp_servers': 5 * s, 'launch_rules': 5 * s}

    fd = forge_data(rg, s)
    dump(os.path.join(forge, 'forge.json'), fd)
    man['forge'] = {'repos': len(fd['repos']), 'prs': len(fd['prs'])}

    dump(os.path.join(out, 'manifest.json'), man)
    summary = {
        'scale': s,
        'chats': man['chats']['on_disk'],
        'chat_bytes': man['chats']['bytes_total'],
        'tabs': man['tabs']['count'],
        'run_nodes': run['nodes'],
        'history': [man['history_sessions'], hist_runs],
        'files': man['files']['total'],
        'repos': len(repos),
        'prs': man['forge']['prs'],
    }
    print(json.dumps(summary))
    return 0


if __name__ == '__main__':
    sys.exit(main())
