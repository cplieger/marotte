// The search unit stamp: every element the transcript draws as one search item carries `data-find-unit` with the
// item's kind, the server's `ItemKind` (internal/chat/search_items.go), pinned by search-items-contract.test.ts.

import type { EntryKind, ItemKind } from "./wire/types.gen.js";

export const FIND_UNIT_SELECTOR = "[data-find-unit]";

/** The unit an entry's own element is, for the kinds the server searches; absent for the rest. */
const ENTRY_UNIT: Partial<Record<EntryKind, ItemKind>> = {
  text: "message",
  thinking: "reasoning",
  tool_call: "tool",
  steer: "steer",
  plan: "plan",
  compaction_failed: "notice",
  safety_blocked: "notice",
  model_routed: "notice",
  model_fallback: "notice",
};

/** Stamp `el` as the unit `kind` is, or clear a stamp a reused element carried. */
export function stampFindUnit(el: HTMLElement, kind: ItemKind | undefined): void {
  if (kind === undefined) {
    delete el.dataset["findUnit"];
  } else {
    el.dataset["findUnit"] = kind;
  }
}

/** The unit an element drawn for an entry of `kind` is. */
export function entryUnit(kind: EntryKind): ItemKind | undefined {
  return ENTRY_UNIT[kind];
}
