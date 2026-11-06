import type { Confirmation } from "../confirm.js";

/** A mocked confirm's question as one string, title then body, for text assertions. */
export function confirmText(q: Confirmation | undefined): string {
  if (q === undefined) {
    return "";
  }
  return q.body === undefined ? q.title : `${q.title} ${q.body}`;
}
