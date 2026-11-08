// Launch-rule wire types. Hand-declared beside the feature (the forge-types.ts precedent) rather
// than generated: the surface is the launch-rule endpoints and one record.

/** Recurrence frequency; mirrors internal/launchrule.Freq. */
export type ScheduleFreq = "minutely" | "hourly" | "daily" | "weekly" | "monthly";

/** MonthDay sentinel meaning "the last day of the month". */
export const LAST_DAY = -1;

/** Bounds on the `interval` step, per frequency, mirroring internal/launchrule's minMinuteInterval
 *  / maxMinuteInterval and its hourly range. The minute FLOOR is not cosmetic, and it is the
 *  reason this table exists at all: "every minute" is what a user types when they mean "often",
 *  and the server refuses it because four mechanisms misbehave there (the once-a-minute runner
 *  tick, the 3-minute miss grace, the one-live-run-per-recipe rule, and the run's own
 *  interval-derived deadline, which cancels a run still going when its next slot comes due). */
export const INTERVAL_BOUNDS = {
  minutely: { min: 5, max: 59 },
  hourly: { min: 1, max: 24 },
} as const;

/** One recurrence rule; mirrors internal/launchrule.Spec. */
export interface ScheduleSpec {
  freq: ScheduleFreq;
  /** Recurrence step in the unit its freq names: minutes for `minutely`, hours for `hourly`.
   *  Bounded per frequency by INTERVAL_BOUNDS. */
  interval?: number;
  /** Days for `weekly`, as JS/Go weekday numbers (0 = Sunday). */
  weekdays?: number[];
  /** Day for `monthly` (1-31, or LAST_DAY). */
  month_day?: number;
  hour: number;
  /** Minute of the hour (0-59). The time of day for the calendar frequencies, the offset past
   *  each stepped hour for `hourly`, and the step's PHASE for `minutely` (taken as minute %
   *  interval, so the chosen minute is always itself a fire time). */
  minute: number;
}

/** One recipe's clock rule as the schedule form reads it, next run already resolved. */
export interface ScheduleView {
  spec: ScheduleSpec;
  enabled: boolean;
  /** RFC3339; the server computes this so the client never does. */
  next_run_at?: string;
  last_run_at?: string;
  /** How the last slot ended; mirrors internal/launchrule.OutcomeStatus. */
  last_status?: "started" | "completed" | "failed" | "unknown" | "skipped";
  /** Why, as plain text for a person to read. */
  last_reason?: string;
  /** RFC3339 due time of a slot whose launch is retried until the agent engine answers. */
  waiting_for?: string;
}

/** The forge event an event rule watches for; mirrors internal/launchrule.Kind. */
export type EventKind =
  "issue_labeled" | "pr_labeled" | "issue_opened" | "pr_opened" | "run_concluded";

/** What an event rule holds while its recipe cannot launch; mirrors internal/launchrule.Hold. */
export interface Hold {
  policy: "newest" | "fifo";
  cap: number;
}

/** Mirrors internal/launchrule.Filter; which fields a kind takes is the server's rule. */
export interface EventFilter {
  label?: string;
  branch?: string;
  workflow?: string;
  authors?: string[];
  result?: ("success" | "failure" | "neutral")[];
  run_triggers?: ("push" | "schedule" | "manual" | "pull_request" | "other")[];
}

/** One event rule's `when`; mirrors internal/launchrule.EventSpec. */
export interface EventSpec {
  connection: string;
  repo: string;
  kind: EventKind;
  filter: EventFilter;
  interval_min?: number;
  hold?: Hold;
}

/** A stretch in which an event rule may have missed a run; mirrors internal/launchrule.Gap. */
export interface Gap {
  from: string;
  to: string;
  cause: "outage" | "aged_out" | "result_window";
  run_id?: number;
  omitted_at_least?: number;
  queue_full?: boolean;
  /** An outage's read horizon: runs created before it that ended in the gap were not checked. */
  created_before?: string;
}

/** A launch rule's row; mirrors internal/launchrule.Status. */
export interface LaunchRuleStatus {
  last_run_at?: string;
  waiting_for?: string;
  last_status?: ScheduleView["last_status"];
  last_reason?: string;
  /** Why an event rule launches nothing until it is edited or resumed. */
  blocked?: string;
  /** An event rule's launch state as its row reads it. */
  row_text?: string;
  /** The key of an event rule's last confirmed event. */
  last_event?: string;
  /** RFC3339 start of an event rule's last applied poll. */
  checked_at?: string;
  /** Where and why an event rule's last walk stopped short while it has no baseline. */
  baseline_stop?: string;
  /** Why an event rule's reads wait for a forge or trigger budget, and until when. */
  paused?: string;
  gaps?: Gap[];
}

/** An event rule's cursor as counts and sentences; mirrors internal/trigger.CursorSummary. */
interface CursorSummary {
  blocked?: string;
  notes?: string[];
  baselined: boolean;
}

/** One launch rule as GET /api/launch-rules renders it; `when` holds exactly one of the two. */
export interface LaunchRuleView {
  id: string;
  source: string;
  name?: string;
  enabled: boolean;
  model?: string;
  effort?: string;
  inputs?: Record<string, string>;
  when: { clock?: ScheduleSpec; event?: EventSpec };
  next_run_at?: string;
  /** Absent, with `error` set, when the rule's state file would not load. */
  status?: LaunchRuleStatus;
  error?: string;
  cursor?: CursorSummary;
  held: number;
}

export interface LaunchRulesResponse {
  rules: LaunchRuleView[];
}

/** The schedule form's view of a clock rule; null for an event rule. A rule whose state file would
 *  not load reads as failed, with the load error as its reason. */
export function scheduleViewOf(r: LaunchRuleView): ScheduleView | null {
  const spec = r.when.clock;
  if (spec === undefined) {
    return null;
  }
  const st = r.status;
  const status = r.error === undefined ? st?.last_status : "failed";
  const reason = r.error ?? st?.last_reason;
  return {
    enabled: r.enabled,
    spec,
    ...(r.next_run_at === undefined ? {} : { next_run_at: r.next_run_at }),
    ...(st?.last_run_at === undefined ? {} : { last_run_at: st.last_run_at }),
    ...(status === undefined ? {} : { last_status: status }),
    ...(reason === undefined ? {} : { last_reason: reason }),
    ...(st?.waiting_for === undefined ? {} : { waiting_for: st.waiting_for }),
  };
}
