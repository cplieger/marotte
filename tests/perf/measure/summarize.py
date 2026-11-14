#!/usr/bin/env python3
"""One measure shard's results.json, the file cplieger/ci's perf_gate.py reads, from the raw reps
under --raw: per unit `<scenario>.<scale>.<engine>.<width>`, view.mjs's `.r<n>.json` reps, the
profiled `.rp.json` rep, servercpu.py's `.r<n>.servercpu.json` replays and run.sh's `.seconds`.
Each metric of a window becomes one cell with its reps in rep order; a time metric a rep lacks,
a failed rep, or a planned window a rep did not measure is an error; a count a rep did not see
is 0 in that rep.
Usage: summarize.py --cells CELLS.json --raw DIR --out results.json
"""

import argparse
import json
import pathlib
import re
import sys

UNIT_FIELDS = ('scenario', 'scale', 'engine', 'width')
# Windows whose reads and largest answers are exact; a stream window's move by a frame at its edge.
EXACT_READ_WINDOWS = {'cold', 'rest', 'back'}
STREAM_WINDOWS = {'live', 'away'}
SETTLED_WINDOWS = {'cold', 'back'}
# Routes answering the whole run or the whole log: their server CPU is reported, never gated.
WHOLE_ROUTES = re.compile(r'^GET /api/runs/\{run\}$|^GET /api/chats/\{chat\}/(export|search)\b')
REP = re.compile(r'\.r(\d+)\.json$')


class SummaryError(Exception):
    pass


def stem(unit: dict) -> str:
    return '.'.join(unit[f] for f in UNIT_FIELDS)


def is_number(x: object) -> bool:
    return isinstance(x, int | float) and not isinstance(x, bool)


def browser_metrics(engine: str, name: str, w: dict) -> list[tuple[str, str, object]]:
    """(metric, kind, value) of one browser window; a value of None is not measured."""
    out = [('cpu_ms', 'time', w.get('cpuMs'))]
    if name in SETTLED_WINDOWS:
        out.append(('dom', 'count', w.get('viewDom')))
        out.append(('painted_ms', 'report', w.get('paintedMs')))
        if engine == 'chromium':
            out.append(('heap_mb', 'time', w.get('heapMB')))
        else:
            out.append(('rss_mb', 'time', w.get('rssMB')))
    read_kind = 'count' if name in EXACT_READ_WINDOWS else 'report'
    for route, e in w.get('routes', {}).items():
        out.append((f'reads:{route}', read_kind, e['count']))
        out.append((f'max_bytes:{route}', read_kind, e['maxBytes']))
    out.append(('kiro_cli', 'count', w.get('kiroCli')))
    out.append(('git_fetch', 'count', w.get('gitFetch')))
    if engine == 'chromium':
        out.append(('sse_frames', 'count', w.get('sseFrames')))
        out.append(('long_task_ms', 'report', w.get('longTaskMs')))
    if name in STREAM_WINDOWS:
        out.append(('frame_p95_ms', 'report', w.get('frameP95Ms')))
        if engine == 'webkit':
            out.append(('heartbeat_gap_ms', 'report', w.get('heartbeatGapMs')))
    return out


def profiled_metrics(name: str, w: dict) -> list[tuple[str, str, object]]:
    if name not in STREAM_WINDOWS:
        return []
    out = [
        ('layouts', 'report', w.get('layouts')),
        ('style_recalcs', 'report', w.get('styleRecalcs')),
    ]
    shares = (w.get('decode') or {}).get('shares', {})
    out.extend((f'decode_share:{fam}', 'report', v) for fam, v in sorted(shares.items()))
    return out


def replay_metrics(replay: dict) -> list[tuple[str, str, object]]:
    return [
        (
            f'server_cpu_ms:{route}',
            'report' if WHOLE_ROUTES.match(route) else 'server_cpu',
            e['cpu_ms'],
        )
        for route, e in replay['routes'].items()
    ]


def load(path: pathlib.Path) -> dict:
    try:
        return json.loads(path.read_text())
    except (OSError, ValueError) as e:
        raise SummaryError(f'{path}: {e}') from e


