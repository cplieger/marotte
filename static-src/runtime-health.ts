// Degraded-runtime banner (degraded-not-dead start).

import { apiGetOrError } from "./api-client.js";
import { showBanner, clearBannerCodes, GLOBAL_BANNER } from "./banner-stack.js";
import { onBus, BUS_RECONCILE } from "./bus.js";
import { openSetting } from "./settings-highlight.js";
import { showLoginModal } from "./modals.js";
import { getVersions } from "./versions.js";
import type { BannerLevel } from "./types.js";

const CODE = "runtime_degraded";

/** The sign-in family's own banner code, separate from the install family's so clearing one
 *  cannot silently drop the other. */
const AUTH_CODE = "runtime_signed_out";

/** The reason prefix every kiro-cli readiness verdict shares. It is what separates a kiro-cli
 *  verdict from the server's own startup/shutdown 503 (`starting up or shutting down`), which
 *  must NOT raise this banner. */
const KIRO_REASON_PREFIX = "kiro-cli";

/** The sign-in family's prefix (internal/server/authready.go `reasonSignIn`). */
const AUTH_REASON_PREFIX = "sign-in required";

interface RuntimeState {
  readonly message: string;
  readonly level: BannerLevel;
}

// Installing and retrying are expected states with nothing for the user to do, so they are
// informational; the two dead ends are errors.
const STATES: Record<string, RuntimeState> = {
  "kiro-cli installing": {
    message:
      "The kiro-cli agent runtime is downloading, so chats cannot start yet. " +
      "This is normal on a first boot. It's a few hundred megabytes, and this banner clears itself.",
    level: "info",
  },
  "kiro-cli install retrying": {
    message:
      "The kiro-cli agent runtime failed to install and is being retried, so chats cannot start yet. " +
      "No action is needed unless it keeps failing. If it does, check the container logs.",
    level: "info",
  },
  "kiro-cli required settings not enforced": {
    message:
      "The kiro-cli agent runtime is installed, but its auto-update could not be switched off, " +
      "so chats stay blocked. A self-replacing binary would break the pinned version. Check the container logs.",
    level: "error",
  },
};

const FALLBACK: RuntimeState = {
  message:
    "The kiro-cli agent runtime is not installed, so chats cannot start. " +
    "The install failed and its retries are exhausted. Check the container logs, then restart the container to try again.",
  level: "error",
};

/** Shape of /api/health's JSON envelope (both 200 and 503 bodies). */
interface HealthBody {
  reason?: string;
}

// The sidebar status card reads connection / agent runtime / account, and the middle line had no
// writer from the first commit — it showed a literal "-" forever, because the readiness verdict it
// wanted did not exist until the install moved into the server.

const LINE_READY = "kiro-cli ready";
const LINE_SIGNED_OUT = "kiro-cli signed out";

/** Neither ready nor degraded: the server is starting up or shutting down, or the probe never
 *  reached it. The connection line above carries that story. */
const LINE_UNKNOWN = "kiro-cli unknown";

let runtimeLine = LINE_UNKNOWN;

/** The agent-runtime line as of the last probe. Painted by status.ts. The READY line names the
 *  version, because "which kiro-cli am I talking to" is the question this line gets asked and
 *  the answer was two clicks away in own wording rendered verbatim (see the header), and a
 *  version appended to `kiro-cli installing` would name the pin rather than anything running. */
export function runtimeStatusLine(): string {
  if (runtimeLine !== LINE_READY) {
    return runtimeLine;
  }
  const build = getVersions().kiroCli;
  return build === "" ? LINE_READY : `kiro-cli ${build} ready`;
}

function reasonOf(body: unknown): string {
  if (typeof body === "object" && body !== null) {
    const r = (body as HealthBody).reason;
    if (typeof r === "string") {
      return r;
    }
  }
  return "";
}

const SIGNED_OUT: RuntimeState = {
  message:
    "The agent runtime is signed out, so chats will open and then fail. " +
    "This usually means the sign-in expired while you were away.",
  level: "error",
};

