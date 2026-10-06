// `<call>:result` and `<steer>:ack` pair by id with no join table; `internal/marotte/entry.go` mints the same formats.

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
