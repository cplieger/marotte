# Contributing to marotte

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Scope

A change to how chats or settings are stored adds no migration code, compatibility layer or reader for files an older build left behind.

## Rules

- Every change a user makes to chat state goes through `POST /api/command`, is written to disk, and reaches every open tab over `/api/events`. The transcript draws a user message only after `turn_opened` arrives, so two devices never show different chats.
- A chat's record is removed only by `delete_chat`, by closing its tab while chat retention is off, and by the retention purge. A session or engine exit, model switch, restart or error never removes one.
- kiro-cli's ACP notifications become marotte entries and events in `internal/translate` before anything reaches a client. Never forward a raw ACP frame. The client has no type or decoder for one, so it would drop or misread it.
- A failed user action shows its action's toast. A form validation error shows beside the field that failed, and a condition the user must see on every view is a banner in `banner-stack.ts`. Background polls and stream errors only log.
- An action whose failure a form or a banner already shows sets `error: false`, or the user sees the same failure twice.
- A control in the prompt-pill row opens through `pill-expand.ts`, never a floating popup. A popup outside it breaks the one-pill-open-at-a-time rule the other pills share.
- Each `@cplieger/*` package pinned in `static-src/package.json` is fetched again by a Dockerfile `ARG` at the same exact version. Change both together, or the image bundles a different version from the one you tested.

## Checks

The census tests in `internal/kascap` compare the capability table with kiro-cli's own agent-server bundle, which they read from `$KIRO_RUNTIME_DIR`, else `~/.local/share/kiro-cli/kas/`. On a pull request that changes `KIRO_CLI_VERSION` or a `KIRO_CLI_SHA256` digest, CI downloads each release archive whose pin changed, x86_64 and aarch64 alike, checks its sha256, reads the agent server out of it and runs them against each. Every other CI run skips them.

After a change to that package, run `go test -v ./internal/kascap/` on a machine with the pinned kiro-cli, and check that no census test reports SKIP. A failure prints the `-update` command, and every line it adds is a capability to judge.

To unpack a pinned kiro-cli that is not installed, run it once against a scratch home. `env -u` keeps an inherited `KIRO_KAS_*` path, `KIRO_RUNTIME_DIR` or `KIRO_HOME` from pointing it at another tree, and keeps a kiro-cli of another version from pruning the live runtime directory:

```sh
curl -fsSLO https://desktop-release.q.us-east-1.amazonaws.com/<version>/kirocli-x86_64-linux.zip
unzip -q kirocli-x86_64-linux.zip
env -u KIRO_KAS_SERVER_PATH -u KIRO_KAS_NODE_PATH -u KIRO_RUNTIME_DIR -u KIRO_HOME HOME=<scratch> ./kirocli/bin/kiro-cli acp --agent-engine v3 </dev/null
env -u KIRO_RUNTIME_DIR -u KIRO_HOME HOME=<scratch> go test ./internal/kascap/
```

The `e2e-sse` test project runs against a real server binary. In CI, ts-ci builds it from the `sseFixture` and `sseFixtureTags` keys in `static-src/package.json` and passes it to every shard as `SSE_FIXTURE`, so the project runs there, and its setup fails a CI run where `SSE_FIXTURE` is unset rather than skipping. Locally it skips unless `SSE_FIXTURE` names a binary. After a change to the SSE stream, the sync digest or how the chat store publishes headers, build that binary and run the project:

```sh
go build -tags marotte_test -o /tmp/marotte-test .
cd static-src && SSE_FIXTURE=/tmp/marotte-test npx vitest --run --project e2e-sse
```

A second marotte you start by hand needs its own `KIRO_CONFIG_DIR`, `HOME` and `KIRO_HOME`, or it finds the running one's lock and refuses to start.

To look at the Kiro sign-in banners without an outage, start that `marotte_test` binary with `MAROTTE_TEST_SIGNIN_STUB=1`, and optionally `MAROTTE_TEST_SIGNIN_GRACE=20s` to shorten the 2 minute grace. It signs a fake Builder ID account in at boot, asks for a credential every 15 seconds as kiro-cli does, and answers every renewal from the file `qa-signin-mode` in `KIRO_CONFIG_DIR`: `ok` (or no file), `transient` for an unreachable sign-in service, `rejected` for an ended sign-in. Signing in from the app completes on its own after 2 seconds.

