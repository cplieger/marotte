// Who sent a message the reader did not write, in the words every surface shows for it.

import type { Caller } from "./types.js";

/** The caller's label, or "an API caller" when the program gave none. */
export function callerName(caller: Caller): string {
  const label = caller.label?.trim() ?? "";
  return label === "" ? "an API caller" : label;
}

/** "Sent by <label>", or "Sent by an API caller" when the program gave no label. */
export function sentBy(caller: Caller): string {
  return `Sent by ${callerName(caller)}`;
}

/** The sender fields a prompt and a search hit carry: an API caller's, or `automatic` for a prompt marotte sent
 *  on its own (the resume after a failsafe compaction). */
export interface Sender {
  readonly caller?: Caller | undefined;
  readonly automatic?: boolean | undefined;
}

/** The Sent-by words of a prompt marotte sent on its own. */
export const SENT_BY_MAROTTE = "Sent by marotte";

/** The Sent-by words for a message the reader did not write; undefined for the reader's own. */
export function senderWords(s: Sender | undefined): string | undefined {
  if (s?.automatic === true) {
    return SENT_BY_MAROTTE;
  }
  return s?.caller === undefined ? undefined : sentBy(s.caller);
}
