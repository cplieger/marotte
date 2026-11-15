// Chat export: a browser download of a chat's transcript, rendered by the server from the
// persisted chat at GET /api/chats/{id}/export?format=md|json. A transient same-origin
// anchor, not a fetch: api-client.ts discards non-2xx bodies and cannot stream to disk, and
// the anchor carries the session cookie.

import { el } from "@cplieger/reactive";

import { exportFilename } from "./export-filename.js";
import { getChatsByIdExportEndpoint } from "./wire/client.gen.js";

function chatExportURL(chatID: string, format: "md" | "json"): string {
  return getChatsByIdExportEndpoint.request({ id: chatID, format }).path;
}

/** Trigger a browser download of chatID's transcript in the given format
 *  (default Markdown). Uses a transient same-origin anchor click. No-op for
 *  an empty chatID. */
export function downloadChatExport(
  chatID: string,
  name: string,
  format: "md" | "json" = "md",
): void {
  if (chatID === "") {
    return;
  }
  const a = el("a", {
    href: chatExportURL(chatID, format),
    download: exportFilename(name, chatID, format),
    rel: "noopener",
  });
  document.body.appendChild(a);
  a.click();
  a.remove();
}
