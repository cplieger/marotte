#!/usr/bin/env python3
"""Server CPU per request: after a rep, with no browser attached, replay one URL of every GET
route the rep read, N times in a row, and write per route the server's CPU per read (utime and
stime of the marotte process across the N reads, divided by N), the status and the body bytes.
Usage: servercpu.py --instance DIR --rep REP.json --out FILE [--n 5]
"""

import argparse
import http.client
import json
import os
import pathlib
import urllib.parse

LIVE_PORT = 9847
TICK_MS = 1000 / os.sysconf('SC_CLK_TCK')


def cpu_ms(pid: int) -> float:
    fields = pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(') ', 1)[1].split()
    return (int(fields[11]) + int(fields[12])) * TICK_MS


def get(port: int, url: str) -> tuple[int, int]:
    u = urllib.parse.urlsplit(url)
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=300)
    try:
        conn.request('GET', u.path + (f'?{u.query}' if u.query else ''))
        r = conn.getresponse()
        return r.status, len(r.read())
    finally:
        conn.close()


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument('--instance', required=True)
    ap.add_argument('--rep', required=True)
    ap.add_argument('--out', required=True)
    ap.add_argument('--n', type=int, default=5)
    a = ap.parse_args()
    if a.n < 1:
        raise SystemExit('servercpu.py: --n must be at least 1')
    inst = json.loads(pathlib.Path(a.instance, 'instance.json').read_text())
    if inst['port'] == LIVE_PORT:
        raise SystemExit(f'servercpu.py: {LIVE_PORT} is the live marotte')
    rep = json.loads(pathlib.Path(a.rep).read_text())
    origin = f'http://127.0.0.1:{inst["port"]}/'
    urls = {}
    for w in rep['windows'].values():
        for route, e in w.get('routes', {}).items():
            if not route.startswith('GET '):
                continue
            if not e['url'].startswith(origin):
                raise SystemExit(f'servercpu.py: {route} was read from {e["url"]}, not {origin}')
            urls.setdefault(route, e['url'])
    routes = {}
    for route, url in sorted(urls.items()):
        get(
            inst['port'], url
        )  # warm: the replay measures a read the page repeats, not the first one
        c0 = cpu_ms(inst['pid'])
        for _ in range(a.n):
            status, size = get(inst['port'], url)
        routes[route] = {
            'url': url,
            'status': status,
            'bytes': size,
            'cpu_ms': round((cpu_ms(inst['pid']) - c0) / a.n, 2),
        }
    pathlib.Path(a.out).write_text(json.dumps({'n': a.n, 'routes': routes}, indent=1))


if __name__ == '__main__':
    main()
