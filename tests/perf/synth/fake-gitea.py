#!/usr/bin/env python3
"""A fake Gitea (API v1) serving one fixture's forge.json, on 127.0.0.1 only.

Enough for marotte's forge inventory and the git panel's Pull requests view: /version,
/settings/api, /user, /user/repos, /users/<o>/repos, /repos/<o>/<r>, /repos/<o>/<r>/pulls,
/repos/<o>/<r>/issues, /repos/issues/search, /repos/<o>/<r>/commits/<sha>/status and
/repos/<o>/<r>/actions/runs, in the shapes of github.com/cplieger/forgeapi/v2's gitea/wire.go.
Every request is logged as one JSON line (path, status, bytes) to --log. With --churn FILE, while
FILE holds {"every_ms": I, "count": N}, every I ms the N least recently updated pull requests get
a new head sha and updated_at.

Usage: fake-gitea.py --data forge.json --port P [--log FILE] [--churn FILE]
"""

import argparse
import hashlib
import json
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MAX_ITEMS = 50


def build(data, base):
    owner = data['owner']

    def user(login):
        return {
            'login': login,
            'full_name': login,
            'email': '',
            'html_url': f'{base}/{login}',
            'type': 'User',
        }

    def repo(name):
        return {
            'full_name': f'{owner}/{name}',
            'description': f'fixture repository {name}',
            'html_url': f'{base}/{owner}/{name}',
            'clone_url': f'{base}/{owner}/{name}.git',
            'updated_at': '2026-11-02T10:00:00Z',
            'default_branch': 'main',
            'permissions': {'push': True},
            'has_issues': True,
            'private': False,
            'archived': False,
            'fork': False,
            'allow_merge_commits': True,
            'allow_squash_merge': True,
            'allow_rebase': True,
            'allow_rebase_explicit': True,
            'allow_fast_forward_only_merge': False,
        }

    repos = {n: repo(n) for n in data['repos']}
    pulls = {}
    for p in data['prs']:
        r = repos[p['repo']]
        w = {
            'number': p['number'],
            'state': 'open',
            'title': p['title'],
            'body': p['body'],
            'user': user(p['author']),
            'html_url': f'{r["html_url"]}/pulls/{p["number"]}',
            'created_at': p['updated'],
            'updated_at': p['updated'],
            'labels': [
                {'name': l, 'color': '00aabb', 'description': '', 'id': i + 1}
                for i, l in enumerate(p['labels'])
            ],
            'head': {'ref': p['head'], 'sha': p['sha'], 'repo': r},
            'base': {'ref': 'main', 'sha': '0' * 40, 'repo': r},
            'draft': p['draft'],
            'merged': False,
            'mergeable': True,
            'repository': {'full_name': r['full_name']},
            'pull_request': {'draft': p['draft']},
        }
        pulls.setdefault(p['repo'], []).append(w)
    return {
        'owner': owner,
        'login': data['login'],
        'user': user(data['login']),
        'repos': repos,
        'pulls': pulls,
    }


def page(rows, q):
    lim = min(int(q.get('limit', [MAX_ITEMS])[0]), MAX_ITEMS)
    pg = max(1, int(q.get('page', ['1'])[0]))
    return rows[(pg - 1) * lim : pg * lim], len(rows)


