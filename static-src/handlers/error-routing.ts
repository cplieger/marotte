// ---------------------------------------------------------------------------
// Error classification table: maps server ErrorCode to UI surface.
// ---------------------------------------------------------------------------

import type { SettingsTab } from "../route-path.js";
import type { ErrorCode } from "../wire/types.gen.js";

/** The in-app jump a routed error offers, as DATA so the routing table stays a table. `setting`
 *  names a Settings control; `sign-in` opens the login modal. */
export type ErrorAction =
  | { kind: "setting"; tab: SettingsTab; control: string; label: string }
  | { kind: "sign-in"; label: string };

export interface ErrorRoute {
  /** Where this error is reported. `toast`: bottom-right beside the turn's own divider, raised for
   *  EVERY chat and named when its tab is not on screen. `agent-down`: the send button's alert face,
   *  only for "no agent to send to" (see send-state.ts). */
  surface: "toast" | "agent-down";
  action?: ErrorAction;
}

// Whether a failure ends a turn is a property of the EMISSION, not the code (three of the five
// `prompt_failed`/`recovery_failed` emitters open no turn), so the server states it per frame as
// `ErrorPayload.turn_scoped`, read in turn.ts. Absent means no, the direction that reports.

export const ERROR_ROUTES: Readonly<Partial<Record<ErrorCode, ErrorRoute>>> = {
  agent_not_found: { surface: "toast" },
  // About authored `.kiro/agents` configuration, so the CTA is the global instructions box. Sticky:
  // such a typo blocks the chat and nothing else on screen says so.
  agent_config_error: {
    surface: "toast",
    action: {
      kind: "setting",
      tab: "instructions",
      control: "steering-input",
      label: "Open custom instructions",
    },
  },
  rate_limit: { surface: "toast" },
  compaction_failed: { surface: "toast" },
  // The chat is running, just not in the mode that was asked for, and the fix is
  // one click on the mode pill, so this reports without blocking the composer.
  mode_not_applied: { surface: "toast" },
  // Reports without blocking the composer. REQUIRED, not left to the fallthrough: an unmapped code
  // on a generic failure surface would claim the turn failed.
  supervised_not_applied: { surface: "toast" },
  // kiro-cli could not vend a KAS access token, so service-backed surfaces will fail. Sticky, with
  // the login modal as CTA; turn-scoped AND actionable, so the toast is raised beside the turn's
  // inline reason.
  auth_token_unavailable: {
    surface: "toast",
    action: { kind: "sign-in", label: "Sign in" },
  },
  // The four failed-attempt codes: each ends the turn and leaves a promptable chat, so none reaches
  // the send button (the next Send is the retry). `recovery_failed` is explicit because it means the
  // automatic repair gave up.
  prompt_failed: { surface: "toast" },
  recovery_failed: { surface: "toast" },
  switch_failed: { surface: "toast" },
  model_not_served: { surface: "toast" },
  // The ONE code that earns the send button's alert face: kiro-cli could not be
  // spawned, so this chat has no ACP connection behind it. Every other failure
  // here happened to a live agent.
  bridge_start_failed: { surface: "agent-down" },
};
