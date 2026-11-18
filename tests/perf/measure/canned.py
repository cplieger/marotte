#!/usr/bin/env python3
"""record-canned.sh's recording as static-src/perf/canned/: answers/ (the first 2xx answer per
URL and request body, with the wire decoder canned-routes.json names for its route), frames/ (the chat-stream live
window's frame types, sizes and instants; the connect's hello and frames; each driver's stream
with its payloads, per page scope) and facts.json (the seeded ids). Writes nothing
for a route canned-routes.json does not name or a set over MAX_BYTES, the twin of
CANNED_MAX_BYTES in static-src/__test-helpers__/canned.ts, which owns its why.
Usage: canned.py --recorded DIR --instance DIR --plan PLAN --routes ROUTES --out DIR
"""

import argparse
import hashlib
import json
import pathlib
import re
import shutil
import sys
from collections.abc import Callable
from urllib.parse import quote, urlsplit

MAX_BYTES = 5 * 1024 * 1024
FACTS = (
    'run',
    'long_chat',
    'stream_chat',
    'stream_session',
    'run_parent',
    'tabs',
    'shell_lines',
    'work',
)
INSTANCE_PATH = '/perf'
# Every scenario ran its own server process, each with its own epoch and its own forge account
# id; the canned server is one process with one account.
EPOCH = 'ca0000000000ca00'
ACCOUNT = 'CA000000000000000000000000'
FORGE_POLL = 'forge_inventory'
FORGE_CHURN = 'forge-churn'


class CannedError(Exception):
    pass


def compact(v) -> str:
    return json.dumps(v, separators=(',', ':'), ensure_ascii=False)


# The fake forge's id carries the run's port; perf/route-of.ts names it {forge} the same way. The
# excluded segments are internal/forges/http.go's own routes, not ids.
FORGE_ID = re.compile(r'^(\w+ /api/forges/)(?!(?:inventory|oauth|login|detect)(?:/|$|\?))[^/?]+')


def route_key(route: str) -> str:
    return FORGE_ID.sub(r'\1{forge}', route)


def rewriter(inst: str, swaps: dict[str, str]) -> Callable[[str], str]:
    """A recorded text as the canned server answers it: the instance dir as INSTANCE_PATH, raw,
    percent-encoded as a query value carries it, or root-relative as the docs list's paths are, and
    each recorded per-process value as its canned one (swaps)."""

    def fix(text: str) -> str:
        text = text.replace(inst, INSTANCE_PATH)
        text = text.replace(quote(inst, safe=''), quote(INSTANCE_PATH, safe=''))
        text = text.replace(inst.lstrip('/'), INSTANCE_PATH.lstrip('/'))
        for recorded, canned in swaps.items():
            text = text.replace(recorded, canned)
        return text

    return fix


def epochs_of(recorded: pathlib.Path, scenarios: list[str]) -> set[str]:
    """The epoch each scenario's stream hello named."""
    out: set[str] = set()
    for s in scenarios:
        for line in (recorded / f'{s}.json.sse.jsonl').read_text().splitlines():
            r = json.loads(line) if line.strip() else {}
            if r.get('event') == 'sse:hello' and 'data' in r:
                out.add(json.loads(r['data'])['epoch'])
    return out


def accounts_of(recorded: pathlib.Path, scenarios: list[str]) -> set[str]:
    """The forge account ids each scenario's forge list answered."""
    out: set[str] = set()
    for s in scenarios:
        for line in (recorded / f'{s}.answers.jsonl').read_text().splitlines():
            a = json.loads(line) if line.strip() else {}
            if a.get('route') != 'GET /api/forges' or not a.get('body'):
                continue
            body = json.loads(a['body'])
            if isinstance(body, dict):
                out |= {f['account'] for f in body.get('forges') or [] if f.get('account')}
    return out


def answer_name(route: str, url: str, request) -> str:
    slug = re.sub(r'[^A-Za-z0-9]+', '-', route).strip('-')
    key = url if request is None else f'{url} {compact(request)}'
    return f'{slug}.{hashlib.sha256(key.encode()).hexdigest()[:10]}.json'


