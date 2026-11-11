// Find in chat over item hits, in the real transcript: the user's own query in any order, the reveal of the current
// hit's containers only, their restore on a step and on close, the pause before a search and Enter's search at once.

import { describe, it, expect, afterAll, afterEach, beforeAll, beforeEach, vi } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import type { TurnState } from "./types.js";
import type { Entry, Hit, SearchResult } from "./wire/types.gen.js";
import type * as ApiClient from "./api-client.js";

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: vi.fn(),
}));

for (const id of [
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "find-btn",
  "shell-panel",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  document.body.appendChild(d);
}
const scrollerEl = document.createElement("div");
scrollerEl.id = "messages-wrap";
document.getElementById("messages-wrap-outer")?.appendChild(scrollerEl);
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
scrollerEl.appendChild(messagesEl);

const { setSessions, setActive, setPainted, bumpMessages } = await import("./store.js");
const { mountChatView, teardownAll } = await import("./messages.js");
const { scrollToBottom, readingState } = await import("./scroll.js");
const { restAtLiveEdge } = await import("./__test-helpers__/scroll-rest.js");
const { resetFoldState } = await import("./fold-state.js");
const { KEY_ATTR } = await import("./reconcile.js");
const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { handleFindHotkey, closeChatFind } = await import("./find-in-chat.js");
const { CHAT_SEARCH_DEBOUNCE_MS } = await import("./chat-search.js");
const api = await import("./api-client.js");

const VIEWPORT_PX = 720;

function at(turn: string, seq: number, kind: Entry["kind"], payload: unknown, id?: string): Entry {
  return { id: id ?? `${turn}-e${String(seq)}`, turn, kind, seq, ts: seq + 1, payload } as Entry;
}

/** A call and its settled result, so the card's output is the result's. */
function tool(turn: string, seq: number, title: string, output: string): Entry[] {
  const id = `${turn}-e${String(seq)}`;
  return [
    at(turn, seq, "tool_call", {
      id: `${id}-tc`,
      title,
      kind: "execute",
      status: "in_progress",
      ts: 1,
    }),
    at(turn, seq + 1, "tool_result", { status: "completed", output }, `${id}:result`),
  ];
}

const FILLER = "a sentence that wraps across the column more than once ".repeat(8);

/** Three turns: the two older ones fold, the newest stays open. t2's and t3's hits sit in a tool group that
 *  collapses because prose follows it, and inside the card's own disclosure. */
function chatTurns(): Map<string, TurnState> {
  const t1 = [
    at("t1", 0, "turn_open", { prompt: { id: "t1-p", text: "first" }, source: "prompt", n: 1 }),
    ...tool("t1", 1, "Read notes", "nothing here"),
    at("t1", 3, "text", {
      text: `${FILLER}\n\nA security failure hit deadset at night. Later: failure security.`,
    }),
    at("t1", 4, "turn_close", { outcome: "completed" }),
  ];
  const t2 = [
    at("t2", 0, "turn_open", { prompt: { id: "t2-p", text: "second" }, source: "prompt", n: 2 }),
    ...tool("t2", 1, "Read notes", "nothing here"),
    ...tool("t2", 3, "Run checks", "the deadset run logged a security failure"),
    at("t2", 5, "text", { text: `${FILLER}\n\ndone with t2` }),
    at("t2", 6, "turn_close", { outcome: "completed" }),
  ];
  const t3 = [
    at("t3", 0, "turn_open", { prompt: { id: "t3-p", text: "third" }, source: "prompt", n: 3 }),
    at("t3", 1, "text", { text: "deadset security notes" }),
    ...tool("t3", 2, "Read logs", "nothing here"),
    ...tool("t3", 4, "Run again", "failure security deadset again"),
    at("t3", 6, "text", { text: "the end" }),
    at("t3", 7, "turn_close", { outcome: "completed" }),
  ];
  return new Map(
    [t1, t2, t3].map((entries) => [entries[0]?.turn ?? "", { entries, openEntries: new Map() }]),
  );
}

function hitOn(
  turn: string,
  n: number,
  entry: string,
  kind: Hit["kind"],
  segments: Hit["segments"],
): Hit {
  return { turn_id: turn, entry_id: entry, kind, excerpt: "", segments, turn: n };
}

const H1 = hitOn("t1", 1, "t1-e3", "message", ["content"]);
const H2 = hitOn("t2", 2, "t2-e3", "tool", ["tool_output"]);
const H3 = hitOn("t3", 3, "t3-e4", "tool", ["tool_output"]);

/** The server's reply, through the caller's decoder so it is held to the wire shape. */
function stageServerHits(hits: Hit[], spread = false): void {
  const reply: SearchResult = {
    matches: hits,
    spread,
    scanned: 20,
    matched: hits.length,
    truncated: false,
  };
  vi.mocked(api.apiGetTyped).mockImplementation(((path: string, decode: (v: unknown) => unknown) =>
    Promise.resolve(path.includes("/search?q=") ? decode(reply) : null)) as typeof api.apiGetTyped);
}

function searchFetches(): number {
  return vi
    .mocked(api.apiGetTyped)
    .mock.calls.filter(([path]) => String(path).includes("/search?q=")).length;
}

function card(turn: string): HTMLElement {
  const c = messagesEl.querySelector<HTMLElement>(`.turn[${KEY_ATTR}="${turn}"]`);
  if (c === null) {
    throw new Error(`no card for turn ${turn}`);
  }
  return c;
}

function folded(turn: string): boolean {
  return card(turn).hasAttribute("data-folded");
}

function entryEl(id: string): HTMLElement {
  const found = messagesEl.querySelector<HTMLElement>(
    `[data-entry-id="${id}"], [data-entries~="${id}"]`,
  );
  if (found === null) {
    throw new Error(`entry ${id} is not mounted`);
  }
  return found;
}

function groupOf(id: string): HTMLElement {
  const g = entryEl(id).closest<HTMLElement>(".tool-group");
  if (g === null) {
    throw new Error(`entry ${id} is in no tool group`);
  }
  return g;
}

function input(): HTMLInputElement {
  return document.getElementById("chat-find-input") as HTMLInputElement;
}

function countText(): string {
  return document.getElementById("chat-find-count")?.textContent ?? "";
}

function openFind(): void {
  handleFindHotkey(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true }));
}

