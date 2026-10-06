// The spec-approval action RECORDS a human sign-off of one phase against the exact version read;
// nothing enforces phase order. The chat id is EMPTY on purpose: a spec is workspace-global.

import { ActionError, defineAction, IDEMPOTENCY_COMMAND_FIELD } from "./index.js";
import { send as transportSend, type SendResult } from "../transport.js";

export interface ApproveSpecPhaseArgs {
  /** The workspace-relative spec directory, the tab's own ref. */
  dir: string;
  /** One of `requirements` | `design` | `tasks`. */
  phase: string;
  /** The sha256 the reader reviewed. The server's compare-and-swap precondition,
   *  not a value it trusts: a mismatch is refused rather than recorded. */
  hash: string;
}

/** approveSpecPhase records a human sign-off on one phase of one spec. A custom runner: only the
 *  envelope's `reason` says `doc_changed` (the page re-reads) versus a plain failure. `scope` per
 *  SPEC keeps two phases' read-backs in click order. `idempotencyKey: true` mints one key per
 *  dispatch, so a repeat reaches the server instead of replaying a cached success. `error: false`:
 *  the page's status row is the surface. */
export const approveSpecPhase = defineAction<
  ApproveSpecPhaseArgs,
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- nothing reads a result; the page refetches
  void
>({
  name: "spec.approve_phase",
  networkMode: "always",
  scope: ({ dir }) => `spec-approve:${dir}`,
  idempotencyKey: true,
  error: false,
  run: async ({ dir, phase, hash }, signal, ctx) => {
    const cmd: Parameters<typeof transportSend>[0] = {
      type: "approve_spec_phase",
      payload: { dir, phase, hash },
    };
    if (ctx?.idempotencyKey !== undefined) {
      (cmd as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD] = ctx.idempotencyKey;
    }
    const r: SendResult = await transportSend(cmd, { signal, reportSendState: false });
    if (!r.ok) {
      const opts: { status: number; code?: string } = { status: r.status };
      const code = r.reason ?? r.code;
      if (code !== undefined) {
        opts.code = code;
      }
      throw new ActionError(r.error ?? `approve failed (${String(r.status)})`, opts);
    }
  },
});
