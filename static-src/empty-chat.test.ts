// The empty chat against the shipped index.html: the logo stands in the transcript, and the
// model is chosen through the composer's pill.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type * as ChatActions from "./actions/chat.js";
import type * as SessionContext from "./session-context.js";

const { switchDispatch } = vi.hoisted(() => ({ switchDispatch: vi.fn() }));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  switchModel: { dispatch: switchDispatch },
}));
vi.mock("./session-context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SessionContext>()),
  setLastModel: vi.fn(),
  getLastEffortFor: () => "",
}));

import { setSessions, setActive, setPainted, setThinking } from "./store.js";
import { setCatalogModels, setCatalogPhase } from "./model-list.js";
import { initEmptyChatLogo } from "./empty-chat-logo.js";
import { initModelSwitcher } from "./model-switcher.js";
import { collapseAll } from "./pill-expand.js";
import type { Entry, Session, TurnState } from "./types.js";

const shipped = import.meta.glob<string>(["../static/index.html", "../static/favicon.svg"], {
  query: "?raw",
  import: "default",
  eager: true,
});
const INDEX_HTML = shipped["../static/index.html"] ?? "";
const FAVICON = shipped["../static/favicon.svg"] ?? "";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "chat-1",
    name: "test",
    model: "model-a",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    turns: new Map(),
    turn_order: [],
    turn_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...overrides,
  };
}

function residentTurn(text: string): Pick<Session, "turns" | "turn_order" | "turn_count"> {
  const open: Entry = {
    id: "t1-open",
    turn: "t1",
    kind: "turn_open",
    seq: 0,
    ts: 1,
    payload: { source: "prompt", n: 1, prompt: { id: "m-1", text } },
  };
  const state: TurnState = { entries: [open], openEntries: new Map() };
  return { turns: new Map([["t1", state]]), turn_order: ["t1"], turn_count: 1 };
}

/** The reader on chat `id` with its transcript mounted, as a chat tab's activation leaves it. */
function openChat(id: string): void {
  setActive(id);
  setPainted(id);
}

function logo(): HTMLElement {
  const found = document.querySelector<HTMLElement>("#messages-wrap #empty-chat-logo");
  expect(found, "the transcript holds no empty-chat logo").not.toBeNull();
  return found as HTMLElement;
}

function logoShown(): boolean {
  return !logo().classList.contains("hidden");
}

function pill(): HTMLElement {
  const found = document.getElementById("switch-model-btn");
  expect(found, "index.html lost the model pill").not.toBeNull();
  return found as HTMLElement;
}

describe("the shipped empty-chat markup", () => {
  it("draws the logo mark and no model grid in the transcript", () => {
    const doc = new DOMParser().parseFromString(INDEX_HTML, "text/html");
    const wrap = doc.getElementById("messages-wrap");
    expect(wrap).not.toBeNull();
    expect(doc.getElementById("model-picker")).toBeNull();
    expect(doc.querySelector(".picker-grid, .picker-btn")).toBeNull();

    const mark = wrap?.querySelector("#empty-chat-logo svg");
    expect(mark?.getAttribute("aria-hidden")).toBe("true");
    expect(mark?.querySelector("use")?.getAttribute("href")).toBe("#vk-logo-mark");
  });

  it("takes the mark from the favicon's artwork, without its tile", () => {
    const doc = new DOMParser().parseFromString(INDEX_HTML, "text/html");
    const mark = doc.getElementById("vk-logo-mark");
    expect(mark, "index.html defines no tile-free mark").not.toBeNull();
    expect(mark?.querySelector("rect")).toBeNull();
    for (const shape of mark?.querySelectorAll("path, circle") ?? []) {
      expect(shape.getAttribute("fill")).toBe("currentColor");
    }

    const favicon = new DOMParser().parseFromString(FAVICON, "image/svg+xml");
    const faviconPath = favicon.querySelector("g path")?.getAttribute("d");
    expect(faviconPath).toBeTruthy();
    expect(mark?.querySelector("path")?.getAttribute("d")).toBe(faviconPath);
  });
});

describe("the empty chat", () => {
  // One fixture: the logo effect and the pill controller are app-lifetime singletons.
  beforeAll(() => {
    const doc = new DOMParser().parseFromString(INDEX_HTML, "text/html");
    document.body.replaceChildren(
      ...[...doc.body.children].map((n) => document.importNode(n, true)),
    );
    setCatalogModels([
      { model_id: "model-a", model_name: "Model A", rate_multiplier: 1 },
      { model_id: "model-b", model_name: "Model B", rate_multiplier: 1 },
    ]);
    setCatalogPhase("ready");
    initEmptyChatLogo();
    initModelSwitcher(() => Promise.resolve());
  });

  beforeEach(() => {
    collapseAll();
    switchDispatch.mockReset();
    setSessions([makeSession()]);
    openChat("chat-1");
  });

  it("shows the logo", () => {
    expect(logoShown()).toBe(true);
    expect(document.querySelector("#messages-wrap [data-model]")).toBeNull();
  });

  it("shows the logo before a model is chosen", () => {
    setSessions([makeSession({ model: "" })]);
    openChat("chat-1");
    expect(logoShown()).toBe(true);
  });

  it("hides the logo once the chat holds a turn", () => {
    setSessions([makeSession(residentTurn("hello"))]);
    openChat("chat-1");
    expect(logoShown()).toBe(false);
  });

  it("hides the logo for a chat the server already counts turns for", () => {
    setSessions([makeSession({ turn_count: 4 })]);
    openChat("chat-1");
    expect(logoShown()).toBe(false);
  });

  it("hides the logo as soon as a turn starts", () => {
    setThinking("chat-1", true);
    expect(logoShown()).toBe(false);
  });

  it("hides the logo when no chat is painted", () => {
    openChat("");
    expect(logoShown()).toBe(false);
  });

  it("picks a model through the pill", () => {
    pill().click();
    expect(pill().getAttribute("aria-expanded")).toBe("true");
    const option = document.querySelector<HTMLElement>(
      '#model-switch-list .pill-model-item[data-model="model-b"]',
    );
    expect(option, "the pill's card lists no model-b row").not.toBeNull();

    option?.click();

    expect(pill().getAttribute("aria-expanded")).toBe("false");
    expect(switchDispatch.mock.calls).toEqual([[{ chatID: "chat-1", model: "model-b" }]]);
  });
});

describe("the open model card while the catalog loads", () => {
  it("repaints when the catalog lands", () => {
    collapseAll();
    setCatalogModels([]);
    setCatalogPhase("unknown");
    pill().click();
    const scroll = document.querySelector<HTMLElement>("#model-switch-list .pill-model-scroll");
    expect(scroll?.textContent).toContain("Loading models…");

    setCatalogModels([{ model_id: "model-a", model_name: "Model A", rate_multiplier: 1 }]);
    setCatalogPhase("ready");

    expect(scroll?.textContent).not.toContain("Loading models…");
    expect(scroll?.querySelectorAll(".pill-model-item")).toHaveLength(1);
    expect(scroll?.getAttribute("role")).toBe("listbox");
    collapseAll();
  });

  it("repaints when the read gives up", () => {
    setCatalogModels([]);
    setCatalogPhase("unknown");
    pill().click();
    const scroll = document.querySelector<HTMLElement>("#model-switch-list .pill-model-scroll");

    setCatalogPhase("unavailable");

    expect(scroll?.textContent).toContain("Could not load the model list.");
    expect(scroll?.querySelector("button.pill-model-retry")).not.toBeNull();
    collapseAll();
  });
});
