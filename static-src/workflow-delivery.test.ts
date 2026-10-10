import { describe, it, expect, afterEach } from "vitest";
import { buildSteerNote, type SteerNoteData } from "./fundamentals/steer-note.js";
import type { Entry, TurnState } from "./types.js";
import {
  groupWorkflowUpdates,
  releaseWorkflowTakeUps,
  settleWorkflowMessages,
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
  it("names the step on the launching chat's side and the parent on the step's", () => {
    expect(label(note({ step: "review" }))).toBe("review");
    expect(label(note({}))).toBe("To the parent agent");
    expect(label(note({ origin: "parent", step: "review" }))).toBe("To review");
    expect(label(note({ origin: "parent" }))).toBe("From the parent agent");
  });

  it("states when it was sent and when it was delivered, or that it waits", () => {
    expect(times(note({ step: "review", sentAt: SENT }))).toBe(
      `Sent ${clock(SENT)} \u00b7 not delivered yet`,
    );
    const delivered = note({ step: "review", sentAt: SENT, deliveredAt: LATER });
    expect(times(delivered)).toBe(`Sent ${clock(SENT)} \u00b7 delivered ${clock(LATER)}`);
    expect(delivered.dataset["delivered"]).toBe("true");
  });

  it("tells a take-up apart from its send within the same minute, and keeps each instant", () => {
    const row = note({ step: "review", sentAt: SENT, deliveredAt: SENT + 50_000 });
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
    entry("wfmsg-a", "steer", { text: "done", origin: "step", state: "", produced_ts: SENT });

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

  it("writes a clear as the dropped state with no read time", () => {
    const r = row();
    settleWorkflowMessages([
      turn(
        r,
        entry("wfmsg-a:delivered", "steer_delivered", {
          steer_id: "wfmsg-a",
          read_ts: LATER,
          dropped: true,
        }),
      ),
    ]);
    expect(r.payload).toEqual({
      text: "done",
      origin: "step",
      state: "dropped",
      reason: "boundary",
      produced_ts: SENT,
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
    expect(r.payload).toEqual({ text: "done", origin: "step", state: "", produced_ts: SENT });
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

  it("folds consecutive step updates under one toggle naming the count and the runs", () => {
    const rows = [
      note({ step: "a", run: "wf_1" }),
      note({ step: "b", run: "wf_1" }),
      note({ step: "c", run: "wf_2" }),
    ];
    const c = container(document.createElement("p"), ...rows);
    groupWorkflowUpdates(c);

    const toggles = c.querySelectorAll(".steer-group-toggle");
    expect(toggles).toHaveLength(1);
    expect(toggles[0]?.textContent).toBe("3 updates from 2 workflows");
    expect(toggles[0]?.nextElementSibling).toBe(rows[0]);
    expect(toggles[0]?.getAttribute("aria-expanded")).toBe("true");
    expect(rows.every((r) => !r.hidden)).toBe(true);
  });

  it("closes a group that is not the tail, and the toggle opens it", () => {
    const rows = [note({ step: "a", run: "wf_1" }), note({ step: "b", run: "wf_1" })];
    const c = container(...rows, document.createElement("p"));
    groupWorkflowUpdates(c);
    expect(rows.every((r) => r.hidden)).toBe(true);

    c.querySelector<HTMLButtonElement>(".steer-group-toggle")?.click();

    expect(rows.every((r) => !r.hidden)).toBe(true);
    expect(c.querySelectorAll(".steer-group-toggle")).toHaveLength(1);
    expect(c.querySelector(".steer-group-toggle")?.textContent).toBe("2 updates from 1 workflow");
  });

  it("leaves a lone update, a parent's message and the user's words ungrouped", () => {
    const c = container(
      note({ step: "a", run: "wf_1" }),
      note({ origin: "parent", step: "a", run: "wf_1" }),
      note({ origin: "user" }),
    );
    groupWorkflowUpdates(c);
    expect(c.querySelector(".steer-group-toggle")).toBeNull();
  });
});
