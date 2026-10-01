// HTTP load operations for the session store: hydrates the state store.ts owns.

import { join } from "@cplieger/keyenc";

import type { Session, ChatHeader, TurnState } from "./types.js";
import { apiGetTyped, apiGetTypedOrError } from "./api-client.js";
import { asArray, asObject, decodeArray, optBool, reqBool, type Decoder } from "./validators.js";
import {
  decodeChatHeader,
  decodeEntry,
  decodeOpenEntry,
  decodeSubjectStamp,
} from "./wire/decoders.gen.js";
import { registerCleanup } from "./actions/index.js";
import {
  setSessions,
  derivedHasMore,
  get,
  getSessions,
  bumpMessages,
  upsertHeader,
  republishWindowToolCalls,
  markWindowStale,
  setTurnOpen,
} from "./store.js";
import { clearTurnState } from "./turn-teardown.js";
import { observeStamp } from "./subject-versions.js";
import type { Entry, OpenEntry, SubjectStamp } from "./wire/types.gen.js";

// --- Inline decoders ---
const decodeChatListResponseLocal: Decoder<{ chats?: ChatHeader[]; subject?: SubjectStamp }> = (
  v,
) => {
  const o = asObject(v, "$.chat_list");
  const out: { chats?: ChatHeader[]; subject?: SubjectStamp } = {};
  if (o["chats"] !== undefined) {
    out.chats = decodeArray(o["chats"], decodeChatHeader, "$.chat_list.chats");
  }
  // The `chats` digest stamp, observed once the list is committed below. Optional: a
  // server from before the stamp still answers a usable list.
  if (o["subject"] !== undefined && o["subject"] !== null) {
    out.subject = decodeSubjectStamp(o["subject"]);
  }
  return out;
};

/** Describe a member the decoder refused, best-effort: an entry that did not decode may
 *  not carry the fields this line wants either, so every one of them is optional here. */
function describeMember(m: unknown): string {
  if (typeof m !== "object" || m === null) {
    return "a non-object member";
  }
  const o = m as Record<string, unknown>;
  const kind = typeof o["kind"] === "string" ? o["kind"] : "?";
  const id = typeof o["id"] === "string" ? o["id"] : "?";
  const turn = typeof o["turn"] === "string" ? o["turn"] : "?";
  return `${kind} ${id} of turn ${turn}`;
}

/** Decode a list of entries, DROPPING a member the generated decoder refuses: the `seq` gap
 *  it leaves reaches the same hole check every other gap does, and the warn IS the signal.
 *  The CONTAINER still throws — a reply whose `entries` is not an array is not one bad line. */
function decodeTolerant<T>(v: unknown, one: Decoder<T>, path: string): T[] {
  const out: T[] = [];
  const raw = asArray(v, path);
  for (let i = 0; i < raw.length; i++) {
    try {
      out.push(one(raw[i]));
    } catch (e) {
      console.warn(
        `${path}[${String(i)}]: dropped ${describeMember(raw[i])}:`,
        e instanceof Error ? e.message : String(e),
      );
    }
  }
  return out;
}

/** The stamps a page certifies, or none. Optional-tolerant for the reason the `chats`
 *  stamp is: a server from before the list still answers a usable page. */
function decodeStamps(v: unknown, path: string): SubjectStamp[] {
  if (v === undefined || v === null) {
    return [];
  }
  return decodeArray(v, decodeSubjectStamp, path);
}

/** The single-chat GET of section 6.3: `{chat, entries, open_entries, has_more, live,
 *  subject, draft}`. `entries` is every entry of a window of WHOLE turns in FILE order and
 *  `open_entries` the in-memory tails of the open turns inside it, so the in-flight turn
 *  travels on the same channel as everything else and nothing is pushed on connect. */
const decodeChatGetResponseLocal: Decoder<{
  chat: ChatHeader;
  entries: Entry[];
  open_entries: OpenEntry[];
  has_more: boolean;
  live: boolean | undefined;
  subject: SubjectStamp[];
  draft: string;
}> = (v) => {
  const o = asObject(v, "$.chat_get");
  return {
    chat: decodeChatHeader(o["chat"]),
    entries: decodeTolerant(o["entries"], decodeEntry, "$.chat_get.entries"),
    open_entries: decodeTolerant(o["open_entries"], decodeOpenEntry, "$.chat_get.open_entries"),
    has_more: reqBool(o, "has_more", "$.chat_get"),
    // The chat's OWN turn being open, from the turn registry and never from the log.
    // UNDEFINED rather than false when absent, because the teardown arm below turns on the
    // server having STATED the turn closed and a collapse to false would hand it that
    // statement for an answer that said nothing. No owner marker to fold in any more: a
    // step's turn is in the RUN's log, so no turn in this log is a step's.
    live: optBool(o, "live", "$.chat_get"),
    // One `chat` stamp plus one `live_turn` per open turn in the window, read under the
    // same store lock as the entries and the tails, so each certifies exactly what was
    // served; an older page carries the `chat` stamp alone. Observed after the commit.
    subject: decodeStamps(o["subject"], "$.chat_get.subject"),
    draft: typeof o["draft"] === "string" ? o["draft"] : "",
  };
};

