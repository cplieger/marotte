// The SSE adapter's SEAM: which frames reach the bus and when their stamps are observed, what the
// `revalidate` body does with a digest answer, and which reads it drives with the run's signal.

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { RevalidateContext, TabRevalidateContext, WorkerHost } from "@cplieger/sse";

import { BUS_PAGE_RESUMED, BUS_RECONCILE, onBus } from "./bus.js";
import {
  adoptPushSubscription,
  init,
  markHydrated,
  presentedTag,
  setPageScope,
  _resetForTest,
  _revalidateForTest,
  _spawnWorkerForTest,
  _tabRevalidateForTest,
} from "./sse-adapter.js";
import { scopeReady, setRunContentScope } from "./run-content-scope.js";
import { PROFILE_TAG_KEY } from "./sse-tag.js";
import { createSSEHost } from "./sse-worker-host.js";
import { setSessions } from "./store.js";
import {
  hasSubject,
  observeStamp,
  versionMap,
  _resetForTest as resetVersions,
} from "./subject-versions.js";
import type { ServerEvent, Session } from "./types.js";
import { makeServerEvent } from "./__test-helpers__/model.js";
import {
  EPOCH_A,
  EPOCH_B,
  createScriptedFetch,
  json,
  type ScriptedFetch,
} from "./__test-helpers__/sse-fetch.js";
import { until } from "./__test-helpers__/until.js";

const {
  mockLoadList,
  mockLoadMessages,
  mockRequestTurnRange,
  mockListTabs,
  mockRefetchLiveRuns,
  mockRereadHeldRuns,
  mockRunTurnHeldSeq,
  mockRequestRunTurnRange,
  mockFetchCatalog,
  mockInventoryHeld,
  mockRefreshPRs,
} = vi.hoisted(() => ({
  mockLoadList: vi.fn((_signal?: AbortSignal) => Promise.resolve(true)),
  mockLoadMessages: vi.fn((_id: string, _before?: string, _signal?: AbortSignal) =>
    Promise.resolve(true),
  ),
  mockRequestTurnRange: vi.fn((_chatID: string, _turnID: string) => undefined),
  mockListTabs: vi.fn((_signal?: AbortSignal) => Promise.resolve(true)),
  mockRefetchLiveRuns: vi.fn((_signal?: AbortSignal) => Promise.resolve()),
  mockRereadHeldRuns: vi.fn(),
  mockRunTurnHeldSeq: vi.fn((_id: string, _turn: string): number | undefined => undefined),
  mockRequestRunTurnRange: vi.fn((_id: string, _turn: string, _after?: number) => undefined),
  mockFetchCatalog: vi.fn((_opts?: { signal?: AbortSignal }) => Promise.resolve()),
  mockInventoryHeld: vi.fn(() => false),
  mockRefreshPRs: vi.fn((_signal?: AbortSignal) => Promise.resolve()),
}));
vi.mock("./store-load.js", () => ({
  loadList: mockLoadList,
  loadMessages: mockLoadMessages,
  requestTurnRange: mockRequestTurnRange,
}));
vi.mock("./tabs-sync.js", () => ({ listTabs: mockListTabs }));
vi.mock("./run-store.js", () => ({
  refetchLiveRuns: mockRefetchLiveRuns,
  rereadHeldRuns: mockRereadHeldRuns,
  runTurnHeldSeq: mockRunTurnHeldSeq,
}));
vi.mock("./run-turn-range.js", () => ({ requestRunTurnRange: mockRequestRunTurnRange }));
vi.mock("./session-catalog.js", () => ({ fetchCatalog: mockFetchCatalog }));
vi.mock("./git-prs-state.js", () => ({ inventoryHeld: mockInventoryHeld }));
vi.mock("./git-prs-tab.js", () => ({ refreshPRs: mockRefreshPRs }));
vi.mock("./send-state.js", () => ({ setSSEStatus: vi.fn() }));
vi.mock("./actions/index.js", () => ({ registerCleanup: vi.fn() }));

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...over,
  };
}

/** A chat whose resident window holds `turnID`, which is what `chatHoldingTurn` answers a
 *  `live_turn` ref with — the ref is a TURN id, and the range read needs its chat. */
/** A chat holding one turn with three entries seated. The COUNT is load-bearing: the range read
 *  asks past the newest `seq` this client holds, so a turn seated empty would let a read asking
 *  for the whole turn pass. */
function sessionHoldingTurn(id: string, turnID: string): Session {
  const entries = [0, 1, 2].map((seq) => ({
    id: `${turnID}-e${String(seq)}`,
    turn: turnID,
    kind: "text" as const,
    seq,
    ts: seq + 1,
    payload: { text: "x" },
  }));
  return makeSession(id, {
    residency: "loaded",
    turn_order: [turnID],
    turns: new Map([[turnID, { entries, openEntries: new Map() }]]),
    turn_count: 1,
  });
}

function ctx(over: Partial<RevalidateContext> = {}): RevalidateContext {
  return {
    cause: "hello",
    epoch: versionMap().epoch(),
    generation: 1,
    full: false,
    signal: new AbortController().signal,
    ...over,
  };
}

/** The context a host-routed run hands this tab: the same fields plus the host's verdict. */
function tabCtx(over: Partial<TabRevalidateContext> = {}): TabRevalidateContext {
  return { ...ctx(), ...over };
}

function digestAnswer(
  changed: { kind: string; ref: string; version: string }[],
  removed: { kind: string; ref: string; reason: "gone" | "forbidden" }[] = [],
) {
  return (req: { body: string | null }) => {
    const body = JSON.parse(req.body ?? "{}") as { subjects: unknown[] };
    return json({
      epoch: EPOCH_A,
      head: "9",
      must_refetch: false,
      checked: body.subjects.length,
      changed,
      removed,
    });
  };
}

/** `answer`, counting the digest bodies read so far. What a run does after that read is promise
 *  continuations, so a check on a later task sees the run's verdict fully applied. */
function readCounted(answer: (req: { body: string | null }) => Response): {
  responder: (req: { body: string | null }) => Response;
  reads: () => number;
} {
  let reads = 0;
  return {
    responder: (req) => {
      const response = answer(req);
      const read = response.json.bind(response);
      response.json = async (): Promise<unknown> => {
        const parsed: unknown = await read();
        reads++;
        return parsed;
      };
      return response;
    },
    reads: () => reads,
  };
}

let scripted: ScriptedFetch;
let seen: ServerEvent[];
/** The in-page worker host a hosted case attached this tab to, closed after it. */
let hosted: WorkerHost | null = null;