function press(shiftKey = false): void {
  input().dispatchEvent(
    new KeyboardEvent("keydown", { key: "Enter", shiftKey, bubbles: true, cancelable: true }),
  );
}

/** The marks of the current hit, by text. */
function currentMarks(): string[] {
  return [...messagesEl.querySelectorAll("mark.find-hit-current")].map((m) => m.textContent);
}

function currentUnit(): HTMLElement | null {
  return (
    messagesEl.querySelector("mark.find-hit-current")?.closest<HTMLElement>("[data-find-unit]") ??
    null
  );
}

async function load(chat: string, turns = chatTurns()): Promise<void> {
  const order = [...turns.keys()];
  setSessions([
    {
      ...makeSession({ id: chat, name: chat }),
      turns,
      turn_order: order,
      turn_count: order.length,
    },
  ]);
  setActive(chat);
  setPainted(chat);
  bumpMessages(chat, "load");
  await vi.waitFor(() => {
    // Every turn but the newest folds, and the newest's last entry is mounted.
    const last = turns.get(order.at(-1) ?? "")?.entries.at(-2);
    entryEl(last?.id ?? "");
    for (const turn of order.slice(0, -1)) {
      expect(folded(turn)).toBe(true);
    }
  });
  // The reader at the live edge, where a chat opens.
  await restAtLiveEdge(scrollerEl, () => {
    scrollToBottom();
  });
}

/** Search `query` with Enter and wait for the landing on a hit in `turn`. */
async function searchAndLand(query: string, turn: string): Promise<void> {
  openFind();
  input().value = query;
  press();
  await landedIn(turn);
}

async function landedIn(turn: string): Promise<void> {
  await vi.waitFor(() => {
    const unit = currentUnit();
    expect(unit?.closest(`.turn[${KEY_ATTR}]`)?.getAttribute(KEY_ATTR)).toBe(turn);
    expect(unit?.querySelector("mark.find-hit-current")?.checkVisibility()).toBe(true);
  });
}

let style: HTMLStyleElement;

beforeAll(async () => {
  style = mountAppCSS();
  const outer = document.getElementById("messages-wrap-outer");
  if (outer !== null) {
    outer.style.height = `${String(VIEWPORT_PX)}px`;
  }
  await emulateA11yMedia({ reducedMotion: "reduce" });
});

afterAll(async () => {
  style.remove();
  await resetA11yMedia();
});

beforeEach(() => {
  teardownAll();
  mountChatView();
  localStorage.clear();
  resetFoldState();
  setSessions([]);
  setActive("");
  setPainted("");
  vi.mocked(api.apiGetTyped).mockReset();
});

afterEach(() => {
  closeChatFind();
  vi.useRealTimers();
});

