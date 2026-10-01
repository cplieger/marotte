// ---------------------------------------------------------------------------
// The boot snapshot: a bounded projection of what THIS SCREEN was showing, held
// in IndexedDB so a resume paints before the network answers. A PAINT-TIME HINT
// with the standing the theme's localStorage cache has: it advances no tab-set
// version, and `boot.ts` paints it only while its own chat read has yet to ANSWER,
// so the server's answer always wins.
// ---------------------------------------------------------------------------

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

/** THE BOUND THAT ACTUALLY BINDS: a count bound cannot see what grows INSIDE a message.
 *  Measured on the live instance (2026-09-10) at **1,778,339 bytes over SEVEN messages**, one of
 *  them 1,006,210 over 207 tool calls. Its cost is a RELOAD rather than a slow write: the record
 *  is read and JSON-parsed before the FIRST FRAME of every boot, and on WebKit IndexedDB is owned
 *  by the NETWORK process that also owns the page's sockets — so the cost lands in the process
 *  whose death takes the event stream with it and reloads the document, at an interval that
 *  shortened as the conversation grew (~90s -> ~43s). 96 KiB, because the job is one plausible
 *  frame while the network answers. */
const SNAPSHOT_MAX_BYTES = 96 * 1024;

/** A tool card's `.tool-details` is born CLOSED, so its output is not on the frame this record
 *  draws. Truncated rather than dropped: `tool-card.ts` decides whether a card has anything to
 *  reveal from the output being non-blank, so dropping it would withdraw the disclosure until
 *  the real payload lands. `output_spans` styles only those bytes, so it goes entirely; `input`
 *  is trimmed because `.tool-subtitle` renders `input.command` on the visible claim line. */
const SNAPSHOT_MAX_TOOL_OUTPUT = 256;
const SNAPSHOT_MAX_TOOL_INPUT = 256;

/** How long the projection must sit still before it is written. Every write is a
 *  whole-record replace, so a streaming turn would otherwise write per frame. */
const SNAPSHOT_DEBOUNCE_MS = 1_000;

const DB_NAME = "marotte-boot";
const DB_VERSION = 1;
const STORE_NAME = "snapshot";
const RECORD_KEY = "current";

/** One chat row, bounded to what the strip and the context bar paint from it.
 *
 *  Not a `ChatHeader`: that wire type requires `created_at`/`updated_at`, which a
 *  `Session` does not carry, so reusing it would mean inventing two timestamps for
 *  fields nothing here reads. */
interface SnapshotChat {
  readonly id: string;
  readonly name: string;
  readonly model: string;
  readonly current_mode_id: string;
  readonly turn_count: number;
  readonly usage: Usage;
  /** How this chat's newest finished turn ended, and when the chat last moved —
   *  what a resumed row paints its tab dot and that dot's age from. OPTIONAL on
   *  purpose: a record written before these fields existed still paints, because a
   *  hint that rejects itself over a field the strip can do without is worse than a
   *  row with no dot. */
  readonly last_turn_outcome?: TurnOutcome;
  readonly updated_at?: number;
}

/** The transcript window a resume paints: the active chat's newest entries in FILE order, whole
 *  turns, `turn_open` first. No segmentation state travels beside them, because a card's ordinal
 *  is its own `turn_open.n` and a window therefore numbers itself. */
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

/** Read the persisted snapshot. Resolves `null` for every failure — an absent
 *  record, a browser with no IndexedDB, a corrupt or foreign payload — because a
 *  paint-time hint has no failure a caller could act on. */
export async function readBootSnapshot(): Promise<BootSnapshot | null> {
  return decodeSnapshot(await readRecord());
}

/** Forget what this screen was showing, and stop capturing. It does NOT un-paint the current
 *  frame — those rows are the ones this device already had on screen — so what it buys is that
 *  the NEXT boot paints nothing. Every writer stops, the `pagehide` listener included: a page
 *  transition after this would otherwise re-write the record it just deleted. */
export async function clearBootSnapshot(): Promise<void> {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  await deleteRecord();
}

/** Paint a snapshot, and report whether it painted anything.
 *
 *  ORDERED: the chat rows go in first because a chat tab's label is read from the store while its
 *  row is built (`tab-materialize.ts` `chatName`). `setSessions` REPLACES the store, so this may
 *  only run BEFORE the boot's own chat list lands; `boot.ts`'s `restoreWorkspace` owns that
 *  ordering, and the transport holds every SSE frame until `markHydrated`. */
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

