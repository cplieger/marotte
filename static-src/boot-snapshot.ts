// The boot snapshot: a bounded projection of what THIS SCREEN showed, in IndexedDB, so a
// resume paints before the network answers. A paint-time hint like the theme cache: it
// advances no tab-set version, and `boot.ts` paints it only until its own chat read
// answers.

import { effect } from "@cplieger/reactive";

import {
  derivedHasMore,
  get,
  getActiveId,
  getSessions,
  messagesVersionOf,
  setSessions,
  watchActiveId,
} from "./store.js";
import { openTabSubjects, paintProvisionalTabs, tabSetVersion } from "./tabs.js";
import { TURN_OUTCOME_VALUES } from "./turn-severity.js";
import type { Session, TabSubject, TurnState, Usage } from "./types.js";
import { asObject, decodeArray, reqNum, reqStr } from "./validators.js";
import { decodeEntry, decodeTabSubject, decodeUsage } from "./wire/decoders.gen.js";
import type { Entry, EntryToolCall, EntryToolResult, TurnOutcome } from "./wire/types.gen.js";

/** How many of the active chat's newest TURNS are carried. Turns because half a
 *  turn renders as a card with no header. */
const SNAPSHOT_TURNS = 3;

/** Hard cap on the entries those turns may contribute: one turn can hold hundreds of
 *  tool rows, so the turn bound alone is not a bound. */
const SNAPSHOT_MAX_ENTRIES = 80;

/**
 * THE BOUND THAT BINDS: a count bound cannot see what grows INSIDE a message. The record is
 * parsed before the first frame of every boot, and on WebKit IndexedDB lives in the
 * network process that owns the page's sockets, so an oversized record kills the event
 * stream and reloads the page. 96 KiB: one plausible frame while the network answers.
 */
const SNAPSHOT_MAX_BYTES = 96 * 1024;

/**
 * A tool card's `.tool-details` is born CLOSED, so its output is truncated, not dropped:
 * `tool-card.ts` offers the disclosure only for non-blank output. `output_spans` goes;
 * `input` is trimmed (`.tool-subtitle` renders `input.command`).
 */
const SNAPSHOT_MAX_TOOL_OUTPUT = 256;
const SNAPSHOT_MAX_TOOL_INPUT = 256;

/** How long the projection must sit still before it is written. Every write is a
 *  whole-record replace, so a streaming turn would otherwise write per frame. */
const SNAPSHOT_DEBOUNCE_MS = 1_000;

const DB_NAME = "marotte-boot";
const DB_VERSION = 1;
const STORE_NAME = "snapshot";
const RECORD_KEY = "current";

/** Not a `ChatHeader`, whose required timestamps a `Session` does not carry. */
interface SnapshotChat {
  readonly id: string;
  readonly name: string;
  readonly model: string;
  readonly current_mode_id: string;
  readonly turn_count: number;
  readonly usage: Usage;
  /** OPTIONAL, so an older record still paints. */
  readonly last_turn_outcome?: TurnOutcome;
  readonly updated_at?: number;
}

/**
 * The transcript window a resume paints: the active chat's newest entries in FILE order,
 * whole turns, `turn_open` first. A card numbers itself from its `turn_open.n`.
 */
interface SnapshotWindow {
  readonly chat_id: string;
  readonly entries: readonly Entry[];
}

/** What one screen was showing. No active tab: `device-view.ts` persists that per
 *  screen already, and `tabs.ts`'s reset reads it. */
export interface BootSnapshot {
  readonly tabs: readonly TabSubject[];
  readonly chats: readonly SnapshotChat[];
  /** `null` is the explicit "no chat was active", which is why there is no
   *  empty-string sentinel to test for. */
  readonly window: SnapshotWindow | null;
}

/**
 * Read the persisted snapshot, `null` for every failure: a hint has no failure a caller
 * could act on.
 */
export async function readBootSnapshot(): Promise<BootSnapshot | null> {
  return decodeSnapshot(await readRecord());
}

/**
 * Forget what this screen showed and stop capturing, so the NEXT boot paints nothing (the
 * current frame stays). Every writer stops, `pagehide` included, or it would re-write the
 * record.
 */
export async function clearBootSnapshot(): Promise<void> {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  await deleteRecord();
}

/**
 * Paint a snapshot and report whether it painted anything. Chat rows go first: a tab's
 * label is read from the store while its row is built. `setSessions` REPLACES the store, so
 * this runs only before the boot's own chat list lands (`restoreWorkspace` owns that).
 */
export function paintBootSnapshot(snap: BootSnapshot | null): boolean {
  if (snap === null || snap.tabs.length === 0) {
    return false;
  }
  const win = snap.window;
  setSessions(
    snap.chats.map((c) => toProvisionalSession(c, win?.chat_id === c.id ? win : undefined)),
  );
  paintProvisionalTabs(snap.tabs);
  return true;
}

