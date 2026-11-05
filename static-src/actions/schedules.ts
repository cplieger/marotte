// Workflow-schedule actions over the clock rules of /api/launch-rules. The server owns the
// recurrence math and next_run_at.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type {
  LaunchRulesResponse,
  LaunchRuleView,
  ScheduleSpec,
  SchedulesResponse,
  ScheduleView,
} from "../schedule-types.js";
import type { RunDefaults } from "../run-defaults-picker.js";
import { serverSentence } from "./server-sentence.js";

/** The schedule form's view of a clock rule; null for an event rule. */
function scheduleViewOf(r: LaunchRuleView): ScheduleView | null {
  const spec = r.when.clock;
  if (spec === undefined) {
    return null;
  }
  const st = r.status;
  const status = r.error === undefined ? st?.last_status : "failed";
  const reason = r.error ?? st?.last_reason;
  return {
    id: r.id,
    source: r.source,
    enabled: r.enabled,
    spec,
    ...(r.name === undefined ? {} : { name: r.name }),
    ...(r.model === undefined ? {} : { model: r.model }),
    ...(r.effort === undefined ? {} : { effort: r.effort }),
    ...(r.next_run_at === undefined ? {} : { next_run_at: r.next_run_at }),
    ...(st?.last_run_at === undefined ? {} : { last_run_at: st.last_run_at }),
    ...(status === undefined ? {} : { last_status: status }),
    ...(reason === undefined ? {} : { last_reason: reason }),
    ...(st?.waiting_for === undefined ? {} : { waiting_for: st.waiting_for }),
  };
}

export const loadSchedules = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SchedulesResponse
>({
  name: "schedules.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/launch-rules" }),
  decode: (data) => ({
    schedules: (data as LaunchRulesResponse).rules
      .map(scheduleViewOf)
      .filter((v): v is ScheduleView => v !== null),
  }),
  error: "Could not load schedules",
});

/** Create a recipe's clock rule, or replace the one `id` names. A refused model pick shows the
 *  server's sentence. */
export const saveSchedule = apiAction<
  { id?: string; source: string; spec: ScheduleSpec; enabled: boolean } & RunDefaults,
  LaunchRuleView
>({
  name: "schedules.save",
  request: (a) => ({
    method: a.id === undefined ? "POST" : "PUT",
    path:
      a.id === undefined ? "/api/launch-rules" : `/api/launch-rules/${encodeURIComponent(a.id)}`,
    body: {
      source: a.source,
      enabled: a.enabled,
      ...(a.model === undefined ? {} : { model: a.model }),
      ...(a.effort === undefined ? {} : { effort: a.effort }),
      when: { clock: a.spec },
    },
  }),
  error: (_args, err) => serverSentence(err) ?? "Could not save the schedule",
});

/** Remove a recipe's clock rule by its minted id. */
export const deleteSchedule = apiAction<string, { ok: boolean }>({
  name: "schedules.delete",
  request: (id) => ({ method: "DELETE", path: `/api/launch-rules/${encodeURIComponent(id)}` }),
  error: "Could not remove the schedule",
});
