// The Kiro sign-in's health (GET /api/whoami's `health` and the signin_health event) and its app-wide banner.

import { signal, type ReadonlySignal } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { GLOBAL_BANNER, clearBannerCodes, showBanner } from "./banner-stack.js";
import { BUS_RECONCILE, onBus, onSSE } from "./bus.js";
import { showLoginModal } from "./modals.js";
import { setSignInMark } from "./status.js";
import { decodeWhoamiResponse } from "./wire/decoders.gen.js";
import type { SignInHealth } from "./wire/types.gen.js";

const CODE = "signin_health";

/** The newest health either path delivered; null until the first. */
const health = signal<SignInHealth | null>(null);

/** The banner's words per state. The failing sentence carries the run tab's pause cause
 *  (internal/marotte SignInUnreachableCause), so a held run and the banner name one cause. */
const FAILING_TEXT =
  "marotte can't renew your Kiro sign-in: the Kiro sign-in service is unreachable. " +
  "Chats and workflows fail until it answers, and marotte resumes held workflows by itself.";
const SIGNED_OUT_TEXT =
  "Your Kiro sign-in ended, so chats and workflows fail. Sign in to Kiro to continue.";
/** The API key's words (internal/marotte SignInAPIKeyCause and SignInAPIKeyRemedy): marotte refuses every sign-in
 *  while the key is set, so this banner offers none. */
const API_KEY_TEXT =
  "Kiro refused the KIRO_API_KEY marotte signs in with, so chats and workflows fail. " +
  "Replace the key in marotte's environment, then restart marotte.";

/** The health shown, for readers that repaint with it (the status card's auth row). */
export function signInHealth(): ReadonlySignal<SignInHealth | null> {
  return health;
}

function isNewer(next: SignInHealth, held: SignInHealth | null): boolean {
  if (held === null) {
    return true;
  }
  if (next.version.boot_ms !== held.version.boot_ms) {
    return next.version.boot_ms > held.version.boot_ms;
  }
  return next.version.seq > held.version.seq;
}

/** Paint `h` unless the health shown is newer: the whoami read and the event arrive in any order. */
export function adoptSignInHealth(h: SignInHealth): void {
  if (!isNewer(h, health.peek())) {
    return;
  }
  health.value = h;
  paint(h);
}

/** Whether a sign-in can mend the sign-in's state: not once Kiro refused the API key, which the server keeps
 *  refusing sign-ins for. */
export function signInMends(h: SignInHealth | null): boolean {
  return h?.cause !== "api_key_refused";
}

function paint(h: SignInHealth): void {
  setSignInMark(h.state);
  const link = { label: "Sign in again", onClick: showLoginModal };
  switch (h.state) {
    case "failing":
      showBanner(GLOBAL_BANNER, CODE, FAILING_TEXT, "warning", false, link);
      return;
    case "signed_out":
      if (signInMends(h)) {
        showBanner(GLOBAL_BANNER, CODE, SIGNED_OUT_TEXT, "error", false, link);
      } else {
        showBanner(GLOBAL_BANNER, CODE, API_KEY_TEXT, "error", false);
      }
      return;
    case "ok":
    case "none":
      clearBannerCodes(GLOBAL_BANNER, [CODE]);
  }
}

/** Re-read after the stream lost its claims. A different process (another boot_ms) answers for itself, so its
 *  health replaces the held one even where a stepped-back clock would order it older. */
async function reread(signal: AbortSignal): Promise<void> {
  const who = await apiGetTyped("/api/whoami", decodeWhoamiResponse, signal);
  const h = who?.health;
  if (h === undefined) {
    return;
  }
  if (h.version.boot_ms !== health.peek()?.version.boot_ms) {
    health.value = null;
  }
  adoptSignInHealth(h);
}

/** Subscribe to the event and re-read the health on a reconcile. Once, from boot's post-auth fan-out. */
export function initSignInHealth(): void {
  onSSE("signin_health", (_chatID, p) => {
    adoptSignInHealth(p);
  });
  onBus(BUS_RECONCILE, ({ signal }) => {
    void reread(signal);
  });
}