beforeEach(() => {
  scripted = createScriptedFetch();
  vi.stubGlobal("fetch", scripted.fetch);
  seen = [];
  setSessions([]);
  resetVersions();
  // Bound to the epoch the scripted hello presents, so a stamp a case observes before connecting
  // survives the bind; the one case about the bind itself rebinds first.
  versionMap().bind(EPOCH_A);
  localStorage.removeItem(PROFILE_TAG_KEY);
  mockLoadList.mockResolvedValue(true);
  mockLoadMessages.mockResolvedValue(true);
  mockRequestTurnRange.mockReturnValue(undefined);
  mockListTabs.mockResolvedValue(true);
  mockRefetchLiveRuns.mockResolvedValue(undefined);
  mockRequestRunTurnRange.mockReturnValue(undefined);
  mockRunTurnHeldSeq.mockReturnValue(undefined);
  mockFetchCatalog.mockResolvedValue(undefined);
  mockInventoryHeld.mockReturnValue(false);
  mockRefreshPRs.mockClear();
  // The hub's answer to a rescope, at the epoch every scripted hello presents.
  scripted.respond("/api/events/scope", () => json({ epoch: EPOCH_A, offset: "3" }));
});

afterEach(() => {
  _resetForTest();
  hosted?.close();
  hosted = null;
});

async function connect(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
  init(
    (evt) => {
      seen.push(evt);
    },
    () => undefined,
  );
  await until(() => scripted.connections.length === 1, "the first connection");
  const conn = scripted.connections[0];
  conn?.hello();
  return conn;
}

function syncs(): number {
  return scripted.requests.filter((r) => r.url === "/api/sync").length;
}

function acks(): ScriptedFetch["requests"] {
  return scripted.requests.filter((r) => r.url === "/api/events/alive");
}

/** Every chat GET stays in flight until `release()`; `atSettle()` is the connection count as one
 *  settles. `started()` resolves on a task after the first GET is entered, so every continuation
 *  the run can take without the GET's answer has run. */
function heldGet(): {
  started: () => Promise<void>;
  release: () => void;
  atSettle: () => number;
} {
  let entered = false;
  let atSettle = Number.NaN;
  let release = (): void => undefined;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  mockLoadMessages.mockImplementation(async () => {
    entered = true;
    await gate;
    atSettle = scripted.connections.length;
    return true;
  });
  return {
    started: async () => {
      await until(() => entered, "the chat's GET to start");
      await new Promise((r) => setTimeout(r, 0));
    },
    release,
    atSettle: () => atSettle,
  };
}

/** Connect and let the fresh hello's own digest settle on an empty answer, so a case driving
 *  `revalidate` by hand is the only revalidation in flight. */
async function connectSettled(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
  scripted.respond("/api/sync", digestAnswer([]));
  const conn = await connect();
  markHydrated();
  await until(() => syncs() === 1, "the /api/sync request");
  return conn;
}

describe("the connect", () => {
  it("presents the persisted profile tag as SSE-Client, minting one when none is held", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    const conn = await connect();
    expect(conn?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");
    _resetForTest();

    localStorage.removeItem(PROFILE_TAG_KEY);
    scripted = createScriptedFetch();
    vi.stubGlobal("fetch", scripted.fetch);
    const fresh = await connect();
    const minted = fresh?.headers.get("SSE-Client") ?? "";
    expect(minted).toMatch(/^[A-Za-z0-9_-]{22}$/);
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe(minted);
  });

  it("adopts the subscription's derived tag with ONE reconnect, and none when it already matches", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    await connect();
    expect(presentedTag()).toBe("held-tag_0123456789AB");

    // The golden endpoint from internal/push/testdata/tag_golden.json.
    const endpoint =
      "https://fcm.googleapis.com/fcm/send/dQw4w9WgXcQ:APA91bGolden-Fixture-Endpoint-0001";
    await adoptPushSubscription({ endpoint });
    await until(() => scripted.connections.length === 2, "the second connection");
    expect(scripted.connections[1]?.headers.get("SSE-Client")).toBe("amxAEqwvwjG23476CxNmK6");
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe("amxAEqwvwjG23476CxNmK6");

    // The worker reports the same subscription again (a resubscribe that kept the endpoint):
    // nothing moves. The adopt resolves after its reconnect decision, and a reconnect aborts the
    // live connection on the spot.
    await adoptPushSubscription({ endpoint });
    expect(scripted.connections[1]?.aborted()).toBe(false);
    expect(scripted.connections).toHaveLength(2);
    expect(presentedTag()).toBe("amxAEqwvwjG23476CxNmK6");
  });

  it("acknowledges each keepalive with a POST carrying SSE-Client, and stops when the stream ends", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    scripted.respond("/api/events/alive", () => new Response(null, { status: 204 }));
    const conn = await connect();
    conn?.named("heartbeat", "{}");
    await until(() => acks().length === 1, "the first heartbeat ack");
    const ack = acks()[0];
    expect(ack?.method).toBe("POST");
    expect(ack?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");
    conn?.named("heartbeat", "{}");
    await until(() => acks().length === 2, "the second heartbeat ack");

    // The reset ends the connection on the spot, so no later keepalive can reach the parser.
    _resetForTest();
    expect(conn?.aborted()).toBe(true);
  });

  it("binds the version map to the hello's epoch, dropping what a previous epoch held", async () => {
    versionMap().bind(EPOCH_B);
    observeStamp({ kind: "chats", ref: "", version: "9" });
    await connect();
    await until(() => versionMap().epoch() === EPOCH_A, "the version map to adopt epoch A");
    expect(versionMap().snapshot().held).toEqual([]);
  });
});

