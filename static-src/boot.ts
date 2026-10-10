// The boot: five reads in the FIRST FRAME, each adopted in its own region as it lands;
// nothing waits on the identity verdict. Two orderings are real: the tab set follows the
// chat fold (a chat tab is named from the chat store), and retention precedes it.
// `applyRoute` is injected so boot.test.ts can observe it without route-apply's graph.

import { subscribeByName } from "./actions/index.js";
import { logout } from "./actions/settings.js";
import { GLOBAL_BANNER, showBanner } from "./banner-stack.js";
import { $ } from "./dom.js";
import { bootMode, clearReloadGuard, noteBootAlive, reloadCount } from "./reload-guard.js";
import { abortReadsForRevert, loadList, requestTurnRange } from "./store-load.js";
import {
  getActive,
  getActiveId,
  getSessions,
  registerEvictionExemption,
  registerTurnRepair,
  registerRevertReadAbort,
  startEvictionSweep,
} from "./store.js";
import {
  clearBootSnapshot,
  paintBootSnapshot,
  readBootSnapshot,
  startBootSnapshot,
} from "./boot-snapshot.js";
import type { BootSnapshot } from "./boot-snapshot.js";
import { clearDeviceKeys } from "./ls-keys.js";
import { resetFoldState } from "./fold-state.js";
import {
  adoptThemeFromSettings,
  initPostAuthUI,
  loadSettings,
  renderIdentity,
  restoreAll,
} from "./settings.js";
import type { EffectiveSettings } from "./persist.js";
import { restoreLastEffort, restoreLastModel } from "./session-context.js";
import { adoptLinkGuard } from "./link-guard.js";
import { resolveIdentity } from "./identity.js";
import type { IdentityVerdict } from "./identity.js";
import { fetchCatalog } from "./session-catalog.js";
import * as sse from "./sse-adapter.js";
import { showLoginModal } from "./modals.js";
import { activateRestoredTab, getActiveTabRoute, hasTab } from "./tabs.js";
import { listTabs } from "./tabs-sync.js";
import { parseRoute } from "./route-path.js";
import type { Route } from "./route-path.js";
import {
  claimLocation,
  navigationOrigin,
  releaseLocation,
  replaceRoute,
  suppressPush,
} from "./router.js";
import type { RouteOrigin } from "./router.js";
import { createSession } from "./chat.js";
import { initGovernance } from "./governance.js";
import { initRuntimeHealth } from "./runtime-health.js";
import { initStatusVersions, setStatus } from "./status.js";
import type { ConnectionStatus } from "./types.js";
import { loadVersions } from "./versions.js";
import { refreshRetention } from "./retention.js";
import {
  registerRunLogObserver,
  registerRunStateDemand,
  registerRunTurnRepair,
} from "./run-store.js";
import { forgetRunStepSteers, retireStepSteers } from "./run-step-steers.js";
import { requestRunTurnRange } from "./run-turn-range.js";
import { chatTabFoldsRun } from "./chat-run-dots.js";
import { subagentTabProjectsChat } from "./subagent-view.js";
import { markBootDone } from "./view-swap.js";
import { dropStoredShellPanel, releaseStoredShellPanel } from "./shell-height.js";
import { restoreShell } from "./shell.js";
import { applyShareTarget } from "./share-target.js";
import { error as toastError } from "./toast.js";

/** What the boot chain needs from the composition root. */
interface BootDeps {
  /**
   * Navigate to a route (owned by `app.ts`). RESOLVES when the view is open; the router's
   * location claim is held across it.
   */
  applyRoute: (route: Route, origin?: RouteOrigin) => Promise<void>;
}

let deps: BootDeps | null = null;

