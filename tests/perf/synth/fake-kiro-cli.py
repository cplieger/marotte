#!/usr/bin/env python3
"""A fake kiro-cli (ACP, v3 engine) for the performance harness.

It answers enough for marotte to boot, load the fixture's chat sessions, list History and the
recipes, and read the fixture's runs, and it drives a run or a chat turn live on demand.

Environment:
  FAKE_KIRO_RUNS         JSON list of `_kiro/workflow/list` rows; recipes.json beside it answers
                         `_kiro/workflow/listRecipes`
  FAKE_KIRO_INSPECT_DIR  one `<workflowId>.json` per run: the `_kiro/workflow/inspect` reply
                         {workflowId, state, nodePlan}, held in memory and changed by live ops
  FAKE_KIRO_SESSIONS     JSON list of `session/list` rows
  FAKE_KIRO_TRIGGER_DIR  live ops: a `trigger-<n>.json` file runs and is renamed `.done`
  FAKE_KIRO_LOG          one JSON line per invocation (its argv), per frame received and per op

Ops (a trigger file's JSON):
  {"op": "start"}  run_start for every running run, as the engine sends on a resume
  {"op": "starts", "count": N, "interval_ms": I}  that run_start N times, I ms apart
  {"op": "steps", "workflowId": W, "count": N, "lanes": K, "chunks": C, "tools": T,
   "interval_ms": I, "polls": P}  N steps round-robin over the run's first K lanes, K in flight
   at once; each adds the lane's next phase to the tree (build, the two reviews, the gate, a new
   iteration), sends node_start, streams C thought chunks and T tool calls on its own step
   session (each frame carrying `_meta.kiro.workflow`) and node_complete; P watch_poll frames
   per step
  {"op": "chat", "sessionId": S, "turns": N, "chunks": C, "tools": T, "interval_ms": I,
   "text_chunks": X, "edit_paths": [...]}  N turns on a chat's session: T rounds of C thought
   chunks and one tool call, X answer chunks; with edit_paths each call is an edit that appends
   a line to the next path before it settles
A `session/prompt` streams one turn shaped by TRIGGER_DIR/prompt-shape.json (the chat op's
fields). Frame shapes follow marotte's internal/agent and internal/translate readers.
"""

import copy
import json
import os
import sys
import threading
import time
import uuid

OUT_LOCK = threading.Lock()
STATE_LOCK = threading.Lock()
LOG = os.environ.get('FAKE_KIRO_LOG', '')
TRIGGER_DIR = os.environ.get('FAKE_KIRO_TRIGGER_DIR', '')
INSPECT_DIR = os.environ.get('FAKE_KIRO_INSPECT_DIR', '')
RUNS = {}  # workflowId -> inspect reply


def load_json(path):
    with open(path) as f:
        return json.load(f)


def log(obj):
    if LOG:
        with open(LOG, 'a') as f:
            f.write(json.dumps(obj) + '\n')


def send(frame):
    line = json.dumps(frame, separators=(',', ':'))
    with OUT_LOCK:
        sys.stdout.write(line + '\n')
        sys.stdout.flush()


def result(rid, res):
    send({'jsonrpc': '2.0', 'id': rid, 'result': res})


def notify(method, params):
    send({'jsonrpc': '2.0', 'method': method, 'params': params})


def now_iso():
    t = time.time()
    return time.strftime('%Y-%m-%dT%H:%M:%S', time.gmtime(t)) + f'.{int((t % 1) * 1000):03d}Z'


MODES = {
    'currentModeId': 'default-v2',
    'availableModes': [{'id': 'default-v2', 'name': 'Default', 'description': 'default agent'}],
}
CONFIG = [
    {
        'id': 'model',
        'currentValue': 'model-a',
        'options': [
            {
                'value': 'model-a',
                'name': 'Model A',
                'description': 'x',
                '_meta': {'kiro': {'rateMultiplier': 1.0}},
            },
            {'value': 'auto', 'name': 'Auto'},
        ],
    }
]


