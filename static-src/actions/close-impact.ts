// The refusal a tab close or a chat delete gets when the confirm's named work went stale: another
// device changed what it stops. The server answers its own impact in the error envelope.

import { ActionError, type ActionErrorLike } from "./index.js";
import type { SendResult } from "../transport.js";
import { decodeCloseImpact } from "../wire/decoders.gen.js";
import type { CloseImpact } from "../wire/types.gen.js";

/** The refusal's machine reason, and the ActionError's code. */
export const CLOSE_IMPACT_CHANGED = "close_impact_changed";

/** The decoded impact a refusal carries as its `cause`; a class, so a reader narrows the
 *  framework's `unknown` cause with `instanceof` instead of asserting it. */
class ServerImpact {
  constructor(readonly impact: CloseImpact) {}
}

/** The server's impact on a `close_impact_changed` refusal, or undefined (another failure, or a
 *  refusal whose impact did not decode). */
export function serverImpactOf(err: ActionErrorLike): CloseImpact | undefined {
  return err.code === CLOSE_IMPACT_CHANGED && err.cause instanceof ServerImpact
    ? err.cause.impact
    : undefined;
}

/** `plain` (the failed send as its action reads it) carrying the server's impact when `r` is a
 *  `close_impact_changed` refusal whose impact decodes; `plain` itself otherwise. */
export function impactRefusal(r: SendResult, plain: ActionError): ActionError {
  if (r.reason !== CLOSE_IMPACT_CHANGED) {
    return plain;
  }
  let impact: CloseImpact;
  try {
    const body = r.body;
    impact = decodeCloseImpact(
      typeof body === "object" && body !== null
        ? (body as Record<string, unknown>)["impact"]
        : undefined,
    );
  } catch {
    return plain;
  }
  return new ActionError(plain.message, {
    status: r.status,
    code: CLOSE_IMPACT_CHANGED,
    cause: new ServerImpact(impact),
  });
}
