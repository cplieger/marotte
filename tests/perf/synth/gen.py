#!/usr/bin/env python3
"""Generate a synthetic lanes-shaped workflow run of any size.

One parallel of lane repeats, each iteration a sequence of a build loop (a repeat of one build
step), a parallel of two review steps and a gate step. At --scale 1 the lanes hold 93 iterations
(24, 20, 10, 10, 5, 1, 1, 1, 6, 14, 1), about 1,000 nodes; --scale N multiplies each lane's
iterations by N. The run starts three days before --now.

Writes, under --out:
  inspect/<wid>.json        the `_kiro/workflow/inspect` reply the fake engine serves
  runs/<wid>/entries.jsonl  marotte's run log for it (a turn per started node)
  lease.json, row.json      the runs.json lease and the `_kiro/workflow/list` row

Usage: gen.py --out DIR [--scale N] [--now UNIX_SECONDS] [--wid ID] [--parent-session S]
              [--chat C] [--content K] [--seed S]
"""

import argparse
import json
import os
import random
import time

LANES = [
    ('core', 24, 'completed'),
    ('ts', 20, 'completed'),
    ('media', 10, 'running'),
    ('search', 10, 'running'),
    ('platform', 5, 'paused'),
    ('api', 1, 'paused'),
    ('web', 1, 'paused'),
    ('web2', 1, 'paused'),
    ('libs', 6, 'completed'),
    ('auth-gates', 14, 'paused'),
    ('final', 1, 'paused'),
]

OUT_TEXT = (
    'The patch adds a retry to the fetch client, bounded at three attempts, and a unit test for '
    'every error arm. Checks: the statedb tests pass with -race, golangci-lint reports 0 issues, '
    "and deadset reports 0 findings at CI's pins. "
)
BACKDATE_MS = 3 * 86400 * 1000


