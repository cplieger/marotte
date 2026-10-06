// A fake tab server: holds the collection, mints opaque ids, bumps the version and emits the
// `tabs_changed` frames, so suites exercise the REAL `tabs.ts`, `tabs-sync.ts` and
// `actions/tabs.ts`. Shared so every suite agrees on the INTERLEAVING, which `mode` sets:
//   - "event-first" (default): the frame lands BEFORE the response resolves.
//   - "response-first": the response resolves first, the frame a macrotask later (the
//     common production order).
//   - "manual": nothing is emitted until `flushFrames()`, for cases about the gap itself.

import { vi } from "vitest";

import type { TabKind, TabList, TabSubject, TabsChangedPayload } from "../types.js";

/** The two entry points of the sync layer this harness drives. Handed in, not imported: the
 *  `api-client.js` mock's factory imports THIS module, so a static `../tabs-sync.js` import
 *  deadlocks the mocker silently and the run never starts. */
export interface SyncSeam {
  ingest: (frame: TabsChangedPayload) => void;
  /** `unknown`, so the real `listTabs` (which reports whether it adopted) is accepted. */
  list: () => Promise<unknown>;
}

let sync: SyncSeam | null = null;

/** Wire the harness to the real sync layer. Called once per suite, at module
 *  scope, after the mocks are declared. */
export function bindTabsSync(seam: SyncSeam): void {
  sync = seam;
}

function ingest(frame: TabsChangedPayload): void {
  if (sync === null) {
    throw new Error("tabs-server: bindTabsSync was never called");
  }
  sync.ingest(frame);
}

export type Interleaving = "event-first" | "response-first" | "manual";

interface Sent {
  type: string;
  payload: Record<string, unknown>;
}

interface State {
  subjects: TabSubject[];
  version: number;
  nextID: number;
  mode: Interleaving;
  /** Frames the harness has committed but not yet handed to the sync layer. */
  pending: TabsChangedPayload[];
  /** Every command the projection dispatched, in order. */
  sent: Sent[];
  /** Command types the next dispatch of which fails, with the status to use. */
  failures: { type: string; status: number; error: string }[];
  /** Answers `GET /api/tabs` gives, queued. Falls through to the live set. */
  listAnswers: (TabList | null)[];
  listCalls: number;
  /** Releasers for responses being HELD, or null when responses resolve at once. */
  held: (() => void)[] | null;
}

const state: State = {
  subjects: [],
  version: 0,
  nextID: 0,
  mode: "event-first",
  pending: [],
  sent: [],
  failures: [],
  listAnswers: [],
  listCalls: 0,
  held: null,
};