def load_recipes():
    path = os.path.join(os.path.dirname(os.environ.get('FAKE_KIRO_RUNS', '')), 'recipes.json')
    if not os.path.exists(path):
        return []
    return load_json(path)


def load_runs():
    path = os.environ.get('FAKE_KIRO_RUNS', '')
    if not path or not os.path.exists(path):
        return []
    rows = load_json(path)
    with STATE_LOCK:
        for r in rows:
            wid = r['workflowId']
            if wid in RUNS:
                r['status'] = RUNS[wid]['state'].get('status', r['status'])
    return rows


def load_inspects():
    if not INSPECT_DIR or not os.path.isdir(INSPECT_DIR):
        return
    for n in os.listdir(INSPECT_DIR):
        if n.endswith('.json'):
            RUNS[n[:-5]] = load_json(os.path.join(INSPECT_DIR, n))


# ---- live ops ---------------------------------------------------------------------------


def lanes_of(state):
    """The run's lane repeats: the children of the first parallel under the root."""
    root = state['root']
    for c in root.get('children') or []:
        if c.get('type') == 'parallel':
            return c, [x for x in c.get('children') or [] if x.get('type') == 'repeat']
    return None, []


def prefix_of(lane_id):
    return lane_id.removeprefix('lane-')


def step_node(node_id, agent, model, iteration, branch=None):
    n = {
        'nodeId': node_id,
        'type': 'step',
        'status': 'running',
        'agentName': agent,
        'modelId': model,
        'effortLevel': 'high',
        'sessionId': 'sess_' + str(uuid.uuid4()),
        'startedAt': now_iso(),
        'iteration': iteration,
    }
    if branch:
        n['branchId'] = branch
    return n


def next_phase(lane):
    """Adds the lane's next phase to the tree and returns [(step node, frame nodePath)]."""
    pfx = prefix_of(lane['nodeId'])
    iters = lane.setdefault('children', [])
    lane['status'] = 'running'
    lane.pop('endedAt', None)
    cur = iters[-1] if iters else None
    phase = len(cur.get('children') or []) if cur and cur.get('status') == 'running' else 3
    if phase >= 3:
        n = len(iters)
        cur = {
            'nodeId': f'{lane["nodeId"]}#{n}',
            'type': 'sequence',
            'status': 'running',
            'startedAt': now_iso(),
            'iteration': n,
            'children': [],
        }
        iters.append(cur)
        phase = 0
    n = cur['iteration']
    base = ['lanes-c', lane['nodeId'], f'iter-{n}']
    if phase == 0:
        s = step_node(pfx + '-build', 'builder', 'model-a', 0)
        loop = {
            'nodeId': pfx + '-build-loop',
            'type': 'repeat',
            'status': 'running',
            'startedAt': now_iso(),
            'iteration': n,
            'children': [
                {
                    'nodeId': pfx + '-build-loop#0',
                    'type': 'sequence',
                    'status': 'running',
                    'startedAt': now_iso(),
                    'iteration': 0,
                    'children': [s],
                }
            ],
        }
        cur['children'].append(loop)
        return [
            (s, [*base, pfx + '-build-loop', 'iter-0', s['nodeId']], [loop, loop['children'][0]])
        ]
    if phase == 1:
        a = step_node(
            pfx + '-review-a',
            'reviewer-a',
            'model-a',
            n,
            pfx + '-review-a',
        )
        b = step_node(pfx + '-review-b', 'reviewer-b', 'model-c', n, pfx + '-review-b')
        par = {
            'nodeId': pfx + '-reviews',
            'type': 'parallel',
            'status': 'running',
            'startedAt': now_iso(),
            'iteration': n,
            'children': [a, b],
        }
        cur['children'].append(par)
        return [
            (a, [*base, par['nodeId'], a['nodeId']], [par]),
            (b, [*base, par['nodeId'], b['nodeId']], [par]),
        ]
    g = step_node(pfx + '-gate', 'review-gate', 'model-a', n)
    cur['children'].append(g)
    return [(g, [*base, g['nodeId']], [cur])]


