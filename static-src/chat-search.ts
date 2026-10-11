// Transcript search: the server pre-pass that makes the DOM search honest. find-in-chat.ts
// highlights in the DOM, which cannot ENUMERATE unmounted, skipped or collapsed content, so
// the server owns both the count and the step list Enter walks; the reveal below lifts a
// folded turn so the walker can MARK its hit. The answer is the server's whole envelope;
// this module keeps only the hit turns other surfaces read.

import { apiGetTyped } from "./api-client.js";
import { openForSearch, clearSearchOpened } from "./fold-state.js";
import { bumpMessages } from "./store.js";
import { decodeSearchResult } from "./wire/decoders.gen.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";

/** The answer to a question nobody asked (no chat, a blank query): nothing read,
 *  nothing matched, nothing cut. */
const EMPTY_ANSWER: SearchResult = { matches: [], scanned: 0, matched: 0, truncated: false };

/**
 * The on-demand body build for ONE hit's turn, injected by messages.ts (a static import
 * would cycle); inert until wired. The hit's ENTRY crosses, since only the projection can
 * resolve its `seq`.
 */
let buildRevealedTurn: (chatID: string, turnID: string, entryID?: string) => Promise<void> = () =>
  Promise.resolve();

/** The same build for the search-WIDE loop, whose grant is scoped to the reveal
 *  rather than to the one navigation the reader is making. */
let buildWalkTurn: (chatID: string, turnID: string) => Promise<void> = () => Promise.resolve();

let endWalkReveal: (chatID: string) => void = () => undefined;

export function initSearchRevealBuilder(
  reveal: (chatID: string, turnID: string, entryID?: string) => Promise<void>,
  forWalk: (chatID: string, turnID: string) => Promise<void>,
  endWalk: (chatID: string) => void,
): void {
  buildRevealedTurn = reveal;
  buildWalkTurn = forWalk;
  endWalkReveal = endWalk;
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
 * Run the server search and reveal every hit's turn BEFORE the DOM pass, which prunes
 * hidden subtrees. An envelope is the server's; `EMPTY_ANSWER` answers an empty question;
 * `null` means the FETCH failed, so the caller keeps its standing answer.
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
  // A failed fetch (logged centrally): keep the previous reveal and return `null`, so the
  // reader's walk stays whole.
  if (d === null) {
    return null;
  }
  const hits = d.matches;

  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  searchedChatID = chatID;
  for (const h of hits) {
    hitTurns.add(h.turn);
    countsByTurn.set(h.turn, (countsByTurn.get(h.turn) ?? 0) + 1);
  }

  // Open by the hit's `turn_id`, the same id the fold set, the card and the store key on.
  const revealTurns = new Set<string>();
  for (const h of hits) {
    if (h.turn_id !== "") {
      revealTurns.add(h.turn_id);
    }
  }
  for (const id of revealTurns) {
    openForSearch(chatID, id);
  }
  // A revealed STUB has no body to mark yet, so each is built (under its folded card) and
  // completes before this resolves, since the caller re-runs the walker on resolution.
  for (const id of revealTurns) {
    await buildWalkTurn(chatID, id);
  }
  // Nudge the renderer so the reveal takes effect before the DOM walker runs.
  // A reveal changes which turns are open and mounted: `shape`, stated.
  bumpMessages(chatID, "shape");
  return d;
}

/**
 * Drop the reveal and the hit marks. Turns opened BY SEARCH re-fold; hand-opened turns keep
 * their override. Keyed on the chat this module SEARCHED, with no argument, because the box
 * closes after a tab change moved the active id.
 */
export function resetServerSearch(): void {
  hitTurns = new Set<number>();
  countsByTurn = new Map<number, number>();
  const searched = searchedChatID;
  searchedChatID = "";
  // Unconditional: inside the branch below, grants would outlive a `searchOpened` emptied
  // elsewhere.
  endWalkReveal(searched);
  if (searched !== "" && clearSearchOpened(searched)) {
    // The re-fold is a shape change: revealed turns fold and pinned ordinals unmount
    // (`block-window.ts`).
    bumpMessages(searched, "shape");
  }
}

/**
 * Reveal ONE hit's turn on demand (open, build around the hit's entry, repaint) before
 * navigation selects it: a hit's turn can arrive after the search ran, or be re-folded by
 * the reader. Idempotent.
 */
export async function revealHitTurn(chatID: string, hit: Hit): Promise<void> {
  if (chatID === "" || hit.turn_id === "") {
    return;
  }
  openForSearch(chatID, hit.turn_id);
  await buildRevealedTurn(chatID, hit.turn_id, hit.entry_id);
  // Same stated cause as the search-wide reveal: which turns are open and
  // mounted changed. `shape`.
  bumpMessages(chatID, "shape");
}