export const tabServer = {
  /** Drop every trace of a previous case. Call FIRST in a beforeEach, before
   *  `_resetForTest()` re-registers the projection. */
  reset(mode: Interleaving = "event-first"): void {
    state.subjects = [];
    state.version = 0;
    state.nextID = 0;
    state.mode = mode;
    state.pending = [];
    state.sent = [];
    state.failures = [];
    state.listAnswers = [];
    state.listCalls = 0;
    state.held = null;
  },

  /** Switch the interleaving mid-case. */
  setMode(mode: Interleaving): void {
    state.mode = mode;
  },

  /** Hand every committed-but-unemitted frame to the sync layer, oldest first. */
  flushFrames(): void {
    const frames = state.pending;
    state.pending = [];
    for (const frame of frames) {
      ingest(frame);
    }
  },

  /** Committed frames not yet handed to the sync layer. Zero after an event-first open means the
   *  row landed BEFORE the response resolved. */
  pendingCount(): number {
    return state.pending.length;
  },

  /** Hold every response until `releaseResponses`, so two dispatches are IN FLIGHT at once: the
   *  only window the framework's `dedupe` covers (it evicts its slot in the result's `finally`). */
  holdResponses(): void {
    state.held = [];
  },

  /** Resolve every held response, oldest first. */
  releaseResponses(): void {
    const held = state.held ?? [];
    state.held = null;
    for (const release of held) {
      release();
    }
  },

  /** Feed ONE frame the harness never committed: another device's mutation, a
   *  frame at a version the collection never reached, an order that omits a tab.
   *  Carries no `op_id`, so the projection reads it as remote. */
  emitRaw(frame: TabsChangedPayload): void {
    ingest(frame);
  },

  /** The collection as the server holds it. */
  subjects(): readonly TabSubject[] {
    return state.subjects;
  },

  version(): number {
    return state.version;
  },

  /** The id the server minted for a subject, or "" — the harness's own view,
   *  independent of what the projection believes. */
  idFor(kind: TabKind, ref = ""): string {
    return state.subjects.find((s) => s.kind === kind && s.ref === ref)?.id ?? "";
  },

  /** Every command the projection dispatched. */
  sent(): readonly Sent[] {
    return state.sent;
  },

  sentOfType(type: string): readonly Sent[] {
    return state.sent.filter((c) => c.type === type);
  },

  /** Make the next dispatch of `type` fail. Consumed by that dispatch. */
  failNext(type: string, status = 500, error = "nope"): void {
    state.failures.push({ type, status, error });
  },

  /** Queue an answer for the next `GET /api/tabs`. `null` is the unreachable
   *  case. Unqueued reads answer with the live collection. */
  queueList(answer: TabList | null): void {
    state.listAnswers.push(answer);
  },

  listCalls(): number {
    return state.listCalls;
  },

  /** Put a subject in the collection WITHOUT a mutation, then adopt the whole
   *  set through a real `GET /api/tabs`. The boot read, and the shortest way to
   *  arrange a strip a case wants to start from. */
  async seed(...specs: readonly SeedSpec[]): Promise<void> {
    for (const spec of specs) {
      commitOpen(spec);
    }
    state.pending = [];
    if (sync === null) {
      throw new Error("tabs-server: bindTabsSync was never called");
    }
    await sync.list();
  },

  /** Open a tab the way ANOTHER DEVICE does and withhold its frame, so a `reorder` sees a moved
   *  set and answers 409. `flushFrames` lets the projection catch up instead. */
  openElsewhere(spec: SeedSpec): TabSubject {
    return commitOpen(spec);
  },

  /** Close a tab the way ANOTHER device does: commit it and emit a frame with no
   *  `op_id`, so the projection runs its local cleanup and dispatches nothing. */
  closeRemotely(id: string): void {
    const removed = descendants(id);
    if (removed.length === 0) {
      return;
    }
    state.subjects = state.subjects.filter((s) => !removed.includes(s.id));
    state.version++;
    ingest({ removed_ids: removed, version: state.version });
  },
};

export interface SeedSpec {
  kind: TabKind;
  ref?: string;
  parent?: string;
  owns?: boolean;
  pinned?: boolean;
}

function mint(): string {
  state.nextID++;
  // Opaque: a case reads an id back through `tabIdFor`.
  return `tb_${String(state.nextID).padStart(3, "0")}`;
}

function subjectFor(kind: TabKind, ref: string): TabSubject | undefined {
  return state.subjects.find((s) => s.kind === kind && s.ref === ref);
}

/** Append a subject at its canonical position: a child after its parent's
 *  existing children, a top-level tab at the end. Mirrors the store's Open. */
function commitOpen(spec: SeedSpec): TabSubject {
  const ref = spec.ref ?? "";
  const parent = spec.parent ?? "";
  const subject: TabSubject = {
    id: mint(),
    kind: spec.kind,
    ref,
    // A parent that is not open promotes the tab to top level (the server's rule).
    parent: parent !== "" && state.subjects.some((s) => s.id === parent) ? parent : "",
    pinned: spec.pinned ?? false,
    owns: spec.owns ?? true,
  };
  if (subject.parent === "") {
    state.subjects.push(subject);
  } else {
    let at = state.subjects.findIndex((s) => s.id === subject.parent) + 1;
    while (at < state.subjects.length && state.subjects[at]?.parent === subject.parent) {
      at++;
    }
    state.subjects.splice(at, 0, subject);
  }
  state.version++;
  state.pending.push({ changed: subject, version: state.version });
  return subject;
}

/** An id and every tab beneath it, deepest first, which is the order a close
 *  commits them in. */