interface TurnRangeResponse {
  entries: Entry[];
  open_entries: OpenEntry[];
  subject: SubjectStamp[];
}

/** The range read of section 6.4: one turn's entries past `after` plus its open tails. */
const decodeTurnRangeResponseLocal: Decoder<TurnRangeResponse> = (v) => {
  const o = asObject(v, "$.turn_range");
  return {
    entries: decodeTolerant(o["entries"], decodeEntry, "$.turn_range.entries"),
    open_entries: decodeTolerant(o["open_entries"], decodeOpenEntry, "$.turn_range.open_entries"),
    subject: decodeStamps(o["subject"], "$.turn_range.subject"),
  };
};

/** The turns a page's entries describe, plus the gaps the walk could not fold in. */
interface PageTurns {
  turns: Map<string, TurnState>;
  /** The page's turn ids in FILE order, which is the order the transcript renders. */
  order: string[];
  /** Turn id to the highest `seq` held before the gap, or undefined for a turn with no
   *  `turn_open`, which asks for the whole turn. */
  holes: Map<string, number | undefined>;
}

/** Build the turn map by walking the page's entries in FILE order: a `turn_open` opens a
 *  `TurnState` and every other entry appends at the `seq` it carries. An entry whose `seq` is
 *  not the next one, or naming a turn this page did not open, is a HOLE rather than an append
 *  at the wrong index. Idempotent by turn id. */
function buildPageTurns(entries: readonly Entry[], open: readonly OpenEntry[]): PageTurns {
  const turns = new Map<string, TurnState>();
  const order: string[] = [];
  const holes = new Map<string, number | undefined>();
  for (const e of entries) {
    if (e.kind === "turn_open") {
      if (!turns.has(e.turn)) {
        turns.set(e.turn, { entries: [e], openEntries: new Map() });
        order.push(e.turn);
      }
      continue;
    }
    const state = turns.get(e.turn);
    if (state === undefined) {
      holes.set(e.turn, undefined);
      continue;
    }
    if (e.seq !== state.entries.length) {
      // The FIRST gap's watermark, kept: a later entry of the same turn sits past it, so
      // its index would ask the repair for less than the turn is actually missing.
      if (!holes.has(e.turn)) {
        holes.set(e.turn, state.entries.length - 1);
      }
      continue;
    }
    state.entries.push(e);
    if (e.kind === "turn_close") {
      // The one cached lookup, the same one `appendEntry` caches: `turnLive` reads it.
      state.closeAt = e.seq;
    }
  }
  seatOpenEntries(turns, holes, open);
  return { turns, order, holes };
}

/** Seat each open tail under its `(turn, lane)`. A tail naming a turn the walk did not
 *  open is the dropped-`turn_open` hole again. */
function seatOpenEntries(
  turns: Map<string, TurnState>,
  holes: Map<string, number | undefined>,
  open: readonly OpenEntry[],
): void {
  for (const o of open) {
    const state = turns.get(o.turn);
    if (state === undefined) {
      holes.set(o.turn, undefined);
      continue;
    }
    const lane = o.lane ?? "";
    state.openEntries.set(lane, { ...o, lane });
  }
}

/** Merge one turn the page describes with the copy the store already holds. The page is a
 *  point-in-time read, so an entry that landed during the flight is NEWER than the answer and
 *  goes back on past its end, contiguously; a held open tail travels the same way unless the
 *  page carries that entry SEALED, which is the answer that the tail is history. */
function mergeTurn(page: TurnState, held: TurnState): TurnState {
  for (let i = page.entries.length; i < held.entries.length; i++) {
    const e = held.entries[i];
    if (e?.seq !== i) {
      break;
    }
    page.entries.push(e);
    if (e.kind === "turn_close") {
      page.closeAt = e.seq;
    }
  }
  if (held.openEntries.size > 0) {
    const sealed = new Set(page.entries.map((e) => e.id));
    for (const [lane, open] of held.openEntries) {
      if (sealed.has(open.id)) {
        continue;
      }
      const fromPage = page.openEntries.get(lane);
      if (fromPage === undefined || (fromPage.id === open.id && open.n > fromPage.n)) {
        page.openEntries.set(lane, open);
      }
    }
  }
  return page;
}

/** Commit a page's turns into the session's window, and answer whether the page speaks for the
 *  window's LEFT EDGE, which decides whose `has_more` is the answer. A newest page keeps the
 *  resident turns older than its own oldest — pages already fetched that the answer says
 *  nothing about — anchored on that turn id rather than on a count, so no overlap replaces.
 *
 *  `heldAtRequest` is the window as it stood when the newest page was asked for. A held turn
 *  past the page's newest is an ARRIVAL only when it was not held then: one held before the
 *  read that the answer neither carries nor precedes is a turn the server no longer serves,
 *  which is what a client that missed a `turn_revert` frame is holding. Not by ordinal: a
 *  reverted turn sits above the surviving count, and a prompt after the revert reuses its n. */
