// The tabs that want the reader, rendered on the surfaces OUTSIDE this page: the tab title's count,
// the installed app's badge and the tab icon. A latched sidebar dot is not reliably on
// screen (a scrolled list, the mobile drawer, a hidden page), and Notification is absent
// on iOS Safari outside an installed app. STATE, not events: a pure render of what is
// true, so nothing here has a timer. Ported from web-terminal-ui's attention.ts, which its
// `exports` map does not publish. DOM-free above "The browser binding".

import { ackTabCommand } from "./actions/tabs.js";
import { cueCandidates, getActiveTabId, openTabSubjects, subscribeTabCues } from "./tabs.js";
import { BUS_PAGE_RESUMED, BUS_RECONCILE, BUS_TAB_CHANGED, onBus, onSSE } from "./bus.js";
import { $ } from "./dom.js";
import { closeNotificationsFor } from "./notify.js";
import { chatTarget, runTarget } from "./push-subject.js";
import { runState } from "./run-store.js";
import { watchSession } from "./store.js";
import { newOpID } from "./transport.js";

/**
 * The dot states that raise an attention cue, the states that WANT the reader. `working`
 * and `idle` are excluded (nothing to act on), `dirty` here and in `cueCandidates`. `done`
 * MEANS A TURN ENDED, cancelled or unreadable included: the cue is the dot carried
 * off-page, so it must not disagree with the dot. No per-outcome carve-out:
 * `tabs.test.ts` pins `cueCandidates` as verbatim.
 */
export type CueStatus = "input" | "waiting" | "failed" | "done";

/**
 * Severity order over CueStatus, most severe first, AND the complete set isCueStatus
 * tests, so type and list cannot drift. The tab icon paints the most severe cue. `input`
 * ranks above `failed` (unlike web-terminal-ui): an ask BLOCKS the turn, matching
 * `tabStatusFor` in store.ts, so icon and tab dot agree.
 */
export const CUE_SEVERITY: readonly CueStatus[] = ["input", "failed", "waiting", "done"];

/** isCueStatus narrows a raw dot state to a cue-worthy one. */
export function isCueStatus(status: string): status is CueStatus {
  return (CUE_SEVERITY as readonly string[]).includes(status);
}

/** worseCue returns whichever of two cues is more severe; "" means no cue. */
export function worseCue(a: CueStatus | "", b: CueStatus | ""): CueStatus | "" {
  if (a === "") {
    return b;
  }
  if (b === "") {
    return a;
  }
  return CUE_SEVERITY.indexOf(a) <= CUE_SEVERITY.indexOf(b) ? a : b;
}

/**
 * The icon variant a cue paints, not one per cue: `waiting` shares the `input` asset,
 * because a 16px badge cannot carry the dot's hollow-vs-solid distinction and both mean
 * "wants you" (three variants, pinned by favicon-variants.test.ts). A Record, so a new
 * cue cannot ship unmapped.
 */
const CUE_ICON: Readonly<Record<CueStatus, "input" | "done" | "alert">> = {
  input: "input",
  waiting: "input",
  failed: "alert",
  done: "done",
};

export function cueIconName(status: CueStatus): "input" | "done" | "alert" {
  return CUE_ICON[status];
}

/** What the fold reads per chat or run tab. A structural type, so tabs.ts passes its own
 *  projection without this module knowing what else a TabViewSpec carries. */
export interface CueCandidate {
  readonly id: string;
  readonly kind: "chat" | "run";
  /** The folded dot state. */
  readonly status: string;
  /** `TabSubject.seen`: the read position the server holds as acknowledged. */
  readonly seen: number;
  /** The tab's read position, 0 when unknown: a chat header's or a run reply's `read_position`. */
  readonly position: number;
}

/**
 * cueClass is the one predicate behind count, icon and acknowledgement. "needs" is an open ask or a
 * paused run: it counts while open and no acknowledgement clears it. "unread" is a finished state
 * whose read position is past what the server holds as seen. A running tab is neither.
 */
