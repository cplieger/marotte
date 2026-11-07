// Transcript search: the server pre-pass that makes the DOM search honest. find-in-chat.ts
// highlights in the DOM, which cannot ENUMERATE unmounted, skipped or collapsed content, so
// the server owns both the count and the step list Enter walks. Only the hit find-in-chat
// lands on is revealed (`revealHitTurn`); every other hit's turn stays as it was, and its
// folded row advertises its count. This module keeps only the hit turns other surfaces read.

import { apiGetTyped } from "./api-client.js";
import { openForSearch, clearSearchOpened } from "./fold-state.js";
import { bumpMessages } from "./store.js";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

/** The typing pause before Find in chat and History search. 300 ms waits out a typical keystroke gap (about 230 ms
 *  at 52 WPM) and still answers well inside NN/g's one-second flow limit; VS Code's search-on-type uses the same. */
export const CHAT_SEARCH_DEBOUNCE_MS = 300;

/** The answer to a question nobody asked (no chat, a blank query): nothing read,
 *  nothing matched, nothing cut. */
const EMPTY_ANSWER: SearchResult = {
  matches: [],
  spread: false,
  scanned: 0,
  matched: 0,
  truncated: false,
};

/**
 * The on-demand body build for ONE hit's turn, injected by messages.ts (a static import
 * would cycle); inert until wired. The hit's ENTRY crosses, since only the projection can
 * resolve its `seq`.
 */
let buildRevealedTurn: (chatID: string, turnID: string, entryID?: string) => Promise<void> = () =>
  Promise.resolve();

/** Folds a turn the reveal released at once, injected by messages.ts like the build; inert until wired. */
let foldReleased: (chatID: string, turnID: string) => void = () => undefined;

export function initSearchRevealBuilder(
  reveal: (chatID: string, turnID: string, entryID?: string) => Promise<void>,
  fold: (chatID: string, turnID: string) => void,
): void {
  buildRevealedTurn = reveal;
  foldReleased = fold;
}

/** The turn numbers holding hits for the current query, for the turn map
 *  and the folded rows' match counts. */
let hitTurns = new Set<number>();
/** Hits per turn number, so a folded row can advertise what is inside it rather
 *  than hiding it. */
let countsByTurn = new Map<number, number>();
/**
 * The chat the standing search ran in, so its reveal is released there: the close path
 * may run after a chat switch.
 */
let searchedChatID = "";

export function searchHitTurns(): ReadonlySet<number> {
  return hitTurns;
}

export function searchHitCount(turn: number): number {
  return countsByTurn.get(turn) ?? 0;
}

/**
 * Run the server search and record which turns hold hits. An envelope is the server's;
 * `EMPTY_ANSWER` answers an empty question; `null` means the FETCH failed, so the caller
 * keeps its standing answer.
 */
export async function runServerSearch(
  chatID: string,
  query: string,
  caseSensitive = false,
): Promise<SearchResult | null> {
  if (chatID === "" || query.trim() === "") {
    resetServerSearch();
    return EMPTY_ANSWER;
  }
  // `case=1` only when asked. The server treats an absent parameter as
  // insensitive, so the default stays the behaviour it has always had.
  const flag = caseSensitive ? "&case=1" : "";
  const d = await apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}/search?q=${encodeURIComponent(query)}${flag}`,
    decodeSearchResult,
  );
  // A failed fetch (logged centrally): keep the previous counts and return `null`, so the
  // reader's walk stays whole.
  if (d === null) {
    return null;
  }
  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  searchedChatID = chatID;
  for (const h of d.matches) {
    hitTurns.add(h.turn);
    countsByTurn.set(h.turn, (countsByTurn.get(h.turn) ?? 0) + 1);
  }
  // The folded rows' counts and the turn map read the new set on the next pass.
  bumpMessages(chatID, "fact");
  return d;
}

/**
 * Drop the reveal and the hit counts. Turns opened BY SEARCH re-fold; hand-opened turns keep
 * their override. Keyed on the chat this module SEARCHED, with no argument, because the box
 * closes after a tab change moved the active id.
 */
export function resetServerSearch(): void {
  const hadHits = countsByTurn.size > 0;
  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  const searched = searchedChatID;
  searchedChatID = "";
  if (searched === "") {
    return;
  }
  const opened = clearSearchOpened(searched);
  if (opened.length > 0) {
    // At once, as on a step; then the shape change unmounts what the reveal pinned (`block-window.ts`).
    for (const turnID of opened) {
      foldReleased(searched, turnID);
    }
    bumpMessages(searched, "shape");
  } else if (hadHits) {
    bumpMessages(searched, "fact");
  }
}

/**
 * Reveal ONE hit's turn on demand (open, build around the hit's entry, repaint) before
 * navigation selects it. Idempotent.
 */
export async function revealHitTurn(chatID: string, hit: Hit): Promise<void> {
  if (chatID === "" || hit.turn_id === "") {
    return;
  }
  // Recorded before the await: a step that lands while this builds already counts the turn as the chain's.
  searchedChatID = chatID;
  openForSearch(chatID, hit.turn_id);
  await buildRevealedTurn(chatID, hit.turn_id, hit.entry_id);
  // Which turns are open and mounted changed. `shape`.
  bumpMessages(chatID, "shape");
}

/** The reveal moved off `turnID` (`search-reveal.ts`): it folds again now, not when the reader stops reading. */
export function refoldAfterReveal(chatID: string, turnID: string): void {
  foldReleased(chatID, turnID);
  bumpMessages(chatID, "shape");
}