THOUGHT = "Reading the lane's patch and the gate's last verdict before the next change. "
OUTPUT = (
    'UNIT-READY. The change applies the ruling and the checks pass: the package tests with -race, '
    'golangci-lint 0 issues, deadset 0 findings. '
) * 12


def step_update(sid, wid, node, path, upd):
    upd.setdefault('_meta', {}).setdefault('kiro', {})['workflow'] = {
        'workflowId': wid,
        'nodeId': node,
        'nodePath': path,
    }
    notify('session/update', {'sessionId': sid, 'update': upd})


def stream_step(wid, parent, node, path, shape):
    sid = node['sessionId']
    gap = float(shape.get('interval_ms', 40)) / 1000.0
    nid = node['nodeId']
    notify(
        '_kiro/workflow/node_start',
        {
            'workflowId': wid,
            'nodeId': nid,
            'nodePath': path,
            'type': 'step',
            'agentName': node['agentName'],
            'sessionId': sid,
            'iteration': node.get('iteration'),
            'branchId': node.get('branchId'),
            'parentSessionId': parent,
        },
    )
    tid = str(uuid.uuid4())
    step_update(
        sid,
        wid,
        nid,
        path,
        {
            'sessionUpdate': 'session_info_update',
            '_meta': {
                'kiro': {
                    'turnStart': True,
                    'kind': 'turn_start',
                    'messageId': tid + '-turn-start',
                    'executionId': tid,
                    'turnId': tid,
                    'timestamp': now_iso(),
                    'boundarySource': 'observed',
                }
            },
        },
    )
    for p in range(int(shape.get('polls', 0))):
        notify(
            '_kiro/workflow/watch_poll',
            {
                'workflowId': wid,
                'nodeId': nid,
                'nodePath': path,
                'outcome': 'pending',
                'at': now_iso(),
                'parentSessionId': parent,
            },
        )
        time.sleep(gap)
    for i in range(int(shape.get('tools', 4))):
        for w in range(int(shape.get('chunks', 6))):
            step_update(
                sid,
                wid,
                nid,
                path,
                {
                    'sessionUpdate': 'agent_thought_chunk',
                    'content': {'type': 'text', 'text': THOUGHT},
                },
            )
            time.sleep(gap)
        call = f'toolu_fake_{tid[:8]}_{i:03d}'
        fpath = f'/workspace/subflux/internal/lane/file{i:03d}.go'
        raw = {'path': fpath}
        step_update(
            sid,
            wid,
            nid,
            path,
            {
                'sessionUpdate': 'tool_call',
                'toolCallId': call,
                'title': 'Read File',
                'kind': 'read',
                'status': 'pending',
                'rawInput': raw,
                'locations': [{'path': fpath}],
            },
        )
        time.sleep(gap)
        body = 'package lane\n' + '\n'.join(f'const line{k} = {k}' for k in range(40))
        step_update(
            sid,
            wid,
            nid,
            path,
            {
                'sessionUpdate': 'tool_call_update',
                'toolCallId': call,
                'status': 'completed',
                'title': 'Read File',
                'rawInput': raw,
                'rawOutput': {'message': body},
                'content': [{'type': 'content', 'content': {'type': 'text', 'text': body}}],
                'locations': [{'path': fpath}],
            },
        )
        time.sleep(gap)
    step_update(
        sid,
        wid,
        nid,
        path,
        {'sessionUpdate': 'agent_message_chunk', 'content': {'type': 'text', 'text': OUTPUT}},
    )
    step_update(
        sid,
        wid,
        nid,
        path,
        {
            'sessionUpdate': 'session_info_update',
            '_meta': {
                'kiro': {
                    'turnEnd': {'stopReason': 'end_turn'},
                    'kind': 'turn_end',
                    'executionId': tid,
                    'turnId': tid,
                    'timestamp': now_iso(),
                    'boundarySource': 'observed',
                    'stopReason': 'end_turn',
                    'messageId': tid + '-turn-end',
                }
            },
        },
    )
    with STATE_LOCK:
        node['status'] = 'completed'
        node['endedAt'] = now_iso()
        node['capturedOutput'] = OUTPUT
        node['completionSignal'] = 'success'
        node['completionSignalSource'] = 'send_message'
        node['credits'] = [{'unit': 'credit', 'value': 3.2}]
    notify(
        '_kiro/workflow/node_complete',
        {
            'workflowId': wid,
            'nodeId': nid,
            'nodePath': path,
            'status': 'completed',
            'capturedOutput': OUTPUT,
            'parentSessionId': parent,
        },
    )


