// Actions for the native (Cedar) permission policy, the sole tool-call authorization surface.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { PolicyExplainResult } from "../types.js";

// These write permissions.yaml, which KAS hot-reloads. No optimistic update: the view is a server
// projection refetched after an edit (and on permissions_changed), so it cannot drift from KAS.

/** Add, remove, or update a native policy rule. "add" defaults an empty effect to "ask"; "remove"
 *  and "update" need the exact existing rule; any widening change needs confirm=true. */
interface NativeRuleArgs {
  op: "add" | "remove" | "update";
  scope: "user" | "workspace";
  capability: string;
  /** allow | deny | ask. Empty on add → server defaults to ask. */
  effect: string;
  /** Target effect for op="update". */
  new_effect?: string;
  match?: string[];
  exclude?: string[];
  confirm?: boolean;
}

export const editNativeRule = apiAction<NativeRuleArgs, { ok?: boolean; error?: string }>({
  name: "permissions.edit_native_rule",
  // Both ops no-op server-side when already applied, so a retried timeout is safe.
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  idempotencyKey: true,
  scope: "permissions",
  request: (a) => ({ method: "POST", path: "/api/permissions/rules", body: a }),
  error: "Could not update policy rule",
});

/** Simulate the policy decision for a capability/resource. KAS evaluateSingleResource raises no
 *  consent prompt, so this is a safe UI pre-flight. Errors are shown inline. */
export const explainPolicy = apiAction<
  { capability?: string; tool_id?: string; resource?: string },
  PolicyExplainResult
>({
  name: "permissions.explain",
  scope: "permissions",
  request: (a) => ({ method: "POST", path: "/api/permissions/explain", body: a }),
  error: false,
});

/** Select the security profile. Its own endpoint: a selection REPLACES the policy, clearing both
 *  writable permissions files; `seed` (Customize) first materialises the profile into the table.
 *  Not retryable or keyed: a replay after a partial failure would clear a policy the user has
 *  since started editing. */
export const setSecurityProfile = apiAction<
  { profile: string; seed: boolean },
  { ok?: boolean; error?: string }
>({
  name: "permissions.set_profile",
  scope: "permissions",
  request: (a) => ({ method: "POST", path: "/api/permissions/profile", body: a }),
  error: false,
});