export function cueClass(c: CueCandidate): "needs" | "unread" | "" {
  if (!isCueStatus(c.status)) {
    return "";
  }
  if (c.status === "input" || (c.kind === "run" && c.status === "waiting")) {
    return "needs";
  }
  return c.position > c.seen ? "unread" : "";
}

/** The whole attention state, and the only thing the sinks are allowed to see. */
export interface Attention {
  /** How many tabs need the reader or hold an unread state. */
  readonly count: number;
  /** The most severe of them, or "" when there is none. */
  readonly worst: CueStatus | "";
}

export const NO_ATTENTION: Attention = { count: 0, worst: "" };

/**
 * summarize folds the tabs into what every surface renders: a COUNT for title and badge, the
 * single WORST for the icon, neither naming a tab. `watchedTab` is the tab on screen: its unread
 * state is this page's own projection of the acknowledgement it sends, so it is left out, while
 * its "needs" still counts. Stateless: another window counts that tab until the ack's frame lands.
 */
export function summarize(candidates: readonly CueCandidate[], watchedTab: string): Attention {
  let count = 0;
  let worst: CueStatus | "" = "";
  for (const candidate of candidates) {
    const cls = cueClass(candidate);
    if (cls === "" || (cls === "unread" && candidate.id === watchedTab)) {
      continue;
    }
    count += 1;
    worst = worseCue(worst, candidate.status as CueStatus);
  }
  return { count, worst };
}

/**
 * titlePrefixFor is the title text for a count. The count goes FIRST, because a tab strip
 * truncates the end of a title; parenthesised digits are the mail-client convention.
 */
export function titlePrefixFor(count: number): string {
  return count > 0 ? `(${String(count)}) ` : "";
}

/**
 * The capabilities the sinks need, all optional except the title; an absent one is a
 * silent no-op. `setBadge` (Linux) and `setIcon` (Safari) can fail INVISIBLY, so never
 * make the title a fallback for them: the title is gated on nothing and is the floor.
 */
export interface AttentionEnv {
  /** Set (or clear, with "") the document-title prefix. A COMPOSING writer: it
   *  owns the base title, so repeated calls cannot compound a prefix. */
  titlePrefix: (text: string) => void;
  /** Set the installed app's icon badge to a count, or clear it at zero. */
  setBadge?: ((count: number) => void) | undefined;
  /** Point every icon link at a variant, or restore them with null. */
  setIcon?: ((variant: "input" | "done" | "alert" | null) => void) | undefined;
}

interface AttentionSurfaces {
  /** Each surface is written only when its own value changed. */
  apply: (next: Attention) => void;
  /** A no-op until the first apply, so boot writes through apply's count gate. */
  assertBadge: () => void;
  /** Leaves the badge: it means "unread", which the page going away does not change. */
  release: () => void;
}

export function createAttention(env: AttentionEnv): AttentionSurfaces {
  // The title and icon are page-local and gated on what was painted: the title is also the
  // bookmark name, and re-assigning an icon href makes some browsers re-fetch it.
  let painted: Attention | null = null;
  // The badge is OS state the page cannot read back: WebKit resolves setAppBadge before the write
  // lands and may drop it silently (Source/WebCore/page/Navigator.cpp, webpushd's setAppBadge).
  // This memory gates only the count-change write; assertBadge writes whatever it holds.
  let badge: number | null = null;
  // The count already written in this microtask turn: several sync points firing together
  // (an acknowledgement that also changed the count) cost one OS write.
  let writtenThisTurn: number | null = null;

  function writeBadge(count: number): void {
    badge = count;
    if (writtenThisTurn === count) {
      return;
    }
    if (writtenThisTurn === null) {
      queueMicrotask(() => {
        writtenThisTurn = null;
      });
    }
    writtenThisTurn = count;
    env.setBadge?.(count);
  }

  return {
    apply(next: Attention): void {
      if (painted?.count !== next.count) {
        env.titlePrefix(titlePrefixFor(next.count));
      }
      if (painted?.worst !== next.worst) {
        env.setIcon?.(next.worst === "" ? null : cueIconName(next.worst));
      }
      painted = next;
      // The badge takes the SAME number as the title.
      if (badge !== next.count) {
        writeBadge(next.count);
      }
    },

    assertBadge(): void {
      if (badge !== null) {
        writeBadge(badge);
      }
    },

    release(): void {
      env.titlePrefix("");
      env.setIcon?.(null);
      painted = null;
    },
  };
}

