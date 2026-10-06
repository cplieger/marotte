// Two opposite coalescing properties. A DOUBLE gesture is one mutation (`dedupe` keyed on
// (kind, ref)). A REPEATED gesture is several: pin → unpin → pin ends pinned and A → B → A ends
// at A, so neither dedupe nor an arg-composite idempotency key may collapse the third onto the first.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Inert: present only so real-ESM linking succeeds.
  newOpID: vi.fn(() => "op-test"),
}));

vi.mock("../api-client.js", () => ({
  apiGetOrError: vi.fn(),
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Inert: present only so real-ESM linking succeeds.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { error as toastError } from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import {
  openTabCommand,
  closeTabCommand,
  pinTabCommand,
  reorderTabsCommand,
  REORDER_STALE,
} from "./tabs.js";

const mockSend = vi.mocked(transportSend);
const mockToastError = vi.mocked(toastError);

/** One sent envelope, as the assertions read it. */
interface Sent {
  type: string;
  payload: Record<string, unknown>;
}

function sent(): Sent[] {
  return mockSend.mock.calls.map((c) => c[0] as unknown as Sent);
}

/** The per-dispatch idempotency keys, in send order. */
function idempotencyKeys(): (string | undefined)[] {
  return mockSend.mock.calls.map((c) => {
    const v = (c[0] as unknown as Record<string, unknown>)["idempotency_key"];
    return typeof v === "string" ? v : undefined;
  });
}

function okWith(body: unknown): { ok: true; status: 200; body: unknown } {
  return { ok: true, status: 200, body };
}

function subject(id: string, over: Record<string, unknown> = {}): Record<string, unknown> {
  return { id, kind: "chat", ref: "c-1", parent: "", pinned: false, owns: true, ...over };
}

/** A reply held until released, so a second dispatch is IN FLIGHT: the only window `dedupe` covers. */
function heldReply(body: unknown): { release: () => void } {
  let release = (): void => {
    /* replaced by the promise below */
  };
  const held = new Promise<void>((r) => {
    release = r;
  });
  mockSend.mockImplementationOnce(async () => {
    await held;
    return okWith(body) as never;
  });
  return { release };
}

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockSend.mockResolvedValue(okWith({ ok: true }) as never);
});

describe("open_tab", () => {
  it("sends kind, ref, parent, owns and the op id", async () => {
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1"), created: true, version: 4 }) as never,
    );
    await openTabCommand.dispatch({
      kind: "editor",
      ref: "/workspace/a.ts",
      parent: "p1",
      owns: true,
      opID: "op-1",
    });
    expect(sent()[0]?.type).toBe("open_tab");
    expect(sent()[0]?.payload).toEqual({
      kind: "editor",
      op_id: "op-1",
      ref: "/workspace/a.ts",
      parent: "p1",
      owns: true,
    });
  });

  it("carries a FRESH idempotency key per dispatch, never one derived from the args", async () => {
    // An arg-composite key would replay a cached success inside the 5-minute cache; `op_id` correlates.
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: true }) as never);
    const args = { kind: "chat", ref: "c-1", parent: "", owns: true } as const;
    await openTabCommand.dispatch({ ...args, opID: "op-1" });
    await openTabCommand.dispatch({ ...args, opID: "op-2" });
    const keys = idempotencyKeys();
    expect(keys[0]).toBeTypeOf("string");
    expect(keys[0]).not.toBe(keys[1]);
  });

  it("omits ref, parent and owns rather than sending empty values", async () => {
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1", { kind: "settings", ref: "" }), created: true }) as never,
    );
    await openTabCommand.dispatch({
      kind: "settings",
      ref: "",
      parent: "",
      owns: false,
      opID: "op-2",
    });
    expect(sent()[0]?.payload).toEqual({ kind: "settings", op_id: "op-2" });
  });

  it("reports created:false for a tab that is already open", async () => {
    // Nothing committed, so NO frame follows: a caller waiting for one would wait forever.
    mockSend.mockResolvedValue(
      okWith({ subject: subject("t1"), created: false, version: 9 }) as never,
    );
    const reply = await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-3",
    });
    expect(reply?.created).toBe(false);
    expect(reply?.subject.id).toBe("t1");
    // The widened reply: the collection version rides to the pending-op machine.
    expect(reply?.version).toBe(9);
  });

  it("collapses two dispatches 0ms apart into ONE round trip", async () => {
    const { release } = heldReply({ subject: subject("t1"), created: true });
    const first = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    // The trap: the default key is safeStringify(args), so the unique op id would collapse nothing.
    const second = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    release();
    const [a, b] = await Promise.all([first, second]);
    expect(mockSend).toHaveBeenCalledTimes(1);
    // Both callers get the SAME answer, so each runs its own activation against one open.
    expect(a?.subject.id).toBe("t1");
    expect(b?.subject.id).toBe("t1");
  });

  it("does NOT collapse a different (kind, ref)", async () => {
    const { release } = heldReply({ subject: subject("t1"), created: true });
    const first = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    mockSend.mockResolvedValueOnce(okWith({ subject: subject("t2"), created: true }) as never);
    const second = openTabCommand.dispatch({
      kind: "chat",
      ref: "c-2",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    release();
    await Promise.all([first, second]);
    expect(mockSend).toHaveBeenCalledTimes(2);
  });

  it("reaches the server again once the first dispatch has resolved", async () => {
    // `dedupe` covers the IN-FLIGHT window only; outside it the server's (kind, ref) uniqueness
    // answers a late tap with the tab already open.
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: true }) as never);
    await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-a",
    });
    mockSend.mockResolvedValue(okWith({ subject: subject("t1"), created: false }) as never);
    const again = await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-b",
    });
    expect(mockSend).toHaveBeenCalledTimes(2);
    expect(again?.created).toBe(false);
  });

  it("names the remedy when the workspace is at its tab limit", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "too many tabs" } as never);
    await openTabCommand.dispatch({
      kind: "chat",
      ref: "c-1",
      parent: "",
      owns: true,
      opID: "op-4",
    });
    // The refusal has a remedy; "failed" alone reads as broken.
    expect(mockToastError.mock.calls[0]?.[0]).toContain("Close a tab first");
  });

  it("surfaces a failure and returns nothing to paint from", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" } as never);
    const out = await openTabCommand
      .dispatch({ kind: "chat", ref: "c-1", parent: "", owns: true, opID: "op-5" })
      .catch(() => null);
    expect(out).toBeNull();
    expect(mockToastError).toHaveBeenCalled();
  });
});

