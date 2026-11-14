#!/usr/bin/env python3
"""A second stream client on an instance's /api/events, scoped as the measured page is.

The scope file holds the page's topics, a JSON array or null for everything: the first read is
the connect's SSE-Topics header, and each later change is POSTed to /api/events/scope for this
connection; <out>.scopes.json lists each POST with its status. One JSON line per frame: t (epoch
ms), event, bytes (the data's), and the payload's type, chat_id, workflow_id and kind when it
carries them. Stops when --stop-file appears, and exits 1 when the stream ends before it.
Usage: sse-capture.py --port N --out FILE --scope-file FILE [--stop-file FILE]
"""

import argparse
import contextlib
import http.client
import json
import os
import pathlib
import socket
import threading
import time

LIVE_PORT = 9847
TAG = 'perf-capture'


def read_scope(path: pathlib.Path) -> tuple[bool, list | None]:
    try:
        return True, json.loads(path.read_text())
    except OSError, ValueError:
        return False, None


def record(event: str, data: list[str]) -> dict:
    raw = '\n'.join(data)
    rec = {'t': round(time.time() * 1000), 'event': event or 'message', 'bytes': len(raw.encode())}
    try:
        d = json.loads(raw)
    except ValueError:
        return rec
    if isinstance(d, dict):
        rec['type'] = d.get('type', '')
        rec['chat_id'] = d.get('chat_id', '')
        p = d.get('payload')
        if isinstance(p, dict):
            rec.update({k: p[k] for k in ('workflow_id', 'kind') if k in p})
    return rec


class Rescope(threading.Thread):
    """POSTs each change of the scope file once the hello named this connection."""

    def __init__(self, port: int, path: pathlib.Path, topics: list | None, stop: threading.Event):
        super().__init__(daemon=True)
        self.port, self.path, self.topics, self.stop = port, path, topics, stop
        self.conn: str | None = None
        self.seq = 0
        self.posted: list[dict] = []

    def run(self) -> None:
        while not self.stop.wait(0.1):
            ok, topics = read_scope(self.path)
            if not ok or topics == self.topics or self.conn is None:
                continue
            self.seq += 1
            body = json.dumps({'conn': self.conn, 'seq': self.seq, 'topics': topics})
            c = http.client.HTTPConnection('127.0.0.1', self.port, timeout=30)
            try:
                c.request('POST', '/api/events/scope', body, {'Content-Type': 'application/json'})
                status = c.getresponse().status
            except OSError:
                status = 0
            finally:
                c.close()
            self.posted.append({'t': round(time.time() * 1000), 'topics': topics, 'status': status})
            self.topics = topics


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument('--port', type=int, required=True)
    ap.add_argument('--out', required=True)
    ap.add_argument('--scope-file', required=True)
    ap.add_argument('--stop-file', default='')
    a = ap.parse_args()
    if a.port == LIVE_PORT:
        raise SystemExit(f'sse-capture.py: {LIVE_PORT} is the live marotte')
    scope_path = pathlib.Path(a.scope_file)
    ok, topics = read_scope(scope_path)
    if not ok:
        raise SystemExit(f'sse-capture.py: no scope in {scope_path}')
    headers = {'Accept': 'text/event-stream', 'SSE-Wire': '1', 'SSE-Client': TAG}
    if topics is not None:
        headers['SSE-Topics'] = json.dumps(topics, separators=(',', ':'))
    conn = http.client.HTTPConnection('127.0.0.1', a.port)
    conn.request('GET', '/api/events', headers=headers)
    resp = conn.getresponse()
    stop = threading.Event()
    rescope = Rescope(a.port, scope_path, topics, stop)
    rescope.start()

    def watch_stop() -> None:
        # Shutting the socket down ends the blocked readline below.
        while not stop.wait(0.2):
            if a.stop_file and os.path.exists(a.stop_file):
                stop.set()
        with contextlib.suppress(OSError, AttributeError):
            conn.sock.shutdown(socket.SHUT_RDWR)

    threading.Thread(target=watch_stop, daemon=True).start()
    try:
        read_frames(resp, a.out, rescope, stop)
    finally:
        stop.set()
        pathlib.Path(a.out + '.scopes.json').write_text(json.dumps(rescope.posted))


def read_frames(
    resp: http.client.HTTPResponse, path: str, rescope: Rescope, stop: threading.Event
) -> None:
    event, data = '', []
    with open(path, 'w') as out:
        while True:
            try:
                line = resp.fp.readline()
            except OSError:
                # The shutdown that ends the read can surface as a reset rather than EOF.
                if stop.is_set():
                    return
                raise
            if not line:
                if not stop.is_set():
                    raise SystemExit('sse-capture.py: the stream ended before the stop file')
                return
            line = line.decode('utf-8', 'replace').rstrip('\r\n')
            if line:
                field, _, value = line.partition(':')
                if field == 'event':
                    event = value.strip()
                elif field == 'data':
                    data.append(value.removeprefix(' '))
                continue
            if not data and not event:
                continue
            rec = record(event, data)
            if event == 'sse:hello':
                rescope.conn = json.loads('\n'.join(data)).get('conn')
            out.write(json.dumps(rec) + '\n')
            out.flush()
            event, data = '', []


if __name__ == '__main__':
    main()
