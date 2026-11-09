import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { TurnSummary } from "./rail-merge.js";
import type { Session } from "./types.js";
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

// A re-point between chats the reader keeps open: the map of a chat shown before must come back
// as the rows it had, never a fresh mount, since styling a long map's rows costs a frame tens of
// ms in Firefox and WebKit. The real stylesheet, so where a held map sits is the shipped rule's.

vi.mock("./api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));
vi.mock("./store-load.js", async (importOriginal) => ({
  ...(await importOriginal<typeof StoreLoad>()),
  loadMessages: vi.fn(),
  loadList: vi.fn(),
}));

mountAppCSS();
document.body.style.margin = "0";

const area = document.createElement("div");
area.id = "chat-area";
area.style.cssText = "position:fixed;top:0;left:0;margin:0;width:1120px;height:700px";
const view = document.createElement("div");
view.id = "chat-view";
view.style.cssText = "display:flex;flex-direction:column;flex:1;min-height:0";
const outer = document.createElement("div");
outer.id = "messages-wrap-outer";
const wrap = document.createElement("div");
wrap.id = "messages-wrap";
wrap.tabIndex = -1;
const messagesEl = document.createElement("div");
messagesEl.id = "messages";
const filler = document.createElement("div");
filler.style.blockSize = "5000px";
messagesEl.appendChild(filler);
wrap.appendChild(messagesEl);
const resume = document.createElement("button");
resume.id = "scroll-bottom";
resume.appendChild(document.createElement("span"));
outer.append(wrap, resume);
view.appendChild(outer);
area.appendChild(view);
document.body.appendChild(area);
for (const [id, tag] of [
  ["send-btn", "button"],
  ["prompt-input", "textarea"],
] as const) {
  const e = document.createElement(tag);
  e.id = id;
  document.body.appendChild(e);
}

const rail = await import("./turn-rail.js");
const { apiGet } = await import("./api-client.js");
const { setSessions } = await import("./store.js");

rail.mountTurnRail(outer);

const COUNTS: Record<string, number> = {
  "c-a": 40,
  "c-b": 30,
  "c-c": 20,
  "c-d": 25,
  "c-e": 35,
  "c-empty": 0,
};

function turns(chat: string): TurnSummary[] {
  return Array.from({ length: COUNTS[chat] ?? 0 }, (_, i) => ({
    id: `${chat}-u${String(i + 1)}`,
    n: i + 1,
    outcome: "completed" as const,
    ts: (i + 1) * 60_000,
    first_line: `request ${String(i + 1)}`,
  }));
}

function session(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: { context_pct: 0, context_size: 0, credits: 0, last_turn_ms: 0, has_real_data: false },
    turns: new Map(),
    turn_order: [],
    turn_count: COUNTS[id] ?? 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** The row links of `chat`'s map, wherever the map holds them. */
function linksOf(chat: string): HTMLAnchorElement[] {
  return [...outer.querySelectorAll<HTMLAnchorElement>(".turn-map .turn-pill-link")].filter((a) =>
    (a.getAttribute("href") ?? "").includes(`/${chat}`),
  );
}

/** Whether the reader can see, point at, tab to or hear `link`. */
function reachable(link: HTMLAnchorElement): boolean {
  const r = link.getBoundingClientRect();
  const onScreen = r.bottom > 0 && r.top < window.innerHeight && r.height > 0;
  const hit = onScreen
    ? document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
    : null;
  return (
    (hit !== null && link.contains(hit)) ||
    link.getAttribute("tabindex") === "0" ||
    link.closest('[aria-hidden="true"]') === null
  );
}

async function settle(): Promise<void> {
  const root = outer.querySelector(".turn-map");
  let last = "";
  let stable = 0;
  for (let i = 0; i < 120 && stable < 3; i++) {
    await new Promise((r) => requestAnimationFrame(() => setTimeout(r, 0)));
    const now = root?.innerHTML ?? "";
    stable = now === last ? stable + 1 : 0;
    last = now;
  }
}

async function show(chat: string): Promise<void> {
  await rail.loadTurnRail(chat);
  await settle();
}

beforeEach(() => {
  rail.resetTurnRail();
  setSessions(Object.keys(COUNTS).map(session));
  vi.mocked(apiGet).mockImplementation((path: string) =>
    Promise.resolve({ turns: turns(/\/api\/chats\/([^/]+)\//.exec(path)?.[1] ?? "") } as never),
  );
});

afterEach(() => {
  document.querySelector(".uip-tooltip")?.remove();
});

describe("a switch back to a chat whose map was shown", () => {
  it("shows the rows it had, mounting none", async () => {
    await show("c-a");
    const before = linksOf("c-a");
    expect(before.length, "the premise: the map binned c-a's turns into rows").toBeGreaterThan(10);
    await show("c-b");

    const added: Node[] = [];
    const seen = new MutationObserver((records) => {
      for (const r of records) {
        added.push(...r.addedNodes);
      }
    });
    seen.observe(outer, { childList: true, subtree: true });
    await show("c-a");
    seen.disconnect();

    const after = linksOf("c-a").filter(reachable);
    expect(after.length, "c-a's rows show again").toBe(before.length);
    after.forEach((link, i) => {
      expect(link, `row ${String(i)} is the element it was`).toBe(before[i]);
    });
    // A re-inserted row or stack is restyled whole, which is the cost a held map exists to skip.
    const inserted = added.filter(
      (n) =>
        n instanceof Element && (n.matches(".turn-pill") || n.querySelector(".turn-pill") !== null),
    );
    expect(inserted, "no row entered the document for the switch back").toEqual([]);
  });

  it("keeps the other chat's rows out of reach while they wait", async () => {
    await show("c-a");
    await show("c-b");

    expect(linksOf("c-b").filter(reachable).length, "c-b's rows are the ones shown").toBe(
      linksOf("c-b").length,
    );
    for (const link of linksOf("c-a")) {
      expect(reachable(link), `${link.getAttribute("href") ?? ""} is out of reach`).toBe(false);
    }
  });

  it("holds the shown chat's map and three others, the least recent dropped first", async () => {
    for (const chat of ["c-a", "c-b", "c-c", "c-d", "c-e"]) {
      await show(chat);
    }
    expect(outer.querySelectorAll(".turn-map-stack").length).toBeLessThanOrEqual(4);
    expect(linksOf("c-a"), "the least recent chat's rows are gone").toEqual([]);
    expect(linksOf("c-b").length, "the next one's are still held").toBeGreaterThan(0);
  });

  it("drops a chat's rows once the chat leaves the store", async () => {
    await show("c-a");
    await show("c-b");
    setSessions(["c-b", "c-c"].map(session));
    await show("c-c");
    expect(linksOf("c-a")).toEqual([]);
  });

  it("keeps a keyboard reader on the map, on the shown chat's tab stop", async () => {
    const stop = (chat: string): HTMLAnchorElement | undefined =>
      linksOf(chat).find((a) => a.getAttribute("tabindex") === "0" && reachable(a));
    await show("c-a");
    stop("c-a")?.focus();
    expect(document.activeElement, "the premise: a row of c-a holds focus").toBe(stop("c-a"));

    await show("c-b");

    const now = document.activeElement;
    expect(now, "focus is on c-b's tab stop").toBe(stop("c-b"));
    expect(now?.closest('[aria-hidden="true"]') ?? null, "never on a held row").toBeNull();
  });

  it("moves focus to the transcript when the shown chat's map has no rows", async () => {
    await show("c-a");
    linksOf("c-a")
      .find((a) => a.getAttribute("tabindex") === "0")
      ?.focus();
    expect(document.activeElement?.closest(".turn-map"), "the premise").not.toBeNull();

    await show("c-empty");

    expect(document.activeElement).toBe(wrap);
  });
});
