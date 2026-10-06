// KAS asks the client to open a URL (usually an MCP OAuth page). An SSE event is no user gesture,
// so window.open() would be popup-blocked: a persistent banner with a link, http/https only,
// keyed to the chat whose bridge asked.

import { onSSE } from "../bus.js";
import { showBanner } from "../banner-stack.js";
import { isSafeURL } from "../url-safety.js";

onSSE("open_external_url", (chatID, p) => {
  if (chatID === "" || typeof p.url !== "string" || !isSafeURL(p.url)) {
    return;
  }
  showBanner(chatID, "open_external_url", "An integration needs you to sign in.", "info", true, {
    label: "Open sign-in page",
    href: p.url,
  });
});