describe("frames", () => {
  it("dispatches an applied frame and THEN observes its stamp", async () => {
    setSessions([makeSession("c1", { residency: "loaded" })]);
    let heldAtDispatch: boolean | undefined;
    init(
      (evt) => {
        heldAtDispatch = hasSubject("chat", "c1");
        seen.push(evt);
      },
      () => undefined,
    );
    await until(() => scripted.connections.length === 1, "the first connection");
    const conn = scripted.connections[0];
    conn?.hello();
    markHydrated();
    conn?.frame(
      {
        type: "entry_appended",
        chat_id: "c1",
        payload: {
          entry: { id: "e1", turn: "t1", kind: "text", seq: 1, ts: 1, payload: { text: "hi" } },
        },
        subject: { kind: "chat", ref: "c1", version: "4" },
      },
      `${EPOCH_A}:1`,
    );
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(seen[0]?.type).toBe("entry_appended");
    // Not yet held while the handlers ran: the version certifies APPLIED state.
    expect(heldAtDispatch).toBe(false);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "c1", version: "4" }]);
  });

  it("observes a workspace-wide stamp whatever the store holds", async () => {
    const conn = await connect();
    markHydrated();
    conn?.frame({
      type: "chat_updated",
      chat_id: "c9",
      // A whole ChatHeader, because the wire's own decoder runs here: `chat_updated` requires
      // `turn_count`, `usage`, `created_at` and `run`.
      payload: {
        id: "c9",
        name: "n",
        run: {
          send: "prompt",
          entries: [],
          next_wait: { after: "next" },
          unread_wait: { after: "next" },
          version: 0,
        },
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        created_at: 1,
        updated_at: 1,
        turn_count: 0,
      },
      subject: { kind: "chats", ref: "", version: "12" },
    });
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(hasSubject("chats", "")).toBe(true);
  });

  it("does not observe a chat stamp for a chat whose window is not resident", async () => {
    // The frame was applied to nothing, so recording its version would make the digest name — and
    // the wake refetch — a transcript nobody holds.
    setSessions([makeSession("c1", { residency: "evicted" })]);
    const conn = await connect();
    markHydrated();
    conn?.frame({
      type: "entry_appended",
      chat_id: "c1",
      payload: {
        entry: { id: "e1", turn: "t1", kind: "text", seq: 1, ts: 1, payload: { text: "hi" } },
      },
      subject: { kind: "chat", ref: "c1", version: "4" },
    });
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(hasSubject("chat", "c1")).toBe(false);
  });

  it("observes a forge_inventory stamp only while the PR tab holds the inventory", async () => {
    const inventoryFrame = (version: string) => ({
      type: "forge_inventory",
      chat_id: "",
      payload: {
        entry: {
          forge_id: "github:github.com",
          account: "acct-1",
          state: "ready",
          cycle_id: version,
          credential: "valid",
          scopes: [],
          clones: [],
          fetched_at: 1,
        },
      },
      subject: { kind: "forge_inventory", ref: "github:github.com", version },
    });
    mockInventoryHeld.mockReturnValue(false);
    const conn = await connect();
    markHydrated();
    conn?.frame(inventoryFrame("3"));
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(hasSubject("forge_inventory", "github:github.com")).toBe(false);

    mockInventoryHeld.mockReturnValue(true);
    conn?.frame(inventoryFrame("4"));
    await until(() => seen.length === 2, "the second frame to be delivered");
    expect(hasSubject("forge_inventory", "github:github.com")).toBe(true);
  });

  it("observes nothing when the decoder rejects the frame, and asks the digest instead", async () => {
    // Something held, so the rejection's `revalidate("hello")` has a question to ask.
    observeStamp({ kind: "chats", ref: "", version: "1" });
    scripted.respond("/api/sync", digestAnswer([]));
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const conn = await connect();
    markHydrated();
    // The connect's own fresh-hello digest lands first; the rejection earns a second.
    await until(
      () => scripted.requests.filter((r) => r.url === "/api/sync").length === 1,
      "the first /api/sync request",
    );
    conn?.frame({
      type: "chat_deleted",
      chat_id: "c1",
      payload: { id: 1 },
      subject: { kind: "chats", ref: "", version: "2" },
    });
    await until(
      () => scripted.requests.filter((r) => r.url === "/api/sync").length === 2,
      "the second /api/sync request",
    );
    expect(seen).toEqual([]);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chats", ref: "", version: "1" }]);
    expect(errors).toHaveBeenCalled();
  });

  it("refuses an entry_appended frame carrying the retired message shape", async () => {
    // The wire's own decoder makes a message-shaped payload unrenderable rather than merely
    // ignored: `decodeEntryAppendedPayload` requires `entry`, so a `{id, role, ts, content}`
    // body throws and the frame reaches no handler.
    const errors = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const warns = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const conn = await connect();
    markHydrated();
    conn?.frame(
      makeServerEvent({
        type: "entry_appended",
        chat_id: "c1",
        payload: { id: "m1", role: "assistant", ts: 1, content: "hi" },
      }),
    );
    // The in-fence control: the live entry shape goes through the SAME decoder and lands, so the
    // refusal cannot be a frame the adapter dropped for another reason. Written second, it
    // reaches the bus only after the refused frame was decided.
    conn?.frame({
      type: "entry_appended",
      chat_id: "c1",
      payload: {
        entry: { id: "e1", turn: "t1", kind: "text", seq: 1, ts: 1, payload: { text: "hi" } },
      },
    });
    await until(() => seen.length > 0, "a frame to be delivered");
    expect(seen).toHaveLength(1);
    expect(seen[0]?.type).toBe("entry_appended");
    expect(seen[0]?.payload).toHaveProperty("entry.id", "e1");
    expect(errors).toHaveBeenCalled();
    // It names no turn, so there is nothing to salvage by a range read.
    expect(mockRequestTurnRange).not.toHaveBeenCalled();
    expect(warns).toHaveBeenCalled();
  });

  it("holds frames until markHydrated and releases them in arrival order", async () => {
    scripted.respond("/api/events/alive", () => new Response(null, { status: 204 }));
    const conn = await connect();
    conn?.frame({
      type: "connected",
      payload: { busy_stated: false, live_runs_stated: false, live_runs_version: "0" },
    });
    // `options` is required by the wire's own decoder, which runs here.
    conn?.frame({
      type: "permission_needed",
      chat_id: "c1",
      payload: { request_id: 1, options: [] },
    });
    // A keepalive written after them is acknowledged only once the parser has handed both frames
    // on: nothing may reach the bus yet.
    conn?.named("heartbeat", "{}");
    await until(() => acks().length === 1, "the first heartbeat ack");
    expect(seen).toEqual([]);
    markHydrated();
    expect(seen.map((e) => e.type)).toEqual(["connected", "permission_needed"]);
  });

  it("runs the subject's refetch on subject_changed and observes nothing", async () => {
    await connect();
    markHydrated();
    scripted.connections[0]?.frame({
      type: "subject_changed",
      chat_id: "c1",
      subject: { kind: "chat", ref: "c1", version: "6" },
    });
    await until(() => mockLoadMessages.mock.calls.length === 1, "the transcript reload");
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, undefined);
    expect(seen).toEqual([]);
    expect(hasSubject("chat", "c1")).toBe(false);
  });

  it("refetches a loaded, fresh, EMPTY window on subject_changed, and keeps its old version until the page lands", async () => {
    // The window a `session/load` swap has to reach: `loaded`, holding a version, and EMPTY — what
    // a chat whose first newest-page load answered zero messages holds.
    setSessions([makeSession("c1", { turn_count: 2, turns: new Map(), residency: "loaded" })]);
    observeStamp({ kind: "chat", ref: "c1", version: "5" });
    // Something is held, so the fresh hello digests; let that settle first, or the frame below sits
    // behind a revalidation nobody answers.
    const conn = await connectSettled();
    conn?.frame({
      type: "subject_changed",
      chat_id: "c1",
      subject: { kind: "chat", ref: "c1", version: "6" },
    });
    await until(() => mockLoadMessages.mock.calls.length === 1, "the transcript reload");
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, undefined);
    // The refetch's own commit is what moves the version; the frame's stamp is not it.
    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "c1", version: "5" }]);
  });
});

