// Toggles a workspace hook. The list is server-canonical (.kiro/hooks/*.json via KAS,
// internal/hub/hooks.go) and refetched after every mutation, so no optimistic state.

import { apiAction } from "./index.js";

const HOOKS_API = "/api/hooks";

interface SetEnabledArgs {
  id: string;
  enabled: boolean;
}

/** Flip a hook's enabled flag, persisted to its .kiro/hooks/*.json file. Deduped per hook id. */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for an action with no result
export const setHookEnabled = apiAction<SetEnabledArgs, void>({
  name: "hooks.set_enabled",
  dedupe: (args) => `hooks.set_enabled:${args.id}`,
  request: ({ id, enabled }) => ({
    method: "POST",
    path: `${HOOKS_API}/${encodeURIComponent(id)}/enabled`,
    body: { enabled },
  }),
  error: "Could not update hook",
});