/**
 * iconVariantHref rewrites an icon URL to its variant, by the asset generator's
 * convention: `favicon` gains `-<variant>` (`/favicon-32x32.png` becomes
 * `/favicon-input-32x32.png`), extension kept. A filename not starting with `favicon`
 * returns null rather than a 404.
 */
export function iconVariantHref(href: string, variant: string): string | null {
  const match = /(^|\/)favicon(?=[-.])/.exec(href);
  if (match === null) {
    return null;
  }
  const at = match.index + match[0].length;
  return `${href.slice(0, at)}-${variant}${href.slice(at)}`;
}

/** How an acknowledgement went. "refused" is a 400, or a successful reply whose marker is below
 *  the ack, whatever the cause: the same position is not resent until a sync point, and a new one
 *  is sent at once. "failed" is a transport error, a 409 or a 5xx, resent on the next refresh. */
export type AckResult = "acked" | "refused" | "failed";

/** Everything the controller needs from outside itself. */
interface AttentionWiring {
  /** The chat and run tabs, their dot states, seen markers and read positions. */
  candidates: () => readonly CueCandidate[];
  /**
   * The TAB the reader is looking at, "" for none. A tab id like every key here, never the
   * chat store's active id: the watched-tab rule could never match it.
   */
  activeTabID: () => string;
  /** Whether the page is in front of the reader at all. */
  pageVisible: () => boolean;
  /** The tab ids whose sidebar row the reader can actually SEE right now. */
  rowsInView: () => readonly string[];
  /** Called once when a tab stops needing the reader or being unread, on any device's action. */
  retract: (tabID: string) => void;
  /** Send the acknowledgement of a tab up to a read position. */
  sendAck: (tabID: string, seen: number) => Promise<AckResult>;
  surfaces: AttentionSurfaces;
}

interface AttentionController {
  /** Apply the observation rules to current state, then re-render the surfaces.
   *  The recompute funnel's target; idempotent, so calling it more often than
   *  necessary costs a loop over a handful of tabs. */
  refresh: () => void;
  /** refresh, then write the badge whether or not its count moved: for every event after
   *  which the OS badge or the server may disagree with this page. Resends refused acks. */
  sync: () => void;
  /** Acknowledge what the reader can see: the tab on screen, plus every sidebar
   *  row actually in view. */
  ackSeen: () => void;
  /** Acknowledge the tab the reader just switched to. */
  ackSwitch: (tabID: string) => void;
}