export async function startBoot(d: BootDeps): Promise<void> {
  deps = d;

  // Claim the location the DOCUMENT loaded at before anything can push, until the route is
  // applied (released in the workspace region's `finally`).
  claimLocation(location.pathname + location.hash);

  try {
    // Past a threshold this boot withholds what it can (reload-guard.ts). Armed first:
    // the stability clear is what makes a boot that STAYS up cost the next one nothing.
    noteBootAlive();
    const reduced = bootMode() === "reduced";
    if (reduced) {
      // `autofocus` already fired, so blur: the on-screen keyboard would shorten the viewport.
      $.promptInput.blur();
      announceReloadLoop();
    }

    // `identity` needs no rejection handler: every failure IS its `unavailable` arm
    // (identity.ts). Nor does `snapshotRead`, which resolves null for every failure.
    const settingsRead = loadSettings();
    const identity = resolveIdentity();
    const chatsRead = loadList();
    const retentionRead = refreshRetention();
    const snapshotRead = readBootSnapshot();

    // The workspace region owns every view the boot swaps, so it is what the animation
    // flag waits on: a slow whoami must not hold the first tab switch's transition back.
    const workspace = restoreWorkspace(chatsRead, retentionRead, snapshotRead).finally(() => {
      // The route has been applied (or the region failed), so ordinary pushes resume.
      // This covers a throw inside the region; the catch below covers one before it.
      releaseLocation();
      markBootDone();
    });
    await Promise.allSettled([
      settingsRead.then(adoptSettings),
      identity.then((v) => adoptIdentity(v, workspace)),
      identity.then(adoptShell),
      workspace,
    ]);
  } catch (err) {
    // The region's `finally` above cannot run for a throw before that region exists,
    // and an unreleased claim freezes the address bar for the life of the page.
    releaseLocation();
    throw err;
  }
}

/**
 * The settings answer: theme, model and effort seeds, workspace prefs. Null means the
 * fetch FAILED, so nothing is restored: an invented value would persist on the next write.
 */
function adoptSettings(settings: EffectiveSettings | null): void {
  if (settings === null) {
    return;
  }
  restoreLastModel(settings.last_model);
  restoreLastEffort(settings.last_effort_by_model);
  adoptLinkGuard(settings);
  // Where the server's choice replaces the pre-paint cache, and where that cache
  // is carried across if the server has none.
  adoptThemeFromSettings(settings);

  suppressPush(true);
  try {
    restoreAll(settings);
  } catch {
    /* best-effort */
  }
  suppressPush(false);
}

/**
 * This device's shell panel, gated on the identity verdict (a restore opens the terminal's
 * socket), not on settings. A signed-out boot shuts the pre-painted panel at once.
 */
function adoptShell(v: IdentityVerdict): void {
  try {
    if (v.state === "signed_out") {
      dropStoredShellPanel($.shellPanel);
    } else {
      restoreShell();
    }
  } finally {
    // The release for a path that restored nothing; after a restore it is a no-op.
    releaseStoredShellPanel();
  }
}

/**
 * The identity answer: one sidebar row, the post-auth fan-out, and what needs a verdict.
 * `signed_out` raises the login modal and holds the post-auth fetches; `unavailable` comes
 * up working with a re-read offered. `workspace` is awaited for "anything to show".
 */
async function adoptIdentity(v: IdentityVerdict, workspace: Promise<boolean>): Promise<void> {
  renderIdentity(v);
  if (v.state === "signed_out") {
    // Nothing hydrates the store behind a login modal, so release the held frames
    // rather than stalling the stream until the watchdog fires.
    sse.markHydrated();
    // And nothing this device remembers may survive to a login screen.
    forgetDeviceState();
    showLoginModal();
    return;
  }
  if (v.state === "unavailable") {
    toastError(`Could not confirm who is signed in. ${v.reason}`, {
      label: "Retry",
      onClick: () => {
        void resolveIdentity().then((next) => adoptIdentity(next, workspace));
      },
    });
  }
  initPostAuth();

  // A rejected region is the same answer as `false`: the chats are not in the store.
  const chatsOK = await workspace.catch(() => false);
  if (!chatsOK) {
    // Before the fallback below, so a fresh chat does not read as the user's.
    toastError("Could not load your chats.", { label: "Reload", onClick: reload });
  }
  if (getSessions().length === 0) {
    // The chat STORE, not `chatsOK`: a painted snapshot or a share may already have rows.
    await createSession();
  }
}

/**
 * The local snapshot, the chats answer, the tab set, then the route, resolving whether the
 * chat list was READ. One region because these are ordered (see the header).
 */