def request_of(body: str | None):
    """A request body as the canned server matches it: JSON parsed with every op_id dropped,
    since the client mints one per gesture; None for no body."""
    if body is None:
        return None
    try:
        parsed = json.loads(body)
    except ValueError:
        return body

    def drop(v):
        if isinstance(v, dict):
            return {k: drop(x) for k, x in sorted(v.items()) if k != 'op_id'}
        if isinstance(v, list):
            return [drop(x) for x in v]
        return v

    return drop(parsed)


def assemble(
    recorded: pathlib.Path, fix: Callable[[str], str], scenarios: list[str], routes: dict
) -> dict:
    """The canned set as {relative path: text}; raises CannedError on an unnamed route."""
    out: dict[str, str] = {}
    seen: set[tuple[str, str]] = set()
    unknown: set[str] = set()
    for s in scenarios:
        f = recorded / f'{s}.answers.jsonl'
        if not f.exists():
            raise CannedError(f'no recording of {s} ({f})')
        for line in f.read_text().splitlines():
            if not line.strip():
                continue
            a = json.loads(line)
            if not isinstance(a['status'], int) or not 200 <= a['status'] < 300:
                continue
            u = urlsplit(a['url'])
            rel = fix(u.path + (f'?{u.query}' if u.query else ''))
            raw = a.get('request')
            request = request_of(None if raw is None else fix(raw))
            if (a['method'], rel, compact(request)) in seen:
                continue
            seen.add((a['method'], rel, compact(request)))
            route = route_key(a['route'])
            if route not in routes:
                unknown.add(route)
                continue
            body = None if a.get('body') is None else json.loads(fix(a['body']))
            doc = {'route': route, 'url': rel, 'decoder': routes[route], 'body': body}
            if request is not None:
                doc['request'] = request
            if a['status'] != 200:
                doc['status'] = a['status']
            out['answers/' + answer_name(route, rel, request)] = compact(doc) + '\n'
    if unknown:
        raise CannedError('canned-routes.json names no decoder for ' + ', '.join(sorted(unknown)))
    return out


def stream_frames(records: list[dict], start: int, end: int, keep_poll: bool) -> list[dict]:
    """The application frames between start and end, each with its id and its envelope parsed,
    at ms from start: the hub's own sse: frames and keepalives left out, and the forge's own poll
    unless keep_poll, since that poll lands in a window by chance."""
    return [
        {'at': r['t'] - start}
        | ({'id': r['id']} if 'id' in r else {})
        | {'envelope': json.loads(r['data'])}
        for r in records
        if start <= r['t'] <= end
        and not r['event'].startswith('sse:')
        and not r.get('keepalive')
        and r.get('type')
        and (keep_poll or r['type'] != FORGE_POLL)
    ]


def scope_at(doc: dict, t: int) -> list | None:
    """The page's stream topics at t (None: everything), from view.mjs's scope record."""
    held: list | None = []
    for s in doc['scopes']:
        if s['t'] <= t:
            held = s['topics']
    return held


def streams(
    recorded: pathlib.Path, fix: Callable[[str], str], plan: dict, scenarios: list[str]
) -> dict:
    """frames/connect.json (the first scenario's hello, its epoch ms, and the frames its stream
    carried through the cold window) and one frames/stream.*.json per window, driver, page scope
    and the chats its frames name: the first in plan order that ran it."""
    out: dict[str, str] = {}
    taken: set[tuple[str, str]] = set()
    for s in scenarios:
        doc = json.loads((recorded / f'{s}.json').read_text())
        records = [
            json.loads(fix(line))
            for line in (recorded / f'{s}.json.sse.jsonl').read_text().splitlines()
            if line.strip()
        ]
        if 'frames/connect.json' not in out:
            out['frames/connect.json'] = compact(connect(s, doc, records)) + '\n'
        for window in ('live', 'away'):
            w = doc['windows'].get(window)
            driver = plan['scenarios'][s]['drivers'].get(window)
            if w is None or driver is None:
                continue
            topics = scope_at(doc, w['end'])
            frames = stream_frames(records, w['start'], w['end'], driver == FORGE_CHURN)
            chats = sorted({f['envelope'].get('chat_id') or '' for f in frames} - {''})
            key = (window, driver, compact(topics), compact(chats))
            if key in taken:
                continue
            taken.add(key)
            stream = {'driver': driver, 'scenario': s, 'window': window, 'topics': topics}
            name = f'frames/stream.{driver}.{s}-{window}.json'
            out[name] = compact(stream | {'frames': frames}) + '\n'
    return out


