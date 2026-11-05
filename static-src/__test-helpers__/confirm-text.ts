import type { Confirmation } from "../confirm.js";

/** A mocked confirm's question as one string, title then body, for text assertions. */
export function confirmText(q: unknown): string {
  if (typeof q !== "object" || q === null || !("title" in q)) {
    return "";
  }
  const { title, body } = q as Confirmation;
  return body === undefined ? title : `${title} ${body}`;
}
