// A full pass re-renders only the cards whose inputs moved: a chat-level fact (the working label, a steer row) that
// no card shows costs no per-card work, while a fact every card shows (busy, which disables Rewind) still reaches all
// of them. The per-card count is the cost a long chat pays per streamed frame.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { toolResultID } from "./entry-ids.js";
import { KEY_ATTR } from "./reconcile.js";
import { runAt, turnEntry } from "./__test-helpers__/run-order.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  if (id === "messages-wrap") {
    document.getElementById("messages-wrap-outer")?.appendChild(d);
  } else if (id === "messages") {
    document.getElementById("messages-wrap")?.appendChild(d);
  } else {
    document.body.appendChild(d);
  }
}

vi.mock("./fundamentals/turn-header.js", { spy: true });
vi.mock("./fundamentals/turn-footer.js", { spy: true });

const store = await import("./store.js");
const header = await import("./fundamentals/turn-header.js");
const footerMod = await import("./fundamentals/turn-footer.js");
const hookFold = await import("./hook-fold.js");
const messages = await import("./messages.js");
const folds = await import("./fold-state.js");

messages.mountChatView();

const CARDS = 40;
let seq = 0;

function entry(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

/** `count` sealed prompt turns, each answered and closed, the shape a page GET lands. */
function longChat(id: string, count: number): Session {
  const s = makeSession({ id, name: id });
  for (let n = 1; n <= count; n++) {
    const t = `${id}-t${String(n)}`;
    s.turns.set(t, {
      entries: [
        entry(t, 0, "turn_open", {
          prompt: { id: `${t}-p`, text: `ask ${String(n)}` },
          source: "prompt",
          n,
        }),
        entry(t, 1, "text", { text: `answer ${String(n)}` }),
        entry(t, 2, "turn_close", { outcome: "completed" }),
      ],
      openEntries: new Map(),
      closeAt: 2,
    });
    s.turn_order.push(t);
  }
  s.turn_count = count;
  return s;
}

/** A `Run Workflow` call and its result naming `runID`, added to the turn at `at`; the first such call hosts the card. */
function withRun(s: Session, runID: string, at = 0): Session {
  const t = s.turn_order[at]!;
  const held = s.turns.get(t)!;
  const call = `${t}-launch`;
  held.entries.splice(
    2,
    0,
    {
      id: call,
      turn: t,
      kind: "tool_call",
      seq: 2,
      ts: 3,
      payload: {
        id: `tool-${runID}`,
        title: "Run Workflow",
        status: "completed",
        kind: "other",
        ts: 3,
      },
    },
    {
      id: toolResultID(call),
      turn: t,
      kind: "tool_result",
      seq: 3,
      ts: 4,
      payload: { status: "completed", workflow_id: runID },
    },
  );
  const close = held.entries[4]!;
  held.entries[4] = { ...close, seq: 4 };
  held.closeAt = 4;
  return s;
}

function seed(s: Session): void {
  store.setSessions([s]);
  store.setActive(s.id);
  store.setPainted(s.id);
  store.bumpMessages(s.id, "load");
}

function rewindButtons(chatID: string): HTMLButtonElement[] {
  const view = messages.transcriptViewFor(chatID);
  return [...(view?.querySelectorAll<HTMLButtonElement>(".turn-rewind") ?? [])];
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

function headerUpdates(): number {
  return vi.mocked(header.updateTurnHeader).mock.calls.length;
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

describe("a full pass updates only the cards whose inputs changed", () => {
  it("a working-label change updates no card", async () => {
    const id = `c-label-${String(++seq)}`;
    seed(longChat(id, CARDS));
    await flushed();
    const before = headerUpdates();

    store.setWorkingLabel(id, "Reading files");
    await flushed();

    expect(store.renderCauseOf(id).cause, "the bump was a full-pass fact").toBe("fact");
    expect(headerUpdates() - before, "header updates for a label no card shows").toBe(0);
  });

  it("an entry appended to the newest turn updates that card alone", async () => {
    const id = `c-append-${String(++seq)}`;
    const s = longChat(id, CARDS);
    const tail = s.turn_order[s.turn_order.length - 1]!;
    // The newest turn still running, so a sealed entry can land in it.
    const held = s.turns.get(tail)!;
    held.entries.pop();
    delete held.closeAt;
    seed(s);
    await flushed();
    const before = headerUpdates();

    store.appendEntry(id, entry(tail, 2, "text", { text: "more" }));
    await flushed();

    expect(headerUpdates() - before, "header updates for one turn's new entry").toBe(1);
  });

  it("switching to another chat and back updates no card of the first", async () => {
    const first = withRun(longChat(`c-switch-a-${String(++seq)}`, CARDS), "wf-a");
    const second = withRun(longChat(`c-switch-b-${String(seq)}`, CARDS), "wf-b");
    store.setSessions([first, second]);
    store.setActive(first.id);
    store.setPainted(first.id);
    store.bumpMessages(first.id, "load");
    await flushed();
    store.setActive(second.id);
    store.setPainted(second.id);
    store.bumpMessages(second.id, "load");
    await flushed();
    const before = headerUpdates();

    store.setActive(first.id);
    store.setPainted(first.id);
    await flushed();

    expect(messages.transcriptViewFor(first.id)?.querySelectorAll(".turn")).toHaveLength(CARDS);
    expect(headerUpdates() - before, "header updates on returning to an unchanged chat").toBe(0);
  });

  it("a run's card moves to the next mention when the launch's turn leaves", async () => {
    const id = `c-owner-${String(++seq)}`;
    const s = withRun(withRun(longChat(id, CARDS), "wf-moved"), "wf-moved", 1);
    const [launch, mention] = [s.turn_order[0]!, s.turn_order[1]!];
    // The reader opened both, so both bodies render: a folded card draws no run card at all.
    folds.setTurnOpen(id, launch, true);
    folds.setTurnOpen(id, mention, true);
    seed(s);
    await flushed();
    const runCard = (turnID: string): Element | null | undefined =>
      messages
        .transcriptViewFor(id)
        ?.querySelector(`.turn[${KEY_ATTR}="${turnID}"] .run-card[data-run="wf-moved"]`);
    expect(runCard(mention), "a later mention draws no card while the launch is held").toBeNull();

    s.turns.delete(launch);
    s.turn_order.shift();
    store.bumpMessages(id, "shape");
    await flushed();

    expect(runCard(mention), "the first mention left hosts the card").not.toBeNull();
  });

  it("a hook visibility change reaches the counts of a card holding a quiet hook", async () => {
    const id = `c-hooks-${String(++seq)}`;
    const s = longChat(id, CARDS);
    const t = s.turn_order[0]!;
    const held = s.turns.get(t)!;
    held.entries.splice(2, 0, {
      id: `${t}-hook`,
      turn: t,
      kind: "tool_call",
      seq: 2,
      ts: 3,
      payload: { id: `${t}-hook-call`, title: "lint", status: "completed", kind: "hook", ts: 3 },
    });
    held.entries[3] = { ...held.entries[3]!, seq: 3 };
    held.closeAt = 3;
    seed(s);
    await flushed();
    const footer = messages
      .transcriptViewFor(id)
      ?.querySelector(`.turn[${KEY_ATTR}="${t}"] > .turn-footer`);
    // The data the footer was last drawn from, by its builder or its updater.
    const hookCount = (): number | undefined => {
      const built = vi.mocked(footerMod.buildTurnFooter).mock;
      const updated = vi.mocked(footerMod.updateTurnFooter).mock;
      const draws = [
        ...built.calls.flatMap(([d], i) =>
          built.results[i]?.value === footer ? [{ d, at: built.invocationCallOrder[i]! }] : [],
        ),
        ...updated.calls.flatMap(([f, d], i) =>
          f === footer ? [{ d, at: updated.invocationCallOrder[i]! }] : [],
        ),
      ].sort((a, b) => a.at - b.at);
      return draws.at(-1)?.d.kindCounts?.hook;
    };
    expect(footer, "the hook card earned a footer").not.toBeNull();
    expect(hookCount(), "a quiet hook is counted with the switch on").toBe(1);

    hookFold.setHookStatusShown(false);
    await flushed();
    const hidden = hookCount();
    hookFold.setHookStatusShown(true);
    await flushed();

    expect(hidden, "and not counted with it off").toBeUndefined();
    expect(hookCount(), "and counted again with it on").toBe(1);
  });

  it("a busy flip still reaches every card's Rewind", async () => {
    const id = `c-busy-${String(++seq)}`;
    seed(longChat(id, CARDS));
    await flushed();
    // Every turn but the newest has a later prompt to rewind to.
    expect(rewindButtons(id)).toHaveLength(CARDS - 1);
    expect(rewindButtons(id).every((b) => !b.disabled)).toBe(true);

    store.applyRun(id, runAt(1, [turnEntry("t-live")], { cancel: "t-live" }));
    await flushed();

    expect(
      rewindButtons(id).every((b) => b.disabled),
      "Rewind disabled on every card",
    ).toBe(true);
  });
});

// A geometry read after a DOM write forces a synchronous layout, so a pass reading once per card lays the whole
// transcript out once per card: the 1.7 s freezes a long chat showed while it streamed.
describe("a full pass reads no geometry per card", () => {
  function geometryReads(): { count: () => number; stop: () => void } {
    const rect = vi.spyOn(Element.prototype, "getBoundingClientRect");
    const rects = vi.spyOn(Element.prototype, "getClientRects");
    return {
      count: () => rect.mock.calls.length + rects.mock.calls.length,
      stop: () => {
        rect.mockRestore();
        rects.mockRestore();
      },
    };
  }

  it("for a fact no card shows", async () => {
    const id = `c-geo-label-${String(++seq)}`;
    seed(longChat(id, CARDS));
    await flushed();
    const reads = geometryReads();
    store.setWorkingLabel(id, "Reading files");
    await flushed();
    const n = reads.count();
    reads.stop();
    expect(n).toBe(0);
  });

  it("for a fact every card shows", async () => {
    const id = `c-geo-busy-${String(++seq)}`;
    seed(longChat(id, CARDS));
    await flushed();
    const reads = geometryReads();
    store.applyRun(id, runAt(1, [turnEntry("t-live")], { cancel: "t-live" }));
    await flushed();
    const n = reads.count();
    reads.stop();
    expect(
      rewindButtons(id).every((b) => b.disabled),
      "the pass reached every card",
    ).toBe(true);
    expect(n, "geometry reads in a pass over every card").toBeLessThan(CARDS);
  });
});