async function restoreWorkspace(
  chatsRead: Promise<boolean>,
  retentionRead: Promise<void>,
  snapshotRead: Promise<BootSnapshot | null>,
): Promise<boolean> {
  // RACED against the chat list: the paint REPLACES the chat store, so it may only precede
  // the answer. An empty list IS an answer; only a read that SUCCEEDED counts as one.
  const hint = await Promise.race([
    snapshotRead,
    chatsRead.then(
      (ok) => (ok ? null : snapshotRead),
      () => snapshotRead,
    ),
  ]);

  // Best-effort: the restore below is authoritative. A REDUCED boot skips the paint.
  let resumed = false;
  try {
    resumed = bootMode() === "full" && resumeSnapshot(hint);
  } catch {
    /* best-effort */
  }

  // A rejected read is the same answer as `false` — the chats are not in the
  // store — so the boot has one failure path rather than two.
  const chatsOK = await chatsRead.catch(() => false);
  // The rule both of these follow is at `bootChatsRead`.
  bootChatsRead = chatsOK;
  recoverFailedBootRead();
  // Release the held frames: they need only a chat ROW (see sse-adapter.ts
  // holdUntilHydrated). Idempotent.
  sse.markHydrated();

  try {
    // Both awaits sit OUTSIDE the suppression window below: a window spanning an await
    // silences every shell push.
    try {
      await retentionRead;
    } catch {
      /* the close path reads the default */
    }
    // The rule both of these follow is at `bootTabsRead`.
    await readTabSet();
    recoverFailedBootTabs();
    // The window is the RESTORE itself and nothing else: `activateRestoredTab` ends in
    // `pushRoute`, which would add a history entry for a tab nobody navigated to.
    suppressPush(true);
    try {
      clearTabStripSkeleton();
      if (!resumed) {
        activateRestoredTab();
      }
    } finally {
      suppressPush(false);
    }
  } finally {
    // A throw above must not leave the placeholder shimmering forever.
    clearTabStripSkeleton();
  }

  // OUTSIDE the window: both WRITE the URL, and `?agent=planner` needs the share's chat.
  await applyShareTarget();
  // AWAITED: the claim is released when this region settles, and an arm that opens its
  // view through a dynamic import has not opened it yet when the call returns.
  await applyInitialRoute();
  return chatsOK;
}

/**
 * Paint the snapshot AND run its activation in ONE window with pushes suppressed; reports
 * whether the resume COMPLETED. `activateRestoredTab` ends in `pushRoute`, which would
 * add a history entry and rewrite `location.pathname` before `applyInitialRoute` reads
 * it. Its own window, so a real click from the painted shell is not swallowed.
 */
function resumeSnapshot(snap: BootSnapshot | null): boolean {
  suppressPush(true);
  try {
    if (!paintBootSnapshot(snap)) {
      return false;
    }
    clearTabStripSkeleton();
    // Brought forward from the tab set: this paints the transcript. Its window is
    // stale, so `loadMessages` still refetches.
    activateRestoredTab();
    return true;
  } finally {
    suppressPush(false);
  }
}

function reload(): void {
  location.reload();
}

/** Not dismissible; its link is the only control that clears the count. No duration claim: the count
 *  is an unbounded sliding run. */
function announceReloadLoop(): void {
  const n = reloadCount();
  showBanner(
    GLOBAL_BANNER,
    "reload-loop",
    `This page reloaded ${String(n)} times in a row, so it started with less loaded.`,
    "warning",
    false,
    {
      label: "Start in full mode",
      onClick: () => {
        clearReloadGuard();
        reload();
      },
    },
  );
}

/** Drop the tab strip's authored placeholder by id; `tabs.ts` owns the rows. Idempotent. */
function clearTabStripSkeleton(): void {
  document.getElementById("tab-strip-skeleton")?.remove();
}

