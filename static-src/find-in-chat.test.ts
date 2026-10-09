// Find-in-chat: the FindEngine itself, the Ctrl-F overlay, and stepping through the server's hit list.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./scroll.js", () => ({
  jumpTo: vi.fn(),
  // Inert: these tests drive re-runs through the module's own paths.
  onTranscriptMutate: vi.fn(() => () => undefined),
}));
// Spy-wrapped: `vi.spyOn(namespace, name)` cannot patch an ESM namespace in a real browser.
vi.mock("./chat-search.js", { spy: true });
// The store and pagination likewise, handed a fixture chat.
vi.mock("./store.js", { spy: true });
vi.mock("./store-load.js", { spy: true });
// A one-export factory: reached by a lazy `await import`, so replacing it keeps the exec-view chunk out.
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
// Same for the delegate tab.
vi.mock("./subagent-view.js", () => ({ openSubagentView: vi.fn() }));
// The renderer's prose-run offset table, the one export reached: elements resolve from the card's own stamps.
const { runOffsetOf } = vi.hoisted(() => ({
  runOffsetOf: vi.fn<
    (turnID: string, entryID: string) => { offset: number; total: number } | undefined
  >(() => undefined),
}));
vi.mock("./messages-blocks.js", () => ({ runOffsetOf }));

import { FindEngine } from "./find-engine.js";
import type * as ModFindInChat from "./find-in-chat.js";

/** Cache-buster: `vi.resetModules()` does not re-evaluate in Browser Mode, whose module map is URL-keyed. */
let bootSeq = 0;

function root(html: string): HTMLElement {
  const d = document.createElement("div");
  d.innerHTML = html;
  document.body.replaceChildren(d);
  return d;
}

function marks(el: HTMLElement): HTMLElement[] {
  return [...el.querySelectorAll<HTMLElement>("mark.find-hit")];
}