/** `residency` stays unset so `transcriptStale` reads true and the activation refetches;
 *  `provisional` covers rows the server answer does not name (see `Session.provisional`). */
function toProvisionalSession(c: SnapshotChat, win: SnapshotWindow | undefined): Session {
  const window =
    win === undefined
      ? { turns: new Map<string, TurnState>(), order: [] }
      : turnsFromEntries(win.entries);
  return {
    id: c.id,
    name: c.name,
    model: c.model,
    acp_session_id: "",
    current_mode_id: c.current_mode_id,
    usage: c.usage,
    turns: window.turns,
    turn_order: window.order,
    turn_count: c.turn_count,
    // The shared derivation, over the REAL resident count: row and window are built here.
    has_more: derivedHasMore(c.turn_count, window.order.length),
    thinking: false,
    working_label: "Thinking",
    provisional: true,
    // Conditional: under `exactOptionalPropertyTypes` an explicit `undefined` is a value, and
    // the outcome tells this row's dot from a chat that never ran a turn.
    ...(c.last_turn_outcome !== undefined && { last_turn_outcome: c.last_turn_outcome }),
    ...(c.updated_at !== undefined && { updated_at: c.updated_at }),
  };
}

let pending: ReturnType<typeof setTimeout> | undefined;
/** Production disposes it on a sign-out; a test that drives two boots does too. */
let disposeCapture: (() => void) | undefined;
/** Lifetime of the capture's DOM listener, so `clearBootSnapshot` can revoke it. */
let captureAbort: AbortController | undefined;

/** Start persisting the projection. Called once, from the post-auth door: there
 *  is nothing worth remembering about a login screen. */
export function startBootSnapshot(): void {
  if (disposeCapture !== undefined) {
    return;
  }
  captureAbort = new AbortController();
  disposeCapture = effect(() => {
    // The three reads that move the projection: the tab set (which a rename also
    // bumps, via `renameTab`), which chat is active, and that chat's transcript.
    tabSetVersion();
    const active = watchActiveId();
    if (active !== "") {
      // eslint-disable-next-line @typescript-eslint/no-unused-expressions
      messagesVersionOf(active).value;
    }
    schedule();
  });
  // Best-effort, on the last event a backgrounded PWA reliably gets: the debounce is
  // usually still pending, and the write may not land.
  addEventListener(
    "pagehide",
    () => {
      flush();
    },
    { signal: captureAbort.signal },
  );
}

function schedule(): void {
  clearTimeout(pending);
  pending = setTimeout(flush, SNAPSHOT_DEBOUNCE_MS);
}

function flush(): void {
  clearTimeout(pending);
  pending = undefined;
  void writeRecord(captureBootSnapshot());
}

/** Project the live state into a snapshot. Exported for the capture test, which
 *  is the only reader outside `flush`. */
export function captureBootSnapshot(): BootSnapshot {
  const active = getActiveId();
  const tabs = openTabSubjects();
  // Only the chats a tab names: the store also holds closed ones, not what was on screen.
  const open = new Set(tabs.filter((t) => t.kind === "chat").map((t) => t.ref));
  return {
    tabs,
    chats: getSessions()
      .filter((s) => open.has(s.id))
      .map(projectChat),
    window: newestWindow(active),
  };
}

function projectChat(s: Session): SnapshotChat {
  return {
    id: s.id,
    name: s.name,
    model: s.model,
    current_mode_id: s.current_mode_id,
    turn_count: s.turn_count,
    usage: s.usage,
    ...(s.last_turn_outcome !== undefined && { last_turn_outcome: s.last_turn_outcome }),
    ...(s.updated_at !== undefined && { updated_at: s.updated_at }),
  };
}

/**
 * The active chat's newest turns as ENTRIES, in file order, capped by dropping WHOLE turns
 * (a body without its `turn_open` is a headerless card). Open tails have no `seq` and are
 * not carried.
 */
function newestWindow(chatID: string): SnapshotWindow | null {
  const s = get(chatID);
  if (s === undefined) {
    return null;
  }
  const ids = s.turn_order.slice(-SNAPSHOT_TURNS);
  const out: Entry[] = [];
  let bytes = 0;
  for (let i = ids.length - 1; i >= 0; i--) {
    const id = ids[i];
    const state = id === undefined ? undefined : s.turns.get(id);
    if (state === undefined) {
      continue;
    }
    const whole = state.entries.map(lighten);
    const cost = whole.reduce((n, e) => n + sizeOf(e), 0);
    if (out.length + whole.length <= SNAPSHOT_MAX_ENTRIES && bytes + cost <= SNAPSHOT_MAX_BYTES) {
      out.unshift(...whole);
      bytes += cost;
      continue;
    }
    if (out.length === 0) {
      // The newest turn alone over budget is trimmed to a PREFIX from `turn_open`
      // (`entries[i].seq === i` cannot seat a tail); the activation's refetch fills it.
      out.push(...admitPrefix(state.entries));
    }
    break;
  }
  if (out.length === 0) {
    return null;
  }
  return { chat_id: chatID, entries: out };
}

