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

On a pull request, CI runs the `pr` set of `tests/perf/plan.json` on a hosted runner and compares each cell with `tests/perf/budgets.json`. A cell is one scenario at one fixture scale (`x1` or `x10`), browser engine and width. Its scenarios are listed under `scenarios` in `plan.json`.

A measured metric of a cell has at most one entry in `budgets.json`, in one of three tiers that a ten-run calibration on the hosted runner decided. A metric with no entry only reports, such as the answer size of `GET /api/engine`, which moves with the clock. A window's `sse_frames` counts the stream's frames without the hub's own `sse:` frames (hello, reset) and without the keepalive event the stream's hello names, so a window that runs past the server's 15-second keepalive counts the same frames as one that does not. An `exact` count, such as a route's reads, is one every rep of the calibration agreed on. When its middle rep of three differs from its baseline, CI measures it again on fresh runners, and it fails the job when the two middle values of the six reps are not both its baseline, so one rep that a race moved never fails it while a change that moves every rep does. A `gated` time or size fails when its median over the reps is above its budget, which the calibration set from its baseline and a headroom for the runner's spread. A `reported` metric is shown and never fails. Every baseline comes from a calibration file under `tests/perf/calibrations/`, signed by cplieger/ci's `perf-calibrate` workflow, and CI refuses a baseline that no signed file holds. A change that moves an exact count or a gated cost therefore needs a new calibration of the scenarios it moves. The maintainer runs it on the pull request's branch and commits the new baselines with its signed file, so say in the pull request which scenarios you expect to move.

To measure one cell on your machine, build the test binary once, write the cell to a file, and run it. The run generates a synthetic fixture, then starts each rep on a fresh instance with a fake kiro-cli and a fake forge. It never touches your own marotte, and `serve.sh` refuses a `PERF_PORT` that would bind 9847:

```sh
tests/perf/build.sh /tmp/perf-build
(cd static-src && npx playwright install chromium webkit)
echo '[{"scenario": "chat-long", "scale": "x10", "engine": "chromium", "width": "desktop", "seconds": 240}]' >/tmp/cell.json
PERF_BUILD=/tmp/perf-build PERF_PORT=20151 PERF_REPS=1 tests/perf/run.sh /tmp/cell.json /tmp/perf-out
```

`/tmp/perf-out/results.json` holds every metric of every window, and `/tmp/perf-out/raw/` holds each rep's own record. Add `"profile": true` to the cell for one more rep that records a CPU profile, and `"windows": ["cold"]` to measure only the windows it lists. `run-drive` measures the server alone: use engine `server` and width `none`. A rep that does not settle exits 1 and names the window it waited for.

To compare a result with the budgets as the pull request's `perf` job does, run `perf_gate.py` from a clone of `cplieger/ci`. It gates only results measured on the runner that `budgets.json` names, so the copy below takes that runner's name. An exact count, such as a route's reads or `git_fetch`, is the same on any machine except for the answer sizes and the races named below; a time measured on yours is compared with budgets calibrated on CI's runner. `--base` names the head file, so no budget needs its signed calibration here. The gate reads results only from folders named as CI's artifacts, `perf-results-<prefix>-measure-shard-<i>-of-<n>-attempt-<k>`, so the copy goes into one such folder and `--prefix` names it:

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
python3 /tmp/cplieger-ci/scripts/perf_gate.py --budgets tests/perf/budgets.json \
  --base tests/perf/budgets.json --results /tmp/perf-res --prefix cplieger-marotte \
  --calibrations tests/perf/calibrations \
  --repo cplieger/marotte --head "$(git rev-parse HEAD)" --plan-doc tests/perf/plan.json --set pr \
  --only chat-long --remeasure-out /tmp/perf-remeasure.json
```

Two results concern the cell you measured. An exact count whose middle rep differs from its budget, or a timed cell whose median is over its budget, shows `remeasure` in the "Status" column: CI measures it again on fresh runners before it fails the job. With `PERF_REPS=1` the middle rep is the one rep.

The `max_bytes` counts of `GET /api/settings`, `GET /api/tabs` and `GET /api/version` differ on your machine with no change of yours: those answers hold values of the machine the instance runs on, such as the instance's own directory, which lies under `RUNNER_TEMP` on the runner and under `TMPDIR` or `/tmp` on yours. The answer of `GET /api/engine` holds the time the engine last changed state and a sequence number, so its size moves with the clock on any machine, and `budgets.json` holds no entry for it. An exact count can also differ when a frame lands at a window's edge or a rep meets a race the calibration did not see. Read a `remeasure` count for those three routes as the machine, and any other one as your change or such a race: the pull request's own `perf` job on CI is the verdict.

`--only chat-long` keeps every `pr` cell of that scenario, so the plan also holds its WebKit cell, which measures only the `cold` window. Once `budgets.json` holds a gated or exact entry for a cell the plan holds and your run did not measure, "Failures" lists it as an entry "that no shard measured" and the command exits 1. That line names a cell CI measures, not a fault in the cell you measured.