// Everything that must not fire on the login screen, behind ONE door guarded once,
// so a boot that is already signed in and a first login reach the same set.
let postAuthInitDone = false;
export function initPostAuth(): void {
  // OUTSIDE the latch: the capture's lifetime is the SESSION, not the page, so a
  // login after a sign-out disposed it has to be able to start it again. Idempotent.
  startBootSnapshot();
  if (postAuthInitDone) {
    return;
  }
  postAuthInitDone = true;
  // Gates MCP availability, the policy disclosure and the code-reference chip.
  initGovernance();
  // Version info (Settings → About) + git panel wiring incl. the badge.
  initPostAuthUI();
  // Degraded-runtime banner; re-checks on every gap so recovery self-heals.
  initRuntimeHealth();
  // The FETCH-ONLY fan-outs a reduced boot withholds, each with a usable empty state; the two
  // above stay (capability, degraded runtime).
  if (bootMode() === "full") {
    // The marotte + kiro-cli build pair. Fire-and-forget: the lines repaint through a
    // signal, so nothing waits on the `--version` subprocess behind it.
    initStatusVersions();
    void loadVersions();
    // So the pickers have content before the first chat's session/new lands.
    void fetchCatalog();
  }
  // Registered here because store.ts is a leaf. The store DETECTS a `seq` hole and
  // `store-load.ts` owns every read, so the repair is injected.
  registerTurnRepair(requestTurnRange);
  // The revert's own half of that inversion: the handler drops turns, and the reads
  // already out for one of them are this module's to cancel.
  registerRevertReadAbort(abortReadsForRevert);
  // The run log's twin: `run-store.ts` detects the `seq` hole and `run-turn-range.ts`
  // owns the read, so the repair is injected here rather than imported.
  registerRunTurnRepair(requestRunTurnRange);
  registerRunLogObserver({ steer: retireStepSteers, forget: forgetRunStepSteers });
  // A step's entries are the RUN's, so only the delegate page pins a chat's window.
  registerEvictionExemption(subagentTabProjectsChat);
  // Who still needs a run's state cell, so `forgetRun` needs no enumeration of its readers.
  registerRunStateDemand((id) => hasTab("run", id));
  registerRunStateDemand(chatTabFoldsRun);
  startEvictionSweep();
  // Logout leaves the page running, so stop the debounce writing the workspace; keyed on
  // `logout.name` so every door and a rename are covered.
  subscribeByName(logout.name, (inst) => {
    if (inst.status === "success") {
      forgetDeviceState();
    }
  });
}

/**
 * Forget everything this SCREEN remembers about the signed-in workspace, from both sign-out
 * doors. The current frame stays; the next boot or login starts from nothing.
 */
function forgetDeviceState(): void {
  void clearBootSnapshot();
  clearDeviceKeys();
  resetFoldState();
}

async function applyInitialRoute(): Promise<void> {
  const route = parseRoute(location.pathname, location.hash);
  if (route.kind !== "chat" || route.id !== "") {
    await deps?.applyRoute(route, navigationOrigin());
    return;
  }
  // Canonicalize "/" to what is visible: the active chat, else a restored non-chat tab.
  const active = getActive();
  if (getActiveId() !== "" && active !== undefined) {
    replaceRoute({ kind: "chat", id: getActiveId() });
    return;
  }
  const tabRoute = getActiveTabRoute();
  if (tabRoute !== null && tabRoute.kind !== "chat") {
    replaceRoute(tabRoute);
  }
}

/**
 * Whether the boot's own chat-list read has SETTLED and whether it ANSWERED; `undefined`
 * while out. `onTransportStatus` reads the first half, `recoverFailedBootRead` the second.
 */
let bootChatsRead: boolean | undefined;

/**
 * The same latch for the TAB set, ANSWER half only: digests and reconciles re-list it,
 * except for a boot read that never landed.
 */
let bootTabsRead: boolean | undefined;

let streamUp = false;

/**
 * Fetch the chat list when the boot's read FAILED under an already-up stream, which no
 * later `connected` will cover.
 */
function recoverFailedBootRead(): void {
  if (bootChatsRead === false && streamUp) {
    void loadList();
  }
}

/** The tab set's twin of `recoverFailedBootRead`, for the same uncovered case. */
function recoverFailedBootTabs(): void {
  if (bootTabsRead === false && streamUp) {
    void listTabs();
  }
}

/**
 * Read the tab set, record the answer, and say so when it did not answer. The notice
 * retries HERE (only the GET failed); a repeat failure raises it again.
 */
async function readTabSet(): Promise<void> {
  bootTabsRead = await listTabs();
  if (!bootTabsRead) {
    toastError("Could not restore your tabs.", {
      label: "Retry",
      onClick: () => {
        void readTabSet();
      },
    });
  }
}

/**
 * The stream's status callback: paint the indicator and load the chat list on every
 * connection the boot's read does not cover. The first `connected` lands mid-read and is
 * covered; once settled EVERY connection fetches, since an offline boot has nothing to
 * digest.
 */
export function onTransportStatus(status: ConnectionStatus): void {
  setStatus(status);
  streamUp = status === "connected";
  if (!streamUp) {
    return;
  }
  if (bootChatsRead === undefined) {
    return;
  }
  void loadList();
  if (bootTabsRead === false) {
    void listTabs();
  }
}
