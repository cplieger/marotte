// `<call>:result`, `<steer>:ack` and `<say>#k` pair by id with no join table; `internal/marotte/entry.go` mints the
// same formats.

const RESULT_SUFFIX = ":result";
const ACK_SUFFIX = ":ack";

/** The tool_result entry id for a tool call. */
export function toolResultID(callID: string): string {
  return callID + RESULT_SUFFIX;
}

/** The tool call a tool_result entry settles, or null when the id is not one. */
export function callIDOfToolResult(entryID: string): string | null {
  return entryID.endsWith(RESULT_SUFFIX) ? entryID.slice(0, -RESULT_SUFFIX.length) : null;
}

/** The steer_ack entry id for a steer. */
export function steerAckID(steerID: string): string {
  return steerID + ACK_SUFFIX;
}

/** The say a text entry id belongs to: the id less a `#k` segment suffix (`marotte.SayIDOf`). */
function sayIDOf(entryID: string): string {
  const i = entryID.lastIndexOf("#");
  return i >= 0 && /^\d+$/u.test(entryID.slice(i + 1)) ? entryID.slice(0, i) : entryID;
}

/** Whether `say` is an id turnlog minted for id-less deltas (`<turn>:e<k>`, `turnlog.Turn.mintID`). */
function isMintedSay(say: string, turnID: string): boolean {
  return say.startsWith(turnID) && /^:e\d+$/u.test(say.slice(turnID.length));
}

/** The NAMED say a text entry belongs to, or null under a minted say, which names no message (`turnlog.sayRef`). */
export function namedSay(e: { readonly id: string; readonly turn: string }): string | null {
  const say = sayIDOf(e.id);
  return isMintedSay(say, e.turn) ? null : say;
}

/**
 * Groups consecutive text entries into the agent's messages, fed in stream order. A run's say is its first named
 * segment's (`<say>#k` segments share one); a minted segment rides the run it follows and never bridges two says.
 */
export class MessageRun {
  private say: string | null = null;

  /** Take `e` into the run, or refuse it, leaving the run unchanged, as another message's. */
  take(e: { readonly id: string; readonly turn: string }): boolean {
    const say = namedSay(e);
    if (say === null) {
      return true;
    }
    if (this.say !== null && this.say !== say) {
      return false;
    }
    this.say = say;
    return true;
  }
}