/** A chat row and, for the chat the snapshot carried one for, its window.
 *
 *  `residency` is left unset deliberately: `transcriptStale` then reads true, so the activation
 *  this paint enables refetches the window rather than trusting a hint — which is what makes the
 *  server's answer overwrite this and not the reverse. `provisional` covers the rows that answer
 *  does not name; its rule is at `types.ts` `Session.provisional`. */
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
    // Through the shared derivation, so one rule answers this for every row built
    // without a window — and the count it reads is the REAL resident count, because
    // the row and its window are built by this one call.
    has_more: derivedHasMore(c.turn_count, window.order.length),
    thinking: false,
    working_label: "Thinking",
    provisional: true,
    // CONDITIONAL, both of them: under `exactOptionalPropertyTypes` an explicit `undefined`
    // is a value rather than an omission, and the outcome is what tells this row's dot from
    // a chat that has never run a turn.
    ...(c.last_turn_outcome !== undefined && { last_turn_outcome: c.last_turn_outcome }),
    ...(c.updated_at !== undefined && { updated_at: c.updated_at }),
  };
}

let pending: ReturnType<typeof setTimeout> | undefined;
/** The capture's effect, or undefined while nothing is capturing. Production
 *  disposes it on a sign-out; a test that drives two boots does too. */
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

/** The active chat's newest turns as ENTRIES, in file order, capped.
 *
 *  Reads the store's own resident window rather than re-deriving one, newest-first, which is what
 *  makes the entry cap drop WHOLE turns instead of cutting the flattened tail — a body whose
 *  `turn_open` is gone is the headerless card `SNAPSHOT_TURNS` exists to prevent. Open tails are
 *  not carried: an `OpenEntry` has no `seq`, and this record's job is one settled frame. */
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
      // The newest turn alone over budget is the case both caps exist for, and dropping it
      // would resume with no transcript. Trimmed to a contiguous PREFIX from `turn_open`
      // rather than to its newest end, because `TurnState`'s invariant is
      // `entries[i].seq === i`: a tail cannot be seated at all, where a prefix is a card
      // with its own header and a short body, and the activation's own refetch fills it.
      out.push(...admitPrefix(state.entries));
    }
    break;
  }
  if (out.length === 0) {
    return null;
  }
  return { chat_id: chatID, entries: out };
}

/** What one entry costs the record, in the currency the budget is stated in. The store
 *  holds a structured clone rather than JSON, so this is a proxy — and it is the same
 *  proxy the 1,778,339-byte measurement above was taken with, which is what makes the
 *  number and the bound comparable. */
function sizeOf(e: Entry): number {
  return JSON.stringify(e).length;
}

/** As much of one turn as both bounds allow, from its `turn_open` forward and stopping at
 *  the first entry that does not fit. Its own function because the two-bound walk is the
 *  part a `slice` cannot express. */
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

/** The turns a persisted window describes, grouped in FILE order.
 *
 *  A turn whose entries are not `seq`-contiguous from 0 is DROPPED rather than seated:
 *  that is `TurnState`'s invariant, this record is a hint nothing repairs, and the store's
 *  own hole path is for a page that has a turn to re-read. */
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

/** One entry with the fields a first frame does not paint cut down to size. A tool entry is
 *  carried trimmed; an entry of any other kind is returned ITSELF, so the ordinary prose row
 *  allocates nothing.
 *
 *  Nothing here DROPS an entry, so what a turn contributes is decided by the admission paths
 *  alone (`newestWindow`, `admitPrefix`), each pricing this lightened copy through `sizeOf`
 *  against the record's own budgets. A turn that does not fit paints its trigger alone, or
 *  nothing, rather than a body whose `seq` run has a hole in it. */
function lighten(e: Entry): Entry {
  if (e.kind !== "tool_call" && e.kind !== "tool_result") {
    return e;
  }
  return { ...e, payload: lightenToolPayload(e.payload) };
}

/** The two payloads that carry a tool's output, trimmed. ONE function for both because the
 *  three fields it touches are the same three on each — `tool_result` carrying no `input`
 *  simply has nothing there to trim. These payloads are where the bytes measurably are:
 *  686,630 of the 1,006,210 one message came to in the record `SNAPSHOT_MAX_BYTES` measures. */
