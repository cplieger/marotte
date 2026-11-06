// A full pass re-renders only the cards whose inputs moved: a chat-level fact (the working label, a steer row) that
// no card shows costs no per-card work, while a fact every card shows (busy, which disables Rewind) still reaches all
// of them. The per-card count is the cost a long chat pays per streamed frame.

import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

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

const store = await import("./store.js");
const header = await import("./fundamentals/turn-header.js");
const messages = await import("./messages.js");

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

function seed(s: Session): void {
  store.setSessions([s]);
  store.setActive(s.id);
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

  it("a busy flip still reaches every card's Rewind", async () => {
    const id = `c-busy-${String(++seq)}`;
    seed(longChat(id, CARDS));
    await flushed();
    // Every turn but the newest has a later prompt to rewind to.
    expect(rewindButtons(id)).toHaveLength(CARDS - 1);
    expect(rewindButtons(id).every((b) => !b.disabled)).toBe(true);

    store.setThinking(id, true);
    await flushed();

    expect(
      rewindButtons(id).every((b) => b.disabled),
      "Rewind disabled on every card",
    ).toBe(true);
  });
});
