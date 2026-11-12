// The view a page reports to the server's notice ledger: when it reports, what it says about its
// active tab (only a chat or run tab is named), and that every report survives a freeze.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import { BUS_TAB_CHANGED, dispatch, emitBus } from "./bus.js";
import type { TabSubject } from "./wire/types.gen.js";
import type * as SSEAdapter from "./sse-adapter.js";
import type * as Tabs from "./tabs.js";

const h = vi.hoisted(() => ({
  tag: "tag-A",
  tagListeners: [] as ((tag: string) => void)[],
  active: "",
  subjects: [] as { id: string; kind: string; ref: string }[],
}));

vi.mock("./sse-adapter.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SSEAdapter>()),
  presentedTag: (): string => h.tag,
  onPresentedTag: (fn: (tag: string) => void): (() => void) => {
    h.tagListeners.push(fn);
    return () => {
      h.tagListeners = h.tagListeners.filter((l) => l !== fn);
    };
  },
}));

vi.mock("./tabs.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Tabs>()),
  getActiveTabId: (): string => h.active,
  openTabSubjects: (): TabSubject[] => h.subjects as unknown as TabSubject[],
}));

const { installPageView, pageID } = await import("./page-view.js");

interface Sent {
  readonly body: Record<string, unknown>;
  readonly tag: string | null;
  readonly keepalive: boolean | undefined;
}

const fetchSpy = vi.fn(
  async (_url: string, _init?: RequestInit): Promise<Response> =>
    new Response(null, { status: 204 }),
);

function sent(): Sent[] {
  return fetchSpy.mock.calls
    .filter(([url]) => url === "/api/events/view")
    .map(([, init]) => ({
      body: JSON.parse(String(init?.body)) as Record<string, unknown>,
      tag: new Headers(init?.headers).get("SSE-Client"),
      keepalive: init?.keepalive,
    }));
}

function last(): Record<string, unknown> | undefined {
  return sent().at(-1)?.body;
}

let visibility: DocumentVisibilityState = "visible";
let teardown: (() => void) | null = null;

function show(state: DocumentVisibilityState): void {
  visibility = state;
  document.dispatchEvent(new Event("visibilitychange"));
}

function activate(kind: string, ref: string): void {
  h.subjects = [{ id: `t-${kind}-${ref}`, kind, ref }];
  h.active = `t-${kind}-${ref}`;
  emitBus(BUS_TAB_CHANGED, { to: h.active, kind });
}

const tick = (): Promise<void> => new Promise((r) => setTimeout(r, 0));

beforeEach(() => {
  h.tag = "tag-A";
  h.tagListeners = [];
  h.active = "";
  h.subjects = [];
  visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
  vi.spyOn(document, "hasFocus").mockReturnValue(true);
  fetchSpy.mockClear();
  vi.stubGlobal("fetch", fetchSpy);
});

afterEach(() => {
  teardown?.();
  teardown = null;
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("installPageView", () => {
  it("reports at install with no connected frame, carrying the tag and keepalive", async () => {
    activate("chat", "c1");
    teardown = installPageView();
    await tick();
    expect(sent()).toEqual([
      {
        body: { page: pageID(), visible: true, focused: true, tab: { kind: "chat", ref: "c1" } },
        tag: "tag-A",
        keepalive: true,
      },
    ]);
  });

  it("reports again on a connected frame, a visibility change and a tab switch", async () => {
    teardown = installPageView();
    await tick();
    const base = sent().length;
    dispatch({ type: "connected", chat_id: "", payload: {} });
    await tick();
    expect(sent()).toHaveLength(base + 1);
    show("hidden");
    await tick();
    expect(last()).toMatchObject({ visible: false });
    activate("run", "wf1");
    await tick();
    expect(last()).toMatchObject({ tab: { kind: "run", ref: "wf1" } });
    expect(sent().every((s) => s.keepalive === true)).toBe(true);
  });

  it("names no tab with an editor tab, Settings or no tab active", async () => {
    activate("editor", "/w/a.go");
    teardown = installPageView();
    await tick();
    expect(last()).toMatchObject({ tab: null });
    activate("settings", "");
    await tick();
    expect(last()).toMatchObject({ tab: null });
    h.active = "";
    h.subjects = [];
    emitBus(BUS_TAB_CHANGED, { to: "", kind: null });
    await tick();
    expect(last()).toMatchObject({ tab: null });
    expect(sent()).toHaveLength(3);
  });

  it("carries a new presented tag on the report it triggers", async () => {
    teardown = installPageView();
    await tick();
    h.tag = "tag-B";
    for (const l of h.tagListeners) {
      l("tag-B");
    }
    await tick();
    expect(sent().at(-1)?.tag).toBe("tag-B");
  });

  it("re-reports every 15 s while visible and not while hidden", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "setInterval", "clearTimeout", "clearInterval"] });
    teardown = installPageView();
    await vi.advanceTimersByTimeAsync(1);
    const atInstall = sent().length;
    await vi.advanceTimersByTimeAsync(15_000);
    expect(sent()).toHaveLength(atInstall + 1);
    show("hidden");
    await vi.advanceTimersByTimeAsync(1);
    const atHide = sent().length;
    await vi.advanceTimersByTimeAsync(45_000);
    expect(sent()).toHaveLength(atHide);
  });

  it("reports the page closed at pagehide", async () => {
    teardown = installPageView();
    await tick();
    window.dispatchEvent(new PageTransitionEvent("pagehide"));
    await tick();
    expect(last()).toEqual({ page: pageID(), closed: true });
    expect(sent().at(-1)?.keepalive).toBe(true);
  });

  it("sends nothing before the stream has a tag, and reports on the next trigger with one", async () => {
    h.tag = "";
    teardown = installPageView();
    show("hidden");
    await tick();
    expect(sent()).toEqual([]);
    h.tag = "tag-A";
    show("visible");
    await tick();
    expect(sent()).toHaveLength(1);
  });
});
