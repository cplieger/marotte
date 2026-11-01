// Real store, renderer, scroll controller and shipped stylesheet: the defect is the near marking
// meeting Firefox's `content-visibility` skip, which css/13-messages.css's `[data-near]` rule owns.

import { describe, it, expect, beforeEach, afterAll } from "vitest";
import { FRAME_BUDGET_MS, testTimeoutFor } from "./__test-helpers__/frame-budget.js";
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
  const parent = {
    "messages-wrap-outer": "chat-view",
    "messages-wrap": "messages-wrap-outer",
    messages: "messages-wrap",
  }[id];
  (parent === undefined ? document.body : document.getElementById(parent))?.appendChild(d);
}

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
const appCSS = mountAppCSS();
// The app shell's geometry: a 500px transcript the turns overflow.
const shell = document.createElement("style");
shell.textContent =
  "#chat-view, #messages-wrap-outer { height: 500px; } #messages-wrap-outer { display: flex; flex-direction: column; } #messages-wrap { flex: 1; min-height: 0; }";
document.head.appendChild(shell);

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");
messages.mountChatView();

const chatView = document.getElementById("chat-view") as HTMLElement;

afterAll(() => {
  messages.teardownAll();
  appCSS.remove();
  shell.remove();
});

function sealed(
  turnID: string,
  seq: number,
  kind: Entry["kind"],
  payload: unknown,
  id?: string,
): Entry {
  return { id: id ?? `${turnID}-e${String(seq)}`, turn: turnID, kind, seq, ts: seq + 1, payload };
}

function openTurnChat(id: string): { session: Session; append: (kind: "tool" | "text") => void } {
  const session = makeSession({ id, name: id });
  const t = `${id}-t1`;
  session.thinking = true;
  session.turns.set(t, {
    entries: [
      sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "go" }, source: "prompt", n: 1 }),
    ],
    openEntries: new Map(),
  });
  session.turn_order.push(t);
  session.turn_count = 1;
  let seq = 1;
  const append = (kind: "tool" | "text"): void => {
    if (kind === "text") {
      store.appendEntry(
        id,
        sealed(t, seq++, "text", {
          text: `Prose ${String(seq)} reads across three lines. `.repeat(6),
        }),
      );
      return;
    }
    const call = `${t}-c${String(seq)}`;
    store.appendEntry(
      id,
      sealed(
        t,
        seq++,
        "tool_call",
        { id: call, title: `run ${call}`, kind: "execute", status: "completed", ts: seq },
        call,
      ),
    );
    store.appendEntry(
      id,
      sealed(t, seq++, "tool_result", { status: "completed" }, `${call}:result`),
    );
  };
  return { session, append };
}

async function frames(n: number): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  }
}

function unlaidRows(): string[] {
  const rows = messages.activeTranscriptView()?.querySelectorAll<HTMLElement>(".msg-row") ?? [];
  return [...rows]
    .filter((row) => {
      const bubble = row.firstElementChild?.getBoundingClientRect().height ?? 0;
      return bubble === 0 || Math.abs(row.getBoundingClientRect().height - bubble) > 1;
    })
    .map((row) => (row.textContent ?? "").slice(0, 12));
}

function unmarkedInPort(): string[] {
  const port = scroll.getScrollEl().getBoundingClientRect();
  const rows = messages.activeTranscriptView()?.querySelectorAll<HTMLElement>(".msg-row") ?? [];
  return [...rows]
    .filter((row) => {
      const r = row.getBoundingClientRect();
      return r.bottom > port.top && r.top < port.bottom && !row.hasAttribute("data-near");
    })
    .map((row) => (row.textContent ?? "").slice(0, 12));
}

/** A turn of prose between tool calls, long enough to overflow the scrollport. */
async function fill(append: (kind: "tool" | "text") => void): Promise<void> {
  for (let k = 0; k < 6; k++) {
    append("tool");
    append("text");
    await frames(2);
  }
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  chatView.classList.remove("hidden");
});

describe(
  "a transcript shown again after a tab switch",
  { timeout: testTimeoutFor(FRAME_BUDGET_MS) },
  () => {
    it("lays out every prose row after the chat view was hidden behind another tab", async () => {
      const { session, append } = openTurnChat("c-reshow");
      store.setSessions([session]);
      store.setActive(session.id);
      store.bumpMessages(session.id, "load");
      await frames(4);
      await fill(append);
      expect(unlaidRows(), "laid out before the switch").toEqual([]);

      // `tabs.ts` `showView` hides every other view with the `.hidden` utility.
      chatView.classList.add("hidden");
      await frames(3);
      append("tool");
      append("text");
      await frames(3);
      chatView.classList.remove("hidden");
      await frames(30);

      expect({ unlaid: unlaidRows(), unmarked: unmarkedInPort() }).toEqual({
        unlaid: [],
        unmarked: [],
      });
    });

    it("lays out every prose row after switching back to a chat the reader had scrolled up in", async () => {
      const { session, append } = openTurnChat("c-reread");
      const other = makeSession({ id: "c-other", name: "c-other" });
      store.setSessions([session, other]);
      store.setActive(session.id);
      store.bumpMessages(session.id, "load");
      await frames(4);
      await fill(append);
      // Reading: a wheel gesture moves the reader up off the live edge.
      const scroller = scroll.getScrollEl();
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -1 }));
      scroller.scrollTo({ top: scroller.scrollTop - 120, behavior: "instant" });
      scroller.dispatchEvent(new Event("scroll"));
      await frames(4);
      expect(scroll.readingState()).toBe("reading");

      store.setActive(other.id);
      await frames(4);
      append("tool");
      append("text");
      await frames(2);
      store.setActive(session.id);
      await frames(30);

      expect({ unlaid: unlaidRows(), unmarked: unmarkedInPort() }).toEqual({
        unlaid: [],
        unmarked: [],
      });
    });
  },
);