def connect(scenario: str, doc: dict, records: list[dict]) -> dict:
    hello = next((r for r in records if r['event'] == 'sse:hello'), None)
    if hello is None or 'data' not in hello:
        raise CannedError(f'the {scenario} stream recorded no hello payload')
    cold = doc['windows']['cold']
    frames = stream_frames(records, hello['t'], cold['end'], False)
    return {
        'scenario': scenario,
        'at': hello['t'],
        'hello': json.loads(hello['data']),
        'frames': frames,
    }


def live_frames(doc: dict, records: list[dict]) -> list[dict]:
    """The frames the page's stream carried in the live window, as frame-batch replays them:
    the hub's own sse: frames and the keepalive left out, as stream-frames.mjs counts."""
    w = doc['windows']['live']
    shown = [
        r
        for r in records
        if w['start'] <= r['t'] <= w['end']
        and not r['event'].startswith('sse:')
        and not r.get('keepalive')
        and r.get('type')
    ]
    if not shown:
        raise CannedError('the chat-stream live window carried no frame')
    t0 = shown[0]['t']
    return [{'at': r['t'] - t0, 'event': r['type'], 'bytes': r['bytes']} for r in shown]


def build(recorded: pathlib.Path, inst: str, plan: dict, routes: dict) -> dict:
    browser = [s for s in plan['scenarios'] if (recorded / f'{s}.answers.jsonl').exists()]
    missing = [s for s in plan['scenarios'] if s != 'run-drive' and s not in browser]
    if missing:
        raise CannedError('no recording of ' + ', '.join(missing))
    swaps = dict.fromkeys(epochs_of(recorded, browser), EPOCH)
    swaps |= dict.fromkeys(accounts_of(recorded, browser), ACCOUNT)
    fix = rewriter(inst, swaps)
    out = assemble(recorded, fix, browser, routes)
    doc = json.loads((recorded / 'chat-stream.json').read_text())
    records = [
        json.loads(l)
        for l in (recorded / 'chat-stream.json.sse.jsonl').read_text().splitlines()
        if l.strip()
    ]
    out['frames/chat-stream-live.json'] = compact({'frames': live_frames(doc, records)}) + '\n'
    out |= streams(recorded, fix, plan, browser)
    seeded = json.loads(pathlib.Path(inst, 'seeded.json').read_text())
    out['facts.json'] = fix(json.dumps({k: seeded[k] for k in FACTS}, indent=1)) + '\n'
    total = sum(len(t.encode()) for t in out.values())
    if total > MAX_BYTES:
        raise CannedError(f'the canned set is {total} bytes, over the {MAX_BYTES} cap')
    return out


def write(out: dict, dest: pathlib.Path) -> None:
    for sub in ('answers', 'frames'):
        shutil.rmtree(dest / sub, ignore_errors=True)
    for rel, text in out.items():
        p = dest / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)


def main() -> int:
    ap = argparse.ArgumentParser()
    for a in ('recorded', 'instance', 'plan', 'routes', 'out'):
        ap.add_argument('--' + a, required=True)
    args = ap.parse_args()
    try:
        out = build(
            pathlib.Path(args.recorded),
            str(pathlib.Path(args.instance).resolve()),
            json.loads(pathlib.Path(args.plan).read_text()),
            json.loads(pathlib.Path(args.routes).read_text()),
        )
    except CannedError as e:
        print(f'canned.py: {e}', file=sys.stderr)
        return 1
    write(out, pathlib.Path(args.out))
    print(f'canned.py: wrote {len(out)} files to {args.out}')
    return 0


if __name__ == '__main__':
    sys.exit(main())