describe("a run's step turn is a subject of its own", () => {
  /** A run-scoped entry frame: an EMPTY chat id, `workflow_id` beside the entry, and a
   *  `run_turn` stamp whose ref the server spells `<workflowID>/<turn>`. */
  function runFrame(turn: string, version: string): ServerEvent {
    return {
      type: "entry_appended",
      chat_id: "",
      payload: {
        workflow_id: "wf-1",
        entry: { id: "e1", turn, kind: "text", seq: 1, ts: 1, payload: { text: "step" } },
      },
      subject: { kind: "run_turn", ref: `wf-1/${turn}`, version },
    };
  }

  beforeEach(() => {
    setRunContentScope("wf-1", "");
  });
  afterEach(() => {
    setRunContentScope("", "");
  });

  it("observes the stamp while the run's log holds that turn", async () => {
    mockRunTurnHeldSeq.mockReturnValue(1);
    const conn = await connect();
    markHydrated();
    conn?.frame(runFrame("t-1", "5"));
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(hasSubject("run_turn", "wf-1/t-1")).toBe(true);
    // The ref splits into the run and the turn the residency question is asked about.
    expect(mockRunTurnHeldSeq).toHaveBeenCalledWith("wf-1", "t-1");
  });

  it("observes nothing for a turn this client is not holding", async () => {
    // Same rule as an evicted chat's window: the frame was applied to nothing, so recording its
    // version would make the digest name a projection nobody holds. A scoped run's sibling step
    // moves only its dock line, holding no turn.
    const conn = await connect();
    markHydrated();
    conn?.frame(runFrame("t-1", "5"));
    await until(() => seen.length === 1, "the frame to be delivered");
    expect(hasSubject("run_turn", "wf-1/t-1")).toBe(false);
  });

  it("answers a moved step turn with THAT turn's range read, past the seq it holds", async () => {
    // The ref names one turn of one run, so the repair is that turn's range read and not the run's
    // own state: `<workflowID>/<turn>` splits on the FIRST separator, and the `after` is what this
    // client holds rather than a version off the digest.
    mockRunTurnHeldSeq.mockReturnValue(4);
    observeStamp({ kind: "run_turn", ref: "wf-1/t-1", version: "5" });
    scripted.respond(
      "/api/sync",
      digestAnswer([{ kind: "run_turn", ref: "wf-1/t-1", version: "6" }]),
    );

    await _revalidateForTest(ctx());

    expect(mockRequestRunTurnRange).toHaveBeenCalledWith("wf-1", "t-1", 4);
    expect(mockLoadMessages).not.toHaveBeenCalled();
    expect(mockRequestTurnRange).not.toHaveBeenCalled();
  });

  it("asks for the WHOLE turn when it holds none of it", async () => {
    // The lost-`turn_opened` case: `after` omitted, which is what the route answers a whole turn
    // for. A held seq of 0 is a real position and must not collapse to this.
    mockRunTurnHeldSeq.mockReturnValue(undefined);
    observeStamp({ kind: "run_turn", ref: "wf-1/t-9", version: "3" });
    scripted.respond(
      "/api/sync",
      digestAnswer([{ kind: "run_turn", ref: "wf-1/t-9", version: "4" }]),
    );

    await _revalidateForTest(ctx());

    expect(mockRequestRunTurnRange).toHaveBeenCalledWith("wf-1", "t-9", undefined);
  });

  it("reads the tail of a step turn that CLOSED while this client was away", async () => {
    // A `gone` verdict runs the SAME range read as a mismatch, which is
    // what brings in the tail and the `turn_close` — and that entry is what stops a step whose turn
    // closed during a gap reading as live for the tab's life.
    mockRunTurnHeldSeq.mockReturnValue(2);
    observeStamp({ kind: "run_turn", ref: "wf-1/t-1", version: "5" });
    scripted.respond(
      "/api/sync",
      digestAnswer([], [{ kind: "run_turn", ref: "wf-1/t-1", reason: "gone" }]),
    );

    await _revalidateForTest(ctx());

    expect(hasSubject("run_turn", "wf-1/t-1")).toBe(false);
    expect(mockRequestRunTurnRange).toHaveBeenCalledWith("wf-1", "t-1", 2);
    expect(mockLoadMessages).not.toHaveBeenCalled();
  });
});

describe("a run's content reaches only the page that draws it", () => {
  afterEach(() => {
    setRunContentScope("", "");
  });

  it("delivers a run's step content while this page's scope names the run", async () => {
    setRunContentScope("wf-1", "");
    const conn = await connect();
    markHydrated();

    conn?.frame({
      type: "entry_delta",
      chat_id: "",
      payload: { workflow_id: "wf-1", turn: "t-1", entry_id: "e1", lane: "", n: 1, delta: "x" },
    });
    await until(() => seen.length === 1, "the content frame");

    expect(seen.map((e) => e.type)).toEqual(["entry_delta"]);
  });

  it("drops a run's step content while no view of this page shows the run, and keeps its lifecycle", async () => {
    const conn = await connect();
    markHydrated();

    conn?.frame({
      type: "entry_delta",
      chat_id: "",
      payload: { workflow_id: "wf-1", turn: "t-1", entry_id: "e1", lane: "", n: 1, delta: "x" },
    });
    conn?.frame({
      type: "run_progress",
      chat_id: "",
      payload: { workflow_id: "wf-1", kind: "node_start", node_id: "build" },
    });
    await until(() => seen.some((e) => e.type === "run_progress"), "the lifecycle frame");

    expect(seen.map((e) => e.type)).toEqual(["run_progress"]);
  });
});