function applyPage(
  session: Session,
  page: PageTurns,
  older: boolean,
  heldAtRequest: ReadonlySet<string>,
): boolean {
  if (older) {
    const fresh = page.order.filter((id) => !session.turns.has(id));
    for (const id of fresh) {
      const state = page.turns.get(id);
      if (state !== undefined) {
        session.turns.set(id, state);
      }
    }
    session.turn_order = [...fresh, ...session.turn_order];
    return true;
  }
  const held = session.turns;
  const oldest = page.order[0];
  const newest = page.order[page.order.length - 1];
  const anchor = oldest === undefined ? -1 : session.turn_order.indexOf(oldest);
  const tail = newest === undefined ? -1 : session.turn_order.indexOf(newest);
  const kept = anchor > 0 ? session.turn_order.slice(0, anchor) : [];
  const arrived =
    tail >= 0
      ? session.turn_order
          .slice(tail + 1)
          .filter((id) => !page.turns.has(id) && !heldAtRequest.has(id))
      : [];
  const turns = new Map<string, TurnState>();
  for (const id of kept) {
    const state = held.get(id);
    if (state !== undefined) {
      turns.set(id, state);
    }
  }
  for (const id of page.order) {
    const state = page.turns.get(id);
    if (state === undefined) {
      continue;
    }
    const prev = held.get(id);
    turns.set(id, prev === undefined ? state : mergeTurn(state, prev));
  }
  for (const id of arrived) {
    const state = held.get(id);
    if (state !== undefined) {
      turns.set(id, state);
    }
  }
  session.turns = turns;
  session.turn_order = [...kept, ...page.order, ...arrived].filter((id) => turns.has(id));
  return kept.length === 0;
}

// --- Abort controllers ---
let listController: AbortController | null = null;
/** The page load in flight per chat, and whether it reads the NEWEST page: a revert aborts
 *  it and re-asks for that one, because the frame proves a read issued now is served after
 *  the record (`abortReadsForRevert`). */
const msgControllers = new Map<string, { controller: AbortController; newest: boolean }>();

/** The load a revert re-issued in place of an aborted newest one, keyed by the aborted
 *  load's controller: its caller is answered with the re-issue's result, because an
 *  activation reads `false` as a failed open. */
const reissuedLoads = new WeakMap<AbortController, Promise<boolean>>();

/** Forget a finished load's entry, but only its own: a load superseded by a newer one wakes
 *  after the newer one registered, and deleting by key alone would orphan the newer read. */
function releaseLoad(chatID: string, controller: AbortController): void {
  if (msgControllers.get(chatID)?.controller === controller) {
    msgControllers.delete(chatID);
  }
}

// Observability of the newest-page refetch, printed on every outcome; nothing branches on it.
const loadOutcomes = { changed: 0, unchanged: 0, load_failed: 0 };

// An open stream has no timing entry until its body ends, so the protocol is read off a
// request that completes.
function readNextHopProtocol(path: string): string {
  const href = new URL(path, document.baseURI).href;
  const entries = performance.getEntriesByName(href, "resource") as PerformanceResourceTiming[];
  const last = entries.at(-1);
  return last !== undefined && last.nextHopProtocol !== "" ? last.nextHopProtocol : "?";
}

function reportLoadOutcome(
  chatID: string,
  outcome: "changed" | "unchanged" | "load_failed",
  path: string,
): void {
  loadOutcomes[outcome]++;
  const line = `chat_get: ${chatID} ${outcome} changed=${loadOutcomes.changed} unchanged=${loadOutcomes.unchanged} load_failed=${loadOutcomes.load_failed} proto=${readNextHopProtocol(path)}`;
  if (outcome === "load_failed") {
    console.warn(line);
  } else {
    console.debug(line);
  }
}

/** Whether `loadList` has ever succeeded. Read through `chatListLoaded`. */
let listLoaded = false;

/** What the last `loadList` attempt established about the SERVER, not about that request.
 *  An abort is a fact about a request only, so the abort path leaves this alone. */
type ListReach = "unknown" | "reachable" | "unreachable";
let listReach: ListReach = "unknown";

/** The ladder behind a list load that reached the network and failed: three attempts,
 *  1s doubling. Bounded because the next SSE `connected` refetches the list anyway, so
 *  what this covers is the window in between — a stream that stayed up while the list
 *  request died, which no other trigger revisits. */
const LIST_RETRY_LIMIT = 3;
const LIST_RETRY_BASE_MS = 1000;

let listRetryTimer: ReturnType<typeof setTimeout> | undefined;
let listRetryAttempts = 0;

/** Forget a ladder in flight, rungs and all: a successful `loadList` has nothing left to
 *  retry, and a new gap arms one of its own rather than stacking beside this. */
function cancelListRetry(): void {
  if (listRetryTimer !== undefined) {
    clearTimeout(listRetryTimer);
    listRetryTimer = undefined;
  }
  listRetryAttempts = 0;
}

/** Arm the next rung, or report the ladder exhausted. Private because it is the
 *  CONTINUATION: it keeps the attempt count that `scheduleListRetry` resets. */
