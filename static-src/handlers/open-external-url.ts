// KAS opens a sign-in page no standing Sign in took (a mid-session re-auth, the organization sign-in a refused tool
// call starts, or a URL after the Sign in's own). The URL names no chat, so every page shows it. An SSE event is no
// user gesture, so window.open() would be popup-blocked: a banner with a link, http/https only, and the paste box for
// a callback the browser cannot reach.

import { onSSE } from "../bus.js";
import { clearBannerCodes, GLOBAL_BANNER, showBanner } from "../banner-stack.js";
import { renderOAuthRelay } from "../mcp-ui.js";
import { isHttpURL } from "../url-safety.js";
import type { OpenExternalURLPayload } from "../wire/types.gen.js";

const CODE = "open_external_url";
const FAILED_CODE = "mcp_sign_in_failed";

let expiry: ReturnType<typeof setTimeout> | undefined;
/** The mid-session re-auth the banner shows, "" for a connect's URL, which mcp_sign_in_closed never names. */
let shownFlow = "";
/** The organization the banner's sign-in page is for, null while it shows none. */
let shownOrg: string | null = null;

function bannerText(p: OpenExternalURLPayload): string {
  if (p.identity_provider === true) {
    const org = p.identity_provider_name ?? "";
    return org === ""
      ? "Sign in to your organization to use its MCP servers."
      : `Sign in to ${org} to use your organization's MCP servers.`;
  }
  const server = p.server ?? "";
  return server === "" ? "An integration needs you to sign in." : `${server} needs you to sign in.`;
}

function closeBanner(): void {
  shownFlow = "";
  shownOrg = null;
  clearTimeout(expiry);
  clearBannerCodes(GLOBAL_BANNER, [CODE]);
}

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
  clearBannerCodes(GLOBAL_BANNER, [CODE, FAILED_CODE]);
  shownFlow = p.flow_id ?? "";
  shownOrg = p.identity_provider === true ? (p.identity_provider_name ?? "") : null;
  showBanner(
    GLOBAL_BANNER,
    CODE,
    bannerText(p),
    "info",
    true,
    { label: "Open sign-in page", href: p.url },
    // The relay picks the sign-in by the pasted callback's state; the server is only logged.
    renderOAuthRelay(p.server ?? ""),
  );
  clearTimeout(expiry);
  expiry = setTimeout(() => {
    shownFlow = "";
    shownOrg = null;
    clearBannerCodes(GLOBAL_BANNER, [CODE]);
  }, left);
});

onSSE("mcp_sign_in_closed", (_chatID, p) => {
  if (p.flow_id === "" || p.flow_id !== shownFlow) {
    return;
  }
  closeBanner();
  if (!p.success) {
    // KAS runs the re-auth again on the server's next refused tool call.
    showBanner(
      GLOBAL_BANNER,
      FAILED_CODE,
      `${p.server}: the sign-in did not complete. Its next tool call asks again.`,
      "warning",
      true,
    );
  }
});

// KAS opened the page because the organization sign-in runs, so the first status that is not signing-in ends it.
onSSE("mcp_identity_provider", (_chatID, p) => {
  const org = shownOrg;
  const state = p.status?.state ?? "";
  if (org === null || state === "signing-in") {
    return;
  }
  closeBanner();
  if (state !== "signed-in") {
    // KAS starts the organization sign-in again on the next refused tool call.
    showBanner(
      GLOBAL_BANNER,
      FAILED_CODE,
      `${org === "" ? "The organization sign-in" : `The sign-in to ${org}`} did not complete. The next tool call asks again.`,
      "warning",
      true,
    );
  }
});