## Performance

On a pull request, CI runs the `pr` set of `tests/perf/plan.json` on a hosted runner and guards each timing cell against its baseline in `tests/perf/budgets.json` at the base branch. A cell is one scenario at one fixture scale (`x1` or `x10`), browser engine and width. Its scenarios are listed under `scenarios` in `plan.json`.

A timing metric is a CPU time (`cpu_ms`), a server route's CPU time (`server_cpu_ms`) or a memory size (`heap_mb`, `rss_mb`). A timing cell fails the job when the median of its reps is over twice its baseline and more than 250 ms over it for `cpu_ms` or 10 ms for `server_cpu_ms`, or over 1.25 times its baseline and more than 2 MB over it for `heap_mb` or 50 MB for `rss_mb`. CI measures such a cell again on fresh runners first, and fails the job when the median of all six reps is still over. A red `server_cpu_ms` route that the re-measure did not request is judged on its first three reps instead. These limits sit well past the spread of GitHub's runners, so a red guard is a real regression: fix it, or explain it in the pull request. A deliberate slowdown past the guard keeps that cell red on every later pull request, because the baselines do not move with it, so agree it with the maintainer before it merges. The job also fails when the base branch has no `tests/perf/budgets.json` or its file names no `runner`, when a baselined `cpu_ms`, `heap_mb` or `rss_mb` cell of a window the job still measures was not measured, when a timing cell measured no reps, when no measured timing cell has a baseline, and when a measure job does not finish, which includes a failing `tests/perf/*_test.py`, since each measure job runs those first. A timing cell with no baseline at the base, such as one a pull request adds to `plan.json`, is reported and not guarded. A baselined `server_cpu_ms` route the job did not measure is only a notice, because a route's cell exists only when its window requests the route, and so is a baselined cell of a window that the scenario's `windows` in the head `plan.json` no longer lists. Every other metric, such as a route's reads, an answer's size or a window's `sse_frames`, is information: the job summary lists each count that differs from its base value and never fails on one. A window's `sse_frames` counts the stream's frames without the hub's own `sse:` frames (hello, reset) and without the keepalive event the stream's hello names, so a window that runs past the server's 15-second keepalive counts the same frames as one that does not.

The baselines come from ten-run calibrations on the hosted runner, signed by cplieger/ci's `perf-calibrate` workflow, whose files sit under `tests/perf/calibrations/`; `tests/perf/budgets_test.py` checks that every entry is its calibration's and that the `pr` set's slowest shard fits the pull request's job. The guard reads the base branch's file, so a pull request cannot move its own baselines.

To measure one cell on your machine, build the test binary once, write the cell to a file, and run it. The run generates a synthetic fixture, then starts each rep on a fresh instance with a fake kiro-cli and a fake forge. It never touches your own marotte, and `serve.sh` refuses a `PERF_PORT` that would bind 9847:

```sh
tests/perf/build.sh /tmp/perf-build
(cd static-src && npx playwright install chromium webkit)
echo '[{"scenario": "chat-long", "scale": "x10", "engine": "chromium", "width": "desktop", "seconds": 240}]' >/tmp/cell.json
PERF_BUILD=/tmp/perf-build PERF_PORT=20151 PERF_REPS=1 tests/perf/run.sh /tmp/cell.json /tmp/perf-out
```

`/tmp/perf-out/results.json` holds every metric of every window, and `/tmp/perf-out/raw/` holds each rep's own record. Add `"profile": true` to the cell for one more rep that records a CPU profile, and `"windows": ["cold"]` to measure only the windows it lists. `run-drive` measures the server alone: use engine `server` and width `none`. A rep that does not settle exits 1 and names the window it waited for.

To guard a result as the pull request's `perf` job does, run `perf_gate.py` from a clone of `cplieger/ci`. It guards only results measured on the runner that `budgets.json` names, so the copy below takes that runner's name, and a time measured on your machine is then held to baselines CI's runner measured. The guard reads results only from folders named as CI's artifacts, `perf-results-<prefix>-measure-shard-<i>-of-<n>-attempt-<k>`, so the copy goes into one such folder and `--prefix` names it:

