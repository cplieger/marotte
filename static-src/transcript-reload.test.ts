// A chat loaded by GET with one turn open, no frame after it, and one entry sealed on the
// server while the client was away: the page's own stamps are what let the digest say what
// was missed, and one range read for that turn is the repair.

// Reload recovery, chat half: the stamps the page records, the ONE read a mismatch earns, an older
// page with neither stamp, and a read restoring the undropped fold. The oracle is the fixture
// server's log, and `?after=` is unpinned so the claim is the FOLD.

// The run-pane half is `sse-adapter.ts`'s `run_turn`, pinned in `sse-adapter.test.ts`; its reload
// shape (a pane holding the step GET's stamp) is not covered yet. The digest ANSWER derives from the
// client's request, so the mismatch comes out of the version arithmetic.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

import type * as ApiClient from "./api-client.js";
import type { Entry, OpenEntry, SubjectStamp } from "./wire/types.gen.js";
import type { ChatHeader, Session } from "./types.js";

const api = vi.hoisted(() => ({ getTyped: vi.fn() }));

vi.mock("./api-client.js", async (importOriginal) => {
  const orig = await importOriginal<typeof ApiClient>();
  return { ...orig, apiGetTyped: api.getTyped };
});

// Nothing else is mocked: the store, the loader, the map and the adapter ARE the subject, and
// the reads for subjects this arm never names are never called.
import type { RevalidateContext } from "@cplieger/sse";

import { setSessions, setActive, get, defaultUsage, registerTurnRepair } from "./store.js";
import { loadMessages, requestTurnRange } from "./store-load.js";
import { _revalidateForTest, _resetForTest as resetAdapter, markHydrated } from "./sse-adapter.js";
import { hasSubject, versionMap, _resetForTest as resetVersions } from "./subject-versions.js";
import { clearAllEntrySigs } from "./store-signals.js";
import { createScriptedFetch, json, type ScriptedFetch } from "./__test-helpers__/sse-fetch.js";

const CHAT = "c";
const TURN = "t1";
const EPOCH = "0123456789abcdef";