class H(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    db = None
    logf = None
    lock = threading.Lock()

    def log_message(self, *a):
        pass

    def reply(self, status, obj, total=None):
        body = json.dumps(obj, separators=(',', ':')).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        if total is not None:
            self.send_header('X-Total-Count', str(total))
        self.end_headers()
        self.wfile.write(body)
        if H.logf:
            with H.lock, open(H.logf, 'a') as f:
                f.write(
                    json.dumps(
                        {'t': time.time(), 'path': self.path, 'status': status, 'bytes': len(body)}
                    )
                    + '\n'
                )

    def do_GET(self):
        u = urllib.parse.urlsplit(self.path)
        q = urllib.parse.parse_qs(u.query)
        p = u.path
        db = H.db
        if not p.startswith('/api/v1'):
            return self.reply(404, {'message': 'not found'})
        p = p[len('/api/v1') :]
        parts = [x for x in p.split('/') if x]
        if p == '/version':
            return self.reply(200, {'version': '1.24.0'})
        if p == '/settings/api':
            return self.reply(200, {'max_response_items': MAX_ITEMS, 'default_paging_num': 30})
        if p == '/user':
            return self.reply(200, db['user'])
        if p == '/user/repos' or (len(parts) == 3 and parts[0] == 'users' and parts[2] == 'repos'):
            rows, total = page(list(db['repos'].values()), q)
            return self.reply(200, rows, total)
        if p == '/repos/issues/search':
            if q.get('type', [''])[0] != 'pulls':
                return self.reply(200, [], 0)
            allp = [w for ws in db['pulls'].values() for w in ws]
            allp.sort(key=lambda w: w['updated_at'], reverse=True)
            rows, total = page(allp, q)
            return self.reply(200, rows, total)
        if len(parts) >= 3 and parts[0] == 'repos':
            name = parts[2]
            if name not in db['repos']:
                return self.reply(404, {'message': 'repo not found'})
            rest = parts[3:]
            if not rest:
                return self.reply(200, db['repos'][name])
            if rest == ['pulls']:
                state = q.get('state', ['open'])[0]
                rows = db['pulls'].get(name, []) if state in ('open', 'all') else []
                rows, total = page(rows, q)
                return self.reply(200, rows, total)
            if len(rest) == 2 and rest[0] == 'pulls':
                for w in db['pulls'].get(name, []):
                    if str(w['number']) == rest[1]:
                        return self.reply(200, w)
                return self.reply(404, {'message': 'pull not found'})
            if rest == ['issues']:
                return self.reply(200, [], 0)
            if len(rest) == 3 and rest[0] == 'commits' and rest[2] == 'status':
                return self.reply(
                    200,
                    {
                        'sha': rest[1],
                        'state': 'success',
                        'statuses': [
                            {
                                'context': 'ci/build',
                                'description': 'passed',
                                'target_url': '',
                                'status': 'success',
                                'state': 'success',
                            }
                        ],
                    },
                )
            if rest == ['actions', 'runs']:
                return self.reply(200, {'total_count': 0, 'workflow_runs': []})
            if rest == ['labels']:
                return self.reply(200, [], 0)
        return self.reply(404, {'message': 'not found: ' + p})


def churn(path):
    n = 0
    while True:
        try:
            with open(path) as f:
                spec = json.load(f)
        except OSError, ValueError:
            time.sleep(0.5)
            continue
        every = max(0.05, float(spec.get('every_ms', 5000)) / 1000.0)
        with H.lock:
            allp = sorted(
                (w for ws in H.db['pulls'].values() for w in ws), key=lambda w: w['updated_at']
            )
            for w in allp[: int(spec.get('count', 1))]:
                n += 1
                w['updated_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()).replace(
                    'Z', f'.{n:06d}Z'
                )
                w['head']['sha'] = hashlib.sha1(
                    f'{w["html_url"]}-{n}'.encode(), usedforsecurity=False
                ).hexdigest()
        time.sleep(every)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--data', required=True)
    ap.add_argument('--port', type=int, required=True)
    ap.add_argument('--log', default='')
    ap.add_argument('--churn', default='')
    a = ap.parse_args()
    with open(a.data) as f:
        H.db = build(json.load(f), f'http://127.0.0.1:{a.port}')
    H.logf = a.log
    if a.churn:
        threading.Thread(target=churn, args=(a.churn,), daemon=True).start()
    ThreadingHTTPServer(('127.0.0.1', a.port), H).serve_forever()


if __name__ == '__main__':
    main()
