// Workflow-schedule wire types. Hand-declared beside the feature (the forge-types.ts precedent)
// rather than generated: the surface is the launch-rule endpoints and one record.

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
  /** The rule's server-minted id, which a save of this rule sends back. */
  id: string;
  source: string;
  /** The recipe's name when the schedule was saved. */
  name?: string;
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
  /** The run's model and effort pick, sent at each fire; absent is Default. */
  model?: string;
  effort?: string;
  /** Fixed inputs sent with each fire; a save sends them back, since the server stores the rule whole. */
  inputs?: Record<string, string>;
}

export interface SchedulesResponse {
  schedules: ScheduleView[];
}

/** A launch rule's row; mirrors internal/launchrule.Status. */
interface LaunchRuleStatus {
  last_run_at?: string;
  waiting_for?: string;
  last_status?: ScheduleView["last_status"];
  last_reason?: string;
}

/** One launch rule as GET /api/launch-rules renders it. An event rule's `when` is opaque here. */
export interface LaunchRuleView {
  id: string;
  source: string;
  name?: string;
  enabled: boolean;
  model?: string;
  effort?: string;
  inputs?: Record<string, string>;
  when: { clock?: ScheduleSpec; event?: unknown };
  next_run_at?: string;
  /** Absent, with `error` set, when the rule's state file would not load. */
  status?: LaunchRuleStatus;
  error?: string;
}

export interface LaunchRulesResponse {
  rules: LaunchRuleView[];
}
