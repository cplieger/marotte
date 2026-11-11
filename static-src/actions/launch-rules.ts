// Launch-rule actions over /api/launch-rules. The server owns the recurrence math, next_run_at
// and every event rule's row state.

import { join as joinKey } from "@cplieger/keyenc";
import type { RequestSpec } from "@cplieger/reactive/actions";
import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { LaunchRulesResponse, LaunchRuleView } from "../launch-rule-types.js";
import type { RunDefaults } from "../run-defaults-picker.js";
import { serverSentence } from "./server-sentence.js";

export const loadLaunchRules = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  LaunchRulesResponse
>({
  name: "launch-rules.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/launch-rules" }),
  error: "Could not load the launch rules",
});

/** One rule as a save sends it; the server stores the rule whole, so every field is sent each time. */
export type LaunchRuleBody = {
  id?: string;
  source: string;
  enabled: boolean;
  when: LaunchRuleView["when"];
  inputs?: Record<string, string>;
  /** On an edit, the held-event count the operator agreed to drop; sent only when set. */
  confirm_drop?: number;
} & RunDefaults;

const saveDedupe = (a: LaunchRuleBody): string =>
  a.id === undefined ? joinKey("new", a.source) : joinKey("rule", a.id);

function saveRequest(a: LaunchRuleBody): RequestSpec {
  return {
    method: a.id === undefined ? "POST" : "PUT",
    path:
      a.id === undefined ? "/api/launch-rules" : `/api/launch-rules/${encodeURIComponent(a.id)}`,
    body: {
      source: a.source,
      enabled: a.enabled,
      ...(a.model === undefined ? {} : { model: a.model }),
      ...(a.effort === undefined ? {} : { effort: a.effort }),
      ...(a.inputs === undefined ? {} : { inputs: a.inputs }),
      when: a.when,
      ...(a.id === undefined || a.confirm_drop === undefined
        ? {}
        : { confirm_drop: a.confirm_drop }),
    },
  };
}

/** Create a rule, or replace the one `id` names, answering the stored rule; a refusal toasts the
 *  server's sentence. A create joins one for the same recipe still in flight, because a second POST
 *  would mint a second rule the form never shows. */
export const saveLaunchRule = apiAction<LaunchRuleBody, LaunchRuleView>({
  name: "launch-rules.save",
  dedupe: saveDedupe,
  request: saveRequest,
  error: (_args, err) => serverSentence(err) ?? "Could not save the rule",
});

/** saveLaunchRule with no toast, for the event form: it shows a refusal on its own line, and its
 *  first 409 is the held-event confirm's question, not an error. */
export const saveEventRule = apiAction<LaunchRuleBody, LaunchRuleView>({
  name: "launch-rules.save-event",
  dedupe: saveDedupe,
  request: saveRequest,
  error: false,
});

/** Remove a rule by its minted id; an event rule's held events go with it. */
export const deleteLaunchRule = apiAction<string, { ok: boolean }>({
  name: "launch-rules.delete",
  request: (id) => ({ method: "DELETE", path: `/api/launch-rules/${encodeURIComponent(id)}` }),
  error: "Could not remove the rule",
});

/** Clear an event rule's gap segments, answering the rule as it now reads. */
export const dismissGap = apiAction<string, LaunchRuleView>({
  name: "launch-rules.dismiss-gap",
  request: (id) => ({
    method: "POST",
    path: `/api/launch-rules/${encodeURIComponent(id)}/dismiss-gap`,
  }),
  error: (_args, err) => serverSentence(err) ?? "Could not dismiss the gap",
});
