// The unseen-cue set, rendered on the surfaces OUTSIDE this page: the tab title's count,
// the installed app's badge and the tab icon. A latched sidebar dot is not reliably on
// screen (a scrolled list, the mobile drawer, a hidden page), and Notification is absent
// on iOS Safari outside an installed app. STATE, not events: a pure render of what is
// true, so nothing here has a timer. Ported from web-terminal-ui's attention.ts, which its
// `exports` map does not publish. DOM-free above "The browser binding".

import { cueCandidates, getActiveTabId, subscribeTabCues, setOnTabClosed } from "./tabs.js";
import { BUS_TAB_CHANGED, onBus } from "./bus.js";
import { $ } from "./dom.js";

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

/**
 * isUnseenCue reports whether a chat's CURRENT dot state is a cue this reader has not
 * acknowledged: the one predicate behind count and icon. A watched chat's cue is
 * acknowledged on observation, so no caller special-cases the chat on screen.
 */
export function isUnseenCue(
  status: string,
  id: string,
  seen: ReadonlyMap<string, CueStatus>,
): status is CueStatus {
  return isCueStatus(status) && seen.get(id) !== status;
}

/** What the fold reads per chat tab. A structural type, so tabs.ts passes its own
 *  projection without this module knowing what else a TabViewSpec carries. */
export interface CueCandidate {
  readonly id: string;
  readonly status: string;
}

/** The whole attention state, and the only thing the sinks are allowed to see. */
export interface Attention {
  /** How many chats hold an unacknowledged cue. */
  readonly count: number;
  /** The most severe of them, or "" when there is none. */
  readonly worst: CueStatus | "";
}

export const NO_ATTENTION: Attention = { count: 0, worst: "" };

/**
 * summarize folds the chat tabs and this reader's acknowledgements into what every surface
 * renders: a COUNT for title and badge, the single WORST for the icon, neither naming a
 * chat. A stateless fold, so it cannot go stale and is cheap on every dot write.
 */