/** How long to wait before the FIRST re-probe of an unready runtime. A kiro-cli install is
 *  minutes of work, so this is about how fast the banner should disappear once it finishes
 *  rather than about catching a transition. */
const UNREADY_POLL_MS = 10_000;

/** The steady-state interval the re-probe backs off to, and the reason there is one: an install
 *  converges in minutes, while a page left open against a stopped server never does — at a flat
 *  10s that tab issues one /api/health every 10s for as long as it is open. */
const UNREADY_POLL_MAX_MS = 120_000;

/** A single slot rather than a repeating interval, so a slow probe can never overlap the next one
 *  and a ready answer ends the chain by simply not re-arming. */
let unreadyTimer: ReturnType<typeof setTimeout> | undefined;

/** Consecutive unready answers, which is what the backoff is a function of. Reset by any ok
 *  answer, so a runtime that recovers and degrades again gets the fast interval back rather than
 *  inheriting the previous outage's ceiling. */
let unreadyStreak = 0;

/** Re-arm (or cancel) the unready re-probe. Always cancels first, so the boot probe, a transport
 *  gap and a poll tick all converge on one timer. */
function armUnreadyPoll(unready: boolean): void {
  if (unreadyTimer !== undefined) {
    clearTimeout(unreadyTimer);
    unreadyTimer = undefined;
  }
  if (!unready) {
    unreadyStreak = 0;
    return;
  }
  const delay = Math.min(UNREADY_POLL_MS * 2 ** unreadyStreak, UNREADY_POLL_MAX_MS);
  unreadyStreak += 1;
  unreadyTimer = setTimeout(() => {
    unreadyTimer = undefined;
    void checkRuntimeHealth();
  }, delay);
}

/** Probe /api/health once and reconcile the degraded banner. */
export async function checkRuntimeHealth(): Promise<void> {
  const res = await apiGetOrError<HealthBody>("/api/health");
  // Re-armed before the branching, because every non-ok answer wants another look: the two named
  // families below AND a startup/shutdown 503, which raises no banner and is exactly the window an
  // install begins in.
  armUnreadyPoll(!res.ok);
  const reason = reasonOf(res.body);
  if (!res.ok && reason.startsWith(AUTH_REASON_PREFIX)) {
    runtimeLine = LINE_SIGNED_OUT;
    // Not dismissible, for the same reason as the install family: it blocks the product's core
    // function and clears itself on the next check once a token vends.
    clearBannerCodes(GLOBAL_BANNER, [CODE]);
    showBanner(GLOBAL_BANNER, AUTH_CODE, SIGNED_OUT.message, SIGNED_OUT.level, false, {
      label: "Sign in",
      onClick: showLoginModal,
    });
    return;
  }
  if (!res.ok && reason.startsWith(KIRO_REASON_PREFIX)) {
    runtimeLine = reason;
    // An unknown kiro-cli reason falls back to the terminal wording rather than being ignored: a
    // state the server can report and the client cannot name still blocks chats, and saying so is
    // better than silence.
    const state = STATES[reason] ?? FALLBACK;
    // Not dismissible: the condition blocks the product's core function, and it clears itself on
    // the next check after recovery.
    clearBannerCodes(GLOBAL_BANNER, [AUTH_CODE]);
    showBanner(GLOBAL_BANNER, CODE, state.message, state.level, false, {
      label: "Run diagnostics",
      onClick: () => {
        openSetting("general", "diagnostics-run");
      },
    });
    return;
  }
  // Healthy, network failure, or a startup/shutdown 503: clear BOTH families. The transient cases
  // re-assert on the next gap check if still degraded; a stale banner over a working runtime is the
  // worse failure mode.
  runtimeLine = res.ok ? LINE_READY : LINE_UNKNOWN;
  clearBannerCodes(GLOBAL_BANNER, [CODE, AUTH_CODE]);
}

/** Wire the boot probe + the re-probe on a whole reconcile. Called once from app.ts. */
export function initRuntimeHealth(): void {
  void checkRuntimeHealth();
  onBus(BUS_RECONCILE, () => {
    void checkRuntimeHealth();
  });
}