describe("close_tab", () => {
  it("returns every id the mutation closed, with the committed version", async () => {
    // A parent and its children close as ONE mutation, hence a list.
    mockSend.mockResolvedValue(okWith({ closed: ["p", "c1", "c2"], version: 3 }) as never);
    const reply = await closeTabCommand.dispatch({ id: "p", opID: "op-1" });
    expect(reply?.closed).toEqual(["p", "c1", "c2"]);
    expect(reply?.version).toBe(3);
  });

  it("treats an empty list as a normal answer, not a failure", async () => {
    // Two devices can close one tab: the empty list confirms absence.
    mockSend.mockResolvedValue(okWith({ closed: [], version: 3 }) as never);
    const reply = await closeTabCommand.dispatch({ id: "gone", opID: "op-2" });
    expect(reply?.closed).toEqual([]);
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("collapses two closes of the same tab", async () => {
    const { release } = heldReply({ closed: ["t1"] });
    const a = closeTabCommand.dispatch({ id: "t1", opID: "op-a" });
    const b = closeTabCommand.dispatch({ id: "t1", opID: "op-b" });
    release();
    await Promise.all([a, b]);
    expect(mockSend).toHaveBeenCalledTimes(1);
  });
});

describe("pin_tab", () => {
  it("executes a repeated pin -> unpin -> pin, all three", async () => {
    // The third gesture repeats the first: coalescing on (id, pinned) would leave it UNPINNED.
    await pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-1" });
    await pinTabCommand.dispatch({ id: "t1", pinned: false, opID: "op-2" });
    await pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-3" });
    expect(sent().map((s) => s.payload["pinned"])).toEqual([true, false, true]);
    // Its OWN idempotency key: an arg-composite key would replay the first's cached success.
    expect(idempotencyKeys()[0]).not.toBe(idempotencyKeys()[2]);
  });

  it("executes three concurrent pin gestures rather than collapsing them", async () => {
    const held: (() => void)[] = [];
    mockSend.mockImplementation(async () => {
      await new Promise<void>((r) => held.push(r));
      return okWith({ version: 1 }) as never;
    });
    const all = [
      pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-1" }),
      pinTabCommand.dispatch({ id: "t1", pinned: false, opID: "op-2" }),
      pinTabCommand.dispatch({ id: "t1", pinned: true, opID: "op-3" }),
    ];
    expect(mockSend).toHaveBeenCalledTimes(3);
    for (const release of held) {
      release();
    }
    await Promise.all(all);
    expect(sent().map((s) => s.payload["pinned"])).toEqual([true, false, true]);
  });
});

describe("reorder_tabs", () => {
  it("executes a drag A -> B -> A, all three", async () => {
    await reorderTabsCommand.dispatch({ order: ["a", "b"], opID: "op-1" });
    await reorderTabsCommand.dispatch({ order: ["b", "a"], opID: "op-2" });
    await reorderTabsCommand.dispatch({ order: ["a", "b"], opID: "op-3" });
    expect(sent().map((s) => s.payload["order"])).toEqual([
      ["a", "b"],
      ["b", "a"],
      ["a", "b"],
    ]);
    // The third order equals the first: an arg-composite key would leave the collection at B.
    expect(idempotencyKeys()[0]).not.toBe(idempotencyKeys()[2]);
  });

  it("returns the committed version", async () => {
    mockSend.mockResolvedValue(okWith({ version: 7 }) as never);
    expect(await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" })).toEqual({
      version: 7,
    });
  });

  it("reads a missing version as 0, which every watermark already covers", async () => {
    mockSend.mockResolvedValue(okWith({}) as never);
    expect(await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" })).toEqual({
      version: 0,
    });
  });

  it("reports a 409 as stale rather than as a failure", async () => {
    // The SET moved under the drag: not an error; the caller re-lists and the drag snaps back.
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "order mismatch" } as never);
    expect(await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" })).toBe(REORDER_STALE);
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("surfaces a real failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" } as never);
    await reorderTabsCommand.dispatch({ order: ["a"], opID: "op-1" }).catch(() => null);
    expect(mockToastError).toHaveBeenCalled();
  });

  it("sends the order as a plain array the caller cannot mutate afterwards", async () => {
    const order = ["a", "b"];
    await reorderTabsCommand.dispatch({ order, opID: "op-1" });
    order.push("c");
    expect(sent()[0]?.payload["order"]).toEqual(["a", "b"]);
  });
});
