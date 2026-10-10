// The rest of a `has_full` tool call (GET /api/chats/{id}/tools/{toolCallID}), memoised per CALL:
// one call can be on screen twice. A BULK IS IMMUTABLE (`HasFull` is set only on persisted
// messages), so there is no invalidation and must not be one; the cache needs only a BOUND.

import { apiGetTyped } from "./api-client.js";
import { decodeToolCallBulk } from "./wire/decoders.gen.js";
import type { TextSpan, ToolCallBulk, ToolDiff } from "./wire/types.gen.js";

/** What a reveal renders, and all this module holds. Not the wire `ToolCallBulk`: its `input` is
 *  read by nobody (a `has_full` card is never live) and is `unknown`, so dropping it keeps every
 *  field O(1) to cost. Fields are total. Exported for `tool-card.ts`'s content-piece table. */
export interface ToolBulk {
  readonly output: string;
  readonly outputSpans: TextSpan[];
  readonly diffs: ToolDiff[];
}

/** The ceiling on retained payload, in bytes. BYTES, because one entry spans three orders of
 *  magnitude (~26 KB average, a whole terminal stream at the tail). 4 MiB because the DOM is the
 *  durable copy: retention serves only a SECOND reader of a call, and a miss costs one re-request. */
export const MAX_RETAINED_BYTES = 4 * 1024 * 1024;

/** A JS string is UTF-16, so a code unit is two bytes of heap. */
const BYTES_PER_CODE_UNIT = 2;

/** A `TextSpan` is five small integers; ~48 bytes as an object with its header.
 *  Counted because an ANSI-heavy output carries one per styled run, so the spans
 *  of a large output are a real charge even though no single one is. */
const BYTES_PER_SPAN = 48;

interface Entry {
  readonly promise: Promise<ToolBulk | null>;
  /** This entry's charge against the budget, or `undefined` while in flight, which also makes it
   *  unevictable. A settled entry may cost 0, so the signal is ABSENCE, never zero. */
  cost: number | undefined;
}

const held = new Map<string, Entry>();
let retained = 0;

function key(chatID: string, toolCallID: string): string {
  return `${chatID}\u0000${toolCallID}`;
}

function costOf(bulk: ToolBulk): number {
  let units = bulk.output.length;
  for (const d of bulk.diffs) {
    units += d.path.length + (d.old_text?.length ?? 0) + d.new_text.length;
  }
  return units * BYTES_PER_CODE_UNIT + bulk.outputSpans.length * BYTES_PER_SPAN;
}

/** Evict settled entries, least recently read first, until the budget holds. A hit re-inserts, so
 *  `Map` insertion order IS read order; deleting mid-iteration is defined. */
function sweep(): void {
  for (const [k, e] of held) {
    if (retained <= MAX_RETAINED_BYTES) {
      return;
    }
    if (e.cost === undefined) {
      continue;
    }
    held.delete(k);
    retained -= e.cost;
  }
}

function settle(k: string, entry: Entry, d: ToolCallBulk | null): ToolBulk | null {
  const mine = held.get(k) === entry;
  if (d === null) {
    if (mine) {
      held.delete(k);
    }
    return null;
  }
  const bulk: ToolBulk = {
    output: d.output ?? "",
    outputSpans: d.output_spans ?? [],
    diffs: d.diffs ?? [],
  };
  if (!mine) {
    return bulk;
  }
  const cost = costOf(bulk);
  if (cost > MAX_RETAINED_BYTES) {
    // Answered but not held: evicting every other entry for one that still would
    // not fit trades the whole cache for nothing.
    held.delete(k);
    return bulk;
  }
  entry.cost = cost;
  retained += cost;
  sweep();
  return bulk;
}

/** Fetch one tool call's whole output, style spans and diffs. `null` when unknown to the server or
 *  failed: the held preview is the fallback. A failure is not retained, so a re-open retries. */
export function toolCallBulk(chatID: string, toolCallID: string): Promise<ToolBulk | null> {
  if (chatID === "" || toolCallID === "") {
    return Promise.resolve(null);
  }
  const k = key(chatID, toolCallID);
  const hit = held.get(k);
  if (hit !== undefined) {
    held.delete(k);
    held.set(k, hit);
    return hit.promise;
  }
  const entry: Entry = {
    cost: undefined,
    promise: apiGetTyped(
      `/api/chats/${encodeURIComponent(chatID)}/tools/${encodeURIComponent(toolCallID)}`,
      decodeToolCallBulk,
    ).then((d) => settle(k, entry, d)),
  };
  held.set(k, entry);
  return entry.promise;
}

/** Drop every held bulk. Test seam: the cache is module state, so a file's cases
 *  would otherwise inherit each other's retention. */
// deadset:ignore DS1004 -- test seam: resets the held bulk cache and its retention
export function _resetToolBulkForTest(): void {
  held.clear();
  retained = 0;
}