/**
 * What one entry costs the record in the budget's currency, a JSON proxy for the store's
 * structured clone, the same proxy the bound was measured with.
 */
function sizeOf(e: Entry): number {
  return JSON.stringify(e).length;
}

/**
 * As much of one turn as both bounds allow, from its `turn_open` forward, stopping at the
 * first entry that does not fit.
 */
function admitPrefix(entries: readonly Entry[]): Entry[] {
  const out: Entry[] = [];
  let bytes = 0;
  for (const e of entries) {
    const light = lighten(e);
    const cost = sizeOf(light);
    if (out.length + 1 > SNAPSHOT_MAX_ENTRIES || bytes + cost > SNAPSHOT_MAX_BYTES) {
      break;
    }
    out.push(light);
    bytes += cost;
  }
  return out;
}

/**
 * The turns a persisted window describes, grouped in FILE order. A turn not `seq`-contiguous
 * from 0 is DROPPED: nothing repairs a hint.
 */
function turnsFromEntries(entries: readonly Entry[]): {
  turns: Map<string, TurnState>;
  order: string[];
} {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  for (const e of entries) {
    const held = turns.get(e.turn);
    if (held === undefined) {
      if (e.seq !== 0) {
        continue;
      }
      turns.set(e.turn, { entries: [e], openEntries: new Map() });
      order.push(e.turn);
      continue;
    }
    if (e.seq !== held.entries.length) {
      continue;
    }
    held.entries.push(e);
    if (e.kind === "turn_close") {
      held.closeAt = e.seq;
    }
  }
  return { turns, order };
}

/**
 * One entry with what a first frame does not paint cut down: tool entries trimmed, others
 * returned ITSELF. Nothing here drops an entry; the admission paths price this copy.
 */
function lighten(e: Entry): Entry {
  if (e.kind !== "tool_call" && e.kind !== "tool_result") {
    return e;
  }
  return { ...e, payload: lightenToolPayload(e.payload) };
}

/**
 * The two payloads carrying a tool's output, trimmed by one function: the same three fields
 * on each. These hold most of the record's bytes.
 */
function lightenToolPayload(v: unknown): unknown {
  if (typeof v !== "object" || v === null) {
    return v;
  }
  const out = { ...(v as EntryToolCall & EntryToolResult) };
  // Styles the output bytes this drops.
  delete out.output_spans;
  if (out.output !== undefined && out.output.length > SNAPSHOT_MAX_TOOL_OUTPUT) {
    out.output = out.output.slice(0, SNAPSHOT_MAX_TOOL_OUTPUT);
  }
  if (out.input !== undefined) {
    out.input = lightenInput(out.input);
  }
  return out;
}

/**
 * Truncate a tool input's TOP-LEVEL string values, where a card's painted keys live; the
 * byte budget catches anything nested.
 */
function lightenInput(v: unknown): unknown {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    return v;
  }
  const src = v as Record<string, unknown>;
  const out: Record<string, unknown> = {};
  for (const k of Object.keys(src)) {
    const val = src[k];
    out[k] =
      typeof val === "string" && val.length > SNAPSHOT_MAX_TOOL_INPUT
        ? val.slice(0, SNAPSHOT_MAX_TOOL_INPUT)
        : val;
  }
  return out;
}

/**
 * Narrow a persisted record, validating every ELEMENT through the generated wire decoders.
 * A rejection is the whole record: a half-valid hint is not a hint.
 */
function decodeSnapshot(v: unknown): BootSnapshot | null {
  try {
    const o = asObject(v, "$.boot_snapshot");
    const win = o["window"];
    return {
      tabs: decodeArray(o["tabs"], decodeTabSubject, "$.boot_snapshot.tabs"),
      chats: decodeArray(o["chats"], decodeSnapshotChat, "$.boot_snapshot.chats"),
      // `null` is the value; ABSENT is a foreign record and rejects, so the next capture rewrites
      // it. No migration, and `DB_VERSION` stays: the store itself is unchanged.
      window: win === null ? null : decodeSnapshotWindow(win),
    };
  } catch {
    return null;
  }
}

/**
 * STRICT, unlike the network window decoder: a dropped entry would leave a `seq` hole in a
 * hint nothing repairs, so a refused entry takes the whole record.
 */
function decodeSnapshotWindow(v: unknown): SnapshotWindow {
  const o = asObject(v, "$.boot_snapshot.window");
  return {
    chat_id: reqStr(o, "chat_id", "$.boot_snapshot.window"),
    entries: decodeArray(o["entries"], decodeEntry, "$.boot_snapshot.window.entries"),
  };
}

