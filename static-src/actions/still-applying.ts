// A change made through kiro-cli that has not answered yet may still land, so its failure text says so and the
// list's changed event settles it. internal/agent/engine_mutation.go owns the server side.

import type { ActionErrorLike } from "./index.js";

/** The code of the server's 504 for a change kiro-cli has not answered yet. */
export const STILL_APPLYING = "still_applying";

/** True when the change may still land: the server answered still_applying, or the browser stopped waiting first. */
export function mayStillApply(err: ActionErrorLike): boolean {
  return err.code === STILL_APPLYING || err.code === "timeout";
}

/** An `error` spec: "Still <doing>" while the change may land, else `<failed>: <the server's message>`. */
export function stillOr(
  doing: string,
  failed: string,
): (args: unknown, err: ActionErrorLike) => string {
  return (_args, err) =>
    mayStillApply(err)
      ? `Still ${doing}; the list updates when it lands`
      : `${failed}: ${err.message}`;
}
