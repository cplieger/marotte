// The one owner of the /api/forges poll and payload; consumers subscribe or read through, so one answer serves the
// badge, the PRs tab and Sources. A painter uses onForgeChange + currentForges; a consumer about to act reads
// through ensureForges. Nothing takes an AbortSignal: one consumer navigating away must not abort a fetch others
// await, so callers guard staleness after the await.

import { pollAction } from "./actions/index.js";
import { listForges, type ForgesListResponse } from "./actions/forge-list.js";
import { onSSE } from "./bus.js";
import { signal, subscribe } from "@cplieger/reactive";
import type { ConfiguredForge, ForgeKind } from "./wire/types.gen.js";

/** Re-exported so consumers import the payload shape from the store that owns it. */
export type { ForgesListResponse };

/** pollAction pauses while the document is hidden and refreshes on focus, so this is a ceiling, not a floor. */
const POLL_INTERVAL_MS = 15_000;

/** The last successful payload, or null before the first lands. */
const state = signal<ForgesListResponse | null>(null);

/**
 * True when the most recent fetch failed. Distinct from a null payload (nothing yet): a failure gets Retry and a red
 * badge, a load in flight gets neither.
 */
const failed = signal(false);

let started = false;

/** Start the poll and the invalidation listener. Idempotent: several init paths reach it. */
export function initForgeStore(): void {
  if (started) {
    return;
  }
  started = true;
  // A connection change is the only thing besides time that moves this data, and the server broadcasts it; without
  // this the badge took up to 15s to notice a sign-out.
  onSSE("forges_changed", () => {
    void refreshForges();
  });
  pollAction(listForges, undefined, {
    interval: POLL_INTERVAL_MS,
    onSuccess: apply,
  });
}

function apply(d: ForgesListResponse | null): void {
  if (d === null) {
    failed.value = true;
    return;
  }
  failed.value = false;
  state.value = d;
}

/**
 * Fetch now and publish the result, deduped with any in-flight poll tick. For a consumer with a reason to distrust
 * the cache (Sources after a sign-in or sign-out, the SSE listener).
 */
export async function refreshForges(): Promise<ForgesListResponse | null> {
  const d = await listForges.dispatch(undefined);
  apply(d);
  return d;
}

/**
 * The forge list, fetching once if nothing has landed yet, for a consumer that cannot proceed without one (an
 * empty answer would read as "no connected forges"). A payload in hand is returned as-is, with no round trip.
 */
export async function ensureForges(): Promise<ForgesListResponse | null> {
  const current = state.peek();
  if (current !== null) {
    return current;
  }
  return refreshForges();
}

/** Subscribe to store changes. Fires immediately with the current value. */
export function onForgeChange(fn: () => void): () => void {
  return subscribe(state, fn);
}

/** The configured forges known now, empty before the first successful load. */
export function currentForges(): readonly ConfiguredForge[] {
  return state.value?.forges ?? [];
}

/** Which forge kinds offer the browser-based device flow. */
export function oauthByKind(): Partial<Record<ForgeKind, boolean>> {
  return state.value?.oauth ?? {};
}

/** True when the last fetch failed. See the `failed` signal for why this is not
 *  the same question as an empty list. */
export function forgeLoadFailed(): boolean {
  return failed.value;
}
