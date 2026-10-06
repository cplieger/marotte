// Progressive collapse: which turns are open. One open by default, the newest; a turn folds when the next starts.

import { LS_TURN_FOLDS_KEY } from "./ls-keys.js";
import { readPerChat, writePerChat } from "./per-chat-store.js";
import { severityOf } from "./turn-severity.js";
import type { Turn } from "./turns.js";

/** How many trailing turns stay open regardless of anything else. ONE: a turn
 *  auto-collapses when the next turn starts, and not before. */
const OPEN_TAIL = 1;

// Residency is `block-window.ts`'s, in blocks and tool cards; this module answers open or closed only.

/**
 * Per-chat, per-turn overrides (`true` opened, `false` folded; absent follows the rule), keyed by the turn's opening
 * message id, which is stable across pagination.
 */
type Overrides = Record<string, boolean>;
const overrides = new Map<string, Overrides>();

/** Not persisted, dropped when the search closes: a search must not permanently rearrange the transcript. */
const searchOpened = new Map<string, Set<string>>();

let loaded = false;

function load(): void {
  if (loaded) {
    return;
  }
  loaded = true;
  for (const [chatID, byTurn] of Object.entries(readPerChat(LS_TURN_FOLDS_KEY, validOverrides))) {
    overrides.set(chatID, byTurn);
  }
}

/** Per entry: stale bytes must not reach the renderer's open/closed decision. */
function validOverrides(v: unknown): Overrides | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    return undefined;
  }
  const out: Overrides = {};
  for (const [turnID, open] of Object.entries(v as Record<string, unknown>)) {
    if (turnID !== "" && typeof open === "boolean") {
      out[turnID] = open;
    }
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

/** One chat per key, which is what lets the store evict by chat. */
function persist(chatID: string): void {
  const byTurn = overrides.get(chatID);
  const value = byTurn !== undefined && Object.keys(byTurn).length > 0 ? byTurn : undefined;
  writePerChat(LS_TURN_FOLDS_KEY, Object.fromEntries(overrides), chatID, value);
}

/** Whether a turn renders open. `index`/`total` position it in the projected list (the resident window). */
export function isTurnOpen(chatID: string, t: Turn, index: number, total: number): boolean {
  load();
  // A running turn cannot collapse; this outranks an override so a stale fold cannot hide a live stream.
  if (t.outcome === "running") {
    return true;
  }
  // The newest turn cannot collapse; above the overrides so an old or rewound fold cannot strand it.
  if (index >= total - OPEN_TAIL) {
    return true;
  }
  const explicit = overrides.get(chatID)?.[t.id];
  if (explicit !== undefined) {
    return explicit;
  }
  if (searchOpened.get(chatID)?.has(t.id) === true) {
    return true;
  }
  // A broken turn never auto-folds: its collapsed face hides the reason.
  if (severityOf(t.outcome) === "broken") {
    return true;
  }
  // Everything else folds, a live workflow run included: the run bar (`run-bar.ts`) is its readout.
  return false;
}

/** Record the reader's own choice for a turn. */
export function setTurnOpen(chatID: string, turnID: string, open: boolean): void {
  load();
  let byTurn = overrides.get(chatID);
  if (byTurn === undefined) {
    byTurn = {};
    overrides.set(chatID, byTurn);
  }
  byTurn[turnID] = open;
  persist(chatID);
}

/**
 * Whether the reader asked for this turn's body (opened it, or a search landed on it); unlike `isTurnOpen`, not true
 * for the newest or a running turn.
 */
export function isTurnRevealed(chatID: string, turnID: string): boolean {
  load();
  return overrides.get(chatID)?.[turnID] === true || searchOpened.get(chatID)?.has(turnID) === true;
}

/** Open a turn because a search hit is inside it. */
export function openForSearch(chatID: string, turnID: string): void {
  let set = searchOpened.get(chatID);
  if (set === undefined) {
    set = new Set();
    searchOpened.set(chatID, set);
  }
  set.add(turnID);
}

/** Drop every search-opened turn. Returns true when something changed, so the
 *  caller can skip a repaint that would do nothing. */
export function clearSearchOpened(chatID: string): boolean {
  const set = searchOpened.get(chatID);
  if (set === undefined || set.size === 0) {
    return false;
  }
  searchOpened.delete(chatID);
  return true;
}

/**
 * Drop the in-memory copy so the next read reloads it. Called by the sign-out sweep (`boot.ts` `forgetDeviceState`);
 * deleting the key alone does not reach a module-level cache.
 */
export function resetFoldState(): void {
  overrides.clear();
  searchOpened.clear();
  loaded = false;
}