function descendants(id: string): string[] {
  if (!state.subjects.some((s) => s.id === id)) {
    return [];
  }
  const out: string[] = [];
  const walk = (parent: string): void => {
    for (const child of state.subjects.filter((s) => s.parent === parent)) {
      walk(child.id);
    }
    out.push(parent);
  };
  walk(id);
  return out;
}

interface SendResultLike {
  ok: boolean;
  status: number;
  error?: string;
  body?: unknown;
}

/** Answer one command against the collection, and schedule its frame per the
 *  current interleaving. */
function handle(type: string, payload: Record<string, unknown>): SendResultLike {
  const failAt = state.failures.findIndex((f) => f.type === type);
  if (failAt >= 0) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
    const failure = state.failures[failAt]!;
    state.failures.splice(failAt, 1);
    return { ok: false, status: failure.status, error: failure.error };
  }
  switch (type) {
    case "open_tab": {
      const kind = payload["kind"] as TabKind;
      const ref = (payload["ref"] as string | undefined) ?? "";
      const existing = subjectFor(kind, ref);
      if (existing !== undefined) {
        // Commits nothing, so it emits NOTHING: `created: false` is the caller's only signal.
        return {
          ok: true,
          status: 200,
          body: { subject: existing, created: false, version: state.version },
        };
      }
      const subject = commitOpen({
        kind,
        ref,
        parent: (payload["parent"] as string | undefined) ?? "",
        owns: payload["owns"] === true,
      });
      stamp(payload);
      return {
        ok: true,
        status: 200,
        body: { subject, created: true, version: state.version },
      };
    }
    case "close_tab": {
      const removed = descendants(payload["id"] as string);
      if (removed.length === 0) {
        // Two devices can close one tab: the EMPTY list is the client's confirmation of absence.
        return { ok: true, status: 200, body: { closed: [], version: state.version } };
      }
      state.subjects = state.subjects.filter((s) => !removed.includes(s.id));
      state.version++;
      state.pending.push({ removed_ids: removed, version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { closed: removed, version: state.version } };
    }
    case "pin_tab": {
      const id = payload["id"] as string;
      const pinned = payload["pinned"] === true;
      const at = state.subjects.findIndex((s) => s.id === id);
      if (at < 0) {
        // A pin is a statement ABOUT a tab, so an unknown id is a mistake, not a race.
        return { ok: false, status: 404, error: "no such tab" };
      }
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
      const before = state.subjects[at]!;
      if (before.pinned === pinned) {
        return { ok: true, status: 200, body: { version: state.version } };
      }
      const after: TabSubject = { ...before, pinned };
      state.subjects[at] = after;
      state.version++;
      state.pending.push({ changed: after, version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { version: state.version } };
    }
    case "reparent_tab": {
      const id = payload["id"] as string;
      const parent = payload["parent"] as string;
      const at = state.subjects.findIndex((s) => s.id === id);
      if (at < 0) {
        // Same as a pin: an unknown id is a mistake, not a race.
        return { ok: false, status: 404, error: "no such tab" };
      }
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- guarded by the index check
      const before = state.subjects[at]!;
      if (before.parent === parent) {
        // Commits nothing and emits no frame, so the response carries the subject.
        return { ok: true, status: 200, body: { subject: before, version: state.version } };
      }
      const host = state.subjects.find((s) => s.id === parent);
      if (host === undefined || host.kind !== "chat") {
        return { ok: false, status: 409, error: "the parent must be an open chat tab" };
      }
      if (descendants(id).includes(parent)) {
        return { ok: false, status: 409, error: "that would make a cycle" };
      }
      const after: TabSubject = { ...before, parent };
      // The row MOVES to the child-insert position (the store's rule), hence the frame's `order`.
      state.subjects.splice(at, 1);
      let to = state.subjects.findIndex((s) => s.id === parent) + 1;
      while (to < state.subjects.length && state.subjects[to]?.parent === parent) {
        to++;
      }
      state.subjects.splice(to, 0, after);
      state.version++;
      state.pending.push({
        changed: after,
        order: state.subjects.map((s) => s.id),
        version: state.version,
      });
      stamp(payload);
      return { ok: true, status: 200, body: { subject: after, version: state.version } };
    }
    case "reorder_tabs": {
      const order = payload["order"] as string[];
      const held = state.subjects.map((s) => s.id);
      const exact =
        order.length === held.length &&
        new Set(order).size === order.length &&
        order.every((id) => held.includes(id));
      if (!exact) {
        // The exact-set check is the whole precondition: 409 means re-list, never re-send.
        return { ok: false, status: 409, error: "set moved" };
      }
      state.subjects = order.map(
        // eslint-disable-next-line @typescript-eslint/no-non-null-assertion -- exactness checked above
        (id) => state.subjects.find((s) => s.id === id)!,
      );
      state.version++;
      state.pending.push({ order: [...order], version: state.version });
      stamp(payload);
      return { ok: true, status: 200, body: { version: state.version } };
    }
    default:
      return { ok: true, status: 200, body: { ok: true } };
  }
}

/** Put the dispatching device's `op_id` on the frame this command committed, so
 *  the projection can tell its own echo from another device's. */
function stamp(payload: Record<string, unknown>): void {
  const opID = payload["op_id"];
  const frame = state.pending[state.pending.length - 1];
  if (frame !== undefined && typeof opID === "string") {
    frame.op_id = opID;
  }
}

/** The `transport.js` mock: `send` answers tab commands off the collection, `newOpID` mints the
 *  correlation id. Other commands answer a bare success, since none is the subject. */
export function tabTransportMock(): {
  send: (cmd: { type: string; payload?: unknown }) => Promise<SendResultLike>;
  newOpID: () => string;
  newMessageID: () => string;
  newRequestID: () => string;
  markHydrated: () => void;
  init: () => void;
} {
  let ops = 0;
  return {
    send: vi.fn((cmd: { type: string; payload?: unknown }) => {
      const payload = (cmd.payload ?? {}) as Record<string, unknown>;
      state.sent.push({ type: cmd.type, payload });
      const before = state.pending.length;
      const result = handle(cmd.type, payload);
      const committed = state.pending.slice(before);
      const deliver = (): void => {
        state.pending = state.pending.filter((f) => !committed.includes(f));
        for (const frame of committed) {
          ingest(frame);
        }
      };
      if (committed.length > 0 && state.mode === "event-first") {
        deliver();
      } else if (committed.length > 0 && state.mode === "response-first") {
        setTimeout(deliver, 0);
      }
      const answer = answerWith(result);
      return answer;
    }),
    newOpID: vi.fn(() => {
      ops++;
      return `op-${String(ops)}`;
    }),
    newMessageID: vi.fn(() => "m-test"),
    newRequestID: vi.fn(() => "r-test"),
    markHydrated: vi.fn(),
    init: vi.fn(),
  };
}

/** The `api-client.js` mock's `apiGetTyped`: `GET /api/tabs` answers a queued
 *  list when a case arranged one (a stale snapshot, an unreachable endpoint),
 *  otherwise the live collection at its current version. */
export function tabListRead(): (path: string) => Promise<TabList | null> {
  return vi.fn((path: string) => {
    if (path !== "/api/tabs") {
      return Promise.resolve(null);
    }
    state.listCalls++;
    const queued = state.listAnswers.shift();
    if (queued !== undefined) {
      return Promise.resolve(queued);
    }
    return Promise.resolve({ tabs: state.subjects.map((s) => ({ ...s })), version: state.version });
  });
}

/** Resolve a response now, or park it until `releaseResponses` when a case is
 *  arranging two concurrent dispatches. */
function answerWith(result: SendResultLike): Promise<SendResultLike> {
  const held = state.held;
  if (held === null) {
    return Promise.resolve(result);
  }
  return new Promise<SendResultLike>((resolve) => {
    held.push(() => {
      resolve(result);
    });
  });
}

/** A subject built by hand, for the frames a test feeds through `emitRaw`. */
export function fakeSubject(id: string, over: Partial<TabSubject> = {}): TabSubject {
  return { id, kind: "chat", ref: id, parent: "", pinned: false, owns: true, ...over };
}

/** Let every microtask and the response-first `setTimeout(0)` run. */
export function settleTabs(): Promise<void> {
  return new Promise<void>((resolve) => {
    setTimeout(resolve, 0);
  });
}
