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

/**
 * Whether two text entries are ONE of the agent's messages: one say, which a seal split into `<say>#k` segments, or
 * either under a minted say, which says nothing about where a message ends (`turnlog.sayRef`).
 */
export function sameMessage(
  a: { readonly id: string; readonly turn: string },
  b: { readonly id: string; readonly turn: string },
): boolean {
  const sa = sayIDOf(a.id);
  const sb = sayIDOf(b.id);
  return sa === sb || isMintedSay(sa, a.turn) || isMintedSay(sb, b.turn);
}