export function summarize(
  candidates: readonly CueCandidate[],
  seen: ReadonlyMap<string, CueStatus>,
): Attention {
  let count = 0;
  let worst: CueStatus | "" = "";
  for (const candidate of candidates) {
    if (!isUnseenCue(candidate.status, candidate.id, seen)) {
      continue;
    }
    count += 1;
    worst = worseCue(worst, candidate.status);
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
  /** Render an attention state. Idempotent: a value equal to the last one
   *  applied touches nothing. */
  apply: (next: Attention) => void;
}

export function createAttention(env: AttentionEnv): AttentionSurfaces {
  // Last applied, so each sink fires only on a real change: the title is also the bookmark
  // name, and re-assigning an icon href makes some browsers re-fetch it.
  let applied: Attention = NO_ATTENTION;
  let first = true;

  return {
    apply(next: Attention): void {
      const countChanged = first || next.count !== applied.count;
      const worstChanged = first || next.worst !== applied.worst;
      first = false;
      applied = next;

      if (countChanged) {
        env.titlePrefix(titlePrefixFor(next.count));
        // The badge takes the SAME number as the title.
        env.setBadge?.(next.count);
      }
      if (worstChanged) {
        env.setIcon?.(next.worst === "" ? null : cueIconName(next.worst));
      }
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

/**
 * localStorage key for the cues this reader has SEEN: chat id -> the acknowledged dot
 * state. Its own key, not `marotte.ui-state` (a different cadence). Remembered because
 * every dot input is rebuilt from server state on reload and reconnect. Per device: seen
 * is the READER's property. Keyed per chat, since several chats latch at once.
 */
export const CUE_SEEN_KEY = "marotte.cue-seen";

/**
 * Bound on the acknowledgement map, so a corrupted or hostile stored value cannot make the
 * restore path do unbounded work.
 */
export const MAX_PERSISTED_CUE_SEEN = 200;

/**
 * parseCueSeen reads stored acknowledgements into a clean map, dropping anything it cannot
 * trust (a lost acknowledgement only re-lights a dismissable cue). Pure; the caller owns
 * the read and its try/catch.
 */
export function parseCueSeen(raw: string | null): Map<string, CueStatus> {
  const out = new Map<string, CueStatus>();
  if (raw === null || raw === "") {
    return out;
  }
  let data: unknown;
  try {
    data = JSON.parse(raw);
  } catch {
    return out;
  }
  // Arrays and null are typeof "object" too, and neither is a cue map.
  if (typeof data !== "object" || data === null || Array.isArray(data)) {
    return out;
  }
  for (const [id, status] of Object.entries(data)) {
    if (id === "" || typeof status !== "string" || !isCueStatus(status)) {
      continue;
    }
    out.set(id, status);
    if (out.size >= MAX_PERSISTED_CUE_SEEN) {
      break;
    }
  }
  return out;
}

/**
 * serializeCueSeen encodes acknowledgements for storage without truncating: `mark` keeps
 * the map within the cap.
 */
export function serializeCueSeen(seen: ReadonlyMap<string, CueStatus>): string {
  return JSON.stringify(Object.fromEntries(seen));
}

/** Where acknowledgements are kept. Injected so the store is testable with no
 *  localStorage, and so the quota/disabled-storage try/catch has one home. */
export interface CueSeenStorage {
  read: () => string | null;
  write: (raw: string) => void;
}

interface CueSeen {
  /** The live map, for the fold to read. */
  map: () => ReadonlyMap<string, CueStatus>;
  /** Record that this reader has seen `id` holding `status`. A non-cue status is
   *  not an acknowledgeable event, so it is ignored rather than stored. */
  mark: (id: string, status: string) => void;
  /** Drop an acknowledgement, so the chat's NEXT cue is a fresh one. */
  forget: (id: string) => void;
}

export function createCueSeen(storage: CueSeenStorage): CueSeen {
  const seen = parseCueSeen(storage.read());
  return {
    map: () => seen,
    mark(id: string, status: string): void {
      if (!isCueStatus(status) || seen.get(id) === status) {
        return;
      }
      seen.set(id, status);
      // Evict oldest-first so the live map obeys the parser's cap: a chat that vanished while the
      // page was closed leaves an entry nothing else collects.
      while (seen.size > MAX_PERSISTED_CUE_SEEN) {
        const oldest = seen.keys().next().value;
        if (oldest === undefined) {
          break;
        }
        seen.delete(oldest);
      }
      storage.write(serializeCueSeen(seen));
    },
    forget(id: string): void {
      if (seen.delete(id)) {
        storage.write(serializeCueSeen(seen));
      }
    },
  };
}

/** Everything the controller needs from outside itself. */
interface AttentionWiring {
  /** The chat tabs and their current dot states. */
  candidates: () => readonly CueCandidate[];
  /**
   * The TAB the reader is looking at, "" for none. A tab id like every key here, never the
   * chat store's active id: the watched-chat rule could never match it.
   */
  activeTabID: () => string;
  /** Whether the page is in front of the reader at all. */
  pageVisible: () => boolean;
  /** The chat ids whose sidebar row the reader can actually SEE right now. */
  rowsInView: () => readonly string[];
  storage: CueSeenStorage;
  surfaces: AttentionSurfaces;
}

interface AttentionController {
  /** Apply the observation rules to current state, then re-render the surfaces.
   *  The recompute funnel's target; idempotent, so calling it more often than
   *  necessary costs a loop over a handful of tabs. */
  refresh: () => void;
  /** Acknowledge what the reader can see: the chat on screen, plus every sidebar
   *  row actually in view. */
  ackSeen: () => void;
  /** Acknowledge the chat the reader just switched to. */
  ackSwitch: (chatID: string) => void;
  /** Drop a departed chat's acknowledgement. */
  forget: (chatID: string) => void;
}

export function createAttentionController(wiring: AttentionWiring): AttentionController {
  const seen = createCueSeen(wiring.storage);

  /**
   * refresh is the RAISE rule: a cue raises when the chat is NOT (active AND page-visible)
   * and its state is an unacknowledged cue. Both halves matter: "active" alone swallows the
   * cue of the one chat left running on a hidden page. Safe as a SWEEP, so it is the funnel.
   * The acknowledgement path is the ONLY thing keeping a watched chat out of the count.
   */
  function refresh(): void {
    const watchedTab = wiring.pageVisible() ? wiring.activeTabID() : "";
    for (const candidate of wiring.candidates()) {
      if (candidate.status === "") {
        // No information, not a state: a never-painted dot or a chat the store does not know is no
        // evidence a cue ENDED, and dropping the acknowledgement re-lit it on every reload.
        continue;
      }
      if (!isCueStatus(candidate.status)) {
        // The state moved off a cue, so the next cue must be fresh; this also bounds the map.
        seen.forget(candidate.id);
      } else if (candidate.id === watchedTab) {
        seen.mark(candidate.id, candidate.status);
      }
    }
    wiring.surfaces.apply(summarize(wiring.candidates(), seen.map()));
  }

  return {
    refresh,

    ackSeen(): void {
      if (!wiring.pageVisible()) {
        return;
      }
      const inView = new Set(wiring.rowsInView());
      for (const candidate of wiring.candidates()) {
        if (inView.has(candidate.id)) {
          seen.mark(candidate.id, candidate.status);
        }
      }
      // refresh's watched-chat rule acknowledges the ACTIVE chat, row in view or not (the page
      // is visible here).
      refresh();
    },

    ackSwitch(tabID: string): void {
      // Switching to a chat means looking at it, gated on visibility: the boot restore activates
      // a tab too. refresh cannot cover it: `activeTabID()` may still name the outgoing tab.
      if (wiring.pageVisible()) {
        const candidate = wiring.candidates().find((c) => c.id === tabID);
        if (candidate !== undefined) {
          seen.mark(candidate.id, candidate.status);
        }
      }
      refresh();
    },

    forget(tabID: string): void {
      seen.forget(tabID);
      refresh();
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

/** localStorage-backed acknowledgements, with the two try/catch guards a
 *  disabled or full store needs. Module-private: `initAttention` is the only
 *  caller, and the tests reach it through the key rather than the constructor. */
function browserCueSeenStorage(): CueSeenStorage {
  return {
    read(): string | null {
      try {
        return localStorage.getItem(CUE_SEEN_KEY);
      } catch {
        return null; // storage unavailable (private mode / disabled)
      }
    },
    write(raw: string): void {
      try {
        localStorage.setItem(CUE_SEEN_KEY, raw);
      } catch {
        // ignore quota / disabled storage
      }
    },
  };
}

/** browserAttentionEnv binds the three sinks to the real browser. Every
 *  capability decision is made HERE, once, so the core never probes for one. */
export function browserAttentionEnv(): AttentionEnv {
  // The base title is captured ONCE, so the sink composes prefix + base without a second
  // copy of the <title> literal. Reading document.title back would compound the prefix.
  const base = document.title;
  const env: AttentionEnv = {
    titlePrefix: (text: string): void => {
      // No guard here: createAttention only calls this when the count changed,
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

/**
 * initAttention wires the controller to the app and returns its disposer. The recompute
 * funnel is `subscribeTabCues`, watching `stateVersion` (the chat-tab SET) and
 * `dotVersion` (every dot write); missing either leaves the count stale after a close.
 * Acknowledgement keys on WHAT THE READER CAN SEE, so the scrolled-off chat keeps its cue.
 */
export function initAttention(): () => void {
  const surfaces = createAttention(browserAttentionEnv());
  const controller = createAttentionController({
    candidates: cueCandidates,
    activeTabID: getActiveTabId,
    pageVisible,
    rowsInView: () => rowsInView($.sidebar, $.tabList),
    storage: browserCueSeenStorage(),
    surfaces,
  });

  const stop: (() => void)[] = [subscribeTabCues(controller.refresh)];

  stop.push(
    onBus(BUS_TAB_CHANGED, (e) => {
      if (e.kind === "chat") {
        controller.ackSwitch(e.to);
      }
    }),
  );

  // closeTab is the only production path removing a tab, so one hook drops its acknowledgement.
  setOnTabClosed(controller.forget);

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
  // tab still showing its icon. Through apply() so the change gate stays true and a bfcache
  // restore (pageshow) repaints.
  const onPageGone = (): void => {
    surfaces.apply(NO_ATTENTION);
  };
  const onPageBack = (e: PageTransitionEvent): void => {
    if (e.persisted) {
      controller.refresh();
    }
  };
  window.addEventListener("pagehide", onPageGone);
  window.addEventListener("pageshow", onPageBack);
  stop.push(() => {
    window.removeEventListener("pagehide", onPageGone);
    window.removeEventListener("pageshow", onPageBack);
  });

  controller.refresh();

  return () => {
    for (const dispose of stop) {
      dispose();
    }
  };
}