describe("FindEngine matching", () => {
  it("wraps every match across multiple nodes and marks the first current", () => {
    const el = root(
      `<div class="message">alpha TODO beta</div>` +
        `<div class="message">gamma <b>TODO</b> delta</div>` +
        `<div class="message">nothing here</div>`,
    );
    const eng = new FindEngine(el);
    const n = eng.search("TODO");
    expect(n).toBe(2);
    expect(eng.total).toBe(2);
    expect(marks(el)).toHaveLength(2);
    expect(eng.currentIndex).toBe(0);
    expect(eng.currentMark()).toBe(marks(el)[0]);
    expect(marks(el)[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(marks(el)[1]?.classList.contains("find-hit-current")).toBe(false);
  });

  it("matches case-insensitively but preserves the original casing in the mark", () => {
    const el = root(`<p>Todo todo TODO tODo</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("todo")).toBe(4);
    const texts = marks(el).map((m) => m.textContent);
    expect(texts).toEqual(["Todo", "todo", "TODO", "tODo"]);
  });

  it("matches only the exact casing when case sensitivity is asked for", () => {
    const el = root(`<p>Todo todo TODO tODo</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("todo", true)).toBe(1);
    expect(marks(el).map((m) => m.textContent)).toEqual(["todo"]);
  });

  it("matches an upper-case needle exactly under case sensitivity", () => {
    const el = root(`<p>Todo todo TODO</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("TODO", true)).toBe(1);
    expect(marks(el).map((m) => m.textContent)).toEqual(["TODO"]);
  });

  it("defaults to insensitive when the flag is omitted", () => {
    const el = root(`<p>Alpha alpha</p>`);
    expect(new FindEngine(el).search("ALPHA")).toBe(2);
  });

  it("finds every occurrence within one node under case sensitivity", () => {
    const el = root(`<p>Err err Err err</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("err", true)).toBe(2);
    expect(el.textContent).toBe("Err err Err err");
  });

  it("finds multiple matches within a single text node and preserves surrounding text", () => {
    const el = root(`<p>a TODO b TODO c</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(2);
    expect(el.textContent).toBe("a TODO b TODO c");
  });

  it("returns zero and a null current mark when nothing matches", () => {
    const el = root(`<p>the quick brown fox</p>`);
    const eng = new FindEngine(el);
    expect(eng.search("zzz")).toBe(0);
    expect(eng.currentIndex).toBe(-1);
    expect(eng.currentMark()).toBeNull();
    expect(marks(el)).toHaveLength(0);
  });

  it("treats an empty query as a clear (no marks)", () => {
    const el = root(`<p>TODO TODO</p>`);
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    expect(eng.search("")).toBe(0);
    expect(marks(el)).toHaveLength(0);
  });

  it("clear() removes all marks and restores the original text; re-search still works", () => {
    const el = root(`<div>x TODO y</div><div>z TODO w</div>`);
    const original = el.textContent;
    const eng = new FindEngine(el);
    eng.search("TODO");
    expect(marks(el)).toHaveLength(2);
    eng.clear();
    expect(marks(el)).toHaveLength(0);
    expect(el.textContent).toBe(original);
    expect(eng.search("TODO")).toBe(2);
  });

  it("re-searching replaces the previous highlight (no stale marks)", () => {
    const el = root(`<p>foo bar foo baz</p>`);
    const eng = new FindEngine(el);
    eng.search("foo");
    expect(marks(el)).toHaveLength(2);
    eng.search("ba");
    expect(marks(el)).toHaveLength(2);
    expect(marks(el).map((m) => m.textContent)).toEqual(["ba", "ba"]);
  });
});

describe("FindEngine visibility", () => {
  it("skips text inside .hidden, [hidden], and aria-hidden subtrees", () => {
    const el = root(
      `<div class="message">visible TODO</div>` +
        `<div class="hidden">hidden TODO</div>` +
        `<div hidden>attr TODO</div>` +
        `<div aria-hidden="true">aria TODO</div>`,
    );
    const eng = new FindEngine(el);
    expect(eng.search("TODO")).toBe(1);
  });

  it("skips a closed <details> but searches an open one", () => {
    const closed = root(`<details><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(closed).search("TODO")).toBe(0);

    const open = root(`<details open><summary>Reasoning</summary><p>TODO inside</p></details>`);
    expect(new FindEngine(open).search("TODO")).toBe(1);
  });

  it("skips the live-streaming bubble", () => {
    const el = root(
      `<div class="message assistant streaming">streaming TODO</div>` +
        `<div class="message assistant">settled TODO</div>`,
    );
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });

  it("skips script and style content", () => {
    const el = root(`<p>real TODO</p><script>var TODO=1;</script><style>.TODO{}</style>`);
    expect(new FindEngine(el).search("TODO")).toBe(1);
  });
});

describe("FindEngine stepping", () => {
  function threeHits(): { el: HTMLElement; eng: FindEngine } {
    const el = root(`<p>hit hit hit</p>`);
    const eng = new FindEngine(el);
    eng.search("hit");
    return { el, eng };
  }

  it("next() advances and wraps around", () => {
    const { eng } = threeHits();
    expect(eng.currentIndex).toBe(0);
    eng.next();
    expect(eng.currentIndex).toBe(1);
    eng.next();
    expect(eng.currentIndex).toBe(2);
    eng.next();
    expect(eng.currentIndex).toBe(0);
  });

  it("prev() retreats and wraps around", () => {
    const { eng } = threeHits();
    eng.prev();
    expect(eng.currentIndex).toBe(2);
    eng.prev();
    expect(eng.currentIndex).toBe(1);
  });

  it("moves the current class to the active mark", () => {
    const { el, eng } = threeHits();
    eng.next();
    const ms = marks(el);
    expect(ms[0]?.classList.contains("find-hit-current")).toBe(false);
    expect(ms[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(eng.currentMark()).toBe(ms[1]);
  });

  it("next()/prev() are no-ops with zero matches", () => {
    const el = root(`<p>nada</p>`);
    const eng = new FindEngine(el);
    eng.search("xyz");
    eng.next();
    eng.prev();
    expect(eng.currentIndex).toBe(-1);
  });

  it("setCurrent() clamps to the valid range", () => {
    const { eng } = threeHits();
    eng.setCurrent(2);
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(99);
    expect(eng.currentIndex).toBe(2);
    eng.setCurrent(-5);
    expect(eng.currentIndex).toBe(2);
  });
});

describe("Ctrl-F overlay", () => {
  // The controller keeps module singletons and beforeEach wipes the DOM, so each test imports a fresh graph.
  let onHotkey: (e: KeyboardEvent) => void;
  let toggle: () => void;
  let close: () => void;
  let isOpenFn: () => boolean;
  /** From this test's graph: a fresh find-in-chat.js has its own bus. */
  let switchTab: () => void;

  beforeEach(async () => {
    vi.resetModules();
    bootSeq++;
    document.body.innerHTML = `
      <div id="chat-view" data-tab-view>
        <button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>
        <div id="messages-wrap-outer">
          <div id="messages-wrap">
            <div id="messages">
              <div class="message assistant">first TODO here</div>
              <div class="message assistant">second TODO and TODO again</div>
            </div>
          </div>
        </div>
        <textarea id="prompt-input"></textarea>
      </div>
      <div id="shell-panel" class="hidden"><textarea id="term-input"></textarea></div>`;
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?boot=${bootSeq}`
    )) as typeof ModFindInChat;
    onHotkey = mod.handleFindHotkey;
    toggle = mod.toggleChatFind;
    close = mod.closeChatFind;
    isOpenFn = mod._isChatFindOpen;
    const bus = await import("./bus.js");
    switchTab = (): void => {
      bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    };
  });

  /** Revealed through the popup's `[hidden]` plus `is-open`, never `.hidden` (`display: none !important`, 40-a11y.css). */
  function boxIsOpen(): boolean {
    return document.getElementById("chat-find")?.classList.contains("is-open") === true;
  }

  function findBtn(): HTMLElement | null {
    return document.getElementById("find-btn");
  }

  function ctrlF(): KeyboardEvent {
    return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
  }

  function input(): HTMLInputElement | null {
    return document.getElementById("chat-find-input") as HTMLInputElement | null;
  }

  function typeAndEnter(value: string, shift = false): void {
    const el = input();
    if (el === null) {
      throw new Error("find input not built");
    }
    el.value = value;
    el.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        shiftKey: shift,
        bubbles: true,
        cancelable: true,
      }),
    );
  }

  it("takes no clicks while closed or fading, in the stylesheet", async () => {
    // A source fact: the page loads no stylesheet, and `[hidden]` lands only at the end of a leave.
    const { loadCSS, ruleContaining } = await import("./__test-helpers__/css-rules.js");
    const css = loadCSS("24-find.css");
    // On `.search-pop`, the skin shared with the page search popups, which carry the same hazard.
    expect(ruleContaining(css, ".search-pop", "top").body).toMatch(/pointer-events:\s*none/);
    expect(ruleContaining(css, ".search-pop.is-open", "top").body).toMatch(
      /pointer-events:\s*auto/,
    );
  });

  it("opens on Ctrl-F when the chat view is active and preventDefaults the browser find", () => {
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(true);
    const overlay = document.getElementById("chat-find");
    expect(overlay).not.toBeNull();
    expect(boxIsOpen()).toBe(true);
    expect(overlay?.hidden).toBe(false);
    expect(document.activeElement).toBe(input());
  });

  it("does not hijack Ctrl-F when the chat view is hidden", () => {
    document.getElementById("chat-view")?.classList.add("hidden");
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("does not hijack Ctrl-F while focus is inside the shell panel", () => {
    (document.getElementById("term-input") as HTMLTextAreaElement).focus();
    const ev = ctrlF();
    onHotkey(ev);
    expect(ev.defaultPrevented).toBe(false);
  });

  it("searches, updates the counter, steps with Enter, and closes on Escape", () => {
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");

    typeAndEnter("TODO");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(3);
    expect(count?.textContent).toBe("1 of 3");

    typeAndEnter("TODO");
    expect(count?.textContent).toBe("2 of 3");
    typeAndEnter("TODO", true);
    expect(count?.textContent).toBe("1 of 3");

    const el = input();
    el?.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
    );
    expect(boxIsOpen()).toBe(false);
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  it("re-runs the search when the match-case toggle flips, without retyping", () => {
    // step() re-searches only on a changed string, so the case toggle must force the search.
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");
    const toggle = document.querySelector<HTMLButtonElement>(".chat-find-case");
    expect(toggle?.getAttribute("aria-pressed")).toBe("false");

    typeAndEnter("todo");
    expect(count?.textContent).toBe("1 of 3");

    toggle?.click();
    expect(toggle?.getAttribute("aria-pressed")).toBe("true");
    expect(count?.textContent).toBe("No matches");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    toggle?.click();
    expect(toggle?.getAttribute("aria-pressed")).toBe("false");
    expect(count?.textContent).toBe("1 of 3");
  });

  it("resets to the first match on a toggle rather than keeping a position in the old set", () => {
    onHotkey(ctrlF());
    const count = document.getElementById("chat-find-count");
    typeAndEnter("TODO");
    typeAndEnter("TODO");
    expect(count?.textContent).toBe("2 of 3");
    document.querySelector<HTMLButtonElement>(".chat-find-case")?.click();
    expect(count?.textContent).toBe("1 of 3");
  });

  it("carries an accessible name on the toggle", () => {
    onHotkey(ctrlF());
    const toggle = document.querySelector<HTMLButtonElement>(".chat-find-case");
    expect(toggle?.getAttribute("aria-label")).toBe("Match case");
  });

  it("lets a second Ctrl-F fall through to the browser (escape hatch) while the field is focused", () => {
    onHotkey(ctrlF());
    expect(document.activeElement).toBe(input());
    const second = ctrlF();
    onHotkey(second);
    expect(second.defaultPrevented).toBe(false);
  });

  // The close on every path: marks left behind are welded into the transcript, and a skipped fold reset rearranges it.

  /** Confirms the state a teardown must undo exists, or a teardown assertion passes vacuously. */
  function openWithMatches(): void {
    onHotkey(ctrlF());
    typeAndEnter("TODO");
    expect(document.querySelectorAll("mark.find-hit").length).toBeGreaterThan(0);
    expect(isOpenFn()).toBe(true);
  }

  function expectFullyTornDown(path: string): void {
    expect(isOpenFn(), `${path}: the box should be closed`).toBe(false);
    expect(boxIsOpen(), `${path}: is-open should be gone`).toBe(false);
    expect(
      document.querySelectorAll("mark.find-hit"),
      `${path}: every <mark> must be unwrapped, or highlights stay welded into the transcript`,
    ).toHaveLength(0);
    expect(
      findBtn()?.getAttribute("aria-pressed"),
      `${path}: the trigger must stop announcing itself pressed`,
    ).toBe("false");
  }

  it("closes on a click ANYWHERE outside the box, with the full teardown", () => {
    openWithMatches();
    // The popup installs its outside-click listener one tick after the open.
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        document
          .getElementById("messages")
          ?.dispatchEvent(new MouseEvent("click", { bubbles: true }));
        expectFullyTornDown("outside click");
        resolve();
      }, 0);
    });
  });

  it("closes on Escape pressed OUTSIDE the field, not only inside it", () => {
    // Escape is a document-level listener, so it works after clicking into the transcript.
    openWithMatches();
    const transcript = document.getElementById("messages");
    return new Promise<void>((resolve) => {
      setTimeout(() => {
        transcript?.dispatchEvent(
          new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }),
        );
        expectFullyTornDown("document Escape");
        resolve();
      }, 0);
    });
  });

  it("closes on a TAB SWITCH rather than being hidden with its state intact", () => {
    // A tab switch closes the box fully: hiding its ancestor left marks, folds and the observer live.
    openWithMatches();
    switchTab();
    expectFullyTornDown("tab switch");
  });

  it("FORGETS the query on a tab switch, so the next tab's find opens empty", () => {
    // A tab switch clears the query too, or the next chat's find searches with it.
    openWithMatches();
    expect(input()?.value).toBe("TODO");
    switchTab();
    expect(input()?.value, "a retained query is inherited by whatever tab is opened next").toBe("");
  });

  it("KEEPS the query across an ordinary close, the way the browser's find does", () => {
    // Only a tab switch changes subject; reopening on the same chat keeps the query.
    openWithMatches();
    close();
    expect(input()?.value).toBe("TODO");
  });

  it("closes on the toolbar toggle, and a second toggle re-opens", () => {
    // The trigger toggles rather than re-running the open path.
    openWithMatches();
    toggle();
    expectFullyTornDown("trigger toggle");
    toggle();
    expect(isOpenFn()).toBe(true);
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("true");
  });

  it("announces the open state on the trigger with aria-pressed", () => {
    // aria-pressed, not `.active` (which means "this singleton tab is active").
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("false");
    onHotkey(ctrlF());
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("true");
    close();
    expect(findBtn()?.getAttribute("aria-pressed")).toBe("false");
  });

  it("re-folds the turns the search opened, on every close path", async () => {
    // The fold reset the DOM cannot show: the server pre-pass opens folded turns.
    const chatSearch = await import("./chat-search.js");
    const reset = vi.mocked(chatSearch.resetServerSearch);
    for (const path of ["escape", "toggle", "tab-switch"] as const) {
      reset.mockClear();
      onHotkey(ctrlF());
      expect(isOpenFn(), path).toBe(true);
      if (path === "escape") {
        close();
      } else if (path === "toggle") {
        toggle();
      } else {
        switchTab();
      }
      expect(
        reset,
        `${path}: the fold reset must run, or search-opened turns stay open forever`,
      ).toHaveBeenCalledTimes(1);
    }
    reset.mockClear();
  });

  it("is idempotent: closing an already-closed box changes nothing", () => {
    close();
    expect(isOpenFn()).toBe(false);
    onHotkey(ctrlF());
    close();
    close();
    expectFullyTornDown("double close");
  });
});

// Server-hit navigation: when the walker marked nothing but the server found the text, stepping reveals the hit.

import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { Tally } from "./textsearch/copy.js";
import type { Hit, SearchResult } from "./wire/types.gen.js";
import type * as ModChatSearch from "./chat-search.js";
import type * as ModStore from "./store.js";
import type * as ModStoreLoad from "./store-load.js";
import type * as ModScroll from "./scroll.js";
import type * as ModRunView from "./run-view.js";
import type * as ModSubagentView from "./subagent-view.js";

describe("server-hit navigation", () => {
  let onHotkey: (e: KeyboardEvent) => void;
  let openAt: typeof ModFindInChat.openChatFindAt;
  let closeFind: () => void;
  let chatSearch: typeof ModChatSearch;
  let store: typeof ModStore;
  let storeLoad: typeof ModStoreLoad;
  let scroll: typeof ModScroll;
  let runView: typeof ModRunView;
  let subagentView: typeof ModSubagentView;
  /** From this graph's bus, for the cross-tab return trip. */
  let switchTab: () => void;

  beforeEach(async () => {
    vi.resetModules();
    bootSeq++;
    document.body.innerHTML = `
      <div id="chat-view" data-tab-view>
        <button type="button" id="find-btn" class="icon-btn" aria-pressed="false"></button>
        <div id="messages-wrap-outer">
          <div id="messages-wrap"><div id="messages"></div></div>
        </div>
        <textarea id="prompt-input"></textarea>
      </div>
      <div id="shell-panel" class="hidden"></div>`;
    const mod = (await import(
      /* @vite-ignore */ `./find-in-chat.ts?boot=${bootSeq}`
    )) as typeof ModFindInChat;
    onHotkey = mod.handleFindHotkey;
    openAt = mod.openChatFindAt;
    closeFind = mod.closeChatFind;
    chatSearch = await import("./chat-search.js");
    store = await import("./store.js");
    storeLoad = await import("./store-load.js");
    scroll = await import("./scroll.js");
    runView = await import("./run-view.js");
    subagentView = await import("./subagent-view.js");
    const bus = await import("./bus.js");
    switchTab = (): void => {
      bus.emitBus(bus.BUS_TAB_CHANGED, { to: "__files__", kind: "files" });
    };
  });

  function ctrlF(): KeyboardEvent {
    return new KeyboardEvent("keydown", { key: "f", ctrlKey: true, cancelable: true });
  }

  function typeAndEnter(value: string): void {
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    input.value = value;
    input.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
  }

  function countText(): string {
    return document.getElementById("chat-find-count")?.textContent ?? "";
  }

  function noteText(): string {
    return document.getElementById("chat-find-note")?.textContent ?? "";
  }

  /** A hit keyed `[turn_id, entry_id, segment_kind, offset]`; no lane means the chat's own agent. */
  function serverHit(over: Partial<Hit> = {}): Hit {
    return {
      turn_id: "u1",
      entry_id: "e1",
      excerpt: "…retry…",
      segment_kind: "content",
      turn: 1,
      offset: 0,
      segment_len: 24,
      ...over,
    };
  }

  /** A chat whose resident window is a set of turn ids; residency reads `turns.has`. */
  function stageChat(turnIDs: string[], hasMore = false): Session {
    const session: Session = {
      ...makeSession({ id: "c1", has_more: hasMore }),
      turns: new Map(turnIDs.map((id) => [id, { entries: [], openEntries: new Map() }])),
      turn_order: [...turnIDs],
      turn_count: turnIDs.length,
    };
    vi.mocked(store.getActiveId).mockReturnValue("c1");
    vi.mocked(store.getActive).mockReturnValue(session);
    return session;
  }

  /** Prepended to `turn_order` as `loadMessages` does, so the `?before=` cursor moves. */
  function prependTurn(session: Session, turnID: string): void {
    session.turns.set(turnID, { entries: [], openEntries: new Map() });
    session.turn_order.unshift(turnID);
  }

  /** The server answer as the envelope the overlay adopts whole; `matched` defaults to the list's length. */
  function stageHits(hits: Hit[], tally: Partial<Tally> = {}): void {
    vi.mocked(chatSearch.runServerSearch).mockResolvedValue({
      matches: hits,
      scanned: 1,
      matched: hits.length,
      truncated: false,
      ...tally,
    });
    vi.mocked(chatSearch.revealHitTurn).mockResolvedValue(undefined);
  }

  /** `bodyHTML === null` builds a stub: header only, which the reveal must build before anything is marked. */
  function mountTurnCard(turnID: string, bodyHTML: string | null): HTMLElement {
    const card = document.createElement("div");
    // Class `turn` as production builds it, which `turnCardEl` selects.
    card.className = "turn";
    card.setAttribute("data-reconcile-key", turnID);
    card.innerHTML = `<div class="turn-header">turn</div>`;
    if (bodyHTML !== null) {
      const body = document.createElement("div");
      body.className = "turn-body";
      body.innerHTML = bodyHTML;
      card.appendChild(body);
    }
    document.getElementById("messages")?.appendChild(card);
    return card;
  }

  /** Through the real disclosure, so the collapsed region carries the `aria-hidden` + `inert` the walker prunes. */
  function wireToolCard(card: HTMLElement): void {
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    const details = card.querySelector<HTMLElement>(".tool-details");
    if (toggle === null || details === null) {
      throw new Error("fixture card missing toggle/details");
    }
    createDisclosure(toggle, details, { open: false });
  }

  /** One macrotask drains the hop between the shell's `query` and `render`. */
  function settle(): Promise<void> {
    return new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
  }

  /** Waits on adoption, not counter text, for fixtures whose figures agree. */
  async function openAndAdopt(query: string): Promise<void> {
    onHotkey(ctrlF());
    typeAndEnter(query);
    await settle();
  }

  async function openAndSearch(query: string): Promise<void> {
    onHotkey(ctrlF());
    typeAndEnter(query);
    await vi.waitFor(() => {
      expect(countText()).toMatch(/in chat|matched, not shown here/);
    });
    // The counter paints synchronously; the recorded hits land a few microtasks later.
    await new Promise((resolve) => {
      setTimeout(resolve, 0);
    });
  }

  // The cut is reported twice: the counter carries the whole-chat count, the note the sentence.

  it("reports a cut answer as the whole-chat count and the note's sentence", async () => {
    stageChat(["u1"]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(countText()).toBe("347 matched, not shown here");
    expect(noteText()).toBe("1 of 347 matches shown; 24 messages scanned");
  });

  it("stays silent on an answer the list holds whole", async () => {
    // With every occurrence in the list, the note would restate the counter.
    stageChat(["u1"]);
    stageHits([serverHit()], { scanned: 24 });
    await openAndSearch("retry");
    expect(countText()).toBe("1 matched, not shown here");
    expect(noteText()).toBe("");
  });

  it("does not read the scan's own reach as a cut", async () => {
    // `truncated` means the scan stopped; a cut is `matched` exceeding the list.
    stageChat(["u1"]);
    stageHits([serverHit()], { truncated: true });
    await openAndSearch("retry");
    expect(noteText()).toBe("");
  });

  it("says the answer was cut, and unsays it when the next one is not", async () => {
    stageChat(["u1"]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).toBe("1 of 347 matches shown; 24 messages scanned");

    stageHits([serverHit()]);
    typeAndEnter("retry budget");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
  });

  it("clears the note when the query is refined to zero hits", async () => {
    // Refining is what a cut invites, so the note clears above render's early return.
    stageChat(["u1"]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).not.toBe("");

    stageHits([]);
    typeAndEnter("nothing matches this");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
  });

  it("resolves the entry a hit names when the mounted window starts above the turn's first", async () => {
    // Entries 2..7 of eight are resident; an ordinal from the turn's first entry would name the wrong trace.
    const traces = Array.from(
      { length: 8 },
      (_, i) => `paragraph ${String(i)} weighs the retry budget`,
    );
    stageChat(["u1"]);
    const mounted = [2, 3, 4, 5, 6, 7];
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">${mounted
        .map(
          (i) =>
            `<details class="reasoning-block msg-reasoning" data-entry-id="e${String(i)}">
               <summary class="reasoning-summary">Reasoning</summary>
               <blockquote class="reasoning-body">${traces[i] ?? ""}</blockquote>
             </details>`,
        )
        .join("")}</div>`,
    );
    const want = traces[4] ?? "";
    stageHits([
      serverHit({
        excerpt: want,
        segment_kind: "reasoning",
        entry_id: "e4",
        offset: want.indexOf("retry"),
        segment_len: want.length,
      }),
    ]);

    await openAndSearch("retry");
    // Every trace is a closed <details>, so only server-hit navigation resolves an element.
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector("mark.find-hit-current");
    expect(current?.closest("[data-entry-id]")?.getAttribute("data-entry-id")).toBe("e4");
    expect(document.querySelectorAll("details.reasoning-block[open]")).toHaveLength(1);
  });

  it("selects the entry and says so when its subtree is not rendered", async () => {
    // `content-visibility: hidden` blocks marking either way; the fallback tells the reader and lands on the row.
    stageChat(["u1"]);
    const row = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="content-visibility: hidden">
         <div class="msg-row" data-entry-id="e1">
           <div class="message assistant">see the retry backoff here</div>
         </div>
       </div>`,
      // The bubble, not the row: `entryElement` answers with the `.message` inside.
    ).querySelector('[data-entry-id="e1"] > .message') as HTMLElement;
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        offset: 8,
        segment_len: 26,
      }),
    ]);
    await openAndSearch("retry");

    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    });
    expect(row.classList.contains("find-target-flash")).toBe(true);
    expect(vi.mocked(scroll.jumpTo).mock.lastCall?.[0]).toBe(row);
  });

  it("counts the server's hits with the no-results skin absent when the DOM holds none", async () => {
    // Matches in unmounted entries: a miss would contradict the count beside it.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e3">
             <div class="message assistant">and once more</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({ excerpt: "the retry backoff", offset: 4, segment_len: 17 }),
      serverHit({ excerpt: "retry again", entry_id: "e2", offset: 0, segment_len: 11 }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
    expect(countText()).toBe("2 matched, not shown here");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
  });

  it("selects the entry and says so for a syntax-only hit (match not in rendered text)", async () => {
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      // A prose run is stamped on its row, the shape the normalization steps through.
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">see docs for more</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "see [docs](https://retry.example) for more",
        offset: 19,
        segment_len: 43,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const bubble = document.querySelector(".message.assistant");
    await vi.waitFor(() => {
      expect(bubble?.classList.contains("find-target-flash")).toBe(true);
    });
    expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(bubble, expect.anything());
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  it("resolves a hit on a prose RUN's later entry through the run's data-entries stamp", async () => {
    // A prose run carries two stamps: `data-entry-id` (first member) and `data-entries` (all).
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1" data-entries="e1 e2 e3">
             <div class="message assistant">first retry paragraph. then the retry backoff. and once more.</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({ entry_id: "e3", excerpt: "then the retry backoff.", offset: 9, segment_len: 23 }),
    ]);

    // The marks are resident, so the figures agree and no "in chat" clause is printed.
    await openAndAdopt("retry");
    typeAndEnter("retry");

    // Two occurrences, the hit naming the second: `FindEngine.search` makes the first current on its own.
    const bubble = document.querySelector(".message.assistant") as HTMLElement;
    await vi.waitFor(() => {
      expect(marks(bubble)[1]?.classList.contains("find-hit-current")).toBe(true);
    });
    expect(marks(bubble)[0]?.classList.contains("find-hit-current")).toBe(false);
    expect(countText()).toBe("1 of 1");
  });

  it("ranks a hit inside a prose RUN through the run's own offset table, not its segment", async () => {
    // A run member's offset is re-based by `runOffsetOf` over the whole run.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1" data-entries="e1 e2">
             <div class="message assistant">retry once. retry twice.</div>
           </div>
       </div>`,
    );
    // The table's answer for the run's second member.
    runOffsetOf.mockImplementation((_turnID, entryID) =>
      entryID === "e2" ? { offset: 12, total: 24 } : undefined,
    );
    stageHits([serverHit({ entry_id: "e2", excerpt: "retry twice.", offset: 0, segment_len: 12 })]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    const bubble = document.querySelector(".message.assistant") as HTMLElement;
    await vi.waitFor(() => {
      expect(marks(bubble)[1]?.classList.contains("find-hit-current")).toBe(true);
    });
    // Fails with the run arm gone: fraction 0 re-selects mark 0.
    expect(marks(bubble)[0]?.classList.contains("find-hit-current")).toBe(false);
    expect(runOffsetOf).toHaveBeenCalledWith("u1", "e2");
    expect(countText()).toBe("1 of 1");
  });

  it("walks again once the jump has rendered a skipped card, rather than reporting a miss", async () => {
    // An off-screen card under `content-visibility: auto` holds no walkable text until jumped to.
    stageChat(["u1"]);
    const wrap = document.getElementById("messages-wrap") as HTMLElement;
    wrap.style.cssText = "height: 400px; overflow-y: auto";
    const spacer = document.createElement("div");
    spacer.style.height = "6000px";
    document.getElementById("messages")?.appendChild(spacer);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="content-visibility: auto; contain-intrinsic-size: auto 2.5rem">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">see the retry backoff here</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        offset: 8,
        segment_len: 26,
      }),
    ]);
    // Relevancy is decided in a rendering update, so the premise holds once laid out.
    await vi.waitFor(() => {
      const skipped = document.querySelector<HTMLElement>('[data-entry-id="e1"]');
      expect(skipped?.checkVisibility({ contentVisibilityAuto: true })).toBe(false);
    });
    vi.mocked(scroll.jumpTo).mockImplementation((el: Element) => {
      el.scrollIntoView();
    });

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".message.assistant mark.find-hit-current")).not.toBeNull();
    });
    // Not the miss notice; the position is the session list's.
    expect(countText()).toBe("1 of 1");
  });

  it("releases next/prev when the frame the re-walk waits for is never delivered", async () => {
    // A hidden page gets no animation frames, so the rAF re-walk inside `navBusy` needs a ceiling.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="content-visibility: hidden">
         <div class="msg-row" data-entry-id="e1">
           <div class="message assistant">see the retry backoff here</div>
         </div>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "see the retry backoff here",
        offset: 8,
        segment_len: 26,
      }),
    ]);
    await openAndSearch("retry");

    vi.stubGlobal("requestAnimationFrame", () => 0);
    typeAndEnter("retry");

    // The ceiling released the wait, and the verdict names the miss kind.
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not rendered yet");
    });
  });

  it("steps into a tool card mounted in ANOTHER row of the turn", async () => {
    // The run card owns later step cards, so the stepping call's card is in the launching entry's row.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="run-card" data-entry-id="e1">
             <div class="run-steps">
               <div class="tool-call" data-tool-id="t-step" data-entry-id="e2">
                 <div class="tool-summary">
                   <span class="tool-title">Read notes</span>
                   <button type="button" class="tool-disclosure"></button>
                 </div>
                 <div class="tool-details">
                   <div class="tool-output">bump the retry backoff to 30s</div>
                 </div>
               </div>
             </div>
           </div>
       </div>
       <div data-reconcile-key="a2" class="msg-row"></div>`,
    );
    const card = document.querySelector<HTMLElement>('[data-tool-id="t-step"]');
    wireToolCard(card as HTMLElement);
    stageHits([
      serverHit({
        entry_id: "e2",
        excerpt: "bump the retry backoff to 30s",
        segment_kind: "tool_output",
        offset: 9,
        segment_len: 29,
      }),
    ]);

    // Behind the card's own disclosure, so navigation is the only path.
    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(card?.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    expect(countText()).toBe("1 of 1");
  });

  // tool_diff: the card is the target, the mini-diff the walk, and the notice has three states.

  function previewHTML(row: string): string {
    return `<div class="tool-diff-preview">
              <div class="diff-pane tool-diff-mini"><div class="diff-row">${row}</div></div>
            </div>`;
  }

  /** The mini-diff sits before `.tool-details`, so it is in the card's resting state. */
  function editCard(previewRow: string | null): string {
    const preview = previewRow === null ? "" : previewHTML(previewRow);
    return `<div class="tool-call" data-tool-id="t1" data-entry-id="e1">
              <div class="tool-summary">
                <span class="tool-title">Replace in File</span>
                <button type="button" class="tool-disclosure"></button>
              </div>
              ${preview}
              <div class="tool-details"><div class="tool-output">wrote fetch.go</div></div>
            </div>`;
  }

  /** The deferred half as `tool-card.ts` wires it: the first open fetches the bulk and inserts the mini-diff. */
  function wireDeferredDiff(
    card: HTMLElement,
    previewRow: string,
    lands: boolean,
    into?: HTMLElement,
  ): { requests: () => number } {
    let requests = 0;
    // Past `RENDER_WAIT_CEILING_MS` (64ms) on purpose, so only the deferred-diff wait can cover it.
    const bulkMs = 120;
    const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
    if (toggle === null) {
      throw new Error("fixture card missing toggle");
    }
    toggle.addEventListener("click", () => {
      requests += 1;
      if (!lands || requests > 1) {
        return;
      }
      setTimeout(() => {
        const wrap = document.createElement("div");
        wrap.innerHTML = previewHTML(previewRow);
        const preview = wrap.firstElementChild;
        if (preview === null) {
          return;
        }
        if (into !== undefined) {
          into.appendChild(preview);
          return;
        }
        card.insertBefore(preview, card.querySelector(".tool-details"));
      }, bulkMs);
    });
    return { requests: () => requests };
  }

  /** Wired as `tool-card.ts` does, so `.tool-details` carries what the walker prunes. */
  function mountEditCard(previewRow: string | null, rowStyle = ""): HTMLElement {
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row" style="${rowStyle}">
         ${editCard(previewRow)}
       </div>`,
    );
    const card = document.querySelector<HTMLElement>(".tool-call");
    if (card === null) {
      throw new Error("fixture card missing");
    }
    wireToolCard(card);
    return card;
  }

  function stageEditChat(): void {
    stageChat(["u1"]);
  }

  const DIFF_ROW = "return retry(ctx, fetchOnce)";

  /** The budget the "ceiling not paid" cases assert against (`DEFERRED_DIFF_WAIT_MS` is 1500). */
  const CEILING_NOT_PAID_MS = 1200;

  it("lands a tool_diff hit on the rendered mini-diff row", async () => {
    // Real containment and an off-screen box: the preview is resting state, so only relevance hides it.
    stageEditChat();
    const wrap = document.getElementById("messages-wrap") as HTMLElement;
    wrap.style.cssText = "height: 400px; overflow-y: auto";
    const spacer = document.createElement("div");
    spacer.style.height = "6000px";
    document.getElementById("messages")?.appendChild(spacer);
    mountEditCard(DIFF_ROW, "content-visibility: auto; contain-intrinsic-size: auto 2.5rem");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);
    await vi.waitFor(() => {
      const skipped = document.querySelector<HTMLElement>(".tool-call");
      expect(skipped?.checkVisibility({ contentVisibilityAuto: true })).toBe(false);
    });
    vi.mocked(scroll.jumpTo).mockImplementation((el: Element) => {
      el.scrollIntoView();
    });

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
    });
    // A diff already on screen opens nothing.
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("false");
    expect(countText()).toBe("1 of 1");
  });

  it("opens a preview-less card and lands on the diff its bulk brings back", async () => {
    // The majority case (most diff-bearing calls exceed the preview budget): opening the card makes the diff exist.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
    });
    // It stays open, at one bulk fetch.
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(bulk.requests()).toBe(1);
    expect(countText()).toBe("1 of 1");
  });

  it("says the match is not in the SHOWN hunks when the card renders a preview", async () => {
    // `windowHunks` keeps 24 rows, so a longer diff's preview can lack the match.
    stageEditChat();
    mountEditCard("return fetchOnce(ctx)");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const preview = document.querySelector(".tool-diff-preview");
    await vi.waitFor(() => {
      expect(preview?.classList.contains("find-target-flash")).toBe(true);
    });
    // The narrowed region is what the flash and jump land on.
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(preview, expect.anything());
    expect(countText()).toBe("1 of 1 \u00b7 not in the shown hunks, so open the diff");
  });

  it("opens no disclosure and requests no bulk when the card already renders its diff", async () => {
    // A diff already on screen must stay exactly as it was: nothing opened, nothing fetched.
    stageEditChat();
    const card = mountEditCard("return fetchOnce(ctx)");
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in the shown hunks, so open the diff");
    });
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("false");
    expect(bulk.requests()).toBe(0);
    expect(card.querySelectorAll(".tool-diff-preview")).toHaveLength(1);
  });

  it("says the diff did not load when the open brings nothing back", async () => {
    // The third state's wording: opening the card is what find just did.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, false);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(
      () => {
        expect(countText()).toBe("1 of 1 \u00b7 only in this call's diff, which did not load");
      },
      { timeout: 4000 },
    );
    // It tried: the card is open and one bulk was asked for.
    expect(card.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(bulk.requests()).toBe(1);
    expect(card.querySelector(".tool-diff-preview")).toBeNull();
  });

  /** A second Enter is a second visit to the same card, not a wrap. */
  function deadDiffHits(): Hit[] {
    return [0, 40].map((offset) =>
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset,
        segment_len: 4096,
      }),
    );
  }

  it("answers a second visit to a dead diff hit AT ONCE rather than waiting again", async () => {
    // An empty bulk leaves the preview absent and `detailsBody` will not run again.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, false);
    stageHits(deadDiffHits());

    await openAndSearch("retry");
    typeAndEnter("retry");
    await vi.waitFor(
      () => {
        expect(countText()).toBe("1 of 2 \u00b7 only in this call's diff, which did not load");
      },
      { timeout: 4000 },
    );

    const started = performance.now();
    typeAndEnter("retry");
    await vi.waitFor(
      () => {
        expect(countText()).toBe("2 of 2 \u00b7 only in this call's diff, which did not load");
      },
      { timeout: 4000 },
    );
    expect(performance.now() - started).toBeLessThan(CEILING_NOT_PAID_MS);
    expect(bulk.requests()).toBe(1);
  });

  it("paints NOTHING when the reader has retyped away under the wait", async () => {
    // A deferred diff lands up to 1500ms after the press, so a landing must be refused once the reader moved on.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits(deadDiffHits().slice(0, 1));

    await openAndSearch("retry");
    const before = countText();
    typeAndEnter("retry");
    const box = document.getElementById("chat-find-input") as HTMLInputElement;
    box.value = "budget";

    await vi.waitFor(() => {
      expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    });
    // The wait resolves on a microtask off the observer's callback.
    await settle();
    expect(bulk.requests()).toBe(1);
    expect(countText()).toBe(before);
    expect(document.querySelectorAll(".find-target-flash")).toHaveLength(0);
  });

  it("paints NOTHING when a fresh answer has landed under the wait", async () => {
    // The gate is the whole stepped-branch condition, not ownership alone: a retype adopts a new answer mid-wait.
    stageEditChat();
    const card = mountEditCard(null);
    const bulk = wireDeferredDiff(card, DIFF_ROW, true);
    stageHits(deadDiffHits().slice(0, 1));

    await openAndSearch("retry");
    typeAndEnter("retry");
    // Enter on a changed box runs the query rather than stepping.
    typeAndEnter("budget");
    await settle();
    const afterRetype = countText();

    await vi.waitFor(() => {
      expect(card.querySelector(".tool-diff-preview")).not.toBeNull();
    });
    await settle();
    expect(bulk.requests()).toBe(1);
    expect(countText()).toBe(afterRetype);
    expect(document.querySelectorAll(".find-target-flash")).toHaveLength(0);
  });

  it("sees a diff the bulk inserts NESTED inside the card, on arrival", async () => {
    // The observer's scope matches its callback's question: both read `card.querySelector(".tool-diff…")`.
    stageEditChat();
    const card = mountEditCard(null);
    const slot = document.createElement("div");
    slot.className = "tool-diff-slot";
    card.insertBefore(slot, card.querySelector(".tool-details"));
    wireDeferredDiff(card, DIFF_ROW, true, slot);
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: DIFF_ROW.length,
      }),
    ]);

    await openAndSearch("retry");
    const started = performance.now();
    typeAndEnter("retry");

    await vi.waitFor(
      () => {
        expect(document.querySelector(".tool-diff-preview mark.find-hit-current")).not.toBeNull();
      },
      { timeout: 4000 },
    );
    expect(performance.now() - started).toBeLessThan(CEILING_NOT_PAID_MS);
    expect(countText()).toBe("1 of 1");
  });

  it("says only NOT RENDERED YET for a diff miss with no frame delivered", async () => {
    // A hidden tab's diff is unpainted rather than windowed out; that arm outranks the kind.
    stageEditChat();
    mountEditCard("return fetchOnce(ctx)");
    stageHits([
      serverHit({
        excerpt: DIFF_ROW,
        segment_kind: "tool_diff",
        offset: DIFF_ROW.indexOf("retry"),
        segment_len: 4096,
      }),
    ]);
    await openAndSearch("retry");

    vi.stubGlobal("requestAnimationFrame", () => 0);
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not rendered yet");
    });
    expect(countText()).not.toContain("shown hunks");
    expect(countText()).not.toContain("this call's diff");
  });

  // tool_input: the `<pre>` is inside `.tool-details`, and the narrowing keeps the output's mark from winning.

  /** `detailsBody` puts the input first and the output last in `.tool-details`. */
  function inputCard(input: unknown, output: string): string {
    const pretty = JSON.stringify(input, null, 2);
    return `<div class="tool-call" data-tool-id="t1" data-entry-id="e1">
              <div class="tool-summary">
                <span class="tool-title">Run Command</span>
                <button type="button" class="tool-disclosure"></button>
              </div>
              <div class="tool-details">
                <pre class="tool-input">${pretty}</pre>
                <div class="tool-output">${output}</div>
              </div>
            </div>`;
  }

  function mountInputCard(input: unknown, output: string): HTMLElement {
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         ${inputCard(input, output)}
       </div>`,
    );
    const card = document.querySelector<HTMLElement>(".tool-call");
    if (card === null) {
      throw new Error("fixture card missing");
    }
    wireToolCard(card);
    return card;
  }

  it("opens the card's disclosure for a tool_input hit and lands on the <pre>", async () => {
    stageEditChat();
    mountInputCard({ command: "go test ./... -run Retry" }, "ok 1.013s");
    const leaves = "go test ./... -run Retry";
    stageHits([
      serverHit({
        excerpt: leaves,
        segment_kind: "tool_input",
        offset: leaves.indexOf("Retry"),
        segment_len: leaves.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-input mark.find-hit-current")).not.toBeNull();
    });
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
    expect(countText()).toBe("1 of 1");
  });

  it("keeps a tool_input hit inside .tool-input when the output matches too", async () => {
    // The output also matches, so the narrowing is what picks the input.
    stageEditChat();
    mountInputCard(
      { path: "internal/chat/retry.go", pattern: "retry", flag: "-n" },
      "internal/chat/retry.go:12",
    );
    const leaves = "internal/chat/retry.go retry -n";
    stageHits([
      serverHit({
        excerpt: leaves,
        segment_kind: "tool_input",
        offset: leaves.indexOf("retry"),
        segment_len: leaves.length,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector<HTMLElement>("mark.find-hit-current");
    expect(current?.closest(".tool-input")).not.toBeNull();
    expect(current?.closest(".tool-output")).toBeNull();
    // A mark, not the notice; the counter reports the server's list position.
    expect(countText()).toBe("1 of 1");
  });

  // Turn-level kinds resolve from the turn card ahead of the row lookup; `plan` keeps the row path.

  function stageTurnLevelChat(): void {
    stageChat(["u1"]);
  }

  it("lands a turn_failure hit on the card-level .turn-notice", async () => {
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="message assistant">partial answer</div>
       </div>`,
    ).insertAdjacentHTML("beforeend", `<div class="turn-notice">${reason}</div>`);
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-notice mark.find-hit-current")).not.toBeNull();
    });
    expect(document.querySelector("mark.find-hit-current")?.textContent).toBe("retry");
  });

  it("lands an attachment hit on a pill in the turn header", async () => {
    stageTurnLevelChat();
    const name = "retry-notes.md";
    // Pills are `header > .turn-req > .turn-req-attachments`, which the resolver's scope must reach.
    mountTurnCard("u1", `<div data-reconcile-key="a1" class="msg-row"></div>`)
      .querySelector<HTMLElement>(".turn-header")
      ?.insertAdjacentHTML(
        "beforeend",
        `<div class="turn-req">
           <div class="turn-req-text">look at this</div>
           <ul class="turn-req-attachments attachment-row">
             <li class="attachment-pill"><span class="attachment-name">${name}</span></li>
           </ul>
         </div>`,
      );
    stageHits([
      serverHit({
        excerpt: name,
        segment_kind: "attachment",
        offset: 0,
        segment_len: name.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-req-attachments mark.find-hit-current")).not.toBeNull();
    });
  });

  it("resolves a turn-level hit on a STUB turn, which has no row at all", async () => {
    // A tier-3 stub has a header and notice but no `.turn-body`, so the row lookup is null.
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    mountTurnCard("u1", null).insertAdjacentHTML(
      "beforeend",
      `<div class="turn-notice">${reason}</div>`,
    );
    expect(document.querySelector(".turn-body")).toBeNull();
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".turn-notice mark.find-hit-current")).not.toBeNull();
    });
  });

  it("selects the turn card and says so when the reason is not rendered", async () => {
    // A cancelled turn renders no reason; the hit still counts and steps.
    stageTurnLevelChat();
    const reason = "the retry budget ran out";
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
         <div class="message assistant">partial answer</div>
       </div>`,
    );
    expect(card.querySelector(".turn-notice")).toBeNull();
    stageHits([
      serverHit({
        excerpt: reason,
        segment_kind: "turn_failure",
        offset: reason.indexOf("retry"),
        segment_len: reason.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    });
    expect(card.classList.contains("find-target-flash")).toBe(true);
    expect(document.querySelector("mark.find-hit-current")).toBeNull();
  });

  it("lands a plan hit on the plan card, not on the prose beside it", async () => {
    // A `plan` entry is the plan card, so the hit's entry id decides between two credible marks.
    stageChat(["u1"]);
    const entry = "Trace the retry path";
    mountTurnCard(
      "u1",
      `<div class="msg-row" data-entry-id="e1">
         <div class="message assistant">the retry plan, in prose</div>
       </div>
       <div class="plan-message" data-entry-id="e2">
         <div class="plan-header">Plan</div>
         <div class="plan-entries"><div class="plan-entry">${entry}</div></div>
       </div>`,
    );
    stageHits([
      serverHit({
        entry_id: "e2",
        excerpt: entry,
        segment_kind: "plan",
        offset: entry.indexOf("retry"),
        segment_len: entry.length,
      }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(document.querySelector(".plan-message mark.find-hit-current")).not.toBeNull();
    });
    const current = document.querySelector<HTMLElement>("mark.find-hit-current");
    expect(current?.closest(".message")).toBeNull();
  });

  it("opens the card's disclosure for a tool_denial hit and lands on .tool-denial", async () => {
    // `detailsBody` builds the denial block on first open, so the kind is in `OPENS_TOOL_DETAILS`.
    stageEditChat();
    const resource = "rm -rf /config/retry";
    mountInputCard({ command: resource }, "");
    const card = document.querySelector<HTMLElement>(".tool-call");
    card?.querySelector<HTMLElement>(".tool-details")?.insertAdjacentHTML(
      "afterbegin",
      `<div class="tool-denial">
         <span class="tool-denial-label">Resource</span>
         <span class="tool-denial-value">${resource}</span>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: resource,
        segment_kind: "tool_denial",
        offset: resource.indexOf("retry"),
        segment_len: resource.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector(".tool-denial mark.find-hit-current")).not.toBeNull();
    });
    expect(document.querySelector<HTMLElement>(".tool-disclosure")?.ariaExpanded).toBe("true");
  });

  it("navigates an entry hit to the entry's container (scroll + brief highlight)", async () => {
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div class="msg-row" data-entry-id="e1">
         <div class="tool-call">tool card furniture</div>
       </div>`,
    );
    // The filter-only contract: one synthetic `entry` hit at offset 0, zero length.
    stageHits([
      serverHit({
        excerpt: "List files a.go b.go",
        segment_kind: "entry",
        offset: 0,
        segment_len: 0,
      }),
    ]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    const row = document.querySelector('[data-entry-id="e1"]');
    await vi.waitFor(() => {
      expect(row?.classList.contains("find-target-flash")).toBe(true);
    });
    expect(countText()).toBe("1 of 1");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(row, expect.anything());
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);
  });

  // The trace is longer than rendered and the needle occurs twice; the excerpt discriminates.
  const TRACE =
    "Enable retry on the uploader so flaky links recover without operator " +
    "attention and keep the queue draining smoothly overnight. When the " +
    "budget empties the retry gives up and files an alert instead.";

  it("nearest-match picks the occurrence whose context matches the excerpt", async () => {
    stageChat(["u1"]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <details class="reasoning-block msg-reasoning" data-entry-id="e1">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body"></blockquote>
           </details>
       </div>`,
    );
    // Set programmatically so the fixture cannot drift from the trace the coordinates use.
    const quote = card.querySelector(".reasoning-body");
    if (quote !== null) {
      quote.textContent = TRACE;
    }
    const secondAt = TRACE.lastIndexOf("retry");
    stageHits([
      serverHit({
        excerpt: "When the budget empties the retry gives up and files an alert instead.",
        segment_kind: "reasoning",
        offset: secondAt,
        segment_len: TRACE.length,
      }),
    ]);

    await openAndSearch("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(0);

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    expect(document.querySelector<HTMLDetailsElement>("details.reasoning-block")?.open).toBe(true);
    const marks = [...document.querySelectorAll("mark.find-hit")];
    expect(marks[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(marks[0]?.classList.contains("find-hit-current")).toBe(false);
    // The marks pin the ranking; the counter reports the one-hit session list.
    expect(countText()).toBe("1 of 1");
  });

  it("falls back to relative position when the excerpt cannot discriminate", async () => {
    // A shared excerpt ties similarity, so offset and length decide.
    const short = "retry then retry";
    stageChat(["u1"]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <details class="reasoning-block msg-reasoning" data-entry-id="e1">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body"></blockquote>
           </details>
       </div>`,
    );
    const quote = card.querySelector(".reasoning-body");
    if (quote !== null) {
      quote.textContent = short;
    }
    stageHits([
      serverHit({
        excerpt: short,
        segment_kind: "reasoning",
        offset: 11,
        segment_len: short.length,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    const marks = [...document.querySelectorAll("mark.find-hit")];
    expect(marks[1]?.classList.contains("find-hit-current")).toBe(true);
  });

  it("declines a mark below the similarity floor: block selection, stated", async () => {
    // A stale hit: nothing around the needle matches the excerpt.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <details class="reasoning-block msg-reasoning" data-entry-id="e1">
             <summary class="reasoning-summary">Reasoning</summary>
             <blockquote class="reasoning-body">the retry lives here in this trace</blockquote>
           </details>
       </div>`,
    );
    stageHits([
      serverHit({
        excerpt: "completely unrelated words sharing nothing with that trace",
        segment_kind: "reasoning",
        offset: 4,
        segment_len: 34,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    const details = document.querySelector("details.reasoning-block");
    await vi.waitFor(() => {
      expect(details?.classList.contains("find-target-flash")).toBe(true);
    });
    expect(countText()).toBe("1 of 1 \u00b7 not in rendered text");
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenLastCalledWith(details, expect.anything());
  });

  it("pages older history in until the hit's TURN is resident", async () => {
    // Residency is `turns.has(hit.turn_id)`; the cursor is the oldest resident turn id.
    const session = stageChat(["u9"], true);
    stageHits([
      serverHit({
        excerpt: "the old answer",
        segment_kind: "entry",
        offset: 0,
        segment_len: 0,
      }),
    ]);
    vi.mocked(storeLoad.loadMessages).mockImplementation((_chatID, _beforeID) => {
      prependTurn(session, "u1");
      session.has_more = false;
      return Promise.resolve(true);
    });
    vi.mocked(chatSearch.revealHitTurn).mockImplementation(() => {
      if (document.querySelector('[data-reconcile-key="u1"]') === null) {
        mountTurnCard("u1", `<div data-entry-id="e1" class="msg-row">the old answer</div>`);
      }
      return Promise.resolve();
    });

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(
        document.querySelector('[data-entry-id="e1"]')?.classList.contains("find-target-flash"),
      ).toBe(true);
    });
    expect(vi.mocked(storeLoad.loadMessages)).toHaveBeenCalledExactlyOnceWith("c1", "u9");
    expect(countText()).toBe("1 of 1");
  });

  it("states it when the hit's page cannot be loaded", async () => {
    stageChat(["u9"], false);
    stageHits([serverHit({ segment_kind: "entry", offset: 0, segment_len: 0 })]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 could not be loaded");
    });
    expect(vi.mocked(storeLoad.loadMessages)).not.toHaveBeenCalled();
  });

  it("states it when the revealed turn still holds no element for the entry", async () => {
    // The window no longer holds the entry after the reveal, and stepping must say so.
    stageChat(["u1"]);
    mountTurnCard("u1", null);
    stageHits([serverHit({ segment_kind: "entry", offset: 0, segment_len: 0 })]);

    await openAndSearch("role:assistant");
    typeAndEnter("role:assistant");

    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 1 \u00b7 could not be shown");
    });
  });

  it("sends an ordinary DELEGATE's hit to that delegate's page", async () => {
    // The counter reads the server's figure, so a delegate-output hit is counted though the transcript renders none of it.
    stageChat(["u1"]);
    mountTurnCard("u1", `<div class="msg-row" data-entry-id="e1"></div>`);
    stageHits([
      serverHit({
        entry_id: "e2",
        excerpt: "delegate found the retry backoff",
        segment_kind: "content",
        lane: "sub-9",
        offset: 19,
        segment_len: 32,
      }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");

    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledWith("c1", "sub-9");
    });
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
    expect(vi.mocked(runView.openRunView)).not.toHaveBeenCalled();
    expect(countText()).not.toContain("could not be");
  });

  // The spine: an owned server answer is the step list, whatever the walker marked.

  it("walks the server's list even while resident marks exist for the same query", async () => {
    // 2 DOM marks against 2 server hits, one in a non-resident turn.
    stageChat(["u1", "u9"]);
    mountTurnCard(
      "u1",
      `<div class="msg-row" data-entry-id="e1">
         <div class="message assistant">retry retry</div>
       </div>`,
    );
    const resident = serverHit({
      excerpt: "retry retry",
      offset: 0,
      segment_len: 11,
    });
    const elsewhere = serverHit({ turn_id: "u9", entry_id: "e9" });
    stageHits([resident, elsewhere]);

    onHotkey(ctrlF());
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    await settle();
    vi.mocked(chatSearch.revealHitTurn).mockClear();

    // The first press lands on the resident mark synchronously.
    typeAndEnter("retry");
    expect(countText()).toBe("1 of 2");
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();

    // The second reveals the hit the walker could never mark.
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(chatSearch.revealHitTurn)).toHaveBeenCalledExactlyOnceWith("c1", elsewhere);
    });
    expect(countText()).toContain("2 of 2");
  });

  it("steps a resident hit synchronously, so held Enter presses are not dropped", async () => {
    // `navBusy` drops an Enter mid-navigation, so resident hits must land synchronously.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry and retry</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({ excerpt: "retry and retry", offset: 0, segment_len: 15 }),
      serverHit({ excerpt: "retry and retry", offset: 10, segment_len: 15 }),
    ]);

    await openAndAdopt("retry");
    typeAndEnter("retry");
    typeAndEnter("retry");
    expect(countText()).toBe("2 of 2");
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("walks all 38 hits behind one resident mark without ever going dead", async () => {
    // One mark on screen, dozens of server matches.
    const HITS = 38;
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry</div>
           </div>
       </div>`,
    );
    stageHits(
      Array.from({ length: HITS }, (_, i) =>
        serverHit({ excerpt: "retry", offset: i, segment_len: HITS + 8 }),
      ),
    );

    await openAndAdopt("retry");
    const seen: string[] = [];
    for (let i = 0; i < HITS; i++) {
      typeAndEnter("retry");
      seen.push(countText());
    }
    expect(seen[0]).toBe(`1 of ${String(HITS)}`);
    expect(seen[HITS - 1]).toBe(`${String(HITS)} of ${String(HITS)}`);
    expect(seen.filter((line) => line.includes("could not be"))).toEqual([]);
    typeAndEnter("retry");
    expect(countText()).toBe(`1 of ${String(HITS)}`);
  });

  it("steps the resident marks while the standing answer belongs to the previous query", async () => {
    // The in-flight window: the `query` callback runs the DOM pass before the answer lands.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">budget check</div>
           </div>
       </div>`,
    );
    stageHits([serverHit({ entry_id: "e2", lane: "sub-9", offset: 9 })]);
    await openAndSearch("retry");

    vi.mocked(chatSearch.runServerSearch).mockReturnValue(new Promise(() => undefined));
    typeAndEnter("budget");
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(1);
    });

    typeAndEnter("budget");
    // The counter drops the session figure rather than report the previous query's.
    expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    expect(countText()).toBe("1 of 1");
    // Not the miss skin: unknown is not zero.
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();
  });

  it("visits hits in transcript order even when the only mark is in the last entry", async () => {
    // Order is the server's, not where marks happen to be.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div class="msg-row" data-entry-id="e1"></div>
       <div class="msg-row" data-entry-id="e2"></div>
       <div class="msg-row" data-entry-id="e3">
         <div class="message assistant">third retry</div>
       </div>`,
    );
    stageHits([
      serverHit({ entry_id: "e1", excerpt: "first retry" }),
      serverHit({ entry_id: "e2", excerpt: "second retry" }),
      serverHit({ entry_id: "e3", excerpt: "third retry", offset: 6, segment_len: 11 }),
    ]);

    await openAndSearch("retry");
    for (const id of ["e1", "e2"]) {
      typeAndEnter("retry");
      await vi.waitFor(() => {
        expect(vi.mocked(chatSearch.revealHitTurn).mock.lastCall?.[1].entry_id).toBe(id);
      });
    }
    // The engine marks its first match current on its own, so that is not asserted.
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(document.querySelector('[data-entry-id="e3"] mark.find-hit-current')).not.toBeNull();
    });
    expect(countText()).toContain("3 of 3");
  });

  it("partitions the walk by destination, announcing the boundary once per answer", async () => {
    // A cross-tab step tears the overlay down, so local hits are walked first.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div class="msg-row" data-entry-id="e2">
         <div class="message assistant">local retry</div>
       </div>`,
    );
    stageHits([
      serverHit({ entry_id: "e1", lane: "sub-1", excerpt: "delegate retried" }),
      serverHit({ entry_id: "e2", excerpt: "local retry", offset: 6 }),
    ]);

    await openAndSearch("retry");

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledExactlyOnceWith(
        "c1",
        "sub-1",
      );
    });
    expect(countText()).toBe(
      "2 of 2 \u00b7 the rest are in delegate pages and run tabs \u00b7 opening the delegate's page",
    );

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2 \u00b7 opening the delegate's page");
    });
    // Waits on the open: the counter paints before the lazy import resolves.
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledTimes(2);
    });
  });

  it("puts a MOUNTED delegate invocation in phase 2, because the routing decides", async () => {
    // The transcript draws one delegate entry, its invocation header, so a `tool_title` hit is visible there.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div class="subagent-block" data-entry-id="e1">
         <div class="subagent-header"><span class="tool-title">Sub-agent: retry-sweeper</span></div>
       </div>
       <div class="msg-row" data-entry-id="e2">
         <div class="message assistant">local retry</div>
       </div>`,
    );
    stageHits([
      serverHit({
        entry_id: "e1",
        lane: "sub-1",
        segment_kind: "tool_title",
        excerpt: "Sub-agent: retry-sweeper",
        offset: 11,
        segment_len: 24,
      }),
      serverHit({ entry_id: "e2", excerpt: "local retry", offset: 6 }),
    ]);

    await openAndAdopt("retry");

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 2");
    });
    expect(vi.mocked(subagentView.openSubagentView)).not.toHaveBeenCalled();

    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledExactlyOnceWith(
        "c1",
        "sub-1",
      );
    });
  });

  it("resumes the walk after a cross-tab jump, keeping the query", async () => {
    // The return trip: the box closes but keeps the query, and coming back restores the cursor.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">local retry</div>
           </div>
       </div>`,
    );
    stageHits([
      serverHit({ entry_id: "e1", excerpt: "local retry", offset: 6 }),
      serverHit({ entry_id: "e2", lane: "sub-1", excerpt: "first delegate retry" }),
      serverHit({ entry_id: "e3", lane: "sub-2", excerpt: "second delegate retry" }),
    ]);

    await openAndSearch("retry");
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("1 of 3");
    });
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView)).toHaveBeenCalledWith("c1", "sub-1");
    });

    // Activating a delegate's page does not change the active chat, so the query stays.
    switchTab();
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    expect(input.value).toBe("retry");

    onHotkey(ctrlF());
    await settle();
    typeAndEnter("retry");
    await vi.waitFor(() => {
      expect(vi.mocked(subagentView.openSubagentView).mock.lastCall).toEqual(["c1", "sub-2"]);
    });
  });

  // The History page's handoff opens this box with the query and a named hit.

  it("opens on a handoff carrying the query, landed on the hit it names rather than the first", async () => {
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry and retry</div>
           </div>
       </div>`,
    );
    const first = serverHit({
      excerpt: "retry and retry",
      offset: 0,
      segment_len: 15,
    });
    const second = serverHit({
      excerpt: "retry and retry",
      offset: 10,
      segment_len: 15,
    });
    stageHits([first, second]);

    openAt("retry", second);
    const input = document.getElementById("chat-find-input") as HTMLInputElement;
    expect(input.value).toBe("retry");
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2");
    });
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits).toHaveLength(2);
    expect(hits[1]?.classList.contains("find-hit-current")).toBe(true);
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(false);
    // One scroll: the open's own reveal is withheld while a landing is pending.
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[1], expect.anything());
  });

  it("opens on the answer it has when the handoff's hit is not in it", async () => {
    // The named hit is gone from the fresh answer, so nothing is paged in for it.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry once</div>
           </div>
       </div>`,
    );
    stageHits([serverHit({ excerpt: "retry once", offset: 0, segment_len: 10 })]);

    openAt("retry", serverHit({ turn_id: "u9", excerpt: "a retry that is gone", offset: 2 }));
    await settle();
    expect(countText()).toBe("1 of 1");
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[0], expect.anything());
    expect(vi.mocked(storeLoad.loadMessages)).not.toHaveBeenCalled();
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("does not carry a handoff's landing into a later open the reader makes", async () => {
    // Closed before the answer landed: the next open keeps query and cursor and jumps nowhere.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry and retry</div>
           </div>
       </div>`,
    );
    const first = serverHit({
      excerpt: "retry and retry",
      offset: 0,
      segment_len: 15,
    });
    const second = serverHit({
      excerpt: "retry and retry",
      offset: 10,
      segment_len: 15,
    });
    vi.mocked(chatSearch.runServerSearch).mockReturnValue(new Promise(() => undefined));
    openAt("retry", second);
    closeFind();

    stageHits([first, second]);
    onHotkey(ctrlF());
    await vi.waitFor(() => {
      expect(countText()).toBe("2 of 2");
    });
    const hits = [...document.querySelectorAll<HTMLElement>("mark.find-hit")];
    expect(hits[0]?.classList.contains("find-hit-current")).toBe(true);
    expect(vi.mocked(scroll.jumpTo)).toHaveBeenCalledWith(hits[0], expect.anything());
    expect(vi.mocked(scroll.jumpTo)).not.toHaveBeenCalledWith(hits[1], expect.anything());
  });

  it("steps the marks when there is no server list to walk", async () => {
    // The DOM pass is the whole step list wherever no list is owned (a mark the server never matched).
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="message assistant">workflow and workflow</div>
       </div>`,
    );
    stageHits([]);

    await openAndAdopt("workflow");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    expect(countText()).toBe("1 of 2");

    typeAndEnter("workflow");
    expect(countText()).toBe("2 of 2");
    expect(document.querySelector("mark.find-hit-current")).not.toBeNull();
    expect(vi.mocked(chatSearch.revealHitTurn)).not.toHaveBeenCalled();
  });

  it("keeps the standing answer whole when a later fetch fails", async () => {
    // A transient failure must not unseat the standing answer: hits, tally, order, cursor, note, reveal and ownership.
    stageChat(["u1"]);
    mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry once</div>
           </div>
       </div>
       <div data-reconcile-key="a2" class="msg-row">
           <div class="msg-row" data-entry-id="e2">
             <div class="message assistant">retry twice</div>
           </div>
       </div>`,
    );
    stageHits(
      [
        serverHit({ entry_id: "e1", excerpt: "retry once", offset: 0 }),
        serverHit({ entry_id: "e2", excerpt: "retry twice", offset: 0 }),
      ],
      { scanned: 3, matched: 3 },
    );
    await openAndAdopt("retry");
    expect(noteText()).toBe("2 of 3 matches shown; 3 messages scanned");

    vi.mocked(chatSearch.runServerSearch).mockResolvedValue(null);
    document.querySelector<HTMLElement>(".chat-find-case")?.click();
    await settle();

    expect(noteText()).toBe("2 of 3 matches shown; 3 messages scanned");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
    typeAndEnter("retry");
    expect(countText()).toBe("1 of 2 \u00b7 3 in chat");
    typeAndEnter("retry");
    expect(countText()).toBe("2 of 2 \u00b7 3 in chat");
  });

  it("clears the cut note when a failed fetch leaves an answer for older text", async () => {
    // The note describes one answer, so it paints only while that answer belongs to the box's text.
    stageChat(["u1"]);
    stageHits([serverHit()], { scanned: 24, matched: 347 });
    await openAndSearch("retry");
    expect(noteText()).not.toBe("");

    vi.mocked(chatSearch.runServerSearch).mockResolvedValue(null);
    typeAndEnter("budget");
    await vi.waitFor(() => {
      expect(noteText()).toBe("");
    });
    expect(countText()).toBe("No matches");
    expect(document.getElementById("chat-find")?.classList.contains("chat-find-no-results")).toBe(
      false,
    );
  });

  it("withholds the no-results skin until an owned answer says the text is nowhere", async () => {
    // The miss skin claims what only the server can say, so it waits for the answer.
    stageChat(["u1"]);
    const skinOn = (): boolean =>
      document.getElementById("chat-find")?.classList.contains("chat-find-no-results") === true;

    let answer: (result: SearchResult | null) => void = () => undefined;
    vi.mocked(chatSearch.runServerSearch).mockReturnValue(
      new Promise((resolve) => {
        answer = resolve;
      }),
    );

    onHotkey(ctrlF());
    typeAndEnter("nothing matches this");
    expect(countText()).toBe("No matches");
    expect(skinOn()).toBe(false);

    answer({ matches: [], scanned: 1, matched: 0, truncated: false });
    await vi.waitFor(() => {
      expect(skinOn()).toBe(true);
    });
  });

  it("does not step a mark that appeared after the answer was taken", async () => {
    // Accepted loss: the standing answer is frozen for one open at one query.
    stageChat(["u1"]);
    const card = mountTurnCard(
      "u1",
      `<div data-reconcile-key="a1" class="msg-row">
           <div class="msg-row" data-entry-id="e1">
             <div class="message assistant">retry</div>
           </div>
       </div>`,
    );
    stageHits([serverHit({ excerpt: "retry", offset: 0, segment_len: 5 })]);
    await openAndAdopt("retry");
    expect(document.querySelectorAll("mark.find-hit")).toHaveLength(1);

    // A turn sealing after the answer: the walker can mark it, the answer does not hold it.
    const sealed = document.createElement("div");
    sealed.className = "message assistant";
    sealed.textContent = "retry again";
    card.querySelector(".msg-row")?.appendChild(sealed);

    const rerun = vi.mocked(scroll.onTranscriptMutate).mock.calls[0]?.[0];
    expect(rerun).toBeTypeOf("function");
    rerun?.();
    await vi.waitFor(() => {
      expect(document.querySelectorAll("mark.find-hit")).toHaveLength(2);
    });
    expect(countText()).toBe("1 of 2");

    // Stepping walks the answer, so the new mark is never visited.
    const first = document.querySelectorAll<HTMLElement>("mark.find-hit")[0];
    for (let i = 0; i < 3; i++) {
      typeAndEnter("retry");
      expect(countText()).toBe("1 of 1");
      expect(document.querySelector("mark.find-hit-current")).toBe(first);
    }
  });
});
