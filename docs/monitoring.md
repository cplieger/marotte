# Monitoring and alerts

marotte serves a Prometheus metrics page and ships metric alert rules in [`alerts/promql.yaml`](../alerts/promql.yaml). The rules cover engine crashes, prompt failures, memory pressure, schedules that stopped running, event rules that stopped launching and unattended runs that failed. marotte ships no log rules, so alerting needs a metric store. Without one, marotte's push notifications and its page tell you about turns, permission requests and workflow runs.

## What you get

| You run | You get | You set up |
| --- | --- | --- |
| Prometheus, Mimir or VictoriaMetrics, with Alertmanager | Every alert below, `up` for a dead container, and graphs of every series | A scrape job named `marotte` on port 9847, a scraper name in `ALLOWED_HOSTS`, and `alerts/promql.yaml` in your ruler |
| Loki and Alertmanager, no metric store | No alerts: marotte's rules need a metric store | Push notifications, below |
| Grafana-managed alerting only | The same alerts, imported into Grafana | A Prometheus data source that scrapes marotte, and the rules imported from `alerts/promql.yaml` |
| Docker or Unraid only | Push notifications for finished turns and workflow runs and for permission requests, and Docker health | An uptime prober on `/healthz`, or something that watches Docker health |

Push notifications need marotte installed as an app over HTTPS, as [Features](features.md#notifications) describes.

## Metrics

The page is always on at `http://<host>:9847/metrics`, on the same port as the web page, and answers `GET` only. It has no setting to turn it off.

When `ALLOWED_HOSTS` is set, the scraper must call marotte by a name in that list, or marotte answers `403`. A Docker network name such as `marotte` works once you add it to the list. [Host allowlist](configuration.md#host-allowlist-allowed_hosts) explains the setting.

The page has no login, and marotte has none of its own. If you put marotte behind a reverse proxy that asks for a login, exempt `/metrics` at the proxy. Otherwise, scrape the container directly, but only from a network you already trust with the whole agent, because anyone who reaches port 9847 can use it.

Labels carry fixed values only, with one exception. `rule_id` is the random id marotte gives a schedule or an event rule, and it names no workflow, path, repository or command.

| Series | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `marotte_engine_up` | gauge | none | 1 while the agent engine is ready to take prompts, else 0 |
| `marotte_engine_exits_total` | counter | `cause` | Agent engine exits, by cause |
| `marotte_engine_resident_memory_bytes` | gauge | none | The running engine's resident memory at its last sample, taken every 60 seconds. Absent while no engine runs |
| `marotte_engine_heap_limit_bytes` | gauge | none | The running engine's JavaScript heap limit. Absent while the limit is unknown |
| `marotte_prompt_failures_total` | counter | `class` | Failed prompts, by `fatal`, `pipe_death`, `busy`, `transient`, `throttled`, `rejected` or `auth`. `pipe_death` is a prompt the engine's own exit ended |
| `marotte_signin_save_failures_total` | counter | `backend`, `reason` | Sign-in changes marotte could not save, by `staged_copy_left` or `write_failed` |
| `marotte_schedule_failed_slots` | gauge | `rule_id` | Consecutive slots of an enabled schedule that ended without a completed run. A completed run resets it to 0 |
| `marotte_schedule_next_due_timestamp_seconds` | gauge | `rule_id` | When an enabled schedule's next slot falls due, in Unix seconds. A slot waiting on a busy agent engine reads the end of its wait, the schedule's next slot |
| `marotte_schedule_slots_not_started_total` | counter | `event` | Schedule slots that started no run, by `launch_failed`, `signin_held` or `engine_busy` |
| `marotte_unattended_run_failures_total` | counter | `origin`, `reason` | Scheduled and triggered runs that failed with nobody watching. `origin` is `scheduled` or `triggered`. `reason` is `needs_approval`, `overran`, `backstop`, `stalled`, `paused`, `orphaned` or `cancel_given_up` |
| `marotte_unattended_approvals_total` | counter | `origin` | Tool requests of scheduled and triggered runs approved with nobody watching |
| `marotte_trigger_rule_blocked` | gauge | `rule_id`, `reason` | 1 while an enabled event rule launches nothing until someone acts, else 0, for each `reason`: `launch_failed`, `unnamed_run`, `too_many_items`, `state_unreadable` or `state_unwritten` |
| `marotte_trigger_queue_full` | gauge | `rule_id` | 1 while an enabled event rule's last poll left an event at the forge because its queue was full, else 0 |
| `marotte_trigger_poll_failures` | gauge | `rule_id` | Consecutive polls in which an enabled event rule's read of its repository failed. A good read resets it to 0, and a rate limit leaves it as it is |
| `marotte_trigger_catch_up_gaps_total` | counter | `cause` | Time ranges an event rule could not read back, by `outage`, `aged_out` or `result_window` |

An engine exit's `cause` is one of these. marotte asked for `restart_confirmed`, `restart_auto`, `shutdown` and `stopped`. Another process sent `external_stop`. The engine ended by itself in `crash`, `stdin_stalled`, `parse_breaker` and `lane_overflow`.

A disabled or deleted schedule's or event rule's series leave the page. Every counter starts at 0 for each of its label values, so the first event after a start shows in `increase()`.

The page also carries the Go runtime and process families (`go_*`, `process_*`) under the names Go's official Prometheus client uses, so stock Go dashboards work. marotte bundles no dashboard. These queries are a start in Grafana's Metrics Drilldown:

- `marotte_engine_up` shows when the engine was ready.
- `sum by (cause) (increase(marotte_engine_exits_total[1d]))` shows each day's engine exits by cause.
- `marotte_engine_resident_memory_bytes / marotte_engine_heap_limit_bytes` shows how close the engine runs to its limit.

## Logs

marotte writes logfmt lines to the container's output at INFO, or at DEBUG when **Debug logs** on the **General** tab in **Settings** is on. No alert rule reads the log. The engine, prompt and sign-in alerts name the log line that says more.

## Alerting

Load [`alerts/promql.yaml`](../alerts/promql.yaml) into Prometheus, Mimir or vmalert. It is the only rule file. [Loading metric alert rules](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#loading-metric-alert-rules) shows how.

| Alert | Fires when | Severity |
| --- | --- | --- |
| `MarotteDown` | The scrape of job `marotte` failed or found no target for 5 minutes | warning |
| `MarotteEngineDown` | The agent engine has not been ready for 10 minutes | critical |
| `MarotteEngineCrashed` | The engine ended by itself in the last 15 minutes | warning |
| `MarotteEngineCrashLoop` | The engine ended by itself 3 or more times in 15 minutes | critical |
| `MarotteEngineStoppedExternally` | A signal from another process stopped the engine in the last 15 minutes | warning |
| `MarotteEngineMemoryHigh` | The engine's resident memory stayed above 80% of its heap limit for 2 minutes | warning |
| `MarottePromptFailures` | 5 or more prompts failed in 15 minutes, leaving out those an engine exit ended | warning |
| `MarotteSignInStagedCopyLeft` | A sign-in save was refused because an earlier save's file is still on disk | warning |
| `MarotteScheduleFailing` | A schedule's last 2 or more slots ended without a completed run | warning |
| `MarotteScheduleStalled` | A schedule slot has been overdue by more than 10 minutes for 5 minutes | warning |
| `MarotteScheduleSlotNotStarted` | A schedule slot started no run in the last 24 hours | warning |
| `MarotteUnattendedRunNeedsApproval` | An unattended run failed on a refused permission request in the last 24 hours | warning |
| `MarotteUnattendedRunStopped` | marotte stopped an unattended run that overran, stalled, spent its time budget, paused or was orphaned, in the last 24 hours | warning |
| `MarotteUnattendedRunCancelFailed` | kiro-cli refused every cancel of an unattended run in the last 24 hours | critical |
| `MarotteUnattendedRunSelfApproved` | A tool request of an unattended run was approved with nobody watching in the last 24 hours | warning |
| `MarotteTriggerRuleBlocked` | An event rule launches nothing until someone acts: its launches kept failing, a run it has no record of holds its workflow, it matches more than 5,000 open items, or its saved state does not load | warning |
| `MarotteLaunchRulesNotWritten` | marotte cannot write an event rule's saved state | critical |
| `MarotteTriggerQueueFull` | An event rule's last poll left events at the forge because its queue was full | warning |
| `MarotteTriggerPollFailing` | An event rule's read of its repository failed 3 or more polls in a row | warning |
| `MarotteTriggerCatchUpGap` | An event rule could not read back over a time range in the last hour, so events in it may never launch | warning |

Thresholds and the `severity` labels are starting points. Keep the scrape job name `marotte`, which `MarotteDown` reads, or change the rule to your job name.

A requested engine restart is back within seconds, and a first start can spend up to 5 minutes downloading kiro-cli, so `MarotteEngineDown` waits 10 minutes. `MarotteUnattendedRunSelfApproved` reports what the **Unattended runs** switch on the **Permissions** tab in **Settings** allows. Turn the switch off, or delete the rule, if you do not want to hear about it.

## Knowing the app is alive

| Route | Method | Port | Login | Answers |
| --- | --- | --- | --- | --- |
| `/healthz` | `GET` or `HEAD` | 9847 | none. `ALLOWED_HOSTS` applies, and requests from inside the container are always allowed | `200 {"status":"ok"}`, or `503 {"status":"unhealthy","reasons":["..."]}`. Any other method gets `405` |
| `/metrics` | `GET` | 9847 | none. Keep the port on your network | Prometheus text format |

**What healthy means.** The server is up and not shutting down, the pinned kiro-cli is installed at its exact version with its self-update off, and marotte is signed in to Kiro. [Health and repair](how-it-works.md#health-and-repair) lists each reason. The agent engine's state is not part of it, so `MarotteEngineDown` covers an engine that stays down.

**Docker health.** The image checks itself every 30 seconds with `curl -sf http://127.0.0.1:9847/healthz`. Docker marks the container unhealthy after 3 failures, and allows 5 minutes after a start for the kiro-cli download. Docker's health status notifies nobody on its own, and Docker does not restart an unhealthy container. The example `compose.yaml` restarts a container that exits.

**Who is told.**

| You run | A dead app | A wedged app |
| --- | --- | --- |
| Prometheus with this app's rules | `MarotteDown` | `MarotteEngineDown` for the engine, `MarotteScheduleStalled` for the schedule runner |
| Loki only | an uptime prober on `/healthz` | the same prober, which sees a broken kiro-cli install but not a wedged engine |
| Docker or Unraid only | an uptime prober on `/healthz`, or something that watches Docker health | the same |

**Probing from outside.** Check `GET http://<host>:9847/healthz` and pass on status 200 only. Do not match the body. Use a 5-second timeout, check every 60 seconds, alert after 3 failures, and wait 5 minutes after a first start. When `ALLOWED_HOSTS` is set, address marotte by a name in the list, or it answers `403`. A Kubernetes liveness probe that must ignore readiness uses a `tcpSocket` probe on port 9847.