export function createAttentionController(wiring: AttentionWiring): AttentionController {
  // Request dedupe in page memory: the position last sent per tab. The server's monotonic marker is
  // the state; this only stops a refresh from resending what is in flight or refused, and every
  // sync point clears it, so a refused position is offered once more after a reconnect or a wake.
  const sent = new Map<string, number>();
  // Each tab's class at the previous fold, so a tab leaving it retracts its notifications once.
  const classes = new Map<string, "needs" | "unread">();

  function ack(c: CueCandidate): void {
    if (cueClass(c) !== "unread" || sent.get(c.id) === c.position) {
      return;
    }
    const { id, position } = c;
    sent.set(id, position);
    void wiring.sendAck(id, position).then((result) => {
      if (result === "failed" && sent.get(id) === position) {
        sent.delete(id);
      }
    });
  }

  function retractLeft(candidates: readonly CueCandidate[]): void {
    const now = new Map<string, "needs" | "unread">();
    for (const c of candidates) {
      const cls = cueClass(c);
      if (cls !== "") {
        now.set(c.id, cls);
      }
    }
    for (const id of classes.keys()) {
      if (!now.has(id)) {
        wiring.retract(id);
      }
    }
    classes.clear();
    for (const [id, cls] of now) {
      classes.set(id, cls);
    }
  }

  /**
   * refresh is the RAISE rule: a tab counts unless its unread state is on screen (active AND
   * page-visible). Both halves matter: "active" alone swallows the cue of the one tab left
   * running on a hidden page. Safe as a SWEEP, so it is the funnel.
   */
  function refresh(): void {
    const candidates = wiring.candidates();
    const ids = new Set(candidates.map((c) => c.id));
    for (const id of sent.keys()) {
      if (!ids.has(id)) {
        sent.delete(id);
      }
    }
    const watchedTab = wiring.pageVisible() ? wiring.activeTabID() : "";
    const watched = candidates.find((c) => c.id === watchedTab);
    if (watched !== undefined) {
      ack(watched);
    }
    retractLeft(candidates);
    wiring.surfaces.apply(summarize(candidates, watchedTab));
  }

  function reassert(): void {
    refresh();
    wiring.surfaces.assertBadge();
  }

  return {
    refresh,
    sync(): void {
      sent.clear();
      reassert();
    },

    ackSeen(): void {
      if (wiring.pageVisible()) {
        const inView = new Set(wiring.rowsInView());
        for (const candidate of wiring.candidates()) {
          if (inView.has(candidate.id)) {
            ack(candidate);
          }
        }
      }
      // refresh's watched-tab rule acknowledges the ACTIVE tab, row in view or not.
      reassert();
    },

    ackSwitch(tabID: string): void {
      // Switching to a tab means looking at it, gated on visibility: the boot restore activates a
      // tab too. refresh cannot cover it: `activeTabID()` may still name the outgoing tab.
      if (wiring.pageVisible()) {
        const candidate = wiring.candidates().find((c) => c.id === tabID);
        if (candidate !== undefined) {
          ack(candidate);
        }
      }
      reassert();
    },
  };
}

// The browser binding: the only part of this file that touches globals.

/**
 * Is the page in front of the reader? visibilityState is the reliable test:
 * document.hasFocus() is false for a visible-but-unfocused window. Same signal notify.ts
 * reads.
 */
export function pageVisible(): boolean {
  return document.visibilityState !== "hidden";
}

/**
 * Whether an element is presented: not hidden by CSS (checkVisibility()) AND inside the
 * viewport. checkVisibility() ignores geometry, so the `translateX(-100%)` drawer needs the
 * second test.
 */
function surfaceVisible(el: HTMLElement): boolean {
  const probe = (el as { checkVisibility?: () => boolean }).checkVisibility;
  if (typeof probe === "function" && !probe.call(el)) {
    return false;
  }
  const rect = el.getBoundingClientRect();
  return (
    rect.width > 0 &&
    rect.height > 0 &&
    rect.right > 0 &&
    rect.bottom > 0 &&
    rect.left < window.innerWidth &&
    rect.top < window.innerHeight
  );
}

/**
 * The chat ids whose sidebar row is FULLY inside `#tab-list`'s box, with the sidebar
 * presented. Fully: a lingering count is dismissible, a cue blanked unseen is not. The test
 * is transform-invariant, so it holds while the drawer animates.
 */
export function rowsInView(sidebar: HTMLElement, tabList: HTMLElement): string[] {
  if (!surfaceVisible(sidebar)) {
    return [];
  }
  const clip = tabList.getBoundingClientRect();
  const ids: string[] = [];
  for (const row of tabList.querySelectorAll<HTMLElement>("[data-tab-id]")) {
    const id = row.dataset["tabId"] ?? "";
    if (id === "") {
      continue;
    }
    const rect = row.getBoundingClientRect();
    if (rect.height > 0 && rect.top >= clip.top && rect.bottom <= clip.bottom) {
      ids.push(id);
    }
  }
  return ids;
}