describe("find in chat matches every word of the query in one item", () => {
  it("lands on the nearest item holding all three words in another order, and marks each word", async () => {
    await load("c-q-any-order");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");

    expect(currentUnit()).toBe(entryEl("t3-e4"));
    expect(
      currentMarks()
        .map((t) => t.toLowerCase())
        .sort(),
    ).toEqual(["deadset", "failure", "security"]);
    expect(countText()).toBe("3 of 3");
    expect(folded("t1")).toBe(true);
    expect(folded("t2")).toBe(true);
  });

  it("marks nothing in a message holding only two of the words", async () => {
    await load("c-q-two-words");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");

    expect(entryEl("t3-e1").querySelector("mark.find-hit")).toBeNull();
  });

  it("marks a quoted phrase only where its words are in order", async () => {
    await load("c-q-phrase");
    stageServerHits([H1]);
    await searchAndLand('"security failure"', "t1");

    expect([...messagesEl.querySelectorAll("mark.find-hit")].map((m) => m.textContent)).toEqual([
      "security failure",
    ]);
  });
});

describe("find in chat reveals the current hit only", () => {
  it("opens a collapsed turn holding the hit and shows the word, and opens no other hit's turn", async () => {
    await load("c-q-reveal");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    press();
    await landedIn("t1");

    expect(folded("t1")).toBe(false);
    expect(folded("t2")).toBe(true);
    expect(currentUnit()).toBe(entryEl("t1-e3"));
  });

  it("re-folds the previous hit's turn and tool group when stepping on", async () => {
    await load("c-q-step");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(false);
    press();
    await landedIn("t1");
    press();
    await landedIn("t2");

    await vi.waitFor(() => {
      expect(folded("t1")).toBe(true);
      expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(true);
    });
    expect(groupOf("t2-e3").classList.contains("tool-group-collapsed")).toBe(false);
  });

  it("re-folds the previous hit's turn at once while the reader is reading, and on close", async () => {
    await load("c-q-reading");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    press();
    await landedIn("t1");
    // The reader's own wheel: the controller reads from here on, and defers a fold pass until they stop.
    scrollerEl.dispatchEvent(new WheelEvent("wheel", { deltaY: -1, bubbles: true }));
    expect(readingState()).toBe("reading");
    press();
    await landedIn("t2");
    expect(folded("t1")).toBe(true);

    closeChatFind();
    expect(folded("t2")).toBe(true);
  });

  it("leaves a tool group as the reader toggled it during the search", async () => {
    await load("c-q-touched");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    const header = groupOf("t3-e4").querySelector<HTMLElement>(":scope > .tool-group-header");
    // The keyboard, since a click outside find closes it.
    for (let i = 0; i < 2; i++) {
      header?.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
      );
    }
    expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(false);
    press();
    await landedIn("t1");

    expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(false);
  });

  it("closes the tool group it opened when find closes", async () => {
    await load("c-q-untouched");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    closeChatFind();

    await vi.waitFor(() => {
      expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(true);
    });
  });

  it("restores the open chain when find closes, and keeps a turn the reader opened by hand", async () => {
    await load("c-q-close");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    press();
    await landedIn("t1");
    card("t2").querySelector<HTMLButtonElement>(".turn-fold-toggle")?.click();
    await vi.waitFor(() => {
      expect(folded("t2")).toBe(false);
    });

    closeChatFind();
    await vi.waitFor(() => {
      expect(folded("t1")).toBe(true);
    });
    expect(folded("t2")).toBe(false);
    expect(groupOf("t3-e4").classList.contains("tool-group-collapsed")).toBe(true);
  });

  it("holds the current hit in place while the previous hit's turn above it folds", async () => {
    await load("c-q-anchor");
    stageServerHits([H1, H2, H3]);
    await searchAndLand("deadset security failure", "t3");
    press();
    await landedIn("t1");
    press();
    await landedIn("t2");
    const top = (): number =>
      messagesEl.querySelector("mark.find-hit-current")?.getBoundingClientRect().top ?? Number.NaN;
    const before = top();
    // Every frame for 400 ms, past the fold's transition, so a fold landing after the jump is seen.
    const drift: number[] = [];
    const start = performance.now();
    while (performance.now() - start < 400) {
      await new Promise((r) => requestAnimationFrame(r));
      drift.push(Math.abs(top() - before));
    }
    expect(folded("t1")).toBe(true);
    expect(Math.max(...drift)).toBeLessThanOrEqual(1);
  });

  it("holds the current hit in place while a tool group above it in the same turn closes", async () => {
    // One open turn, taller than the view on both sides of its hits, so a landing has room to centre and to hold: a
    // tool group holding a hit, then the message holding the next.
    const t = "t1";
    const turns = new Map<string, TurnState>([
      [
        t,
        {
          entries: [
            at(t, 0, "turn_open", { prompt: { id: "t1-p", text: "only" }, source: "prompt", n: 1 }),
            at(t, 1, "text", { text: `${FILLER}\n\n${FILLER}\n\n${FILLER}` }),
            ...tool(t, 2, "Read notes", "nothing here"),
            ...tool(t, 4, "Run checks", "the deadset run logged a security failure"),
            at(t, 6, "text", { text: "security failure in deadset" }),
            ...tool(t, 7, "Read more", "nothing here"),
            at(t, 9, "text", { text: `${FILLER}\n\n${FILLER}\n\n${FILLER}` }),
            at(t, 10, "turn_close", { outcome: "completed" }),
          ],
          openEntries: new Map(),
        },
      ],
    ]);
    await load("c-q-anchor-group", turns);
    stageServerHits([
      hitOn(t, 1, "t1-e4", "tool", ["tool_output"]),
      hitOn(t, 1, "t1-e6", "message", ["content"]),
    ]);
    await searchAndLand("deadset security failure", t);
    if (currentUnit() !== entryEl("t1-e4")) {
      press();
    }
    await vi.waitFor(() => {
      expect(currentUnit()).toBe(entryEl("t1-e4"));
      expect(groupOf("t1-e4").classList.contains("tool-group-collapsed")).toBe(false);
    });
    // With motion, so the group's close tweens while and after the flight to the next hit.
    await emulateA11yMedia({ reducedMotion: "no-preference" });
    try {
      press();
      await vi.waitFor(() => {
        expect(currentUnit()).toBe(entryEl("t1-e6"));
      });
      const mid = (r: DOMRect): number => r.top + r.height / 2;
      const off = (): number =>
        mid(
          messagesEl.querySelector("mark.find-hit-current")?.getBoundingClientRect() ??
            new DOMRect(),
        ) - mid(scrollerEl.getBoundingClientRect());
      // Past the flight, the tween and the hold.
      const settled: number[] = [];
      const start = performance.now();
      while (performance.now() - start < 1200) {
        await new Promise((r) => requestAnimationFrame(r));
        if (performance.now() - start > 1000) {
          settled.push(off());
        }
      }
      expect(groupOf("t1-e4").classList.contains("tool-group-collapsed")).toBe(true);
      // Where the flight aimed it, the view's middle, as the group's height left the content above it.
      expect(Math.max(...settled.map(Math.abs))).toBeLessThanOrEqual(1);
    } finally {
      await emulateA11yMedia({ reducedMotion: "reduce" });
    }
  });
});

