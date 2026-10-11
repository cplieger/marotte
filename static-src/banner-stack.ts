// Banner stack: persistent, acknowledgeable conditions above the transcript. Keyed on
// (chat_id, code), so a re-assert replaces in place (what a toast cannot do). Banners and
// their DISMISSALS are per device (dismissals per chat in localStorage): an
// acknowledgement is the viewer's. One bindList over a computed active-chat view renders
// every add, remove and chat switch.

import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { LS_DISMISSED_BANNERS_KEY } from "./ls-keys.js";
import { readPerChat, writePerChat } from "./per-chat-store.js";
import { isSafeURL } from "./url-safety.js";
import { ICON_CLOSE } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { el, createCollection, bindList, computed } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import type { BannerLevel } from "./types.js";

/** `href` is an EXTERNAL navigation: the server-supplied URL passes isSafeURL (http/https only) and
 *  opens in a new tab. `onClick` is an IN-APP jump, a button with no URL, since a relative path
 *  would fail isSafeURL. `onClick` wins if both are set. */
interface BannerLink {
  readonly label: string;
  readonly href?: string;
  readonly onClick?: () => void;
}

interface BannerEntry {
  readonly code: string;
  readonly chatID: string;
  el: HTMLDivElement;
}

const banners = createCollection<BannerEntry>((e) => bannerKey(e.chatID, e.code));

/**
 * Sentinel chatID for app-global banners, visible on EVERY chat and the empty state.
 * Cleared via `clearBannerCodes(GLOBAL_BANNER, [...])`, never by chat switches.
 */
export const GLOBAL_BANNER = "*";

// Visible banners: the active chat's plus the app-global ones, shallow-equal so a no-op
// recompute does not reconcile.
const visibleIds = computed<readonly string[]>(
  () => {
    const activeID = activeSession.value?.id ?? "";
    return banners
      .items()
      .filter((e) => e.chatID === activeID || e.chatID === GLOBAL_BANNER)
      .map((e) => bannerKey(e.chatID, e.code));
  },
  { equals: (a, b) => a.length === b.length && a.every((x, i) => x === b[i]) },
);

let bound = false;

/**
 * Mount and bind the banner stack; idempotent (`bound`), called by `showBanner` and from
 * chat.ts's chat-switch site.
 */
export function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const container = $.bannerStack;
  container.setAttribute("aria-label", "Notifications");
  // The container is the SINGLE live region; banner nodes carry no role/aria-live, so each
  // announces once.
  container.setAttribute("aria-live", "polite");
  // Reuse the entry-owned element so banner identity (and any ongoing
  // transitions / focus) persists across re-renders.
  bindList(
    container,
    { ids: visibleIds, signalFor: (id) => banners.signalFor(id) },
    { mount: (e) => e.el },
  );
}

/**
 * The COLLECTION key for one banner, built with keyenc `join` because
 * `clearBannersForChat` scans by chat prefix: it keeps a separator-bearing code from
 * reading as another chat's. Byte-identical to `${chatID}:${code}` for today's ids
 * (ids.ValidChatID) and call-site codes.
 */
function bannerKey(chatID: string, code: string): string {
  return join(chatID, code);
}

/** Read the dismissals, per chat. Codes only: the CHAT is the map key now, so a
 *  composite key has nothing to compose and no boundary anything could forge. */
function dismissals(): Record<string, string[]> {
  return readPerChat(LS_DISMISSED_BANNERS_KEY, validCodes);
}

/** Validate one chat's dismissed codes, dropping anything that is not a
 *  non-empty string. */
function validCodes(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) {
    return undefined;
  }
  const out = v.filter((c): c is string => typeof c === "string" && c !== "");
  return out.length > 0 ? out : undefined;
}

/** Write ONE chat's codes, which is what lets the store evict by chat. An empty
 *  list is a delete, so a chat with nothing dismissed keeps no slot. */
function writeCodes(map: Record<string, string[]>, chatID: string, codes: string[]): void {
  writePerChat(LS_DISMISSED_BANNERS_KEY, map, chatID, codes.length > 0 ? codes : undefined);
}

function isDismissed(chatID: string, code: string): boolean {
  return (dismissals()[chatID] ?? []).includes(code);
}

function persistDismiss(chatID: string, code: string): void {
  const map = dismissals();
  const codes = map[chatID] ?? [];
  if (codes.includes(code)) {
    return;
  }
  writeCodes(map, chatID, [...codes, code]);
}

function clearDismiss(chatID: string, code: string): void {
  const map = dismissals();
  const codes = map[chatID] ?? [];
  const filtered = codes.filter((c) => c !== code);
  if (filtered.length !== codes.length) {
    writeCodes(map, chatID, filtered);
  }
}

/**
 * Build the banner's affordance: a button for an in-app jump, an anchor for a safe
 * external URL, or null (an unsafe href is dropped). Both carry `btn-small`, the shared
 * button, and `.banner-link` is layout only: 02-reset.css's `* { padding: 0 }` would
 * otherwise strip a button's padding.
 */