def close_containers(containers):
    with STATE_LOCK:
        for c in containers:
            kids = c.get('children') or []
            if kids and all(k.get('status') == 'completed' for k in kids):
                c['status'] = 'completed'
                c['endedAt'] = now_iso()


ANSWER = (
    'The change applies and the checks pass. **Next**: the gate reads the diff.\n\n'
    '- the store keeps one writer\n- the client renders a window\n\n```go\nfunc f() int { return 1 }\n```\n\n'
)


def chat_turns(op):
    sid = op['sessionId']
    gap = float(op.get('interval_ms', 40)) / 1000.0
    seq_edit = [0]
    for _ in range(int(op.get('turns', 1))):
        tid = str(uuid.uuid4())
        notify(
            'session/update',
            {
                'sessionId': sid,
                'update': {
                    'sessionUpdate': 'session_info_update',
                    '_meta': {
                        'kiro': {
                            'turnStart': True,
                            'kind': 'turn_start',
                            'messageId': tid + '-turn-start',
                            'executionId': tid,
                            'turnId': tid,
                            'timestamp': now_iso(),
                            'boundarySource': 'observed',
                            **({'userMessageTag': op['tag']} if op.get('tag') else {}),
                        }
                    },
                },
            },
        )
        for i in range(int(op.get('tools', 4))):
            for _w in range(int(op.get('chunks', 6))):
                notify(
                    'session/update',
                    {
                        'sessionId': sid,
                        'update': {
                            'sessionUpdate': 'agent_thought_chunk',
                            'content': {'type': 'text', 'text': THOUGHT},
                        },
                    },
                )
                time.sleep(gap)
            call = f'toolu_chat_{tid[:8]}_{i:03d}'
            edits = op.get('edit_paths') or []
            if edits:
                # The file changes on disk before the call settles naming it.
                fpath = edits[(seq_edit[0]) % len(edits)]
                seq_edit[0] += 1
                kind, title = 'edit', 'Edit File'
            else:
                fpath = f'/workspace/app/internal/chat/file{i:03d}.go'
                kind, title = 'read', 'Read File'
            raw = {'path': fpath}
            notify(
                'session/update',
                {
                    'sessionId': sid,
                    'update': {
                        'sessionUpdate': 'tool_call',
                        'toolCallId': call,
                        'title': title,
                        'kind': kind,
                        'status': 'pending',
                        'rawInput': raw,
                        'locations': [{'path': fpath}],
                    },
                },
            )
            time.sleep(gap)
            body = 'package chat\n' + '\n'.join(f'const line{k} = {k}' for k in range(40))
            content = [{'type': 'content', 'content': {'type': 'text', 'text': body}}]
            if edits:
                line = f'// edit {tid[:8]} {i}\n'
                with open(fpath, 'a') as f:
                    f.write(line)
                # An ACP edit settles carrying its diff with an absolute path, which marotte makes
                # workspace-relative.
                content = [{'type': 'diff', 'path': fpath, 'oldText': '', 'newText': line}]
            notify(
                'session/update',
                {
                    'sessionId': sid,
                    'update': {
                        'sessionUpdate': 'tool_call_update',
                        'toolCallId': call,
                        'status': 'completed',
                        'title': title,
                        'kind': kind,
                        'rawInput': raw,
                        'rawOutput': {'message': body},
                        'content': content,
                        'locations': [{'path': fpath}],
                    },
                },
            )
            time.sleep(gap)
        for _x in range(int(op.get('text_chunks', 20))):
            notify(
                'session/update',
                {
                    'sessionId': sid,
                    'update': {
                        'sessionUpdate': 'agent_message_chunk',
                        'content': {'type': 'text', 'text': ANSWER},
                    },
                },
            )
            time.sleep(gap)
        notify(
            'session/update',
            {
                'sessionId': sid,
                'update': {
                    'sessionUpdate': 'session_info_update',
                    '_meta': {
                        'kiro': {
                            'turnEnd': {'stopReason': 'end_turn'},
                            'kind': 'turn_end',
                            'executionId': tid,
                            'turnId': tid,
                            'timestamp': now_iso(),
                            'boundarySource': 'observed',
                            'stopReason': 'end_turn',
                            'messageId': tid + '-turn-end',
                        }
                    },
                },
            },
        )