function armListRetry(): void {
  if (listRetryAttempts >= LIST_RETRY_LIMIT) {
    console.warn(
      `[list] gave up after ${String(LIST_RETRY_LIMIT)} retries; the chat list stays as it is until the next reconnect`,
    );
    listRetryAttempts = 0;
    return;
  }
  if (listRetryTimer !== undefined) {
    // A fresh gap's own `loadList` can fail while a rung is already armed: `scheduleListRetry`
    // arms one, and the ABORTED load a rung is waiting on then settles false with no reach
    // verdict written, so its continuation reads the previous `unreachable` and arms again.
    // The newest failure owns the rung — without this the orphaned timer also fires and the
    // ladder fetches twice per rung instead of staying inside its three.
    clearTimeout(listRetryTimer);
  }
  const delay = LIST_RETRY_BASE_MS * 2 ** listRetryAttempts;
  listRetryAttempts++;
  listRetryTimer = setTimeout(() => {
    listRetryTimer = undefined;
    void loadList().then((ok) => {
      // The door's own gate, for the door's own reason: an ABORT also answers false and
      // writes no reach verdict, so it must not extend a ladder.
      if (!ok && listReach === "unreachable") {
        armListRetry();
      }
    });
  }, delay);
}

/** Retry a `loadList` that failed, bounded.
 *
 *  THE REACH GATE LIVES HERE rather than at the call site: `!ok` alone is not evidence
 *  about the server, because an aborted load returns it too and deliberately records no
 *  verdict, so a ladder armed on `!ok` would chase a load a newer one superseded.
 *  `serverMayAnswer` cannot stand in for the read — it folds `listLoaded` in, so it
 *  answers a different question. */
export function scheduleListRetry(): void {
  if (listReach !== "unreachable") {
    return;
  }
  cancelListRetry();
  armListRetry();
}

/** What the SERVER says about a chat id. `gone` is the only value that licenses a terminal
 *  claim; `unresolved` means nobody answered and the caller holds whatever it has. */
export type ChatVerdict = "exists" | "gone" | "unresolved";

/** The single-chat GET reduced to the one field this question needs. Its own
 *  decoder rather than `decodeChatGetResponseLocal`, because that one requires
 *  `entries`, `open_entries`, `has_more` and `subject` — fields a verdict does not
 *  read, and each an extra way for an answer that DID arrive to be discarded as
 *  undecodable. */
const decodeChatConfirmResponseLocal: Decoder<{ chat: ChatHeader }> = (v) => {
  const o = asObject(v, "$.chat_confirm");
  return { chat: decodeChatHeader(o["chat"]) };
};

/** Is this id SHAPED like a chat id? The only 400 this client can explain to itself, so KEEP
 *  IT AT LEAST AS PERMISSIVE as the server's own gate: admitting an id the server refuses
 *  costs one non-terminal `unresolved`, while refusing one it would ACCEPT reads a
 *  request-level 400 as "no such chat". */
function chatIDShaped(id: string): boolean {
  return id !== "" && id.length <= 128 && /^[A-Za-z0-9_-]+$/.test(id);
}

/** Does this status settle the question ABOUT THIS CHAT, rather than about the request? 404 is
 *  the server reading its own store; a 400 counts only for an id this client can see is not a
 *  chat id, because a request-level 400 is not evidence about a conversation and reading one as
 *  "no such chat" is the false-terminal claim this path exists to remove. Measured against the
 *  route as it stands (`chatIDPattern`, `canonicalAPIPath`) every 400 source IS id-shaped, so
 *  the narrowing changes no verdict today; it stops a middleware added later making one. */
function saysTheChatIsGone(status: number, chatID: string): boolean {
  return status === 404 || (status === 400 && !chatIDShaped(chatID));
}

/** Ask the SERVER whether a chat exists, for an id the store holds no row for.
 *
 *  The store's own absence is not proof: a list that landed cleanly goes STALE, so a chat
 *  created on another device — or during an SSE outage — is missing from a store otherwise
 *  entitled to speak, and reading that as gone is the false-terminal claim. So the server
 *  decides: a 2xx adopts the header through `upsertHeader`, the same door the missed
 *  `chat_created` frame would have used, so no second Session-construction rule appears and
 *  the deep link opens. Everything `saysTheChatIsGone` does not settle is `unresolved`. */
export async function confirmChatExists(chatID: string): Promise<ChatVerdict> {
  // `limit=1` is the cheapest page the endpoint will serve — it honours 1..200 inclusive
  // and answers anything else with its own 20-turn default, so 1 is served rather than
  // widened — and the transcript is not what is being asked about. This is the ONE read
  // that names a limit: the window read below sends none, because the server owns that
  // number and this probe wants the smallest page rather than a good one. No abort
  // controller: two confirmations for one id are an idempotent read plus an idempotent
  // upsert, and the CALLER owns whether a late answer still matters to what is on screen.
  const r = await apiGetTypedOrError(
    `/api/chats/${encodeURIComponent(chatID)}?limit=1`,
    decodeChatConfirmResponseLocal,
  );
  if (r.ok && r.data !== null) {
    upsertHeader(r.data.chat);
    return "exists";
  }
  if (saysTheChatIsGone(r.status, chatID)) {
    return "gone";
  }
  console.warn("chat confirm: no answer", chatID, r.status, r.error);
  return "unresolved";
}

/** Whether the chat list has been read successfully at least once. An empty store has two
 *  meanings that want opposite answers: the server said there are no such chats, or the
 *  server could not be reached. Without this, a reload of any `/chat/<id>` against a
 *  restarting server rewrote the URL and claimed the conversation no longer exists, seconds
 *  after toasting that the chats could not be loaded — a terminal verdict derived from absent
 *  data. LATCHED rather than a snapshot of the last attempt: a later failed refetch does not
 *  un-know rows the store already holds, and `loadList` runs on every SSE `connected`, so a
 *  client whose boot fetch failed self-heals at its first reconnect. */
