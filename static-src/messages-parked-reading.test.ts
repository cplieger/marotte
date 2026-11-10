// A Reading reader parked inside a live turn comes back to the same text, after the unpark rebuilds the turn's body
// (an open turn's bindings were disposed at park) with entries that arrived meanwhile. Real store, renderer, scroll
// owner and the shipped stylesheet, so the rows the rebuild replaces have production's sizes.
import { beforeEach, describe, expect, it } from "vitest";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
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

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

mountAppCSS();
// The phone's scrollport, which the app shell provides.
const shell = document.createElement("style");
shell.textContent = `#messages-wrap-outer { height: 700px; width: 390px; display: flex; flex-direction: column; }
#messages-wrap { flex: 1; min-height: 0; overflow-y: auto; }`;
document.head.appendChild(shell);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");

messages.mountChatView();

let n = 0;
const freshID = (p: string): string => `${p}-${String(++n)}`;

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

/** One open turn of `entries`: runs of two tool calls, which render as a collapsed group, between text rows. */
function liveChat(id: string, entries: number): Session {
  const s = { ...makeSession({ id, name: id }), thinking: true };
  const t = `${id}-t1`;
  const list: Entry[] = [
    sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "go" }, source: "prompt", n: 1 }),
  ];
  for (let i = 1; i <= entries; i++) {
    if (i % 3 === 0) {
      list.push(sealed(t, i, "text", { text: `words ${String(i)} `.repeat(1 + (i % 7) * 12) }));
    } else {
      const call = `${t}-c${String(i)}`;
      const payload = {
        id: call,
        title: `run ${call}`,
        kind: "execute",
        status: "completed",
        ts: i + 1,
      };
      list.push({ ...sealed(t, i, "tool_call", payload), id: call });
    }
  }
  s.turns.set(t, { entries: list, openEntries: new Map() });
  s.turn_order.push(t);
  s.turn_count = 1;
  return s;
}

async function frames(k: number): Promise<void> {
  for (let i = 0; i < k; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  }
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
});

describe("a park and unpark while Reading inside the open turn", () => {
  it.each([700, 2900, 6000])(
    "brings back the tool group header that was on the reading line %ipx above the end",
    async (up) => {
      const a = freshID("c-park");
      const b = freshID("c-park");
      store.setSessions([liveChat(a, 400), liveChat(b, 3)]);
      store.setActive(a);
      store.bumpMessages(a, "load");
      await frames(20);
      const wrap = scroll.getScrollEl();
      const box = wrap.getBoundingClientRect();
      const line = box.top + wrap.clientHeight / 3;
      const group = [
        ...document.querySelectorAll<HTMLElement>(
          ".transcript-view.is-active .tool-group-collapsed",
        ),
      ]
        .filter((g) => g.getBoundingClientRect().top < line - up)
        .at(-1);
      const seq = group?.querySelector<HTMLElement>("[data-entry-seq]")?.dataset["entrySeq"];
      if (group === undefined || seq === undefined) {
        throw new Error(`no collapsed tool group ${String(up)}px above the line`);
      }
      // The reader's wheel move puts the group's header just above the reading line.
      wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
      const top = wrap.scrollTop + group.getBoundingClientRect().top - line + 10;
      wrap.scrollTo({ top, behavior: "instant" });
      wrap.dispatchEvent(new Event("scroll"));
      await frames(10);
      // Found by its entry: the rebuild replaces the group's element.
      const header = (): number | null => {
        const row = wrap.querySelector(`.transcript-view.is-active [data-entry-seq="${seq}"]`);
        const h = row?.closest(".tool-group")?.querySelector(".tool-group-header") ?? null;
        return h === null ? null : h.getBoundingClientRect().top - box.top;
      };
      const before = header();
      expect({ state: scroll.readingState(), shown: before !== null }, "the premise").toEqual({
        state: "reading",
        shown: true,
      });

      store.setActive(b);
      await Promise.resolve();
      await frames(4);
      const t = `${a}-t1`;
      for (let i = 401; i <= 403; i++) {
        store.appendEntry(a, sealed(t, i, "thinking", { text: `late ${String(i)}` }));
      }
      await Promise.resolve();
      store.setActive(a);
      await Promise.resolve();
      await frames(20);

      const after = header();
      expect(after === null || before === null ? null : Math.abs(after - before)).toBeLessThan(1);
    },
  );
});