describe("the page's scope on the stream", () => {
  const RUN = "wf-1";

  afterEach(() => {
    setRunContentScope("", "");
  });

  function scopePosts(): { conn: string; seq: number; topics: string[] | null }[] {
    return scripted.requests
      .filter((r) => r.url === "/api/events/scope")
      .map(
        (r) => JSON.parse(r.body ?? "{}") as { conn: string; seq: number; topics: string[] | null },
      );
  }

  /** What a readiness promise has settled to after a few tasks: "pending" when it has not. */
  async function settledAs<T>(p: Promise<T>): Promise<T | "pending"> {
    let out: T | "pending" = "pending";
    void p.then((v) => {
      out = v;
    });
    for (let i = 0; i < 5; i++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    return out;
  }

  it("connects drawing broadcasts only", async () => {
    const conn = await connect();
    expect(conn?.headers.get("SSE-Topics")).toBe("[]");
  });

  it("asks the hub for the shown run's topic on this connection, and the run reads once the hub delivers it", async () => {
    const conn = await connect();
    markHydrated();
    setRunContentScope(RUN, "");
    expect(await scopeReady(RUN)).toBe(true);
    expect(scopePosts()).toEqual([
      { conn: "conn-00000000000000000", seq: 1, topics: [`run:${RUN}`] },
    ]);
    expect(conn?.headers.get("SSE-Topics")).toBe("[]");
  });

  it.each([
    { run: "", why: "no run" },
    { run: "wf/1", why: "an id no run carries" },
  ])("draws broadcasts only for $why, which the connection already does", async ({ run }) => {
    await connect();
    expect(await setPageScope(run)).toBe("covered");
    expect(scopePosts()).toEqual([]);
  });

  it("draws everything for an id too long for a topic", async () => {
    await connect();
    const long = "w".repeat(125);
    expect(await setPageScope(long)).toBe("covered");
    expect(scopePosts().map((p) => p.topics)).toEqual([null]);
  });

  it("answers owed before the stream exists", async () => {
    expect(await setPageScope(RUN)).toBe("owed");
  });

  it("an ask the link lost is asked again at the next open, and the run reads after that", async () => {
    // The first connection's hub never answers, so the ask is still out when the tab detaches.
    scripted.respond("/api/events/scope", () => new Promise<Response>(() => undefined));
    await connect();
    setRunContentScope(RUN, "");
    await until(() => scopePosts().length === 1, "the first ask");
    const ready = scopeReady(RUN);

    scripted.respond("/api/events/scope", () => json({ epoch: EPOCH_A, offset: "3" }));
    init(
      (evt) => {
        seen.push(evt);
      },
      () => undefined,
    );
    await until(() => scripted.connections.length === 2, "the new seat's connection");
    expect(await settledAs(ready)).toBe("pending");
    scripted.connections[1]?.hello();

    expect(await ready).toBe(true);
    expect(scopePosts().at(-1)).toEqual({
      conn: "conn-00000000000000001",
      seq: 1,
      topics: [`run:${RUN}`],
    });
  });

  it("a refused scope draws everything once: the run reads, and nothing asks for a scope again", async () => {
    const warns = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    scripted.respond("/api/events/scope", () =>
      json({ error: "bad topic", code: "sse_scope" }, 400),
    );
    await connect();
    markHydrated();
    setRunContentScope(RUN, "");
    const ready = scopeReady(RUN);
    await until(() => scripted.connections.length === 2, "the reconnect on everything");
    expect(scripted.connections[1]?.headers.get("SSE-Topics")).toBeNull();
    scripted.connections[1]?.hello();

    expect(await ready).toBe(true);
    setRunContentScope("", "");
    setRunContentScope("wf-2", "");
    expect(await scopeReady("wf-2")).toBe(true);
    expect(scopePosts()).toHaveLength(1);
    expect(warns.mock.calls.some((c) => String(c[1]).includes("scope_refused"))).toBe(true);
  });

  it("an older host that delivers everything covers the run at once", async () => {
    _spawnWorkerForTest(() => {
      const channel = new MessageChannel();
      channel.port2.onmessage = (event: MessageEvent) => {
        const m = event.data as { type?: unknown };
        if (m.type === "attach") {
          // No `scope: true`: a host that takes no set_scope.
          channel.port2.postMessage({
            type: "state",
            state: { kind: "open", generation: 1, cursor: null, visible: true, online: true },
          });
        }
      };
      return { port: channel.port1, addEventListener: () => undefined };
    });
    init(
      (evt) => {
        seen.push(evt);
      },
      () => undefined,
    );
    expect(await setPageScope(RUN)).toBe("covered");
  });

  it("a run's undecodable frame is not re-read while the run is not shown here", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const conn = await connect();
    markHydrated();
    const broken = (run: string) => ({
      type: "entry_appended",
      chat_id: "",
      payload: { workflow_id: run, entry: { id: "e1", turn: "t-1" } },
    });
    conn?.frame(broken("wf-2"));
    setRunContentScope(RUN, "");
    conn?.frame(broken(RUN));
    await until(() => mockRequestRunTurnRange.mock.calls.length === 1, "the scoped run's re-read");
    expect(mockRequestRunTurnRange.mock.calls).toEqual([[RUN, "t-1"]]);
  });

  it("delivers a chat call's delta that names the run it launched, the run not shown here", async () => {
    const conn = await connect();
    markHydrated();
    conn?.frame({
      type: "tool_progress",
      chat_id: "c1",
      payload: { turn: "t1", tool_call_id: "call-1", workflow_id: "wf-launched" },
    });
    await until(() => seen.length === 1, "the chat's delta");
    expect(seen[0]?.type).toBe("tool_progress");
  });
});

