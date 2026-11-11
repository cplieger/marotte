import { describe, it, expect, vi, beforeEach } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type { TurnSummary } from "./rail-merge.js";
import type { Session } from "./types.js";
import type * as ApiClient from "./api-client.js";
import type * as StoreLoad from "./store-load.js";

// Where a keyboard reader on the map lands when the map is handed to another chat. The real
// stylesheet, so a row's box and the tab stop are the shipped ones.

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

const COUNTS: Record<string, number> = { "c-a": 40, "c-b": 30, "c-empty": 0 };

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

describe("a re-point under a keyboard reader on the map", () => {
  const stop = (chat: string): HTMLAnchorElement | undefined =>
    linksOf(chat).find((a) => a.getAttribute("tabindex") === "0");

  it("keeps them on the map, on the new chat's tab stop", async () => {
    await show("c-a");
    stop("c-a")?.focus();
    expect(document.activeElement, "the premise: a row of c-a holds focus").toBe(stop("c-a"));

    await show("c-b");

    expect(document.activeElement).toBe(stop("c-b"));
  });

  it("moves them to the transcript when the new chat's map has no rows", async () => {
    await show("c-a");
    stop("c-a")?.focus();
    expect(document.activeElement?.closest(".turn-map"), "the premise").not.toBeNull();

    await show("c-empty");

    expect(document.activeElement).toBe(wrap);
  });
});