export function chatListLoaded(): boolean {
  return listLoaded;
}

/** Can asking the server about ONE chat id plausibly be answered? The gate on the
 *  confirmation round trip, narrowed to the only thing that makes asking pointless:
 *  EVIDENCE the server cannot answer. A list that landed, a list that was ABORTED (routine —
 *  see `loadList`'s first line) and a page that has not tried yet all answer true; only a
 *  load that reached the network and failed answers false. `listLoaded` is read too and is
 *  not redundant: it is LATCHED where the reach is not, so rows in the store outlive a server
 *  that has since gone down. One fold is conservative — `apiGetTyped` collapses an
 *  undecodable BODY onto a dead network, so such a list reads unreachable. */
export function serverMayAnswer(): boolean {
  return listLoaded || listReach !== "unreachable";
}

registerCleanup(() => {
  listController?.abort();
  cancelListRetry();
});
registerCleanup(() => {
  for (const c of msgControllers.values()) {
    c.controller.abort();
  }
  msgControllers.clear();
});

// --- Load operations ---
/** `signal`, when given, is the revalidation's: it cancels this read beside the loader's
 *  own supersede-the-previous controller. */
export async function loadList(signal?: AbortSignal): Promise<boolean> {
  listController?.abort();
  const controller = new AbortController();
  listController = controller;
  const combined =
    signal === undefined ? controller.signal : AbortSignal.any([controller.signal, signal]);

  const sessionIndex = new Map<string, Session>();
  for (const s of getSessions()) {
    sessionIndex.set(s.id, s);
  }
  const knownBefore = new Set(sessionIndex.keys());

  const d = await apiGetTyped("/api/chats", decodeChatListResponseLocal, combined);
  if (combined.aborted) {
    // `listReach` is deliberately NOT written here. This request was superseded —
    // by a `connected` refetch, or by the page unloading — and it never learned
    // anything about the server, so recording a verdict would put the abort's own
    // false return in front of every later reader. That is the conflation
    // `serverMayAnswer` exists to undo.
    listController = null;
    return false;
  }
  if (d?.chats === undefined) {
    // The request DID resolve and produced no usable list, so the server is the
    // best available explanation. See `serverMayAnswer` for the one case this
    // over-attributes (an undecodable body) and why the fold is safe.
    listReach = "unreachable";
    listController = null;
    return false;
  }
  const next: Session[] = [];
  for (const h of d.chats) {
    const existing = get(h.id);
    const session: Session = {
      id: h.id,
      name: h.name,
      model: h.model ?? "",
      acp_session_id: h.acp_session_id ?? "",
      current_mode_id: h.current_mode_id ?? "",
      supervised_mode: h.supervised_mode ?? false,
      effort: h.effort ?? "",
      // Keep the client's live effort catalog when the header carries none: this
      // list endpoint rebuilds a Session from a header, and blanking the tiers
      // would empty the effort control for every chat on a refresh.
      effort_levels: h.effort_levels ?? existing?.effort_levels ?? [],
      effort_active: h.effort_active ?? existing?.effort_active ?? "",
      usage: h.usage,
      turn_count: h.turn_count,
      // The resident window travels as one value: the map and the order it renders in
      // describe the same turns, so carrying one without the other is a window that
      // renders nothing or renders it twice.
      turns: existing?.turns ?? new Map<string, TurnState>(),
      turn_order: existing?.turn_order ?? [],
      // A header carries no window, so this is the DERIVATION and never an answer — one
      // rule, `store.ts` `derivedHasMore`, over the count the server just sent and whatever
      // window is carried over above. Never OR'd with the previous value: a sticky true can
      // only be wrong in the direction of a Load-older button with nothing behind it, and
      // this runs on boot, on login and on every `connected` handshake.
      has_more: derivedHasMore(h.turn_count, existing?.turn_order.length ?? 0),
      thinking: existing?.thinking ?? false,
      working_label: existing?.working_label ?? "Thinking",
      ...(existing?.steers !== undefined && { steers: existing.steers }),
      // Every OTHER client-only projection is a pure carry-over: the server sends none of them,
      // so rebuilding a Session from a header alone would reset them — and `boot.ts`
      // `onTransportStatus` calls this for more connections than a reconnect, so an ordinary
      // network recovery reaches this rebuild. The reconcile that IS entitled to drop them is
      // `BUS_RECONCILE`, which clears them explicitly and runs first.
      ...(existing?.agent_status !== undefined && { agent_status: existing.agent_status }),
      // Residency describes the carried-over window of turns, so it travels with it:
      // dropping it here would make every reconnect read a loaded chat as never-loaded
      // (or an evicted one as fresh). A card's ordinal rides its own `turn_open.n`, so
      // there is no window base to carry beside it any more.
      ...(existing?.residency !== undefined && { residency: existing.residency }),
      ...(h.compaction_watermark !== undefined && { compaction_watermark: h.compaction_watermark }),
      // The model badge's ONE input, replaced by every header read: an absent
      // `pending_model` is a CLEAR, which is how a pick applied at a turn's close reaches
      // every device.
      ...(h.pending_model !== undefined && { pending_model: h.pending_model }),
      // The two SERVER facts the row used to drop on the floor. The row is rebuilt from the
      // header rather than spread from `existing`, so a conditional spread IS a replace here:
      // nothing carries over because nothing is there. `updated_at` needs no guard — it is
      // required on the wire.
      ...(h.last_turn_outcome !== undefined && { last_turn_outcome: h.last_turn_outcome }),
      updated_at: h.updated_at,
    };
    next.push(session);
  }
  // Preserve sessions added by SSE (upsertHeader) during the await — but NOT the
  // boot snapshot's provisional rows, which satisfy the same "unknown before,
  // unnamed by the server" test and mean the opposite thing: a hint for a chat the
  // server no longer holds, which would otherwise outlive the answer that omitted
  // it. See types.ts `Session.provisional`.
  const currentSessions = getSessions();
  const currentIndex = new Map(currentSessions.map((s) => [s.id, s]));
  const nextIds = new Set(next.map((s) => s.id));
  for (const [id, s] of currentIndex) {
    if (!knownBefore.has(id) && !nextIds.has(id) && s.provisional !== true) {
      next.push(s);
    }
  }
  setSessions(next);
  listController = null;
  // Latched HERE and nowhere else: this is the one point at which the store holds a
  // row for every chat the server named, which is exactly the claim
  // `chatListLoaded` makes. Every earlier return above is an abort or a failed
  // decode and leaves it as it was.
  listLoaded = true;
  // Not latched, unlike the line above: this one describes the LAST attempt, so a
  // later failure is entitled to overwrite it.
  listReach = "reachable";
  // The list landed, so any ladder still climbing toward it is answered.
  cancelListRetry();
  // AFTER the commit: the version certifies the list the store now holds.
  observeStamp(d.subject);
  return true;
}