GOVERNANCE = {
    'features': {
        'mcpEnabled': True,
        'webToolsEnabled': True,
        'usageAnalytics': False,
        'contentCollection': True,
        'promptLogging': False,
        'codeReferenceTracker': False,
        'autonomousAgents': True,
    },
    'isEnterprise': False,
    'disabledReason': '',
}


def find_key(obj, key):
    if isinstance(obj, dict):
        if isinstance(obj.get(key), str):
            return obj[key]
        for v in obj.values():
            hit = find_key(v, key)
            if hit:
                return hit
    elif isinstance(obj, list):
        for v in obj:
            hit = find_key(v, key)
            if hit:
                return hit
    return ''


def prompt_turn(rid, sid, tag):
    """A prompt streams one turn shaped by TRIGGER_DIR/prompt-shape.json (the chat op's fields; absent: a bare
    turn) whose turn_start echoes the prompt's userMessageTag (how kiro-cli binds the bracket to the sent prompt),
    answers the prompt when the turn ends, and drops prompt-<sid>-<n>.done for the harness."""
    shape = {'turns': 1, 'tools': 0, 'chunks': 0, 'text_chunks': 1, 'interval_ms': 10}
    try:
        shape.update(load_json(os.path.join(TRIGGER_DIR, 'prompt-shape.json')))
    except OSError, ValueError:
        pass
    started = time.time()
    err = None
    try:
        chat_turns({**shape, 'sessionId': sid, 'tag': tag})
    except Exception as e:  # noqa: BLE001 - the marker reports it and the prompt is answered anyway
        err = repr(e)
    result(rid, {'stopReason': 'end_turn'})
    if TRIGGER_DIR:
        with open(os.path.join(TRIGGER_DIR, f'prompt-{sid}-{int(started * 1000)}.done'), 'w') as f:
            json.dump({'sessionId': sid, 'seconds': time.time() - started, 'error': err}, f)


def load_sessions():
    path = os.environ.get('FAKE_KIRO_SESSIONS', '')
    if not path or not os.path.exists(path):
        return []
    return load_json(path)


def run_steps(op):
    wid = op['workflowId']
    reply = RUNS[wid]
    st = reply['state']
    parent = st.get('parentSessionId', '')
    with STATE_LOCK:
        st['status'] = 'running'
        _, lanes = lanes_of(st)
    k = int(op.get('lanes') or len(lanes))
    lanes = lanes[:k]
    left = int(op.get('count', 10))
    while left > 0:
        batch = []
        with STATE_LOCK:
            for lane in lanes:
                if left <= 0:
                    break
                batch.extend(next_phase(lane))
                left -= 1
        threads = [
            threading.Thread(target=stream_step, args=(wid, parent, s, p, op), daemon=True)
            for s, p, _ in batch
        ]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        for _, _, containers in batch:
            close_containers(containers)


def start_runs():
    """Sends run_start for every running run, as KAS does on a resume: marotte arms its clock."""
    with STATE_LOCK:
        runs = [(w, r['state']) for w, r in RUNS.items() if r['state'].get('status') == 'running']
    for wid, st in runs:
        notify(
            '_kiro/workflow/run_start',
            {
                'workflowId': wid,
                'workflowName': st.get('workflowName'),
                'inputs': st.get('inputs') or {},
                'nodeTree': [],
                'parentSessionId': st.get('parentSessionId'),
            },
        )


