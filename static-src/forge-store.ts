// The one client owner of the forge connection rows (GET /api/forges) and of Sources' repository listings. Every
// surface renders from it. It starts on the first call of any accessor below, so a subscriber that reads one through
// an effect starts it too; it never polls: the server's forges_changed, a page resume and a stream reconnect re-read.

import { batch, computed, signal, touch } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { listForges, type ForgesListResponse } from "./actions/forge-list.js";
import { listRepoPage } from "./actions/forge.js";
import { apiGetTyped } from "./api-client.js";
import { BUS_PAGE_RESUMED, onBus, onSSE } from "./bus.js";
import { usableStatus } from "./forge-auth-status.js";
import { readFirstPage, readNextPage, type RepoListing } from "./forge-auth-repos.js";
import type { ConfiguredForge } from "./wire/types.gen.js";
import { decodeRepoList } from "./wire/decoders.gen.js";

/** Re-exported so consumers import the payload shape from the store that owns it. */
export type { ForgesListResponse };

const rows = signal<ForgesListResponse | null>(null);
const failed = signal(false);

/** Each row's listing, held for the account it was read through. */
const listings = new Map<string, { account: string; listing: RepoListing }>();
const listingVersion = signal(0);

interface FirstRead {
  fresh: boolean;
  done: Promise<boolean>;
}

/** The first-page read whose answer applies, keyed join(id, account). */
const pendingFirst = new Map<string, FirstRead>();

const pairs = computed<readonly string[] | null>(
  () =>
    rows.value === null
      ? null
      : rows.value.forges
          .filter((f) => usableStatus(f.status))
          .map((f) => join(f.id, f.account))
          .sort(),
  {
    equals: (a, b) =>
      a === b ||
      (a !== null && b !== null && a.length === b.length && a.every((p, i) => p === b[i])),
  },
);

/** Run inside the batch that applies each read of the rows (see onRowsApplied). */
const appliedHooks: (() => void)[] = [];

let started = false;
let issued = 0;
let latestRead: Promise<ForgesListResponse | null> | null = null;

/** Subscribe the store's triggers once; answers whether this call did. */
function subscribe(): boolean {
  if (started) {
    return false;
  }
  started = true;
  onSSE("forges_changed", () => {
    void readForges();
  });
  onBus(BUS_PAGE_RESUMED, () => {
    void readForges();
  });
  return true;
}

function start(): void {
  if (subscribe()) {
    void readForges();
  }
}

/**
 * Read the rows now. Only the newest-issued read applies, so a read issued before a change cannot paint over one
 * issued after it. Resolves this read's answer, null when it failed (the rows held stay).
 */
export async function readForges(): Promise<ForgesListResponse | null> {
  subscribe();
  const mine = ++issued;
  const read = listForges.dispatch(undefined).then((d) => {
    if (mine === issued) {
      latestRead = null;
      if (d === null) {
        failed.value = true;
      } else {
        applyRows(d);
      }
    }
    return d;
  });
  latestRead = read;
  return read;
}

/** The stream (re)connected: the rows may have changed while it was down. Reads only once the store has started. */
export function notifyStreamConnected(): void {
  if (started) {
    void readForges();
  }
}

/** The rows, read once when none are held yet; null when that read failed. */
export async function ensureForges(): Promise<ForgesListResponse | null> {
  start();
  const held = rows.peek();
  if (held !== null) {
    return held;
  }
  await (latestRead ?? readForges());
  return rows.peek();
}

/** The rows held, null before the first read lands. */
export function forgeRows(): ForgesListResponse | null {
  start();
  return rows.value;
}

/** True when the last read of the rows failed. */
export function forgeRowsFailed(): boolean {
  start();
  return failed.value;
}

/** The sorted join(id, account) of every usable row, null before any rows; a new array only when the set changes. */
export function usablePairs(): readonly string[] | null {
  start();
  return pairs.value;
}

/**
 * The account row `id` is usable as, undefined while it is gone or unusable: state read through any other account
 * no longer belongs to the row.
 */
export function usableAccount(id: string): string | undefined {
  const row = forgeRows()?.forges.find((f) => f.id === id);
  return row !== undefined && usableStatus(row.status) ? row.account : undefined;
}

/**
 * Run `fn` inside the batch that applies each read of the rows, so the state it drops for an account usableAccount
 * no longer answers is gone before any effect or reader sees those rows.
 */
export function onRowsApplied(fn: () => void): void {
  appliedHooks.push(fn);
}