/** Close the banners a cue-bearing tab's subject was announced with, through the tag the worker
 *  and notify.ts both derive from that subject. Only chat and run tabs bear a cue. */
function retractTab(tabID: string): void {
  const subject = openTabSubjects().find((s) => s.id === tabID);
  if (subject === undefined) {
    return;
  }
  const target = subject.kind === "run" ? runTarget(subject.ref) : chatTarget(subject.ref);
  void closeNotificationsFor(target).catch(() => {
    /* the page's own banner is closed; a failed listing leaves the worker's delivered */
  });
}

/** browserAttentionEnv binds the three sinks to the real browser. Every
 *  capability decision is made HERE, once, so the core never probes for one. */
export function browserAttentionEnv(): AttentionEnv {
  // The base title is captured ONCE, so the sink composes prefix + base without a second
  // copy of the <title> literal. Reading document.title back would compound the prefix.
  const base = document.title;
  const env: AttentionEnv = {
    titlePrefix: (text: string): void => {
      // No guard here: createAttention calls this only on a count change or a release,
      // and this is the only writer of document.title in the app.
      document.title = text + base;
    },
  };

  // The Badging API, read through `unknown` like notify.ts's Notification: absent on most
  // browsers, and only for installed apps.
  const nav: unknown = globalThis.navigator;
  const setAppBadge = (nav as { setAppBadge?: unknown } | undefined)?.setAppBadge;
  const clearAppBadge = (nav as { clearAppBadge?: unknown } | undefined)?.clearAppBadge;
  if (typeof setAppBadge === "function") {
    env.setBadge = (count: number): void => {
      // Always a NUMBER: iOS renders nothing for a bare `setAppBadge()`. Zero clears. Rejections
      // and throws are swallowed: an unhandled rejection in a status sweep is a page fault.
      try {
        const call =
          count > 0
            ? (setAppBadge as (n: number) => unknown).call(nav, count)
            : typeof clearAppBadge === "function"
              ? (clearAppBadge as () => unknown).call(nav)
              : (setAppBadge as (n: number) => unknown).call(nav, 0);
        void Promise.resolve(call).catch(() => {
          /* an OS that will not paint a badge is a title-only OS */
        });
      } catch {
        /* a synchronous throw is the same non-event */
      }
    };
  }

  // EVERY icon link: browsers pick different ones. apple-touch-icon is NOT matched; the OS
  // caches it at install.
  const links = [...document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')];
  // Each original captured once, and every variant computed from the ORIGINAL
  // rather than from the current value, so repeated swaps cannot compound.
  const originals = new Map<HTMLLinkElement, string>();
  for (const link of links) {
    originals.set(link, link.getAttribute("href") ?? "");
  }
  if (links.length > 0) {
    env.setIcon = (variant): void => {
      for (const link of links) {
        const original = originals.get(link) ?? "";
        if (variant === null) {
          link.setAttribute("href", original);
          continue;
        }
        const next = iconVariantHref(original, variant);
        if (next !== null) {
          link.setAttribute("href", next);
        }
      }
    };
  }

  return env;
}

/** The tabs with their read positions. Tracked reads of the chat headers and run states, so a
 *  frame or reply moving only a position re-runs the fold inside `subscribeTabCues`. */
function positionedCandidates(): CueCandidate[] {
  return cueCandidates().map((c) => ({
    id: c.id,
    kind: c.kind,
    status: c.status,
    seen: c.seen,
    position:
      c.kind === "chat"
        ? (watchSession(c.ref)?.read_position ?? 0)
        : (runState(c.ref)?.readPosition ?? 0),
  }));
}

/** Send an acknowledgement as the background action `tabs.ack`. Deferred a microtask: the fold
 *  runs inside a reactive effect, which must not track or write the action's own state. */
function sendAck(tabID: string, seen: number): Promise<AckResult> {
  return new Promise<void>((resolve) => {
    queueMicrotask(resolve);
  })
    .then(() => ackTabCommand.dispatch({ id: tabID, seen, opID: newOpID() }).outcome)
    .then((outcome): AckResult => {
      if (outcome.status === "success") {
        return outcome.value >= seen ? "acked" : "refused";
      }
      return outcome.status === "error" && outcome.error.status === 400 ? "refused" : "failed";
    });
}

/**
 * initAttention wires the controller to the app and returns its disposer. The recompute
 * funnel is `subscribeTabCues`, watching `stateVersion` (the tab SET and every subject's
 * `seen`) and `dotVersion` (every dot write); missing either leaves the count stale.
 * Acknowledgement keys on WHAT THE READER CAN SEE, so the scrolled-off tab keeps its cue.
 */
export function initAttention(): () => void {
  const surfaces = createAttention(browserAttentionEnv());
  const controller = createAttentionController({
    candidates: positionedCandidates,
    activeTabID: getActiveTabId,
    pageVisible,
    rowsInView: () => rowsInView($.sidebar, $.tabList),
    retract: retractTab,
    sendAck,
    surfaces,
  });

  const stop: (() => void)[] = [subscribeTabCues(controller.refresh)];

  stop.push(
    onBus(BUS_TAB_CHANGED, (e) => {
      if (e.kind === "chat" || e.kind === "run") {
        controller.ackSwitch(e.to);
      }
    }),
  );

  const onVisible = (): void => {
    controller.ackSeen();
  };
  document.addEventListener("visibilitychange", onVisible);
  stop.push(() => {
    document.removeEventListener("visibilitychange", onVisible);
  });

  // The drawer opening is a CLASS toggle, so it is observed, covering every gesture by
  // construction. Paired with transitionend: the mutation lands before the transform, so the
  // settled event acknowledges. Scoped to the sidebar: transitionend bubbles from rows.
  const onSettled = (e: TransitionEvent): void => {
    if (e.target === $.sidebar) {
      controller.ackSeen();
    }
  };
  $.sidebar.addEventListener("transitionend", onSettled);
  stop.push(() => {
    $.sidebar.removeEventListener("transitionend", onSettled);
  });

  const drawer = new MutationObserver(() => {
    controller.ackSeen();
  });
  drawer.observe($.sidebar, { attributes: true, attributeFilter: ["class"] });
  stop.push(() => {
    drawer.disconnect();
  });

  // Restore the page's own icon before the page GOES AWAY: a browser remembers one icon per
  // URL for bookmarks and history. `pagehide`, NOT `freeze`, which fires for a background
  // tab still showing its icon.
  const onPageGone = (): void => {
    surfaces.release();
  };
  const onSync = (): void => {
    controller.sync();
  };
  window.addEventListener("pagehide", onPageGone);
  window.addEventListener("pageshow", onSync);
  window.addEventListener("focus", onSync);
  stop.push(() => {
    window.removeEventListener("pagehide", onPageGone);
    window.removeEventListener("pageshow", onSync);
    window.removeEventListener("focus", onSync);
  });

  // Each event below may follow state this page missed: the stream reopened, the server's
  // snapshots replaced every dot, or the service worker relayed a push or a click.
  stop.push(
    onBus(BUS_RECONCILE, onSync),
    onBus(BUS_PAGE_RESUMED, onSync),
    onSSE("connected", onSync),
    onSSE("status_snapshot", onSync),
    onSSE("pending_snapshot", onSync),
  );
  const worker = (globalThis.navigator as { readonly serviceWorker?: EventTarget } | undefined)
    ?.serviceWorker;
  const onWorkerMessage = (e: Event): void => {
    const data: unknown = (e as MessageEvent).data;
    if (typeof data === "object" && data !== null && (data as { type?: unknown }).type === "push") {
      controller.sync();
    }
  };
  worker?.addEventListener("message", onWorkerMessage);
  stop.push(() => {
    worker?.removeEventListener("message", onWorkerMessage);
  });

  controller.refresh();

  return () => {
    for (const dispose of stop) {
      dispose();
    }
  };
}
