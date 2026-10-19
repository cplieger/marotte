import { describe, it, expect, afterEach } from "vitest";
import { buildSteerNote, type SteerNoteData } from "./fundamentals/steer-note.js";
import type { Entry, EntrySteer, TurnState } from "./types.js";
import {
  groupWorkflowUpdates,
  releaseWorkflowTakeUps,
  settleWorkflowMessages,
  turnsInOrder,
} from "./workflow-delivery.js";

const SENT = Date.UTC(2026, 9, 9, 12, 0, 5);
const LATER = SENT + 5 * 60_000;

function clock(ms: number): string {
  return new Date(ms).toLocaleTimeString(undefined, {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function note(over: Partial<SteerNoteData>): HTMLElement {
  return buildSteerNote({ text: "part 1 done", origin: "step", dropped: false, ...over });
}

function label(root: HTMLElement): string {
  return root.querySelector(".steer-note-label")?.textContent ?? "";
}

function times(root: HTMLElement): string {
  return root.querySelector(".steer-note-times")?.textContent ?? "";
}

const roots: HTMLElement[] = [];
afterEach(() => {
  for (const r of roots.splice(0)) {
    r.remove();
  }
});

describe("a workflow message's row", () => {
  it("names the step on the launching chat's side and the main agent on the step's", () => {
    expect(label(note({ step: "review" }))).toBe("review");
    expect(label(note({}))).toBe("Message to the main agent");
    expect(label(note({ origin: "parent", step: "review" }))).toBe("To review");
    expect(label(note({ origin: "parent" }))).toBe("Message from the main agent");
  });

  it("states when it was sent and when it was delivered, or that it waits", () => {
    expect(times(note({ step: "review", sentAt: SENT, tracked: true }))).toBe(
      `Sent ${clock(SENT)} \u00b7 not delivered yet`,
    );
    const delivered = note({ step: "review", sentAt: SENT, deliveredAt: LATER, tracked: true });
    expect(times(delivered)).toBe(`Sent ${clock(SENT)} \u00b7 delivered ${clock(LATER)}`);
    expect(delivered.dataset["delivered"]).toBe("true");
  });

  it("tells a take-up apart from its send within the same minute, and keeps each instant", () => {
    const row = note({ step: "review", sentAt: SENT, deliveredAt: SENT + 50_000, tracked: true });
    const stamps = [...row.querySelectorAll<HTMLTimeElement>(".steer-note-times time")];
    expect(stamps.map((t) => t.dateTime)).toEqual([
      "2026-10-09T12:00:05.000Z",
      "2026-10-09T12:00:55.000Z",
    ]);
    expect(stamps[0]?.textContent).not.toBe(stamps[1]?.textContent);
    expect(stamps[1]?.dataset["tooltip"]).toBe(new Date(SENT + 50_000).toLocaleString());
  });

  it("says once that a message KAS cleared unread was not delivered", () => {
    const row = note({ step: "review", sentAt: SENT, dropped: true, reason: "boundary" });
    expect(label(row)).toBe("review, not delivered \u00b7 the turn ended first");
    expect(times(row)).toBe(`Sent ${clock(SENT)}`);
    expect(row.dataset["delivered"]).toBe("false");
  });

  it("says Cleared, neutrally, for a message its buffer was emptied of before anything proved it read", () => {
    const row = note({ step: "review", sentAt: SENT, tracked: true, cleared: true });
    expect(times(row)).toBe(`Sent ${clock(SENT)} \u00b7 Cleared`);
    expect(label(row)).toBe("review");
    expect(row.dataset["state"]).toBe("cleared");
    expect(row.dataset["delivered"]).toBe("false");
    expect(row.getAttribute("aria-label")).toBe(
      "review \u00b7 Cleared: the turn was stopped before this was confirmed read: part 1 done",
    );
  });

  it("states only when a KAS notice was sent, since it was logged when read and has no take-up to wait for", () => {
    const row = note({ sentAt: SENT });
    expect(times(row)).toBe(`Sent ${clock(SENT)}`);
    expect(row.dataset["delivered"]).toBeUndefined();
  });

  it("carries no times when it is no workflow message", () => {
    expect(note({ origin: "user" }).querySelector(".steer-note-times")).toBeNull();
  });
});

function turn(...entries: Entry[]): TurnState {
  return { entries, openEntries: new Map() };
}

function entry(id: string, kind: Entry["kind"], payload: unknown): Entry {
  return { id, turn: "t", kind, seq: 0, ts: 0, payload } as Entry;
}

describe("settling a resident row", () => {
  const row = (): Entry =>
    entry("wfmsg-a", "steer", { text: "done", origin: "step", produced_ts: SENT });

  it("writes a later turn's take-up onto the row's own entry", () => {
    const r = row();
    settleWorkflowMessages([
      turn(r),
      turn(entry("wfmsg-a:delivered", "steer_delivered", { steer_id: "wfmsg-a", read_ts: LATER })),
    ]);
    expect(r.payload).toEqual({
      text: "done",
      origin: "step",
      state: "read",
      produced_ts: SENT,
      read_ts: LATER,
    });
  });

  it("writes a clear as cleared, with no state and no read time", () => {
    const r = row();
    settleWorkflowMessages([
      turn(
        r,
        entry("wfmsg-a:delivered", "steer_delivered", {
          steer_id: "wfmsg-a",
          read_ts: LATER,
          cleared: true,
        }),
      ),
    ]);
    expect(r.payload).toEqual({
      text: "done",
      origin: "step",
      produced_ts: SENT,
      cleared: true,
    });
  });

  it("returns a row to waiting when the turn holding its take-up is released", () => {
    const r = entry("wfmsg-a", "steer", {
      text: "done",
      origin: "step",
      state: "read",
      produced_ts: SENT,
      read_ts: LATER,
    });
    const takeUp = turn(
      entry("wfmsg-a:delivered", "steer_delivered", { steer_id: "wfmsg-a", read_ts: LATER }),
    );
    releaseWorkflowTakeUps([takeUp], [turn(r)]);
    expect(r.payload).toEqual({ text: "done", origin: "step", produced_ts: SENT });
  });

  it("returns a cleared row to waiting when the turn holding its clear is released", () => {
    const r = row();
    const clear = turn(
      entry("wfmsg-a:delivered", "steer_delivered", {
        steer_id: "wfmsg-a",
        read_ts: LATER,
        cleared: true,
      }),
    );
    settleWorkflowMessages([turn(r), clear]);
    expect(r.payload).toMatchObject({ cleared: true });

    releaseWorkflowTakeUps([clear], [turn(r)]);

    expect(r.payload).toEqual({ text: "done", origin: "step", produced_ts: SENT });
  });

  const sentTo = (id: string, session?: string): Entry =>
    entry(id, "steer", {
      text: id,
      origin: "step",
      produced_ts: SENT,
      ...(session === undefined ? {} : { to_session: session }),
    });
  const clearOf = (id: string, session: string): Entry => ({
    ...entry(id, "steer_cleared", { cause: "boundary", session, ids: [] }),
    ts: LATER,
  });
  const cleared = (id: string): EntrySteer => ({
    text: id,
    origin: "step",
    produced_ts: SENT,
    to_session: "sess-a",
    cleared: true,
  });

  it("settles from a clear's fact alone each row sent to its session before it", () => {
    const before = sentTo("wfmsg-before", "sess-a");
    const other = sentTo("wfmsg-other", "sess-b");
    const sessionless = sentTo("wfmsg-sessionless");
    const after = sentTo("wfmsg-after", "sess-a");
    const untouched = [other, sessionless, after].map((e) => e.payload);

    settleWorkflowMessages([
      turn(before, other, sessionless),
      turn(clearOf("cleared-1", "sess-a"), after),
    ]);

    expect(before.payload).toEqual(cleared("wfmsg-before"));
    expect([other, sessionless, after].map((e) => e.payload)).toEqual(untouched);
  });

  it("keeps a take-up that came before the clear, and lets a later one replace the clear", () => {
    const early = sentTo("wfmsg-early", "sess-a");
    const late = sentTo("wfmsg-late", "sess-a");
    const takeUp = (id: string): Entry =>
      entry(`${id}:delivered`, "steer_delivered", { steer_id: id, read_ts: SENT + 1 });

    settleWorkflowMessages([
      turn(
        early,
        late,
        takeUp("wfmsg-early"),
        clearOf("cleared-1", "sess-a"),
        takeUp("wfmsg-late"),
      ),
    ]);

    for (const r of [early, late]) {
      expect(r.payload).toMatchObject({ state: "read", read_ts: SENT + 1 });
      expect(r.payload).not.toHaveProperty("cleared");
    }
  });

  it("returns the rows a released clear settled to waiting and keeps an earlier held clear's", () => {
    const first = sentTo("wfmsg-first", "sess-a");
    const second = sentTo("wfmsg-second", "sess-a");
    const held = turn(first, clearOf("cleared-1", "sess-a"), second);
    const released = turn(clearOf("cleared-2", "sess-a"));
    settleWorkflowMessages([held, released]);
    expect(second.payload).toEqual(cleared("wfmsg-second"));

    releaseWorkflowTakeUps([released], [held]);

    expect(first.payload).toEqual(cleared("wfmsg-first"));
    expect(second.payload).toEqual({
      text: "wfmsg-second",
      origin: "step",
      produced_ts: SENT,
      to_session: "sess-a",
    });
  });

  it("reads turns in the order their ids name, then any held turn the order leaves out", () => {
    const a = turn();
    const b = turn();
    const c = turn();
    const held = new Map([
      ["b", b],
      ["c", c],
      ["a", a],
    ]);
    expect(turnsInOrder(["a", "b", "missing"], held)).toEqual([a, b, c]);
  });

  it("leaves a row no take-up names", () => {
    const r = row();
    const before = r.payload;
    settleWorkflowMessages([
      turn(r, entry("wfmsg-b:delivered", "steer_delivered", { steer_id: "wfmsg-b", read_ts: 1 })),
    ]);
    expect(r.payload).toBe(before);
  });
});

describe("the update group", () => {
  function container(...rows: HTMLElement[]): HTMLElement {
    const c = document.createElement("div");
    c.append(...rows);
    document.body.append(c);
    roots.push(c);
    return c;
  }

  function box(c: HTMLElement): HTMLElement | null {
    return c.querySelector<HTMLElement>(":scope > .steer-group");
  }

  function header(c: HTMLElement): HTMLElement | null {
    return c.querySelector<HTMLElement>(":scope > .steer-group > .steer-group-header");
  }

  function body(c: HTMLElement): HTMLElement | null {
    return c.querySelector<HTMLElement>(":scope > .steer-group > .steer-group-body");
  }

  it("moves consecutive step updates into one box whose header names the count and the runs", () => {
    const rows = [
      note({ step: "a", run: "wf_1" }),
      note({ step: "b", run: "wf_1" }),
      note({ step: "c", run: "wf_2" }),
    ];
    const lead = document.createElement("p");
    const c = container(lead, ...rows);
    groupWorkflowUpdates(c);

    expect(c.querySelectorAll(".steer-group")).toHaveLength(1);
    expect(box(c)?.previousElementSibling).toBe(lead);
    expect(header(c)?.textContent).toBe("3 updates from 2 workflows");
    expect(header(c)?.getAttribute("role")).toBe("button");
    expect(header(c)?.getAttribute("aria-expanded")).toBe("true");
    expect([...(body(c)?.children ?? [])]).toEqual(rows);
    expect(c.querySelector(":scope > button")).toBeNull();
  });

  it("closes a box that is not the tail, and its header opens it", () => {
    const rows = [note({ step: "a", run: "wf_1" }), note({ step: "b", run: "wf_1" })];
    const c = container(...rows, document.createElement("p"));
    groupWorkflowUpdates(c);
    expect(header(c)?.getAttribute("aria-expanded")).toBe("false");
    expect(body(c)?.getAttribute("aria-hidden")).toBe("true");

    header(c)?.click();

    expect(header(c)?.getAttribute("aria-expanded")).toBe("true");
    expect(body(c)?.getAttribute("aria-hidden")).toBe("false");
    expect(rows[0]?.dataset["groupOpen"], "the pick rides the lead row").toBe("true");
  });

  it("keeps a seated row where it is on the next pass, and takes a later update in", () => {
    const rows = [note({ step: "a", run: "wf_1" }), note({ step: "b", run: "wf_1" })];
    const c = container(...rows);
    groupWorkflowUpdates(c);
    const held = box(c);
    const seated = rows[0]?.parentElement;
    let moved = 0;
    const watch = new MutationObserver((records) => {
      moved += records.filter((r) => r.removedNodes.length > 0).length;
    });
    if (seated !== null && seated !== undefined) {
      watch.observe(seated, { childList: true });
    }

    const later = note({ step: "c", run: "wf_1" });
    c.appendChild(later);
    groupWorkflowUpdates(c);
    watch.takeRecords();
    watch.disconnect();

    expect(box(c)).toBe(held);
    expect([...(body(c)?.children ?? [])]).toEqual([...rows, later]);
    expect(moved, "no seated row was re-inserted").toBe(0);
    expect(header(c)?.textContent).toBe("3 updates from 1 workflow");
  });

  it("hands the rows back and drops the box once its run falls under two", () => {
    const rows = [note({ step: "a", run: "wf_1" }), note({ step: "b", run: "wf_1" })];
    const c = container(...rows);
    groupWorkflowUpdates(c);
    rows[1]?.remove();

    groupWorkflowUpdates(c);

    expect(box(c)).toBeNull();
    expect([...c.children]).toEqual([rows[0]]);
  });

  it("groups a run of workflow results with the step updates, counting runs only when all name one", () => {
    const rows = [
      note({ origin: "agent", run: "wf_1" }),
      note({ origin: "agent" }),
      note({ step: "a", run: "wf_1" }),
    ];
    const c = container(...rows);
    groupWorkflowUpdates(c);
    expect([...(body(c)?.children ?? [])]).toEqual(rows);
    expect(header(c)?.textContent).toBe("3 workflow updates");
  });

  it("leaves a lone update, a parent's message and the user's words ungrouped", () => {
    const c = container(
      note({ step: "a", run: "wf_1" }),
      note({ origin: "parent", step: "a", run: "wf_1" }),
      note({ origin: "user" }),
    );
    groupWorkflowUpdates(c);
    expect(box(c)).toBeNull();
  });
});