```sh
git clone --depth 1 https://github.com/cplieger/ci.git /tmp/cplieger-ci
shard=/tmp/perf-res/perf-results-cplieger-marotte-measure-shard-1-of-1-attempt-1
mkdir -p "$shard"
python3 - tests/perf/budgets.json /tmp/perf-out/results.json "$shard/results.json" <<'EOF'
import json, sys
budgets, results = (json.load(open(p)) for p in sys.argv[1:3])
results['runner'] = budgets['runner']
json.dump(results, open(sys.argv[3], 'w'))
EOF
python3 /tmp/cplieger-ci/scripts/perf_gate.py --base tests/perf/budgets.json \
  --results /tmp/perf-res --prefix cplieger-marotte --plan-doc tests/perf/plan.json --set pr \
  --only chat-long --remeasure-out /tmp/perf-remeasure.json
```

A timing cell over the guard shows `remeasure` in the "Status" column of "Guarded timing cells": CI measures it again on fresh runners before it fails the job. With `PERF_REPS=1` the median is the one rep. A count that differs from its base value is listed under "Counts that differ from the base (information)".

The `max_bytes` counts of `GET /api/settings`, `GET /api/tabs` and `GET /api/version` differ on your machine with no change of yours: those answers hold values of the machine the instance runs on, such as the instance's own directory, which lies under `RUNNER_TEMP` on the runner and under `TMPDIR` or `/tmp` on yours. The answer of `GET /api/engine` holds the time the engine last changed state and a sequence number, so its size moves with the clock on any machine, and `budgets.json` holds no entry for it. A count can also differ when a frame lands at a window's edge or a rep meets a race.

`--only chat-long` keeps every `pr` cell of that scenario, so the plan also holds its WebKit cell, which measures only the `cold` window. Its `cpu_ms` and `rss_mb` cells have baselines, so a run that did not measure that cell lists them under "Failures" as cells "no shard measured", and the command exits 1. Those lines name cells CI measures, not a fault in the cell you measured.

Two families of counts run with the ordinary test suites and need no calibration, because they come out the same on every machine.

The read budgets in `static-src/perf/read-budget/` hold one file per browser scenario of `plan.json`. Each boots the whole app in Vitest Browser Mode against the recorded answers in `static-src/perf/canned/`, replays the recorded stream on a fake clock through the scenario's five windows (`cold`, `rest`, `live`, `away`, `back`), and counts each route's reads and its largest answer in each window. `static-src/perf/read-budgets.json` holds those counts, and a count that differs either way fails. A change that moves a read by design rewrites the entries, and its pull request names the counts that moved:

```sh
cd static-src && READ_BUDGET_RECORD=1 npx vitest --run perf/read-budget/
```

A scenario added to `perf/views.ts` needs its own file there and its own entry; `perf/read-budget.test.ts` fails until it has both.

`static-src/perf/canned.test.ts` decodes each recorded answer with the generated decoder that `tests/perf/canned-routes.json` names for its route, and fails when the set is over 6 MiB, so a wire change that leaves the recording stale fails it. Record the set again from a fresh x1 instance, then rewrite the read budgets as above, since their counts come from the set:

```sh
tests/perf/build.sh /tmp/perf-build
PERF_BUILD=/tmp/perf-build PERF_PORT=20151 tests/perf/record-canned.sh
```

The recorder refuses an answer from a route that `canned-routes.json` does not name. Add the route with the decoder its client read uses: a `wire/decoders.gen.ts` name, that name with `[]` for an array, a map of field names to decoders for an answer the client wraps in a local decoder, or `null` for a route with no generated type, which the test checks only for being a JSON object.

The `*Cost_*` tests in `internal/chat`, `internal/agent`, `internal/filebrowse`, `internal/git` and `internal/server` hold what each server read costs per request, in allocations and bytes, at the x1 and x10 fixture sizes, within 5% either way. Their tables hold the `-race` build's costs, because CI runs Go tests only with `-race`, and the tests skip without it. A cost that moves by design rewrites its entry in the same diff. `-v` logs every entry's measured cost in the table's own form:

```sh
go test -race -run 'Cost_' -v ./internal/chat/
```
