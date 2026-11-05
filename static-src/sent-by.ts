// Who sent a message a program posted through marotte's API, in the words every surface shows for it.

import type { Caller } from "./types.js";

/** "Sent by <label>", or "Sent by an API caller" when the program gave no label. */
export function sentBy(caller: Caller): string {
  const label = caller.label?.trim() ?? "";
  return label === "" ? "Sent by an API caller" : `Sent by ${label}`;
}