def iso(ms):
    return time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime(ms // 1000)) + f'.{ms % 1000:03d}Z'


class Gen:
    def __init__(self, wid, parent, content, seed, start_ms):
        self.wid = wid
        self.parent = parent
        self.content = content
        self.rng = random.Random(seed)
        self.start = start_ms
        self.clock = start_ms
        self.turn_n = 0
        self.log = []

    def tick(self, ms):
        self.clock += ms
        return self.clock

    def entry(self, eid, tid, kind, payload, seq, ms):
        self.log.append(
            {
                'id': eid,
                'turn': tid,
                'kind': kind,
                'payload': payload,
                'seq': seq,
                'ts': self.tick(ms),
            }
        )

    def turn(self, key, step):
        self.turn_n += 1
        tid = f't-syn{self.turn_n:08d}'
        seq = 0
        opened = {'source': 'workflow_step', 'run': self.wid, 'node_path': key, 'n': self.turn_n}
        self.entry(tid, tid, 'turn_open', opened, seq, 50)
        if step:
            for i in range(self.content):
                seq += 1
                call = f'toolu_syn_{self.turn_n:08d}_{i:03d}'
                path = f'/workspace/subflux/internal/x{i:03d}.go'
                if i % 3 == 0:
                    text = {'text': 'Checking the lane state before the change.\n\n'}
                    self.entry(f'{tid}-{i}-say', tid, 'thinking', text, seq, 400)
                elif i % 3 == 1:
                    payload = {
                        'id': call,
                        'title': 'Read File',
                        'kind': 'read',
                        'status': 'pending',
                        'input': {'path': path},
                        'locations': [{'path': path}],
                    }
                    self.entry(call, tid, 'tool_call', payload, seq, 300)
                else:
                    payload = {
                        'title': 'Read File',
                        'kind': 'read',
                        'status': 'completed',
                        'output': 'package x\n' + 'const a = 1\n' * 60,
                    }
                    self.entry(call + ':result', tid, 'tool_result', payload, seq, 300)
            seq += 1
            self.entry(tid + '-out-say', tid, 'text', {'text': OUT_TEXT}, seq, 500)
        return tid, seq

    def close(self, tid, seq, outcome='completed'):
        payload = {
            'outcome': outcome,
            'stop_reason_raw': 'end_turn',
            'credits': 3.1,
            'elapsed_ms': 120000,
        }
        self.entry(tid + ':e1', tid, 'turn_close', payload, seq + 1, 100)

    def node(self, node_id, typ, path, status, it=None, branch=None, step=None):
        """Opens the node's run-log turn; `step` is (agent, model) for a step node.
        Returns (node, its run-log turn)."""
        key = ':'.join([self.wid, *path])
        n = {'nodeId': node_id, 'type': typ, 'status': status, 'startedAt': iso(self.tick(80))}
        if it is not None:
            n['iteration'] = it
        if branch:
            n['branchId'] = branch
        if step:
            sid = f'sess_{self.rng.getrandbits(32):08x}-0000-4000-8000-{self.rng.getrandbits(48):012x}'
            agent, model = step
            n.update(
                {'agentName': agent, 'modelId': model, 'effortLevel': 'high', 'sessionId': sid}
            )
        else:
            n['children'] = []
        return n, self.turn(key, bool(step))

    def end(self, n, opened, status):
        n['status'] = status
        if status in {'completed', 'failed'}:
            n['endedAt'] = iso(self.tick(60))
            if n['type'] == 'step':
                cut = 900 + self.rng.randrange(1200)
                n['capturedOutput'] = (OUT_TEXT * 8)[:cut] + '\n\nUNIT-READY'
                n['completionSignal'] = 'success'
                n['completionSignalSource'] = 'send_message'
                n['credits'] = [{'unit': 'credit', 'value': round(self.rng.uniform(1, 9), 3)}]
            self.close(*opened)

    def iteration(self, pfx, lane_id, k, last, lane_status):
        base = ['lanes-c', lane_id, f'iter-{k}']
        open_tail = last and lane_status != 'completed'
        it, it_open = self.node(f'{lane_id}#{k}', 'sequence', base, 'running', it=k)
        loop_path = [*base, pfx + '-build-loop']
        loop, loop_open = self.node(pfx + '-build-loop', 'repeat', loop_path, 'running', it=k)
        rounds = 1 + self.rng.randrange(4)
        for j in range(rounds):
            jp = [*loop_path, f'iter-{j}']
            seq, seq_open = self.node(pfx + f'-build-loop#{j}', 'sequence', jp, 'running', it=j)
            s, s_open = self.node(
                pfx + '-build',
                'step',
                [*jp, pfx + '-build'],
                'running',
                it=j,
                step=('builder', 'model-a'),
            )
            stop_here = (
                open_tail
                and j == rounds - 1
                and lane_status == 'running'
                and self.rng.random() < 0.5
            )
            st = 'running' if stop_here else 'completed'
            self.end(s, s_open, st)
            seq['children'].append(s)
            self.end(seq, seq_open, st)
            loop['children'].append(seq)
            if stop_here:
                it['children'].append(loop)
                return it
        self.end(loop, loop_open, 'completed')
        it['children'].append(loop)
        reviews_path = [*base, pfx + '-reviews']
        par, par_open = self.node(pfx + '-reviews', 'parallel', reviews_path, 'running', it=k)
        for rev, agent, model in (
            ('a', 'reviewer-a', 'model-a'),
            ('b', 'reviewer-b', 'model-c'),
        ):
            sid = f'{pfx}-review-{rev}'
            s, s_open = self.node(
                sid, 'step', [*reviews_path, sid], 'running', it=k, branch=sid, step=(agent, model)
            )
            self.end(s, s_open, 'completed')
            par['children'].append(s)
        self.end(par, par_open, 'completed')
        it['children'].append(par)
        g, g_open = self.node(
            pfx + '-gate',
            'step',
            [*base, pfx + '-gate'],
            'running',
            it=k,
            step=('review-gate', 'model-a'),
        )
        if open_tail:
            g_status = 'paused' if lane_status == 'paused' else 'running'
        else:
            g_status = 'completed'
        self.end(g, g_open, g_status)
        it['children'].append(g)
        self.end(it, it_open, g_status if open_tail else 'completed')
        return it

    def lane_plan(self, pfx, count):
        build = {
            'nodeId': pfx + '-build',
            'type': 'step',
            'agentName': 'builder',
            'modelId': 'model-a',
            'effortLevel': 'high',
        }
        reviewers = [
            {
                'nodeId': f'{pfx}-review-{rev}',
                'type': 'step',
                'agentName': 'reviewer-' + rev,
                'modelId': model,
                'effortLevel': 'high',
            }
            for rev, model in (('a', 'model-a'), ('b', 'model-c'))
        ]
        gate = {
            'nodeId': pfx + '-gate',
            'type': 'step',
            'agentName': 'review-gate',
            'modelId': 'model-a',
            'effortLevel': 'high',
        }
        loop = {
            'nodeId': pfx + '-build-loop',
            'type': 'repeat',
            'steps': [build],
            'maxIterations': 12,
            'onMaxIterations': 'continue',
            'stopWhen': f'{{{{{pfx}-build.output}}}} contains UNIT-READY',
        }
        return {
            'nodeId': 'lane-' + pfx,
            'type': 'repeat',
            'steps': [
                loop,
                {'nodeId': pfx + '-reviews', 'type': 'parallel', 'branches': reviewers},
                gate,
            ],
            'maxIterations': max(24, count + 4),
            'onMaxIterations': 'pause',
            'stopWhen': f'{{{{{pfx}-gate.output}}}} contains LANE-{pfx}-DONE',
        }

    def run(self, scale):
        root = {
            'nodeId': self.wid,
            'type': 'sequence',
            'status': 'running',
            'startedAt': iso(self.clock),
            'children': [],
        }
        par, _ = self.node('lanes-c', 'parallel', ['lanes-c'], 'running')
        root['children'].append(par)
        plan_branches = []
        for pfx, iters, status in LANES:
            lane_id = 'lane-' + pfx
            lane, lane_open = self.node(
                lane_id, 'repeat', ['lanes-c', lane_id], 'running', branch=lane_id
            )
            count = max(1, iters * scale)
            for k in range(count):
                lane['children'].append(self.iteration(pfx, lane_id, k, k == count - 1, status))
            self.end(lane, lane_open, status)
            par['children'].append(lane)
            plan_branches.append(self.lane_plan(pfx, count))
        state = {
            'workflowId': self.wid,
            'workflowName': 'subflux-lanes-synth',
            'status': 'running',
            'inputs': {},
            'artifacts': {},
            'capturedOutputs': {},
            'root': root,
            'parentSessionId': self.parent,
            'backgroundExecution': True,
            'rootConversationId': self.parent,
            'workspacePath': '/workspace',
            'createdAt': iso(self.start),
            'runLabel': f'subflux-lanes-synth-x{scale}',
            'originalUserMessages': [],
            'memoryConfig': {'mode': 'disabled', 'reflection': False},
            'memoryConfigSource': 'explicit',
            'parentModelId': 'model-a',
            'parentEffortLevel': 'xhigh',
            'planRevision': 0,
        }
        plan = [{'nodeId': 'lanes-c', 'type': 'parallel', 'branches': plan_branches}]
        return {'workflowId': self.wid, 'state': state, 'nodePlan': plan}


def count_nodes(n):
    return 1 + sum(count_nodes(c) for c in n.get('children') or [])


def dump(path, obj):
    with open(path, 'w') as f:
        json.dump(obj, f, separators=(',', ':'))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--out', required=True)
    ap.add_argument('--scale', type=int, default=1)
    ap.add_argument(
        '--now', type=int, default=None, help='Unix seconds; the run starts 3 days back'
    )
    ap.add_argument('--wid', default=None)
    ap.add_argument('--parent-session', default='sess_00000000-0000-4000-8000-000000000001')
    ap.add_argument('--chat', default='c-00000000000000000000000000000001')
    ap.add_argument('--content', type=int, default=12, help='content entries per step turn')
    ap.add_argument('--seed', type=int, default=7)
    a = ap.parse_args()
    now = a.now if a.now is not None else int(time.time())
    start = now * 1000 - BACKDATE_MS
    wid = a.wid or f'wf_5y{a.scale:014x}'
    g = Gen(wid, a.parent_session, a.content, a.seed, start)
    reply = g.run(a.scale)
    os.makedirs(os.path.join(a.out, 'inspect'), exist_ok=True)
    os.makedirs(os.path.join(a.out, 'runs', wid), exist_ok=True)
    dump(os.path.join(a.out, 'inspect', wid + '.json'), reply)
    with open(os.path.join(a.out, 'runs', wid, 'entries.jsonl'), 'w') as f:
        f.writelines(json.dumps(e, separators=(',', ':')) + '\n' for e in g.log)
    started = iso(start)
    lease = {
        'started_at': started,
        'workflow_id': wid,
        'chat_id': a.chat,
        'parent_session_id': a.parent_session,
        'recipe': 'subflux-lanes-synth',
        'origin': 'agent',
        'keep_chats': [a.chat],
        'unattended': False,
    }
    row = {
        'workflowId': wid,
        'name': reply['state']['runLabel'],
        'workflowName': 'subflux-lanes-synth',
        'status': 'running',
        'createdAt': started,
        'updatedAt': started,
        'startedAt': started,
        'parentSessionId': a.parent_session,
        'currentNodeId': 'lanes-c',
    }
    dump(os.path.join(a.out, 'lease.json'), lease)
    dump(os.path.join(a.out, 'row.json'), row)
    size = os.path.getsize(os.path.join(a.out, 'inspect', wid + '.json'))
    summary = {
        'workflowId': wid,
        'scale': a.scale,
        'nodes': count_nodes(reply['state']['root']),
        'inspect_bytes': size,
        'log_entries': len(g.log),
    }
    print(json.dumps(summary))


if __name__ == '__main__':
    main()
