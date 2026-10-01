// Entry-id formats and their inverses. A tool call's settled value is
// `<call>:result` and a steer's acknowledgement is `<steer>:ack`, so the pairing
// reads off the id with no join table; `internal/marotte/entry.go` mints the same
// two formats, which is what lets the replay merge pair on recorded identity.
// An inverse answers `null` for an id without the suffix: a caller branches on it.

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