describe("revalidate", () => {
  it("emits BUS_PAGE_RESUMED first on a visible cause, then one digest", async () => {
    observeStamp({ kind: "chats", ref: "", version: "1" });
    const order: string[] = [];
    const unsub = onBus(BUS_PAGE_RESUMED, () => {
      order.push("resumed");
    });
    scripted.respond("/api/sync", (req) => {
      order.push("digest");
      return digestAnswer([])(req);
    });
    try {
      await _revalidateForTest(ctx({ cause: "visible" }));
    } finally {
      unsub();
    }
    expect(order).toEqual(["resumed", "digest"]);
    expect(scripted.requests.filter((r) => r.url === "/api/sync")).toHaveLength(1);
  });

  it("asks nothing when the map holds nothing", async () => {
    await _revalidateForTest(ctx());
    expect(scripted.requests).toEqual([]);
  });

  it("refetches exactly the chat the digest named, with the run's signal", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "chat", ref: "Y", version: "3" });
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "X", version: "4" }]));
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, controller.signal);
    // Never copied from the answer: the loader observes its own response stamp.
    expect(
      versionMap()
        .snapshot()
        .held.find((h) => h.ref === "X")?.version,
    ).toBe("3");
  });

  it("leaves the map at the old version when the GET fails", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "X", version: "4" }]));
    mockLoadMessages.mockResolvedValue(false);

    await _revalidateForTest(ctx());

    expect(versionMap().snapshot().held).toEqual([{ kind: "chat", ref: "X", version: "3" }]);
  });

  it("sends the digest the held snapshot and its epoch", async () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chats", ref: "", version: "2" });
    scripted.respond("/api/sync", digestAnswer([]));

    await _revalidateForTest(ctx());

    const body = JSON.parse(scripted.requests[0]?.body ?? "{}") as Record<string, unknown>;
    expect(body).toEqual({ epoch: EPOCH_A, subjects: [{ kind: "chats", ref: "", version: "2" }] });
  });

  it("runs the action column: chats, tabs, runs and catalog each reach their loader", async () => {
    observeStamp({ kind: "chats", ref: "", version: "1" });
    observeStamp({ kind: "tabs", ref: "", version: "1" });
    observeStamp({ kind: "runs", ref: "", version: "1" });
    observeStamp({ kind: "catalog", ref: "", version: "1" });
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chats", ref: "", version: "2" },
        { kind: "tabs", ref: "", version: "2" },
        { kind: "runs", ref: "", version: "2" },
        { kind: "catalog", ref: "", version: "2" },
      ]),
    );
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockLoadList).toHaveBeenCalledWith(controller.signal);
    expect(mockListTabs).toHaveBeenCalledWith(controller.signal);
    expect(mockRefetchLiveRuns).toHaveBeenCalledWith(controller.signal);
    expect(mockFetchCatalog).toHaveBeenCalledWith({ signal: controller.signal });
  });

  it("re-reads the pull-request inventory a moved forge_inventory subject names", async () => {
    observeStamp({ kind: "forge_inventory", ref: "github:github.com", version: "3" });
    scripted.respond(
      "/api/sync",
      digestAnswer([{ kind: "forge_inventory", ref: "github:github.com", version: "5" }]),
    );
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockRefreshPRs).toHaveBeenCalledTimes(1);
    expect(mockRefreshPRs).toHaveBeenCalledWith(controller.signal);
  });

  it("answers a chat and a live_turn with a window GET and a RANGE read, one each", async () => {
    // The two kinds do not collapse onto each other: a `chat` is the window, a `live_turn` is one
    // turn's entries past the newest `seq` this client holds.
    setSessions([sessionHoldingTurn("c1", "X")]);
    observeStamp({ kind: "chat", ref: "c1", version: "3" });
    observeStamp({ kind: "live_turn", ref: "X", version: "7:2" });
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chat", ref: "c1", version: "4" },
        { kind: "live_turn", ref: "X", version: "7:3" },
      ]),
    );

    await _revalidateForTest(ctx());

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, expect.any(AbortSignal));
    // Past the newest `seq` the turn holds, which is what stops the read re-serving entries the
    // store already has.
    expect(mockRequestTurnRange.mock.calls).toEqual([["c1", "X", 2]]);
  });

  it("forgets a removed chat and runs its local drop through the frame door", async () => {
    observeStamp({ kind: "chat", ref: "gone", version: "3" });
    await connectSettled();
    scripted.respond(
      "/api/sync",
      digestAnswer([], [{ kind: "chat", ref: "gone", reason: "gone" }]),
    );

    await _revalidateForTest(ctx());

    expect(hasSubject("chat", "gone")).toBe(false);
    expect(seen).toEqual([{ type: "chat_deleted", chat_id: "", payload: { id: "gone" } }]);
    expect(mockLoadMessages).not.toHaveBeenCalled();
  });

  it("reads the RANGE of a live turn that is gone, which is what brings its turn_close in", async () => {
    // The stamp is forgotten and the turn is re-read: the `turn_close` entry is what every settled
    // surface reads, and a window GET is not what carries it.
    setSessions([sessionHoldingTurn("c1", "X")]);
    observeStamp({ kind: "live_turn", ref: "X", version: "7:2" });
    scripted.respond(
      "/api/sync",
      digestAnswer([], [{ kind: "live_turn", ref: "X", reason: "gone" }]),
    );

    await _revalidateForTest(ctx());

    expect(hasSubject("live_turn", "X")).toBe(false);
    expect(mockRequestTurnRange.mock.calls).toEqual([["c1", "X", 2]]);
    expect(mockLoadMessages).not.toHaveBeenCalled();
  });

  it("leaves a live_turn whose chat this client does not hold with no read at all", async () => {
    // `chatHoldingTurn` answers "" — the frame was applied to nothing, so there is no window to
    // read a range out of.
    observeStamp({ kind: "live_turn", ref: "X", version: "7:2" });
    scripted.respond("/api/sync", digestAnswer([{ kind: "live_turn", ref: "X", version: "7:3" }]));

    await _revalidateForTest(ctx());

    expect(mockRequestTurnRange).not.toHaveBeenCalled();
    expect(mockLoadMessages).not.toHaveBeenCalled();
  });

  it("completes every GET, then reconnects once when pending moved", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "pending", ref: "", version: "1" });
    const first = await connectSettled();
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chat", ref: "X", version: "4" },
        { kind: "pending", ref: "", version: "2" },
      ]),
    );
    const held = heldGet();
    let ran = false;
    const run = _revalidateForTest(ctx({ cause: "visible" })).then(() => {
      ran = true;
    });
    await held.started();
    // The run and its reconnect wait for the GET: one live connection while it runs and as it settles.
    expect({ ran, connections: scripted.connections.length, aborted: first?.aborted() }).toEqual({
      ran: false,
      connections: 1,
      aborted: false,
    });
    held.release();
    await run;
    expect(held.atSettle()).toBe(1);

    await until(() => scripted.connections.length === 2, "the second connection");
    expect(first?.aborted()).toBe(true);
    expect(scripted.connections[1]?.headers.has("Last-Event-ID")).toBe(false);
  });

  it("the fresh hello after a pending reconnect digests once and never reconnects again, though status still reads moved", async () => {
    observeStamp({ kind: "status", ref: "", version: "1" });
    await connectSettled();
    // The realistic answer, left in place: the map still holds status@1 while the hello's own
    // status_snapshot@2 sits held behind the run, so the server keeps naming it until that frame
    // drains.
    const answer = readCounted(digestAnswer([{ kind: "status", ref: "", version: "2" }]));
    scripted.respond("/api/sync", answer.responder);

    await _revalidateForTest(ctx({ cause: "visible" }));
    await until(() => scripted.connections.length === 2, "the second connection");
    const syncsBefore = syncs();
    const readsBefore = answer.reads();
    scripted.connections[1]?.hello();
    // The hello's run has applied its verdict: nothing else is queued behind it, and no third
    // connection was opened.
    await until(() => answer.reads() > readsBefore, "the hello's digest body to be read");
    expect(syncs()).toBe(syncsBefore + 1);
    expect(scripted.connections).toHaveLength(2);
  });

  it("binds the epoch BEFORE the reconcile on must_refetch", async () => {
    versionMap().bind(EPOCH_A);
    observeStamp({ kind: "chats", ref: "", version: "2" });
    scripted.respond("/api/sync", () => json({ epoch: EPOCH_B, head: "0", must_refetch: true }));
    let epochAtReconcile: string | null = "unset";
    let heldAtReconcile = -1;
    const unsub = onBus(BUS_RECONCILE, () => {
      epochAtReconcile = versionMap().epoch();
      heldAtReconcile = versionMap().snapshot().held.length;
    });
    try {
      await _revalidateForTest(ctx({ epoch: EPOCH_A }));
    } finally {
      unsub();
    }
    expect(epochAtReconcile).toBe(EPOCH_B);
    expect(heldAtReconcile).toBe(0);
  });

  it("runs the reconcile body with no digest when the map was just cleared", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    let signalSeen: AbortSignal | undefined;
    const unsub = onBus(BUS_RECONCILE, ({ cause, signal }) => {
      causes.push(cause);
      signalSeen = signal;
    });
    const controller = new AbortController();
    try {
      await _revalidateForTest(ctx({ cause: "hello", full: true, signal: controller.signal }));
    } finally {
      unsub();
    }
    expect(causes).toEqual(["full:hello"]);
    expect(signalSeen).toBe(controller.signal);
    expect(scripted.requests).toEqual([]);
  });
});