def unit_cells(unit: dict, raw: pathlib.Path) -> list[dict]:
    name = stem(unit)
    reps = sorted(
        (int(m[1]), p)
        for p in raw.glob(f'{glob_escape(name)}.r*.json')
        if (m := REP.search(p.name))
    )
    if not reps:
        raise SummaryError(f'{name}: no reps under {raw}')
    docs = [load(p) for _, p in reps]
    for (n, _), d in zip(reps, docs, strict=True):
        if d.get('failures'):
            raise SummaryError(f'{name} rep {n}: {"; ".join(d["failures"])}')
    planned = set(unit['windows']) if 'windows' in unit else None
    values: dict[tuple, list] = {}
    kinds: dict[tuple, str] = {}

    def add(window: str, metric: str, kind: str, rep: int, value: object) -> None:
        if value is None:
            return
        if not is_number(value):
            raise SummaryError(
                f'{name} rep {rep + 1}: {window} {metric} is {value!r}, not a number'
            )
        key = (window, metric)
        kinds[key] = kind
        values.setdefault(key, [None] * len(docs))[rep] = value

    for i, d in enumerate(docs):
        measured = set(d['windows'])
        if planned is not None and not planned <= measured:
            raise SummaryError(f'{name} rep {i + 1}: no {", ".join(sorted(planned - measured))}')
        for window, w in d['windows'].items():
            if unit['engine'] == 'server':
                add(window, 'cpu_ms:server', 'time', i, w.get('serverCpuMs'))
            else:
                for metric, kind, v in browser_metrics(unit['engine'], window, w):
                    add(window, metric, kind, i, v)
        replay = raw / f'{name}.r{reps[i][0]}.servercpu.json'
        if replay.exists():
            for metric, kind, v in replay_metrics(load(replay)):
                add('replay', metric, kind, i, v)
    cells = []
    for (window, metric), reps_of in sorted(values.items()):
        kind = kinds[(window, metric)]
        if kind == 'count':
            reps_of = [0 if v is None else v for v in reps_of]
        elif None in reps_of and kind == 'time':
            missing = [i + 1 for i, v in enumerate(reps_of) if v is None]
            raise SummaryError(f'{name}: {window} {metric} not measured in rep {missing}')
        measured = [v for v in reps_of if v is not None]
        if measured:
            cells.append(cell(unit, window, metric, kind, measured))
    profiled = raw / f'{name}.rp.json'
    if unit.get('profile'):
        d = load(profiled)
        if d.get('failures'):
            raise SummaryError(f'{name} profiled rep: {"; ".join(d["failures"])}')
        for window, w in sorted(d['windows'].items()):
            for metric, kind, v in profiled_metrics(window, w):
                if is_number(v):
                    cells.append(cell(unit, window, metric, kind, [v]))
    return cells


def cell(unit: dict, window: str, metric: str, kind: str, reps: list) -> dict:
    return {
        **{f: unit[f] for f in UNIT_FIELDS},
        'window': window,
        'metric': metric,
        'kind': kind,
        'reps': reps,
    }


def glob_escape(text: str) -> str:
    return re.sub(r'([*?\[])', r'[\1]', text)


def results(units: list[dict], raw: pathlib.Path) -> dict:
    cells, seconds = [], []
    for unit in units:
        cells.extend(unit_cells(unit, raw))
        spent = raw / f'{stem(unit)}.seconds'
        try:
            value = float(spent.read_text())
        except (OSError, ValueError) as e:
            raise SummaryError(f'{spent}: {e}') from e
        seconds.append({**{f: unit[f] for f in UNIT_FIELDS}, 'seconds': round(value, 1)})
    return {'schema': 1, 'cells': cells, 'seconds': seconds}


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('--cells', required=True)
    ap.add_argument('--raw', required=True)
    ap.add_argument('--out', required=True)
    a = ap.parse_args(argv)
    try:
        doc = results(json.loads(pathlib.Path(a.cells).read_text()), pathlib.Path(a.raw))
    except SummaryError as e:
        print(f'summarize.py: {e}', file=sys.stderr)
        return 1
    pathlib.Path(a.out).write_text(json.dumps(doc) + '\n')
    return 0


if __name__ == '__main__':
    sys.exit(main())
