// The push kinds a user can switch off and their settings keys, DOM-free so a module can read them
// without loading the notification runtime.

/** The push kinds the user can switch off, keyed by wire value (`marotte.PushKind`)
 *  and paired with each settings key. `permission` has none on purpose: an ask
 *  blocks the turn, so a channel that could go dark would stall every later turn
 *  with nothing on screen to say why. */
export const KEYED_PUSH_KINDS: Readonly<
  Record<string, "notify_agent_finished" | "notify_pr_status" | "notify_run_outcome">
> = {
  agent_finished: "notify_agent_finished",
  pr_status: "notify_pr_status",
  run_outcome: "notify_run_outcome",
};

/** Each keyed kind's default (`pr_status` is OFF). A mirror the payload cannot
 *  replace: `EffectiveSettings` carries values in force, so a switched-off kind
 *  looks like one defaulting off. push-kinds.test.ts keeps the two in step. */
export const KEYED_PUSH_DEFAULTS: Readonly<Record<keyof typeof KEYED_PUSH_KINDS, boolean>> = {
  agent_finished: true,
  pr_status: false,
  run_outcome: true,
};
