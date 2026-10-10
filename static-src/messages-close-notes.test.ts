// The turn card is the one site that resolves `turn_close`'s durable half over the live one for the refusal
// callout and the licence footnote; both renderers take a resolved value, so a real paint tests the wiring.
import { beforeEach, describe, it, expect, vi } from "vitest";
import type { Session } from "./types.js";
import type { Entry } from "./wire/types.gen.js";
import { makeSession } from "./__test-helpers__/model.js";

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

// Staged, not under test: network reads, and a rewind whose action has its own suite.
const { apiGetMock, rewindMock, confirmMock } = vi.hoisted(() => ({
  apiGetMock: vi.fn(),
  rewindMock: vi.fn(),
  confirmMock: vi.fn(),
}));
vi.mock("./api-client.js", () => ({
  apiGet: apiGetMock,
  // Present-but-inert so real-ESM linking succeeds.
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
  apiGetTypedOrError: vi.fn(),
  apiGetOrError: vi.fn(),
}));
vi.mock("./store-load.js", () => ({ loadMessages: vi.fn(), loadList: vi.fn() }));
vi.mock("./actions/rewind.js", () => ({ rewindChat: { dispatch: rewindMock } }));
vi.mock("./confirm.js", () => ({ confirm: confirmMock }));

const store = await import("./store.js");
const messages = await import("./messages.js");

messages.mountChatView();

// Per test: `mockReset: true` clears implementations, so a confirm staged at load answers `undefined` from case 2.
beforeEach(() => {
  apiGetMock.mockResolvedValue({ turns: [] });
  confirmMock.mockResolvedValue(true);
});

// --- Fixtures -------------------------------------------------------------------------

function session(id: string): Session {
  return makeSession({ id, name: id, turn_count: 1 });
}

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

/** One prompted turn, its reply and its close; `close` is spread onto the `turn_close` payload. */
function turnEntries(id: string, close: Record<string, unknown> = {}): Entry[] {
  return [
    sealed(id, 0, "turn_open", {
      prompt: { id: `${id}-p`, text: `prompt ${id}` },
      source: "prompt",
      n: 1,
    }),
    sealed(id, 1, "text", { text: "I can't continue this conversation." }),
    sealed(id, 2, "turn_close", { outcome: "refused", ...close }),
  ];
}

/** A chat holding one turn; `close` undefined leaves the turn open, the state live values answer for. */
function stage(chatID: string, turnID: string, close?: Record<string, unknown>): void {
  const s = session(chatID);
  const entries =
    close === undefined ? turnEntries(turnID).slice(0, 2) : turnEntries(turnID, close);
  s.turns.set(turnID, { entries, openEntries: new Map() });
  s.turn_order.push(turnID);
  store.setSessions([s]);
  store.setActive(chatID);
}

function close(chatID: string, turnID: string, payload: Record<string, unknown>): void {
  store.appendEntry(chatID, sealed(turnID, 2, "turn_close", { outcome: "refused", ...payload }));
}

let seq = 0;
/** A fresh chat id: `setActive` is a no-op for the active id, so a reused one paints nothing. */
function nextChat(): string {
  seq += 1;
  return `cn${String(seq)}`;
}

function card(): HTMLElement {
  const root = messages.activeTranscriptView();
  const el = root?.querySelector<HTMLElement>(":scope > .turn");
  if (el === null || el === undefined) {
    throw new Error("no turn card painted");
  }
  return el;
}

/** The card's own children by class, in document order, so a case can assert the regions sit above the ledger. */
function region(cls: string): { el: HTMLElement | null; index: number } {
  const c = card();
  const el = c.querySelector<HTMLElement>(`:scope > .${cls}`);
  return { el, index: el === null ? -1 : [...c.children].indexOf(el) };
}

describe("the turn card's close-time regions", () => {
  it("mounts both off `turn_close` as card children above the ledger", () => {
    stage(nextChat(), "t1", {
      refusal: { category: "safety" },
      code_references: [{ license_name: "MIT", repository: "acme/lib" }],
    });
    const callout = region("refusal-callout");
    const refs = region("code-refs");
    const footer = region("turn-footer");
    expect(callout.el?.textContent).toContain("The model declined to continue");
    expect(callout.el?.querySelector(".refusal-chip")?.textContent).toBe("safety");
    expect(refs.el?.textContent).toContain("1 code reference");
    expect(footer.index).toBeGreaterThan(-1);
    expect(callout.index).toBeLessThan(footer.index);
    expect(refs.index).toBeLessThan(footer.index);
  });

  it("renders the store's LIVE values while the turn is open, and lets the close supersede them", async () => {
    const chat = nextChat();
    stage(chat, "t1");
    store.setLiveRefusal(chat, "t1", { category: "live-category" });
    store.setCodeReferences(chat, "t1", [{ license_name: "Apache-2.0" }]);
    expect(region("refusal-callout").el?.querySelector(".refusal-chip")?.textContent).toBe(
      "live-category",
    );
    // The refusal bumps `shape` synchronously; the attributions bump `fact`, coalesced onto a microtask, so the
    // footnote lands a tick later.
    await vi.waitFor(() => {
      expect(region("code-refs").el?.textContent).toContain("1 code reference");
    });
    close(chat, "t1", {
      refusal: { category: "durable-category" },
      code_references: [{ license_name: "MIT" }, { license_name: "BSD-3-Clause" }],
    });
    await vi.waitFor(() => {
      expect(region("code-refs").el?.textContent).toContain("2 code references");
    });
    // The wire stamps a refusal at most once per turn, so `syncRefusal` leaves a mounted callout's chip alone; the
    // claim here is only that the close does not take the callout away.
    expect(region("refusal-callout").el?.querySelector(".refusal-chip")?.textContent).toBe(
      "live-category",
    );
  });

  it("mounts neither for a turn that closed with neither fact", () => {
    stage(nextChat(), "t1", { outcome: "completed" });
    expect(region("refusal-callout").el).toBeNull();
    expect(region("code-refs").el).toBeNull();
  });

  // The callout's Rewind addresses the refused turn's prompt (KAS drops it and everything after); the footer's
  // addresses the next turn's.
  it("routes the callout's Rewind to the refused turn's own prompt", async () => {
    const chat = nextChat();
    stage(chat, "t1", { refusal: {} });
    const btn = region("refusal-callout").el?.querySelector<HTMLButtonElement>(".refusal-rewind");
    expect(btn).not.toBeNull();
    btn?.click();
    await vi.waitFor(() => {
      expect(rewindMock).toHaveBeenCalledWith({ chatID: chat, messageID: "t1-p" });
    });
  });
});
