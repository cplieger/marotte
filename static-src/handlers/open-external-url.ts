// KAS opens a sign-in page no Sign in started (a mid-session re-auth). The URL names no chat or server, so every
// page shows it. An SSE event is no user gesture, so window.open() would be popup-blocked: a banner with a link,
// http/https only, and the paste box for a callback the browser cannot reach.

import { onSSE } from "../bus.js";
import { clearBannerCodes, GLOBAL_BANNER, showBanner } from "../banner-stack.js";
import { renderOAuthRelay } from "../mcp-ui.js";
import { isHttpURL } from "../url-safety.js";

const CODE = "open_external_url";

let expiry: ReturnType<typeof setTimeout> | undefined;

onSSE("open_external_url", (_chatID, p) => {
  if (typeof p.url !== "string" || !isHttpURL(p.url)) {
    return;
  }
  // expires_at is when KAS's callback listener closes, and the link with it; a replayed event keeps it.
  const left = Date.parse(p.expires_at) - Date.now();
  if (!(left > 0)) {
    return;
  }
  // Each URL is a new sign-in: it replaces the last, and a dismissed earlier one does not hide it.
  clearBannerCodes(GLOBAL_BANNER, [CODE]);
  showBanner(
    GLOBAL_BANNER,
    CODE,
    "An integration needs you to sign in.",
    "info",
    true,
    { label: "Open sign-in page", href: p.url },
    // No server: the relay picks the sign-in by the pasted callback's state.
    renderOAuthRelay(""),
  );
  clearTimeout(expiry);
  expiry = setTimeout(() => {
    clearBannerCodes(GLOBAL_BANNER, [CODE]);
  }, left);
});