// The body the worker host routes to this tab. No worker runs here: the host's frames, hellos and
// runs are the library's (sse-worker-host.node.test.ts drives the real host); what this pins is
// what THIS tab does with a run it did not own the cause of.
describe("the body the host routes to this tab", () => {
  it("applies the run's verdict with no digest of its own: the named chat reaches its loader with the run's signal", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    const controller = new AbortController();

    await _tabRevalidateForTest(
      tabCtx({
        signal: controller.signal,
        changed: [{ kind: "chat", ref: "X", version: "4" }],
        removed: [],
      }),
    );

    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, controller.signal);
    // Nothing POSTed: the host performed the profile's one digest.
    expect(scripted.requests).toEqual([]);
  });

  it("completes every GET and asks for no hello of its own when pending moved: that decision is the profile's", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    observeStamp({ kind: "pending", ref: "", version: "1" });
    await connectSettled();
    const held = heldGet();
    let ran = false;
    const run = _tabRevalidateForTest(
      tabCtx({
        cause: "visible",
        changed: [
          { kind: "chat", ref: "X", version: "4" },
          { kind: "pending", ref: "", version: "2" },
        ],
        removed: [],
      }),
    ).then(() => {
      ran = true;
    });
    await held.started();
    expect({ ran, aborted: scripted.connections[0]?.aborted() }).toEqual({
      ran: false,
      aborted: false,
    });
    held.release();
    await run;
    expect(held.atSettle()).toBe(1);

    // The host reconnects the profile's stream once; N tabs asking would open N. A tab's reconnect
    // would abort the live connection inside the run just awaited.
    expect(scripted.connections[0]?.aborted()).toBe(false);
    expect(scripted.connections).toHaveLength(1);
  });

  it("a changed subject this tab does not hold reaches no loader", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });

    await _tabRevalidateForTest(
      tabCtx({
        changed: [
          { kind: "chat", ref: "X", version: "4" },
          { kind: "chat", ref: "Y", version: "7" },
        ],
        removed: [],
      }),
    );

    expect(mockLoadMessages).toHaveBeenCalledTimes(1);
    expect(mockLoadMessages).toHaveBeenCalledWith("X", undefined, expect.any(AbortSignal));
  });

  it("a removed live turn this tab does not hold runs no GET, while a removed chat is dropped whatever this tab holds", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    await connectSettled();

    await _tabRevalidateForTest(
      tabCtx({
        changed: [],
        removed: [
          { kind: "live_turn", ref: "Z", reason: "gone" },
          { kind: "chat", ref: "Y", reason: "gone" },
        ],
      }),
    );

    expect(mockLoadMessages).not.toHaveBeenCalled();
    expect(seen).toEqual([{ type: "chat_deleted", chat_id: "", payload: { id: "Y" } }]);
  });

  it("a run at an epoch this tab does not hold clears the map and reconciles in full, once", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    const unsub = onBus(BUS_RECONCILE, ({ cause }) => {
      causes.push(cause);
    });
    try {
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_B, full: false }));
      expect(versionMap().epoch()).toBe(EPOCH_B);
      expect(versionMap().snapshot().held).toEqual([]);
      // The same epoch again: nothing dropped, an empty verdict, nothing to reconcile.
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_B, full: false }));
    } finally {
      unsub();
    }
    expect(causes).toEqual(["full:hello"]);
    expect(scripted.requests).toEqual([]);
  });

  it("a run at this tab's own epoch keeps the map and applies an empty verdict as nothing", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    const causes: string[] = [];
    const unsub = onBus(BUS_RECONCILE, ({ cause }) => {
      causes.push(cause);
    });
    try {
      await _tabRevalidateForTest(tabCtx({ cause: "hello", epoch: EPOCH_A }));
    } finally {
      unsub();
    }
    expect(causes).toEqual([]);
    expect(versionMap().snapshot().held).toEqual([{ kind: "chats", ref: "", version: "2" }]);
    expect(scripted.requests).toEqual([]);
    expect(mockLoadList).not.toHaveBeenCalled();
  });

  it("emits BUS_PAGE_RESUMED on a profile wake only in a VISIBLE tab", async () => {
    let resumed = 0;
    const unsub = onBus(BUS_PAGE_RESUMED, () => {
      resumed++;
    });
    try {
      const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
      await _tabRevalidateForTest(tabCtx({ cause: "visible" }));
      expect(resumed).toBe(0);
      hidden.mockRestore();

      await _tabRevalidateForTest(tabCtx({ cause: "visible" }));
      expect(resumed).toBe(1);
      await _tabRevalidateForTest(tabCtx({ cause: "hello" }));
      expect(resumed).toBe(1);
    } finally {
      unsub();
    }
  });
});

// This tab attached to the REAL host (sse-worker-host.ts) over a MessageChannel in place of a
// SharedWorker, with the host's stream and digest on the same scripted fetch: what crosses the port
// is what a profile's tabs and worker exchange.
describe("attached to a worker host", () => {
  /** Boot the adapter onto a host this test holds and answer the host's first hello. */
  let tabPort: MessagePort | null = null;
  let pingSeq = 0;

  async function connectHosted(): Promise<ReturnType<ScriptedFetch["connections"]["at"]>> {
    const host = createSSEHost();
    hosted = host;
    _spawnWorkerForTest(() => {
      const channel = new MessageChannel();
      host.attach(channel.port2);
      tabPort = channel.port1;
      return { port: channel.port1, addEventListener: () => undefined };
    });
    init(
      (evt) => {
        seen.push(evt);
      },
      () => undefined,
    );
    await until(() => scripted.connections.length === 1, "the first connection");
    const conn = scripted.connections[0];
    conn?.hello();
    await until(() => host.stream().state().kind === "open", "the host's stream to open");
    return conn;
  }

  /** Resolves on the host's answer to a ping sent now on this tab's port: the host handles one
   *  port's messages in order, so everything the tab posted before it has been handled. */
  async function hostRoundTrip(): Promise<void> {
    const port = tabPort;
    if (port === null) {
      throw new Error("no hosted tab to ping through");
    }
    const seq = 1_000_000 + ++pingSeq;
    let answered = false;
    const onPong = (event: MessageEvent): void => {
      const m = event.data as { type?: unknown; seq?: unknown };
      if (m.type === "pong" && m.seq === seq) {
        answered = true;
      }
    };
    port.addEventListener("message", onPong);
    try {
      port.postMessage({ type: "ping", seq });
      await until(() => answered, "the host's pong");
    } finally {
      port.removeEventListener("message", onPong);
    }
  }

  /** Hide and show the page, which the tab reports and the host's stream wakes on. Shows only
   *  once the host reads the profile hidden: a show that overtakes the hide is no wake. */
  async function wakeProfile(): Promise<void> {
    const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    await until(() => {
      const state = hosted?.stream().state();
      return state?.kind === "open" && !state.visible;
    }, "the host to read the profile hidden");
    hidden.mockRestore();
    document.dispatchEvent(new Event("visibilitychange"));
  }

  it("a stamp this tab records reaches the host's map, whose ONE digest names it, and the verdict comes back to this tab's loader", async () => {
    setSessions([makeSession("c1", { residency: "loaded" })]);
    scripted.respond("/api/sync", digestAnswer([{ kind: "chat", ref: "c1", version: "5" }]));
    const conn = await connectHosted();
    markHydrated();
    conn?.frame(
      {
        type: "entry_appended",
        chat_id: "c1",
        payload: {
          entry: { id: "e1", turn: "t1", kind: "text", seq: 1, ts: 1, payload: { text: "hi" } },
        },
        subject: { kind: "chat", ref: "c1", version: "4" },
      },
      `${EPOCH_A}:1`,
    );
    await until(() => seen.length === 1, "the frame to be delivered");

    await wakeProfile();
    await until(() => mockLoadMessages.mock.calls.length === 1, "the transcript reload");

    expect(syncs()).toBe(1);
    const body = JSON.parse(scripted.requests[0]?.body ?? "{}") as Record<string, unknown>;
    expect(body).toEqual({ epoch: EPOCH_A, subjects: [{ kind: "chat", ref: "c1", version: "4" }] });
    expect(mockLoadMessages).toHaveBeenCalledWith("c1", undefined, expect.any(AbortSignal));
  });

  it("adopting the subscription's tag moves SSE-Client on the PROFILE's stream with one resumed reconnect", async () => {
    localStorage.setItem(PROFILE_TAG_KEY, "held-tag_0123456789AB");
    const first = await connectHosted();
    expect(first?.headers.get("SSE-Client")).toBe("held-tag_0123456789AB");

    const endpoint =
      "https://fcm.googleapis.com/fcm/send/dQw4w9WgXcQ:APA91bGolden-Fixture-Endpoint-0001";
    await adoptPushSubscription({ endpoint });
    await until(() => scripted.connections.length === 2, "the second connection");
    expect(first?.aborted()).toBe(true);
    expect(scripted.connections[1]?.headers.get("SSE-Client")).toBe("amxAEqwvwjG23476CxNmK6");
    // The cursor is kept: the tag moved, the profile lost no frame.
    expect(scripted.connections[1]?.headers.get("Last-Event-ID")).not.toBeNull();
    expect(presentedTag()).toBe("amxAEqwvwjG23476CxNmK6");

    await adoptPushSubscription({ endpoint });
    await hostRoundTrip();
    expect(scripted.connections[1]?.aborted()).toBe(false);
    expect(scripted.connections).toHaveLength(2);
  });
});