/**
 * The optional twin of `validators.ts` `reqOneOf`: anything the vocabulary does not name
 * reads as ABSENT. Here because `validators.ts` is regenerated by `cmd/wire-codegen`.
 */
function optOneOf<T extends string>(
  o: Record<string, unknown>,
  key: string,
  vals: readonly T[],
): T | undefined {
  const v = o[key];
  return typeof v === "string" && (vals as readonly string[]).includes(v) ? (v as T) : undefined;
}

/**
 * The same tolerance for the timestamp: a non-finite number reads as ABSENT (unlike
 * `optNum`), so the dot's tooltip never shows an age of NaN.
 */
function optFiniteNum(o: Record<string, unknown>, key: string): number | undefined {
  const v = o[key];
  return typeof v === "number" && Number.isFinite(v) ? v : undefined;
}

function decodeSnapshotChat(v: unknown): SnapshotChat {
  const o = asObject(v, "$.boot_snapshot.chat");
  const outcome = optOneOf(o, "last_turn_outcome", TURN_OUTCOME_VALUES);
  const updatedAt = optFiniteNum(o, "updated_at");
  return {
    id: reqStr(o, "id", "$.boot_snapshot.chat"),
    name: reqStr(o, "name", "$.boot_snapshot.chat"),
    model: reqStr(o, "model", "$.boot_snapshot.chat"),
    current_mode_id: reqStr(o, "current_mode_id", "$.boot_snapshot.chat"),
    turn_count: reqNum(o, "turn_count", "$.boot_snapshot.chat"),
    usage: decodeUsage(o["usage"]),
    ...(outcome !== undefined && { last_turn_outcome: outcome }),
    ...(updatedAt !== undefined && { updated_at: updatedAt }),
  };
}

/** The one connection, opened lazily and kept, because the capture writes
 *  repeatedly. Resolves `null` for good once an open has failed. */
let dbHandle: Promise<IDBDatabase | null> | undefined;

function db(): Promise<IDBDatabase | null> {
  dbHandle ??= openDB();
  return dbHandle;
}

function openDB(): Promise<IDBDatabase | null> {
  // The capability off the object, not a `typeof` test: a runtime without
  // IndexedDB and one whose open throws (private browsing) are both this branch.
  const factory = (globalThis as { readonly indexedDB?: IDBFactory }).indexedDB;
  if (factory === undefined) {
    return Promise.resolve(null);
  }
  return new Promise((resolve) => {
    let req: IDBOpenDBRequest;
    try {
      req = factory.open(DB_NAME, DB_VERSION);
    } catch {
      resolve(null);
      return;
    }
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE_NAME);
    };
    req.onsuccess = () => {
      resolve(req.result);
    };
    req.onerror = () => {
      resolve(null);
    };
    req.onblocked = () => {
      resolve(null);
    };
  });
}

async function readRecord(): Promise<unknown> {
  const conn = await db();
  if (conn === null) {
    return null;
  }
  return new Promise<unknown>((resolve) => {
    try {
      const req = conn.transaction(STORE_NAME, "readonly").objectStore(STORE_NAME).get(RECORD_KEY);
      req.onsuccess = () => {
        resolve(req.result as unknown);
      };
      req.onerror = () => {
        resolve(null);
      };
    } catch {
      resolve(null);
    }
  });
}

async function deleteRecord(): Promise<void> {
  const conn = await db();
  if (conn === null) {
    return;
  }
  return new Promise<void>((resolve) => {
    try {
      const tx = conn.transaction(STORE_NAME, "readwrite");
      tx.objectStore(STORE_NAME).delete(RECORD_KEY);
      tx.oncomplete = () => {
        resolve();
      };
      tx.onerror = () => {
        resolve();
      };
      tx.onabort = () => {
        resolve();
      };
    } catch {
      resolve();
    }
  });
}

async function writeRecord(snap: BootSnapshot): Promise<void> {
  const conn = await db();
  if (conn === null) {
    return;
  }
  return new Promise<void>((resolve) => {
    try {
      const tx = conn.transaction(STORE_NAME, "readwrite");
      // Structured-cloneable by construction: every field is JSON data off the wire.
      tx.objectStore(STORE_NAME).put(snap, RECORD_KEY);
      tx.oncomplete = () => {
        resolve();
      };
      tx.onerror = () => {
        resolve();
      };
      tx.onabort = () => {
        resolve();
      };
    } catch {
      resolve();
    }
  });
}

/**
 * Forget the pending write, capture effect and connection, for tests driving two boots. The
 * connection is DROPPED, not closed, keeping this reset synchronous.
 */
// deadset:ignore DS1004 -- test seam: resets the pending save timer, capture and database handle
export function _resetForTest(): void {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  dbHandle = undefined;
}