function rowOf(id: string): ConfiguredForge | undefined {
  return rows.peek()?.forges.find((f) => f.id === id);
}

function applyRows(d: ForgesListResponse): void {
  const before = new Map((rows.peek()?.forges ?? []).map((f) => [f.id, f]));
  batch(() => {
    rows.value = d;
    failed.value = false;
    let dropped = false;
    for (const [id, held] of listings) {
      if (usableAccount(id) !== held.account) {
        listings.delete(id);
        dropped = true;
      }
    }
    if (dropped) {
      listingVersion.value = listingVersion.peek() + 1;
    }
    for (const fn of appliedHooks) {
      fn();
    }
  });
  for (const row of d.forges) {
    const prev = before.get(row.id);
    const held = listings.get(row.id);
    const nowConnected = row.status === "connected" && prev?.status !== "connected";
    if (nowConnected && held !== undefined && (held.listing.unread || held.listing.stale)) {
      void readFirst(row.id, row.account, false);
    }
  }
}

/**
 * The listing held for the row's current account. With none held and none in flight it starts the one first-page
 * read for that (id, account) and answers undefined, which a row paints as loading.
 */
export function ensureListing(id: string): RepoListing | undefined {
  start();
  touch(listingVersion);
  const row = rowOf(id);
  if (row === undefined || !usableStatus(row.status)) {
    return undefined;
  }
  const held = listings.get(id);
  if (held?.account === row.account) {
    return held.listing;
  }
  void readFirst(id, row.account, false);
  return undefined;
}

/** Read a row's first page from its forge rather than the server's cache. Resolves false on failure. */
export async function refreshListing(id: string): Promise<boolean> {
  const row = rowOf(id);
  if (row === undefined || !usableStatus(row.status)) {
    return true;
  }
  return readFirst(id, row.account, true);
}

/**
 * Joins the read in flight for (id, account) unless this one is fresh and that one is not; the fresh one then
 * supersedes it, so a cached answer never lands over the forge's.
 */
function readFirst(id: string, account: string, fresh: boolean): Promise<boolean> {
  const key = join(id, account);
  const inFlight = pendingFirst.get(key);
  if (inFlight !== undefined && (inFlight.fresh || !fresh)) {
    return inFlight.done;
  }
  const read: FirstRead = { fresh, done: Promise.resolve(true) };
  pendingFirst.set(key, read);
  read.done = sendFirst(id, account, key, read);
  return read.done;
}

async function sendFirst(
  id: string,
  account: string,
  key: string,
  read: FirstRead,
): Promise<boolean> {
  const page = await apiGetTyped(
    `/api/forges/${encodeURIComponent(id)}/repos${read.fresh ? "?refresh=1" : ""}`,
    decodeRepoList,
  );
  if (pendingFirst.get(key) !== read) {
    return page !== null;
  }
  pendingFirst.delete(key);
  if (usableAccount(id) !== account || (page !== null && page.account !== account)) {
    return page !== null;
  }
  const prev = listings.get(id);
  listings.set(id, {
    account,
    listing: readFirstPage(prev?.account === account ? prev.listing : undefined, page),
  });
  listingVersion.value = listingVersion.peek() + 1;
  return page !== null;
}

/** Read the page after a row's held one. Resolves the failure's message, or undefined. */
export async function readMoreListing(id: string): Promise<string | undefined> {
  const held = listings.get(id);
  if (held === undefined || held.listing.next === "") {
    return undefined;
  }
  const o = await listRepoPage.dispatch({ forgeId: id, after: held.listing.next }).outcome;
  if (o.status === "cancelled") {
    return undefined;
  }
  if (o.status === "error") {
    return o.error.message;
  }
  const now = listings.get(id);
  if (
    now?.account !== held.account ||
    now.listing.next !== held.listing.next ||
    o.value.account !== held.account
  ) {
    return undefined;
  }
  listings.set(id, { account: now.account, listing: readNextPage(now.listing, o.value) });
  listingVersion.value = listingVersion.peek() + 1;
  return undefined;
}

/** Forget every row and listing, for a suite's next case; the subscriptions stay. */
// deadset:ignore DS1004 -- test seam: resets the held rows, listings and read counters
export function _resetForTest(): void {
  rows.value = null;
  failed.value = false;
  listings.clear();
  pendingFirst.clear();
  latestRead = null;
  issued += 1;
  listingVersion.value = listingVersion.peek() + 1;
}