export async function loadMessages(
  chatID: string,
  beforeTurnID?: string,
  signal?: AbortSignal,
): Promise<boolean> {
  msgControllers.get(chatID)?.controller.abort();
  const controller = new AbortController();
  msgControllers.set(chatID, { controller, newest: beforeTurnID === undefined });
  const combined =
    signal === undefined ? controller.signal : AbortSignal.any([controller.signal, signal]);
  // No `limit`: a window is N TURNS and the count is the server's decision (it clamps
  // and defaults its own), so naming one here would be this client asserting a number it
  // has no basis for. `?before=<turn_id>` pages by turn, and whole turns only, so a turn
  // is never split across two pages.
  const params = new URLSearchParams();
  if (beforeTurnID !== undefined) {
    params.set("before", beforeTurnID);
  }
  const query = params.toString();
  const path = `/api/chats/${encodeURIComponent(chatID)}${query === "" ? "" : `?${query}`}`;
  const heldAtRequest = new Set(beforeTurnID === undefined ? (get(chatID)?.turn_order ?? []) : []);
  const d = await apiGetTyped(path, decodeChatGetResponseLocal, combined);
  if (combined.aborted) {
    releaseLoad(chatID, controller);
    return reissuedLoads.get(controller) ?? false;
  }
  if (d === null) {
    releaseLoad(chatID, controller);
    if (beforeTurnID === undefined) {
      const failed = get(chatID);
      if (failed !== undefined) {
        failed.residency = "load_failed";
      }
      reportLoadOutcome(chatID, "load_failed", path);
    }
    return false;
  }
  const session = get(chatID);
  if (session === undefined) {
    releaseLoad(chatID, controller);
    return false;
  }
  const page = buildPageTurns(d.entries, d.open_entries);
  // The byte comparison is the refetch-outcome measurement, bounded by the resident window.
  const heldBefore = beforeTurnID === undefined ? snapshotWindow(session) : "";
  const pageStartsWindow = applyPage(session, page, beforeTurnID !== undefined, heldAtRequest);
  if (beforeTurnID === undefined) {
    reportLoadOutcome(
      chatID,
      snapshotWindow(session) === heldBefore ? "unchanged" : "changed",
      path,
    );
    // A card already on screen has one refresh channel, the per-call signal, and the repaint
    // below writes none — so the page's own tool calls go through that channel. Only the turns
    // the PAGE landed: a prepended older page mounts its cards fresh.
    republishWindowToolCalls(chatID, page.order);
  }
  // Before the read below, so it sees the server's own count.
  session.turn_count = d.chat.turn_count;
  if (pageStartsWindow) {
    session.has_more = d.has_more;
  } else {
    // The page said nothing about this window's left edge, so `has_more` falls back to the
    // derivation rather than preserving the previous value: preserving is only right when
    // that value was an ANSWER, and for a header-built row it is the guess, which left a
    // button on a chat holding every turn it has.
    session.has_more = derivedHasMore(session.turn_count, session.turn_order.length);
  }
  releaseLoad(chatID, controller);
  // Park the server's draft on the session so the composer can adopt it. Only on the newest
  // page: an older page fetch is a scroll-up, not an open. This module deliberately does not
  // reach into the composer — chat.ts owns that call, right where it already sequences the
  // rest of the activation.
  if (beforeTurnID === undefined) {
    session.draft = d.draft;
    // The server's liveness statement, newest page ONLY, for the draft's reason. RECORDED
    // in both directions, and FORGOTTEN when the answer carries no statement, because a
    // `true` left standing would keep `turnLive` answering live off an answer nothing
    // restates.
    if (d.live === undefined) {
      delete session.turn_open;
    } else {
      setTurnOpen(chatID, d.live);
    }
    // A successful newest-page load is the ONE writer of `loaded`: the window is now the
    // server's answer, so an activation may trust it. An older-page prepend extends an
    // already-trusted window and asserts nothing new.
    session.residency = "loaded";
  }
  // Every gap the walk found is MARKED and then asks for the range read of section 6.4 for
  // that turn alone, BEFORE the repaint: the store's own order, so the window is stale in
  // the same pass that renders what did arrive. The mark is what the repair's own restore
  // arm reads, and what stops the next activation trusting a window with a known gap — a
  // repair that gets no answer must leave a refetch trigger behind.
  if (page.holes.size > 0) {
    markWindowStale(chatID);
    for (const [turnID, afterSeq] of page.holes) {
      requestTurnRange(chatID, turnID, afterSeq);
    }
  }
  // `load`, not `shape`: both branches above REPLACED or EXTENDED the window with the
  // server's own answer, so its rows are a replay and the paint must not read them as
  // entries that arrived here (messages.ts `appendNewIds`). The array cannot say so on its
  // own — a cold open paints before this fetch resolves, so the paint it drives is not a
  // chat switch and its predecessor recorded no tail to append past.
  bumpMessages(chatID, "load");
  if (beforeTurnID === undefined) {
    // AFTER the commit: every stamp in the list certifies exactly the entries and tails
    // this page served, so a client that saw no frame afterwards can still say what it
    // missed. Newest page only, like the three writes above; an older page's stamp
    // describes an edge this load did not adopt.
    for (const stamp of d.subject) {
      observeStamp(stamp);
    }
    // A stated `live === false` covers the chat's WHOLE liveness, so it retracts a `thinking`
    // this client is holding for a turn that is over. AFTER `bumpMessages`, so the repaint and
    // the dot read one settled window; nothing to do on any other answer, because the dot is
    // derived from the log this page just served.
    if (d.live === false) {
      clearTurnState(chatID);
    }
  }
  return true;
}