function entry(seq: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${TURN}-e${String(seq)}`, turn: TURN, kind, seq, ts: seq + 1, payload };
}

/** The server's log for the turn, and the tail it has not sealed yet. Both mutable: the
 *  gap is one seal, which moves the tail into the log and the turn's version with it. */
let log: Entry[] = [];
let tail: OpenEntry | undefined;

function serverLog(): void {
  log = [
    entry(0, "turn_open", { source: "prompt", n: 1 }),
    entry(1, "text", { text: "one" }),
    entry(2, "thinking", { text: "hmm" }),
    entry(3, "text", { text: "two" }),
  ];
  tail = { turn: TURN, id: `${TURN}-open4`, kind: "text", text: "thr", n: 1 };
}

/** Seal the open tail as the turn's next entry: the one thing that happens while the client
 *  holds a window and receives no frame. The turn stays OPEN. */
function sealDuringGap(): void {
  log = [...log, entry(4, "text", { text: "three" })];
  tail = undefined;
}

/** A `live_turn` stamp's version is `<turn>:<seq>` at the turn's newest sealed `seq`, the
 *  digest arm's own spelling. */
function turnVersion(): string {
  return `${TURN}:${String((log.at(-1) as Entry).seq)}`;
}

function header(chatID: string): ChatHeader {
  return {
    id: chatID,
    name: chatID,
    usage: defaultUsage(),
    created_at: 1,
    updated_at: 2,
    turn_count: 1,
  };
}

/** The window's own version: its turn SET, which a seal inside an open turn does not move.
 *  That is the split the two stamps exist for -- a turn that CLOSED while the client was away
 *  is the `chat` stamp's business and earns a window refetch. */
const WINDOW_VERSION = "w1";

/** The stamps a newest page certifies: one `chat`, plus one `live_turn` per open turn in the
 *  window. A REST stamp carries the epoch, which is what binds an unbound map. */
function pageStamps(chatID: string): SubjectStamp[] {
  return [
    { kind: "chat", ref: chatID, version: WINDOW_VERSION, epoch: EPOCH },
    { kind: "live_turn", ref: TURN, version: turnVersion(), epoch: EPOCH },
  ];
}

/** The two reads this arm drives, answered from the one log so neither can disagree with the
 *  other about what the server holds. */
function serve(path: string): unknown {
  const range = path.indexOf(`/turns/${TURN}`);
  if (range !== -1) {
    const cut = path.indexOf("?after=");
    const after = cut === -1 ? -1 : Number(path.slice(cut + "?after=".length));
    return {
      entries: log.filter((e) => e.seq > after),
      open_entries: tail === undefined ? [] : [tail],
      subject: [{ kind: "live_turn", ref: TURN, version: turnVersion(), epoch: EPOCH }],
    };
  }
  const chatID = path.slice("/api/chats/".length).split("?")[0] ?? "";
  return {
    chat: header(chatID),
    entries: log,
    open_entries: tail === undefined ? [] : [tail],
    has_more: false,
    live: true,
    subject: pageStamps(chatID),
    draft: "",
  };
}

function session(chatID: string): Session {
  return {
    id: chatID,
    name: chatID,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: defaultUsage(),
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** What the two folds are compared on: the entries at the positions their `seq` claims, the
 *  open tails, and where the closer landed. */
function heldFold(): unknown {
  const state = get(CHAT)?.turns.get(TURN);
  return {
    entries: state?.entries,
    open: [...(state?.openEntries.values() ?? [])],
    closeAt: state?.closeAt,
  };
}

/** The same fold read off the fixture server: the log, its unsealed tail, and no closer while
 *  the turn is open. */
function serverFold(): unknown {
  return { entries: log, open: tail === undefined ? [] : [tail], closeAt: undefined };
}

function rangeReads(): string[] {
  return api.getTyped.mock.calls
    .map((c) => String(c[0]))
    .filter((p) => p.includes(`/turns/${TURN}`));
}

interface Held {
  readonly kind: string;
  readonly ref: string;
  readonly version: string;
}

let scripted: ScriptedFetch;
/** Every digest exchange: what the client asked about, and what the server answered. */
let digests: { held: Held[]; changed: Held[] }[];

function ctx(over: Partial<RevalidateContext> = {}): RevalidateContext {
  return {
    cause: "visible",
    epoch: versionMap().epoch(),
    generation: 1,
    full: false,
    signal: new AbortController().signal,
    ...over,
  };
}

beforeEach(() => {
  resetVersions();
  resetAdapter();
  clearAllEntrySigs();
  serverLog();
  api.getTyped.mockReset();
  api.getTyped.mockImplementation((path: string) => Promise.resolve(serve(path)));
  registerTurnRepair(requestTurnRange);
  digests = [];
  scripted = createScriptedFetch();
  // The real server compares each held version against the log and names what moved; the
  // answer is therefore a reading of the request rather than a fixture.
  scripted.respond("/api/sync", (req) => {
    const body = JSON.parse(req.body ?? "{}") as { subjects?: Held[] };
    const held = body.subjects ?? [];
    const current = new Map<string, string>([
      [`chat\0${CHAT}`, WINDOW_VERSION],
      [`live_turn\0${TURN}`, turnVersion()],
    ]);
    // Answered at the version the server holds NOW, which is what a client compares its own
    // held version against.
    const changed = held
      .filter((h) => current.get(`${h.kind}\0${h.ref}`) !== h.version)
      .map((h) => ({
        kind: h.kind,
        ref: h.ref,
        version: current.get(`${h.kind}\0${h.ref}`) ?? "",
      }));
    digests.push({ held, changed });
    return json({
      epoch: EPOCH,
      head: "9",
      must_refetch: false,
      checked: held.length,
      changed,
      removed: [],
    });
  });
  vi.stubGlobal("fetch", scripted.fetch);
  setSessions([session(CHAT)]);
  setActive(CHAT);
  markHydrated();
});

afterEach(() => {
  registerTurnRepair(() => undefined);
  resetAdapter();
  setSessions([]);
});

describe("a reload with one entry sealed in the gap", () => {
  it("records the window GET's own two stamps and asks the digest about both", async () => {
    expect(await loadMessages(CHAT)).toBe(true);

    expect(hasSubject("chat", CHAT)).toBe(true);
    expect(hasSubject("live_turn", TURN)).toBe(true);
    expect(
      get(CHAT)
        ?.turns.get(TURN)
        ?.entries.map((e) => e.seq),
    ).toEqual([0, 1, 2, 3]);
    expect(get(CHAT)?.turns.get(TURN)?.openEntries.size).toBe(1);

    await _revalidateForTest(ctx());

    // The versions asked about are the ones the page certified, never the log's current ones.
    expect(digests).toHaveLength(1);
    expect(digests[0]?.held).toEqual([
      { kind: "chat", ref: CHAT, version: WINDOW_VERSION },
      { kind: "live_turn", ref: TURN, version: `${TURN}:3` },
    ]);
    expect(digests[0]?.changed).toEqual([]);
    expect(rangeReads()).toEqual([]);
  });

  it("names the live_turn mismatch alone and repairs that turn with ONE range read", async () => {
    expect(await loadMessages(CHAT)).toBe(true);
    sealDuringGap();

    await _revalidateForTest(ctx());

    // The turn's stamp alone, because the window's turn set did not move.
    expect(digests[0]?.changed).toEqual([{ kind: "live_turn", ref: TURN, version: `${TURN}:4` }]);
    // The TURN it addresses is the claim; the query is not (hand-off 2).
    expect(rangeReads()).toHaveLength(1);
    expect(rangeReads()[0]?.startsWith(`/api/chats/${CHAT}/turns/${TURN}`)).toBe(true);
  });

  it("restores a state equal to the undropped fold", async () => {
    expect(await loadMessages(CHAT)).toBe(true);
    sealDuringGap();

    await _revalidateForTest(ctx());

    // The oracle is what a client that lost no frame holds: the server's own entries for the
    // turn, its open tail, and no closer, because the turn is still open. Equality is the
    // claim rather than length, so an answer seated at the wrong position fails here.
    await vi.waitFor(() => {
      expect(heldFold()).toEqual(serverFold());
    });
    // Asserted AFTER the answer landed as well: a seat that refused the answer's first entry
    // asks again, and the count is the one place that second read shows.
    expect(rangeReads()).toHaveLength(1);
  });

  it("records nothing for an OLDER page, so the digest is not asked about an edge it served", async () => {
    expect(await loadMessages(CHAT, "t0")).toBe(true);

    // An older page extended the window rather than adopting it, so its stamps certify no
    // edge this client can be asked about.
    expect(hasSubject("chat", CHAT)).toBe(false);
    expect(hasSubject("live_turn", TURN)).toBe(false);

    await _revalidateForTest(ctx());
    expect(digests).toEqual([]);
  });
});
