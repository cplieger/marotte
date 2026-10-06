// Workflow-schedule actions. The server owns the recurrence math and next_run_at.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ScheduleSpec, SchedulesResponse, ScheduleView } from "../schedule-types.js";

export const loadSchedules = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SchedulesResponse
>({
  name: "schedules.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/schedules" }),
  error: "Could not load schedules",
});

/** Insert or replace one recipe's schedule. */
export const saveSchedule = apiAction<
  { source: string; spec: ScheduleSpec; enabled: boolean },
  ScheduleView
>({
  name: "schedules.save",
  request: (a) => ({
    method: "POST",
    path: "/api/schedules",
    body: { source: a.source, spec: a.spec, enabled: a.enabled },
  }),
  error: "Could not save the schedule",
});

/** Remove a recipe's schedule. */
export const deleteSchedule = apiAction<string, { ok: boolean }>({
  name: "schedules.delete",
  request: (id) => ({ method: "DELETE", path: `/api/schedules/${encodeURIComponent(id)}` }),
  error: "Could not remove the schedule",
});