function buildBannerLink(link: BannerLink): HTMLElement | null {
  if (link.onClick !== undefined) {
    const btn = el("button", { type: "button", className: "btn-small banner-link" }, link.label);
    btn.addEventListener("click", link.onClick);
    return btn;
  }
  if (link.href === undefined || !isSafeURL(link.href)) {
    return null;
  }
  return el(
    "a",
    {
      className: "btn-small banner-link",
      href: link.href,
      target: "_blank",
      rel: "noopener noreferrer",
    },
    link.label,
  );
}

/** Insert/replace the banner link on an existing banner node, before the
 *  dismiss button when present. */
function updateBannerLink(node: HTMLDivElement, link: BannerLink): void {
  node.querySelector(".banner-link")?.remove();
  const a = buildBannerLink(link);
  if (a === null) {
    return;
  }
  const dismissBtn = node.querySelector(".banner-dismiss");
  if (dismissBtn !== null) {
    node.insertBefore(a, dismissBtn);
  } else {
    node.appendChild(a);
  }
}

/**
 * One glyph per severity, as CHARACTERS: a banner is a text notice with no icon slot, and
 * the three differ in SHAPE so a level is never hue-only (WCAG 1.4.1). U+2139 for `info`
 * has text presentation, so it takes the banner's colour. Not `applyOutcome`, which
 * requires a `.tool-icon` and overwrites `aria-label`.
 */
const LEVEL_GLYPH: Readonly<Record<BannerLevel, string>> = {
  error: "\u2717",
  warning: "\u26A0",
  info: "\u2139",
};

export function showBanner(
  chatID: string,
  code: string,
  message: string,
  level: BannerLevel,
  dismissible: boolean,
  link?: BannerLink,
): void {
  if (isDismissed(chatID, code)) {
    return;
  }
  const key = bannerKey(chatID, code);
  const existing = banners.get(key);
  if (existing !== undefined) {
    // Replace message in place on the entry-owned element (a single text node;
    // the row's identity is unchanged, so no structural reconcile is needed).
    const msg = existing.el.querySelector(".banner-msg");
    if (msg !== null) {
      msg.textContent = message;
    }
    if (link !== undefined) {
      updateBannerLink(existing.el, link);
    }
    return;
  }
  const msg = el("span", { className: "banner-msg" }, message);
  // The severity's non-colour channel (WCAG 1.4.1); 40-a11y.css's forced-colors block does
  // not cover banners. aria-hidden: the stack's live region announces the message text.
  const glyph = el(
    "span",
    { className: "banner-glyph", "aria-hidden": "true" },
    LEVEL_GLYPH[level],
  );
  // No per-banner role/aria-live: a live child inside the live container double-announces.
  // Same decoupling as toast.ts.
  const node = el(
    "div",
    {
      className: `banner banner-${level}`,
    },
    glyph,
    msg,
  ) as HTMLDivElement;
  if (link !== undefined) {
    const a = buildBannerLink(link);
    if (a !== null) {
      node.appendChild(a);
    }
  }
  if (dismissible) {
    // `icon-btn` plus the registry's close mark leave nothing for a local rule. An SVG, not
    // `\u00d7`: `align-items` centres a text node's line box, not its ink (close-mark.test.ts).
    // The class stays for `updateBannerLink` and the tests.
    const btn = el(
      "button",
      { type: "button", className: "icon-btn banner-dismiss", "aria-label": "Dismiss" },
      iconEl(ICON_CLOSE),
    );
    btn.addEventListener("click", () => {
      removeBanner(chatID, code);
      persistDismiss(chatID, code);
    });
    node.appendChild(btn);
  }
  const entry: BannerEntry = { code, chatID, el: node };
  ensureBound();
  banners.upsert(entry);
}

function removeBanner(chatID: string, code: string): void {
  if (!banners.has(bannerKey(chatID, code))) {
    return;
  }
  banners.remove(bannerKey(chatID, code));
  clearDismiss(chatID, code);
}

/** Remove all banners matching any of the given codes for a chat. */
export function clearBannerCodes(chatID: string, codes: string[]): void {
  for (const code of codes) {
    removeBanner(chatID, code);
  }
}

/**
 * Drop every in-memory banner for a deleted chat (the chat_deleted bus handler), so
 * orphan entries and their nodes do not accumulate. Dismissals are bounded per chat by
 * per-chat-store.ts.
 */
export function clearBannersForChat(chatID: string): void {
  // keyenc has no prefix primitive, so the separator is written out. Correct because chat
  // ids contain neither reserved character (ids.ValidChatID); the trailing ":" keeps "abc"
  // from clearing "abcd". If ids ever admit ":" or "\\", split each key instead.
  const prefix = `${chatID}:`;
  // bindList detaches the removed entries' elements reactively.
  for (const key of banners.ids.peek()) {
    if (key.startsWith(prefix)) {
      banners.remove(key);
    }
  }
}