/** The resident window as bytes, for the refetch-outcome measurement alone. Turn ids plus
 *  each turn's entry ids and open tails: enough to tell a page that changed nothing from one
 *  that did, without serializing every payload the window holds. */
function snapshotWindow(session: Session): string {
  const parts: string[] = [];
  for (const id of session.turn_order) {
    const state = session.turns.get(id);
    if (state === undefined) {
      continue;
    }
    const open = [...state.openEntries.values()].map((o) => `${o.id}@${String(o.n)}`);
    parts.push(`${id}:${state.entries.map((e) => e.id).join(",")}|${open.join(",")}`);
  }
  return parts.join(";");
}

// --- The range read of section 6.4 ---

/** The repair reads in flight, one per `(chat, turn)`: a burst of holes on one turn asks
 *  once, because the answer covers every gap that arrived while it was out. */
const turnRepairs = new Map<string, { chatID: string; controller: AbortController }>();

registerCleanup(() => {
  for (const r of turnRepairs.values()) {
    r.controller.abort();
  }
  turnRepairs.clear();
});

/** Read one turn's entries past `afterSeq` plus its open tails, and apply them.
 *
 *  The ONE repair for every gap: a `seq` hole, a delta or seal whose count does not match, a
 *  `live_turn` digest mismatch, or a frame naming an unknown turn. `after` omitted returns the
 *  whole turn, which is what a lost `turn_opened` asks for; the answer to a lost frame is to
 *  re-read the range, never to pad or re-index. Registered through `registerTurnRepair`, so the
 *  store owns the DETECTION and this module owns every read. */
export function requestTurnRange(chatID: string, turnID: string, afterSeq?: number): void {
  void runTurnRange(chatID, turnID, afterSeq);
}

/** Abort every read a revert could be answered with. The range reads for the turns it just
 *  dropped, because `applyTurnRange` would seat one again, and the chat's PAGE load whatever
 *  turns it took: a page read before the record carries the reverted turns, possibly ones
 *  this client never held, and its header carries the pre-revert count. Registered through
 *  `registerRevertReadAbort`. An aborted newest page is asked for again, since the frame that
 *  got here proves the record is on disk. The seats keep their own refusals, which is what
 *  covers an answer this abort was too late for. */
export function abortReadsForRevert(chatID: string, turnIDs: readonly string[]): void {
  const load = msgControllers.get(chatID);
  if (load !== undefined) {
    load.controller.abort();
    msgControllers.delete(chatID);
    if (load.newest) {
      reissuedLoads.set(load.controller, loadMessages(chatID));
    }
  }
  for (const turnID of turnIDs) {
    const key = join(chatID, turnID);
    const r = turnRepairs.get(key);
    if (r === undefined) {
      continue;
    }
    r.controller.abort();
    turnRepairs.delete(key);
  }
}

