// Shared fakes for the tab-switch suites, which run the real tabs.ts against the fake tab server
// with the chat opener stubbed. Factories only: each suite's `vi.mock` lines stay in the suite
// (hoisting), and this module imports no production code, because a mock factory imports it.
//
// The chat opener's two halves are fakes of chat.ts's contract: `select` moves the store's active
// chat, `show` moves the painted chat, and the store mock reads both back through `pointers`.
// chat.ts's own suites pin the real halves.
import { vi } from "vitest";
import type { Mock } from "vitest";

/** The store's two chat pointers, as the chat opener's fake halves write them. */
export const pointers = { active: "", painted: "" };

/** Chat ids with resident turns; a chat absent here reads as evicted (known turns, empty window). */
export const resident = new Set<string>();
/** Chat ids with no conversation at all (no turns on the record); a just-created chat. */
export const emptyChats = new Set<string>();

/** Every icon name tabs.ts's graph imports, as "": Browser Mode links real ESM. */
export function iconsMock(): Record<string, string> {
  const names = [
    "ICON_MODEL_UI",
    "ICON_CLOSE",
    "ICON_TAB_CHAT",
    "ICON_TAB_SETTINGS",
    "ICON_TAB_GIT",
    "ICON_TAB_FILES",
    "ICON_TAB_RUN",
    "ICON_TAB_WEB",
    "ICON_TAB_AGENT",
    "ICON_TAB_PLAN",
    "ICON_TAB_SPEC",
    "ICON_TAB_QUICK_SPEC",
    "ICON_TAB_BUG",
    "ICON_TAB_AUTONOMOUS",
    "ICON_TAB_REVIEW",
    "ICON_SUBAGENT_INTROSPECT",
    "ICON_SUBAGENT_GATHERER",
    "ICON_SUBAGENT_TASK",
    "ICON_SUBAGENT_CREATOR",
    "ICON_TAB_EDITOR",
    "ICON_TAB_HISTORY",
    "ICON_TAB_DOCS",
    "ICON_TAB_SUBTAB",
    "ICON_SEND",
    "ICON_SPINNER",
    "ICON_HOURGLASS",
    "ICON_ALERT",
    "ICON_PIN_FILLED",
  ];
  return Object.fromEntries(names.map((n) => [n, ""]));
}

export function deviceViewMock(): { activeView: Mock; setActiveView: Mock } {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
}

const ELEMENT_IDS: Readonly<Record<string, string>> = {
  tabList: "tab-list",
  sidebar: "sidebar",
  promptInput: "prompt-input",
  messages: "messages",
  messagesWrap: "messages-wrap",
  messagesWrapOuter: "messages-wrap-outer",
};

/** The page shell the cover and the strip write into: the strip, the transcript chain and the
 *  composer, attached, with a focusable button inside the outgoing transcript. */
export function mountShell(): void {
  document.body.innerHTML =
    '<aside id="sidebar"><div id="tab-list"></div></aside>' +
    '<div id="messages-wrap-outer" style="position:relative;height:400px">' +
    '<div id="messages-wrap"><div id="messages"><button id="in-transcript">copy</button></div></div>' +
    "</div>" +
    '<textarea id="prompt-input"></textarea>';
}

/** `dom.js` with the registry's getters resolved against `mountShell`'s elements; any other
 *  getter answers a detached div, as the tabs suite's proxy does. */
export function domMock(): Record<string, unknown> {
  const setBusy = (el: Element, busy: boolean): void => {
    if (busy) {
      el.setAttribute("aria-busy", "true");
    } else {
      el.removeAttribute("aria-busy");
    }
  };
  const byId = (id: string): HTMLElement => {
    let found = document.getElementById(id);
    if (found === null) {
      found = document.createElement("span");
      found.id = id;
      document.body.appendChild(found);
    }
    return found;
  };
  return {
    $: new Proxy(
      {},
      {
        get: (_t, prop: string) => {
          const id = ELEMENT_IDS[prop];
          return id === undefined ? document.createElement("div") : byId(id);
        },
      },
    ),
    setBusy,
    byId,
    maybeEl: (id: string) => document.getElementById(id),
    forceReflow: (el: Element) => el.getBoundingClientRect().height,
  };
}

/** The composer half tabs.ts drives, inert. */
export function composerStateMock(): Record<string, Mock> {
  return {
    retargetComposer: vi.fn(),
    restoreFailedSend: vi.fn(),
    saveComposerState: vi.fn(),
    restoreComposerState: vi.fn(),
    flushComposerDraft: vi.fn(),
    dropComposerState: vi.fn(),
    seedComposerState: vi.fn(),
    adoptRemoteComposerState: vi.fn(),
    noteComposerText: vi.fn(),
    initComposerState: vi.fn(),
    _resetComposerStateForTest: vi.fn(),
  };
}

