// A change made through kiro-cli that has not answered yet may still land, so its failure text says so and the
// changed event of its list or chat settles it. internal/agent/engine_mutation.go and
// internal/command/engine_change.go own the server side.

// From the library, not ./index.js: many tests replace ./index.js with a partial mock.
import { retryNetwork, type ActionErrorLike } from "@cplieger/ui/actions";

/** The code of the server's 504 for a change kiro-cli has not answered yet. */
const STILL_APPLYING = "still_applying";

/** True when the change may still land: the server answered still_applying, or the browser stopped waiting first. */
export function mayStillApply(err: ActionErrorLike): boolean {
  return err.code === STILL_APPLYING || err.code === "timeout";
}

/** `retryNetwork` minus a still_applying answer: the first attempt may still land, and a 5xx frees the
 *  Idempotency-Key slot, so a retry would apply the change a second time. */
export function retryUnlessStill(err: ActionErrorLike): boolean {
  return err.code !== STILL_APPLYING && retryNetwork(err);
}

/** An `error` spec: "Still <doing>" while the change may land, else `<failed>: <the server's message>`. `shows` names
 *  what repaints when it lands. */
export function stillOr(
  doing: string,
  failed: string,
  shows: "list" | "chat" = "list",
): (args: unknown, err: ActionErrorLike) => string {
  return (_args, err) =>
    mayStillApply(err)
      ? `Still ${doing}; the ${shows} updates when it lands`
      : `${failed}: ${err.message}`;
}