async function runTurnRange(chatID: string, turnID: string, afterSeq?: number): Promise<void> {
  const key = join(chatID, turnID);
  if (turnRepairs.has(key)) {
    return;
  }
  const controller = new AbortController();
  turnRepairs.set(key, { chatID, controller });
  const params = afterSeq === undefined ? "" : `?after=${String(afterSeq)}`;
  const path = `/api/chats/${encodeURIComponent(chatID)}/turns/${encodeURIComponent(turnID)}${params}`;
  let d: TurnRangeResponse | null;
  // Released the moment it settles, before the seat: a read still counting itself keeps
  // `repairsPending` true so the window never returns to `loaded`, and the in-flight guard
  // above would swallow the second-gap re-ask the seat schedules.
  try {
    d = await apiGetTyped(path, decodeTurnRangeResponseLocal, controller.signal);
  } finally {
    turnRepairs.delete(key);
  }
  if (controller.signal.aborted) {
    return;
  }
  if (d === null) {
    // The window stays `partial`, so the next activation refetches it: a repair that got
    // no answer must not hand the store a window it will trust.
    console.warn(`turn range: no answer for ${chatID} ${turnID}`);
    return;
  }
  applyTurnRange(chatID, turnID, d.entries, d.open_entries);
  for (const stamp of d.subject) {
    observeStamp(stamp);
  }
}

/** Seat a repaired turn's entries at the positions their `seq` claims. A held turn takes them
 *  from its own end, contiguously, and stops at the first `seq` that is not next, which is a
 *  second gap and re-asks the read. A turn the store does not hold is a whole-turn answer,
 *  seated by its session-absolute `turn_open.n`. */
function applyTurnRange(
  chatID: string,
  turnID: string,
  entries: readonly Entry[],
  open: readonly OpenEntry[],
): void {
  const session = get(chatID);
  if (session === undefined) {
    return;
  }
  if (session.reverted?.has(turnID) === true) {
    // The turn went with a revert's window, so this answer describes material no reader
    // may see and `insertTurnByOrdinal` below would put it back. The abort is the primary
    // mechanism; this covers a read it was too late for, which is why the refusal is the
    // REVERTED set rather than "absent from `turn_order`" — that is also every turn whose
    // own `turn_opened` was lost, and re-reading those is what this repair exists for.
    return;
  }
  const held = session.turns.get(turnID);
  const state = held ?? { entries: [], openEntries: new Map<string, OpenEntry>() };
  let applied = 0;
  for (const e of entries) {
    if (e.seq !== state.entries.length) {
      break;
    }
    state.entries.push(e);
    applied++;
    if (e.kind === "turn_close") {
      state.closeAt = e.seq;
    }
  }
  if (state.entries.length === 0) {
    // Nothing to seat and nothing held: the turn's own `turn_open` did not arrive, so there
    // is no turn to render and asking again would ask the same question.
    console.warn(`turn range: ${chatID} ${turnID} answered no turn_open`);
    return;
  }
  state.openEntries = new Map();
  seatOpenEntries(new Map([[turnID, state]]), new Map(), open);
  if (held === undefined) {
    insertTurnByOrdinal(session, turnID, state);
  }
  if (applied < entries.length) {
    // A second gap, re-asked only when this seat made PROGRESS: an answer that fitted
    // nothing is asked the same question forever, which a permanently undecodable entry
    // serves, so that case leaves the window `partial` for the next activation instead.
    console.warn(
      `turn range: ${chatID} ${turnID} left a gap at seq ${String(state.entries.length)}`,
    );
    if (applied > 0) {
      requestTurnRange(chatID, turnID, state.entries.length - 1);
    }
  } else if (session.residency === "partial" && !repairsPending(chatID)) {
    // The window is the server's answer again: every repair this chat asked for has landed.
    session.residency = "loaded";
  }
  bumpMessages(chatID, "load");
}

/** Whether this chat still has a repair out. Read after one lands, so `residency` goes back
 *  to `loaded` only when nothing is missing rather than after whichever answer arrives last. */
function repairsPending(chatID: string): boolean {
  for (const r of turnRepairs.values()) {
    if (r.chatID === chatID) {
      return true;
    }
  }
  return false;
}

/** Seat a turn the store did not hold at the position its `turn_open.n` claims among the
 *  resident turns. The ordinal is session-absolute in every window, so it orders a repaired
 *  turn against the ones already held without a second projection; a turn whose ordinal
 *  cannot be read goes at the end, where a newer turn belongs. */
function insertTurnByOrdinal(session: Session, turnID: string, state: TurnState): void {
  session.turns.set(turnID, state);
  const n = turnOrdinal(state);
  const at = session.turn_order.findIndex((id) => {
    const other = session.turns.get(id);
    return other !== undefined && turnOrdinal(other) > n;
  });
  if (at < 0) {
    session.turn_order.push(turnID);
    return;
  }
  session.turn_order.splice(at, 0, turnID);
}

function turnOrdinal(state: TurnState): number {
  const open = state.entries[0];
  if (open?.kind !== "turn_open") {
    return Number.MAX_SAFE_INTEGER;
  }
  const payload = open.payload as { n?: unknown };
  return typeof payload.n === "number" ? payload.n : Number.MAX_SAFE_INTEGER;
}