/** The store mock's pointer readers, over `pointers`; a chat's resident turns come from
 *  `resident`, its emptiness from `emptyChats`. Spread over the canonical store mock. */
export function storePointerOverrides(): Record<string, unknown> {
  const session = (id: string): unknown =>
    id === ""
      ? undefined
      : {
          id,
          name: id,
          turns: new Map(),
          turn_order: resident.has(id) ? [`${id}-t1`] : [],
          turn_count: emptyChats.has(id) ? 0 : 1,
          has_more: false,
          thinking: false,
        };
  return {
    getActiveId: () => pointers.active,
    getPaintedId: () => pointers.painted,
    getActive: () => session(pointers.active),
    getPainted: () => session(pointers.painted),
    watchActiveId: () => pointers.active,
    watchPaintedId: () => pointers.painted,
    setActive: (id: string) => {
      pointers.active = id;
    },
    setPainted: (id: string) => {
      pointers.painted = id;
    },
    get: (id: string) => session(id),
    isEmptyChat: (s: { id: string } | undefined) =>
      s === undefined || (emptyChats.has(s.id) && !resident.has(s.id)),
  };
}

/** A promise with its settlers, for a page GET a case holds open. */
export interface Held {
  readonly promise: Promise<void>;
  resolve(): void;
  reject(err: unknown): void;
  /** The signal the refresh was handed. */
  signal: AbortSignal | null;
}

export function held(): Held {
  let resolve: () => void = () => undefined;
  let reject: (err: unknown) => void = () => undefined;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  // The rejection is the code under test's to observe; this keeps an unobserved one from failing
  // the file before the code attaches its handler.
  promise.catch(() => undefined);
  return { promise, resolve, reject, signal: null };
}

/** The chat opener's spies. `refresh` answers the next queued page for its chat, else resolves. */
export interface ChatOpenerFake {
  readonly select: Mock<(chatID: string) => void>;
  readonly show: Mock<(chatID: string) => void>;
  readonly prefetch: Mock<(chatID: string, signal: AbortSignal) => void>;
  readonly refresh: Mock<(chatID: string, signal: AbortSignal) => Promise<void>>;
  readonly loadFailed: Mock<(chatID: string) => void>;
  readonly close: Mock<(chatID: string) => void>;
  readonly dot: () => "";
  /** Queue the answer of `chatID`'s next refresh. */
  page(chatID: string, answer?: Held): Held;
}

export function chatOpenerFake(): ChatOpenerFake {
  const queued = new Map<string, Held[]>();
  return {
    select: vi.fn((id: string) => {
      pointers.active = id;
    }),
    show: vi.fn((id: string) => {
      pointers.painted = id;
    }),
    prefetch: vi.fn(),
    refresh: vi.fn((id: string, signal: AbortSignal) => {
      const next = queued.get(id)?.shift();
      if (next === undefined) {
        return Promise.resolve();
      }
      next.signal = signal;
      return next.promise;
    }),
    loadFailed: vi.fn(),
    close: vi.fn(),
    dot: () => "",
    page(chatID, answer = held()) {
      const list = queued.get(chatID) ?? [];
      list.push(answer);
      queued.set(chatID, list);
      return answer;
    },
  };
}

/** Reset the pointer and residency fakes; call first in a beforeEach. */
export function resetPointers(): void {
  pointers.active = "";
  pointers.painted = "";
  resident.clear();
  emptyChats.clear();
}

/** One frame: the strip's `renderDOM` is a frame callback. */
export function frame(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** One frame, then one task: a deferred activation half (`afterNextPaint`) has run. */
export async function activationRan(): Promise<void> {
  await frame();
  await new Promise((r) => setTimeout(r, 0));
}

/** The row element for a tab id. */
export function rowEl(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`#tab-list [data-tab-id="${id}"]`);
  if (el === null) {
    throw new Error(`no row for ${id}`);
  }
  return el;
}

/** A pointer click on a row, as the strip's click listener receives it. */
export function clickRow(id: string): void {
  rowEl(id).dispatchEvent(new MouseEvent("click", { bubbles: true }));
}

/** The cover, or null while none was ever created. */
export function cover(): HTMLElement | null {
  return document.getElementById("transcript-cover");
}

/** The cover is up: present, not hidden, not fading out. */
export function coverUp(): boolean {
  const c = cover();
  return c !== null && !c.hidden && c.getAttribute("data-state") !== "released";
}