function lightenToolPayload(v: unknown): unknown {
  if (typeof v !== "object" || v === null) {
    return v;
  }
  const out = { ...(v as EntryToolCall & EntryToolResult) };
  // Styles the output bytes this drops, so it has nothing left to style. Measured as most
  // of the 137,710 bytes the per-call remainder came to over 207 calls.
  delete out.output_spans;
  if (out.output !== undefined && out.output.length > SNAPSHOT_MAX_TOOL_OUTPUT) {
    out.output = out.output.slice(0, SNAPSHOT_MAX_TOOL_OUTPUT);
  }
  if (out.input !== undefined) {
    out.input = lightenInput(out.input);
  }
  return out;
}

/** Truncate a tool input's own string values. TOP LEVEL only: that is where the keys a
 *  card paints live (`command`, `path`), a tool input is a flat record of scalars in
 *  every shape measured, and a nested value that stays large is caught by the record's
 *  byte budget rather than by a deeper walk here. */
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

/** Narrow a persisted record: `unknown` in, every ELEMENT validated through the
 *  generated wire decoders the network path already runs. A rejection is the whole
 *  record, because a half-valid hint is not a hint. */
function decodeSnapshot(v: unknown): BootSnapshot | null {
  try {
    const o = asObject(v, "$.boot_snapshot");
    const win = o["window"];
    return {
      tabs: decodeArray(o["tabs"], decodeTabSubject, "$.boot_snapshot.tabs"),
      chats: decodeArray(o["chats"], decodeSnapshotChat, "$.boot_snapshot.chats"),
      // `null` is the value; ABSENT is a foreign record and rejects like any other
      // missing required field, so the first boot after this shape ships paints
      // nothing and the capture rewrites the record within the debounce. Invariant 5
      // (no migration code), and `DB_VERSION` stays put: the object store is
      // unchanged and only the record's own shape moved.
      window: win === null ? null : decodeSnapshotWindow(win),
    };
  } catch {
    return null;
  }
}

/** STRICT, unlike the window decoder on the network path: one undecodable entry there
 *  costs that entry and the range read fills the gap, while here a dropped entry would
 *  leave a `seq` hole in a hint nothing repairs — this record is not a page and has no
 *  turn to re-read. So a refused entry takes the whole record, which is the rule
 *  `decodeSnapshot` already applies to every other field. */
function decodeSnapshotWindow(v: unknown): SnapshotWindow {
  const o = asObject(v, "$.boot_snapshot.window");
  return {
    chat_id: reqStr(o, "chat_id", "$.boot_snapshot.window"),
    entries: decodeArray(o["entries"], decodeEntry, "$.boot_snapshot.window.entries"),
  };
}

/** The optional twin of `validators.ts` `reqOneOf`, mirroring that file's own `req*`/`opt*`
 *  pairing — anything the vocabulary does not name, a wrong type included, reads as ABSENT
 *  rather than throwing, so a member the union gains later costs this record nothing.
 *
 *  It lives HERE because `validators.ts` is generated output: `go run ./cmd/wire-codegen`
 *  rewrites the whole file, so an addition there is deleted by the next generator run. */
function optOneOf<T extends string>(
  o: Record<string, unknown>,
  key: string,
  vals: readonly T[],
): T | undefined {
  const v = o[key];
  return typeof v === "string" && (vals as readonly string[]).includes(v) ? (v as T) : undefined;
}

/** The same tolerance for the timestamp beside it: anything that is not a finite
 *  number reads as ABSENT rather than throwing.
 *
 *  Its twin is `validators.ts` `optNum`, which REFUSES a wrong type and takes the whole record
 *  with it — right for a wire payload, wrong for this one. The value is spent as epoch millis by
 *  `relativeTime`, so the failure it prevents is an age of NaN on the dot's tooltip. */
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

/** Forget the pending write, the capture effect and the connection — each is
 *  established once per page, so a test driving two boots needs them dropped.
 *
 *  The connection is DROPPED, not closed: a second one is harmless because the
 *  version never changes, and closing would make this reset async. */
export function _resetForTest(): void {
  clearTimeout(pending);
  pending = undefined;
  disposeCapture?.();
  disposeCapture = undefined;
  captureAbort?.abort();
  captureAbort = undefined;
  dbHandle = undefined;
}