// A run's tree and its step_ends are applied from frames a gap can swallow, and only a run read
// repairs them, so every revalidation re-reads the held runs once, whichever way it exits.
describe("every revalidation re-reads the held runs once", () => {
  it("a same-epoch hello whose digest moved nothing", async () => {
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    scripted.respond("/api/sync", digestAnswer([]));
    let reconciled = 0;
    const unsub = onBus(BUS_RECONCILE, () => {
      reconciled++;
    });
    try {
      await _revalidateForTest(ctx({ cause: "hello", full: false }));
    } finally {
      unsub();
    }
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(reconciled).toBe(0);
  });

  it("a hello with no held stamp", async () => {
    await _revalidateForTest(ctx({ cause: "hello", full: false }));
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(scripted.requests).toEqual([]);
  });

  it("a full revalidation", async () => {
    let atReconcile = -1;
    const unsub = onBus(BUS_RECONCILE, () => {
      atReconcile = mockRereadHeldRuns.mock.calls.length;
    });
    try {
      await _revalidateForTest(ctx({ full: true }));
    } finally {
      unsub();
    }
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(atReconcile).toBe(1);
  });

  it("a digest that answers must_refetch", async () => {
    observeStamp({ kind: "chats", ref: "", version: "2" });
    scripted.respond("/api/sync", () => json({ epoch: EPOCH_B, head: "0", must_refetch: true }));
    await _revalidateForTest(ctx());
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
  });

  it("the same full cause twice re-reads twice", async () => {
    await _revalidateForTest(ctx({ cause: "hello", full: true }));
    await _revalidateForTest(ctx({ cause: "hello", full: true }));
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(2);
  });

  it("a wake re-reads before the wake refresh", async () => {
    let atResume = -1;
    const unsub = onBus(BUS_PAGE_RESUMED, () => {
      atResume = mockRereadHeldRuns.mock.calls.length;
    });
    try {
      await _revalidateForTest(ctx({ cause: "visible", full: false }));
    } finally {
      unsub();
    }
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(atResume).toBe(1);
  });

  it("the body the host routes to this tab, with no verdict", async () => {
    await _tabRevalidateForTest(tabCtx({ changed: [], removed: [] }));
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
  });

  it("the body the host routes to this tab, full", async () => {
    await _tabRevalidateForTest(tabCtx({ full: true }));
    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
  });

  it("a verdict naming the runs subject reads the inventory and re-reads nothing more", async () => {
    observeStamp({ kind: "chats", ref: "", version: "1" });
    observeStamp({ kind: "runs", ref: "", version: "1" });
    scripted.respond(
      "/api/sync",
      digestAnswer([
        { kind: "chats", ref: "", version: "2" },
        { kind: "runs", ref: "", version: "2" },
      ]),
    );
    const controller = new AbortController();

    await _revalidateForTest(ctx({ signal: controller.signal }));

    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(mockRefetchLiveRuns).toHaveBeenCalledTimes(1);
    expect(mockRefetchLiveRuns.mock.calls[0]).toEqual([controller.signal]);
  });

  it("a refused runs frame on the stream re-reads the held runs", async () => {
    // Held, so the hello's own revalidation settles before the frame.
    observeStamp({ kind: "chat", ref: "X", version: "3" });
    const conn = await connectSettled();
    mockRereadHeldRuns.mockClear();
    mockRefetchLiveRuns.mockClear();

    conn?.frame({
      type: "subject_changed",
      chat_id: "",
      subject: { kind: "runs", ref: "", version: "2" },
    });
    await until(() => mockRefetchLiveRuns.mock.calls.length === 1, "the inventory read");

    expect(mockRereadHeldRuns).toHaveBeenCalledTimes(1);
    expect(mockRefetchLiveRuns).toHaveBeenCalledTimes(1);
  });
});

describe("the MCP status subject re-reads the status inventory", () => {
  // The server's `mcp_status` counter, increasing across the cases: the status mirror is module state that outlives
  // a case, while each case starts with an empty version map.
  let counter = 0;
  function nextVersion(): string {
    counter++;
    return String(counter);
  }

  function statusReads(): ScriptedFetch["requests"] {
    return scripted.requests.filter((r) => r.url === "/api/mcp/status");
  }

  function statusAnswer(version: string): Response {
    return json({
      servers: [],
      subject: { kind: "mcp_status", ref: "", version, epoch: EPOCH_A },
    });
  }

  function heldStatusVersion(): string | undefined {
    return versionMap()
      .snapshot()
      .held.find((h) => h.kind === "mcp_status")?.version;
  }

  it("a verdict naming mcp_status reads the status and holds its version once applied", async () => {
    observeStamp({ kind: "mcp_status", ref: "", version: nextVersion() });
    const moved = nextVersion();
    scripted.respond("/api/sync", digestAnswer([{ kind: "mcp_status", ref: "", version: moved }]));
    scripted.respond("/api/mcp/status", () => statusAnswer(moved));

    await _revalidateForTest(ctx());

    expect(statusReads()).toHaveLength(1);
    expect(heldStatusVersion()).toBe(moved);
  });

  it("a refused mcp_status frame reads the status, and the held version moves only with the read", async () => {
    const held = nextVersion();
    const moved = nextVersion();
    observeStamp({ kind: "mcp_status", ref: "", version: held });
    const conn = await connectSettled();
    let release = (): void => undefined;
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    scripted.respond("/api/mcp/status", async () => {
      await gate;
      return statusAnswer(moved);
    });

    conn?.frame({
      type: "subject_changed",
      chat_id: "",
      subject: { kind: "mcp_status", ref: "", version: moved },
    });
    await until(() => statusReads().length === 1, "the status read");
    expect(heldStatusVersion(), "the frame's own stamp is not observed").toBe(held);

    release();
    await until(() => heldStatusVersion() === moved, "the read's stamp to be observed");
    expect(statusReads()).toHaveLength(1);
  });
});
