import type { TransportSendResult } from "@cplieger/reactive/actions";
import type { SendResult } from "../transport.js";

/** A command's answer as `transportAction` reads it: the envelope's `reason` becomes the error code, so an
 *  action's spec branches on the server's failure class as a custom runner's does. */
export function forAction(r: SendResult): TransportSendResult {
  const code = r.reason ?? r.code;
  return {
    ok: r.ok,
    status: r.status,
    ...(r.error === undefined ? {} : { error: r.error }),
    ...(code === undefined ? {} : { code }),
  };
}