describe("find in chat waits for a pause in typing", () => {
  it("searches once the reader stops typing, and at once on Enter", async () => {
    await load("c-q-debounce");
    stageServerHits([H3]);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    openFind();
    for (const value of ["d", "de", "dead", "deadset"]) {
      input().value = value;
      input().dispatchEvent(new Event("input", { bubbles: true }));
      vi.advanceTimersByTime(100);
    }
    const typed = searchFetches();
    vi.advanceTimersByTime(CHAT_SEARCH_DEBOUNCE_MS - 101);
    expect(searchFetches()).toBe(typed);
    vi.advanceTimersByTime(1);
    expect(searchFetches()).toBe(typed + 1);

    input().value = "deadset security";
    input().dispatchEvent(new Event("input", { bubbles: true }));
    press();
    expect(searchFetches()).toBe(typed + 2);
    vi.advanceTimersByTime(CHAT_SEARCH_DEBOUNCE_MS);
    expect(searchFetches()).toBe(typed + 2);
  });

  it("searches at once on Enter while the chat streams, and shows that search's hit", async () => {
    await load("c-q-streaming");
    // A standing search, settled before the clock is faked.
    stageServerHits([H3]);
    await searchAndLand("deadset security", "t3");
    // Past the landing's own walks, whose writes a mutation observed with them would be taken for.
    await new Promise((r) => setTimeout(r, 200));
    stageServerHits([H1]);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const before = searchFetches();
    input().value = "deadset security failure";
    input().dispatchEvent(new Event("input", { bubbles: true }));
    // A streamed chunk lands inside the typing pause, and its re-run fires before the pause ends.
    entryEl("t3-e6").append(" and more");
    await Promise.resolve();
    vi.advanceTimersByTime(150);
    // The re-run refreshed the search that stands, not the words typed since.
    expect(
      [...messagesEl.querySelectorAll("mark.find-hit")].some((m) => /failure/i.test(m.textContent)),
    ).toBe(false);
    expect(searchFetches()).toBe(before);

    press();
    expect(searchFetches()).toBe(before + 1);
    vi.advanceTimersByTime(CHAT_SEARCH_DEBOUNCE_MS);
    expect(searchFetches()).toBe(before + 1);
    vi.useRealTimers();
    // The Enter search's own answer: t1, which the standing one never named.
    await landedIn("t1");
    expect(folded("t1")).toBe(false);
  });

  it("pauses for 300 ms", () => {
    expect(CHAT_SEARCH_DEBOUNCE_MS).toBe(300);
  });
});
