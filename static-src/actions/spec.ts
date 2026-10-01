// ---------------------------------------------------------------------------
// The spec-approval action. ONE action, because an approval is one operation:
// it RECORDS that a human signed off one phase against the exact version they
// read. Nothing here enforces a phase order and no control is disabled by an
// approval.
//
// The chat id is EMPTY on purpose: a spec is workspace-global rather than a
// chat's, which is why its invalidation event is workspace-global too. The
// server's handler validates no chat id, so omitting the field is what the
// command means rather than a value left unset.
// ---------------------------------------------------------------------------

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

/** approveSpecPhase records a human sign-off on one phase of one spec.
 *
 *  A custom runner rather than `transportAction` for `chat.steer`'s reason: the
 *  refusal CLASS decides what the page says, and only the envelope's `reason`
 *  carries it — `doc_changed` means the file moved under the reader and the page
 *  re-reads it, where anything else is a plain failure. `SendResult` carries no
 *  `current_hash`, and the page needs none: it refetches, which brings the live
 *  hash and the derived `stale` together rather than patching a badge from an
 *  error body.
 *
 *  `scope` per SPEC rather than per phase: two phases approved in one gesture
 *  are two commands whose replies both re-read the same record, and the store
 *  merges under its own lock either way — serializing them is what keeps the
 *  page's own read-back in the order the reader clicked.
 *
 *  `idempotencyKey: true` mints one key per DISPATCH (threaded through that
 *  dispatch's retries), never a composite of the arguments: a repeat of the same
 *  approval must reach the server rather than replay a cached success from
 *  inside the 5-minute window.
 *
 *  `error: false`: the page's own status row is the surface, so the toast stack
 *  never doubles it. */
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