def trigger_loop():
    if not TRIGGER_DIR:
        return
    os.makedirs(TRIGGER_DIR, exist_ok=True)
    while True:
        try:
            names = sorted(
                n
                for n in os.listdir(TRIGGER_DIR)
                if n.startswith('trigger-') and n.endswith('.json')
            )
        except OSError:
            names = []
        for n in names:
            p = os.path.join(TRIGGER_DIR, n)
            try:
                op = load_json(p)
            except OSError, ValueError:
                continue
            os.rename(p, p + '.running')
            started = time.time()
            log({'t': started, 'op': op})
            err = None
            try:
                if op.get('op') == 'steps':
                    run_steps(op)
                elif op.get('op') == 'start':
                    start_runs()
                elif op.get('op') == 'starts':
                    for _k in range(int(op.get('count', 5))):
                        start_runs()
                        time.sleep(int(op.get('interval_ms', 2000)) / 1000.0)
                elif op.get('op') == 'chat':
                    chat_turns(op)
            except Exception as e:  # noqa: BLE001 - the op's .done file reports it
                err = repr(e)
            with open(p + '.done', 'w') as f:
                json.dump({'op': op, 'seconds': time.time() - started, 'error': err}, f)
            os.remove(p + '.running')
        time.sleep(0.1)


def inspect_reply(wid):
    with STATE_LOCK:
        r = RUNS.get(wid)
        return copy.deepcopy(r) if r else None


def main():
    log({'t': time.time(), 'argv': sys.argv[1:]})
    if len(sys.argv) > 1 and sys.argv[1] in ('--version', 'version'):
        print('kiro-cli 3.1.0')
        return
    if len(sys.argv) < 2 or sys.argv[1] != 'acp':
        sys.exit(1)
    load_inspects()
    threading.Thread(target=trigger_loop, daemon=True).start()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except ValueError:
            continue
        method = msg.get('method')
        rid = msg.get('id')
        params = msg.get('params') or {}
        log(
            {
                't': time.time(),
                'method': method,
                'id': rid,
                'sid': params.get('sessionId'),
                'wid': params.get('workflowId'),
                'bytes': len(line),
            }
        )
        if method is None:
            continue
        if method == 'initialize':
            result(
                rid,
                {
                    'protocolVersion': 1,
                    'agentCapabilities': {'loadSession': True},
                    'serverInfo': {'name': 'fake-kiro', 'version': '3.1.0'},
                },
            )
        elif method == 'session/new':
            sid = ((params.get('_meta') or {}).get('kiro') or {}).get('overrideSessionId') or (
                'sess_' + str(uuid.uuid4())
            )
            result(rid, {'sessionId': sid, 'modes': MODES, 'configOptions': CONFIG})
            notify('_kiro/governance/state', {'sessionId': sid, **GOVERNANCE})
        elif method == 'session/load':
            result(rid, {'modes': MODES, 'configOptions': CONFIG})
            notify('_kiro/governance/state', {'sessionId': params.get('sessionId'), **GOVERNANCE})
        elif method == 'session/prompt':
            threading.Thread(
                target=prompt_turn,
                args=(rid, params.get('sessionId'), find_key(params, 'userMessageTag')),
                daemon=True,
            ).start()
        elif method == 'session/list':
            result(rid, {'sessions': load_sessions()})
        elif method == '_kiro/workflow/listRecipes':
            result(rid, {'recipes': load_recipes()})
        elif method == '_kiro/workflow/list':
            result(rid, {'runs': load_runs()})
        elif method == '_kiro/workflow/inspect':
            wid = params.get('workflowId')
            r = inspect_reply(wid)
            if r is None:
                send(
                    {
                        'jsonrpc': '2.0',
                        'id': rid,
                        'error': {
                            'code': -32603,
                            'message': 'Workflow not found',
                            'data': {'errorType': 'WorkflowNotFoundError'},
                        },
                    }
                )
            else:
                result(rid, r)
        elif rid is not None:
            result(rid, {})


if __name__ == '__main__':
    main()
