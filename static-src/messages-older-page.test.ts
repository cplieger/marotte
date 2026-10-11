// An older page landing through the real paint: the reader's position must survive it whatever moved the scroller
// while the page was in flight. Real scroll, layout and anchoring, because the drift the pass corrects is measured.
import { describe, it, expect, vi, beforeEach } from "vitest";
import { makeSession } from "./__test-helpers__/model.js";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";

// The DOM the import graph resolves at load, nested as the page nests it.
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
outer.style.cssText = "position:relative;";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.style.cssText = "height:300px;overflow-y:auto;position:relative;";
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
wrap.appendChild(messagesEl);
outer.appendChild(wrap);
document.body.appendChild(outer);
for (const [id, tag] of [
  ["chat-view", "div"],
  ["scroll-bottom", "button"],
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  if (id === "scroll-bottom") {
    e.appendChild(document.createElement("span"));
  }
  document.body.appendChild(e);
}
const style = document.createElement("style");
style.textContent = `.turn{block-size:200px}`;
document.head.appendChild(style);

// The rail's turn index is a network read; an empty one keeps it out of the scene.
const { apiGetMock } = vi.hoisted(() => ({ apiGetMock: vi.fn() }));
vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  apiGet: apiGetMock,
}));

const store = await import("./store.js");
const scroll = await import("./scroll.js");
const messages = await import("./messages.js");

messages.mountChatView();

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

/** Seat whole turns on a session, in front or behind, the shape a page GET lands. */
function seat(s: Session, ns: readonly number[], at: "front" | "back"): void {
  const ids: string[] = [];
  for (const n of ns) {
    const id = `t${String(n)}`;
    s.turns.set(id, {
      entries: [
        sealed(id, 0, "turn_open", {
          prompt: { id: `${id}-p`, text: `prompt ${id}` },
          source: "prompt",
          n,
        }),
        sealed(id, 1, "text", { text: `reply ${String(n)}` }),
        sealed(id, 2, "turn_close", { outcome: "completed" }),
      ],
      openEntries: new Map(),
    });
    ids.push(id);
  }
  if (at === "front") {
    s.turn_order.unshift(...ids);
  } else {
    s.turn_order.push(...ids);
  }
}

async function until(pred: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + FRAME_BUDGET_MS;
  while (!pred()) {
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await new Promise((r) => setTimeout(r, 8));
  }
}

async function land(): Promise<void> {
  await new Promise((r) => setTimeout(r, 120));
}

function readerScrollTo(top: number): void {
  wrap.dispatchEvent(new WheelEvent("wheel", { deltaY: top < wrap.scrollTop ? -1 : 1 }));
  wrap.scrollTop = top;
}

function cardFor(turnID: string): HTMLElement {
  const card = messages
    .activeTranscriptView()
    ?.querySelector<HTMLElement>(`:scope > [data-reconcile-key="${turnID}"]`);
  if (card === null || card === undefined) {
    throw new Error(`no card for ${turnID}`);
  }
  return card;
}

beforeEach(() => {
  messages.teardownAll();
  store.setActive("");
  store.setSessions([]);
  apiGetMock.mockImplementation(() => Promise.resolve({ turns: [] } as never));
});

describe("an older page landing through the paint", () => {
  it("leaves the reader where they scrolled to while the page was in flight", async () => {
    const s = makeSession({ id: "c1", name: "c1", turn_count: 9, has_more: true });
    seat(s, [4, 5, 6, 7, 8, 9], "back");
    store.setSessions([s]);
    store.setActive("c1");
    await until(
      () => messages.activeTranscriptView()?.querySelector(".turn") !== null,
      "the first paint",
    );
    await land();
    const load = vi.fn();
    scroll.setLoadMore(load, true);
    readerScrollTo(400);
    await land();
    readerScrollTo(60);
    await land();
    expect(load).toHaveBeenCalledTimes(1);
    readerScrollTo(30);
    await land();
    const was = cardFor("t4").getBoundingClientRect().top;

    // As `loadMessages` and `chat.ts` land it: the page through one `load` bump, then the skeleton dropped.
    seat(s, [1, 2, 3], "front");
    store.bumpMessages("c1", "load");
    scroll.rebaseLoadMore();
    document.getElementById("load-more-skeleton")?.remove();
    await land();

    expect(cardFor("t4").getBoundingClientRect().top).toBe(was);
  });
});
