// The spec tab as a SUB-TAB: nesting, the close cascade, the per-generation indent, and the one
// mutation that reassigns a parent.

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import type { Mock } from "vitest";

vi.mock("./router.js", () => ({ pushRoute: vi.fn() }));
vi.mock("./icons.js", () => ({
  ICON_CLOSE: "",
  ICON_TAB_CHAT: "",
  ICON_TAB_SETTINGS: "",
  ICON_TAB_GIT: "",
  ICON_TAB_FILES: "",
  ICON_TAB_RUN: "",
  ICON_TAB_WEB: "",
  ICON_TAB_AGENT: "",
  // roles.ts is in this graph (the factory derives a delegate tab's label from it), and Browser
  // Mode links a mock as real ESM, so every name any module here imports has to exist on the
  // factory or the file fails at link time.
  ICON_TAB_PLAN: "",
  ICON_TAB_SPEC: "",
  ICON_TAB_QUICK_SPEC: "",
  ICON_TAB_BUG: "",
  ICON_TAB_AUTONOMOUS: "",
  ICON_SUBAGENT_INTROSPECT: "",
  ICON_SUBAGENT_GATHERER: "",
  ICON_SUBAGENT_TASK: "",
  ICON_SUBAGENT_CREATOR: "",
  ICON_TAB_EDITOR: "",
  ICON_TAB_HISTORY: "",
  ICON_TAB_DOCS: "",
  ICON_TAB_SUBTAB: "",
  ICON_SEND: "",
  ICON_SPINNER: "",
  ICON_HOURGLASS: "",
  ICON_ALERT: "",
  ICON_PIN_FILLED: "",
}));
vi.mock("./device-view.js", () => {
  let active = "";
  return {
    activeView: vi.fn(() => active),
    setActiveView: vi.fn((id: string) => {
      active = id;
    }),
  };
});
vi.mock("./dom.js", () => ({
  $: new Proxy(
    {},
    {
      get: (_t, prop: string) => {
        // tabList has to be a stable document-attached element, because the real renderDOM appends
        // the rows every assertion here reads. promptInput too: closing the last tab moves focus to
        // the composer.
        if (prop === "tabList") {
          let tl = document.getElementById("tab-list");
          if (tl === null) {
            tl = document.createElement("div");
            tl.id = "tab-list";
            document.body.appendChild(tl);
          }
          return tl;
        }
        if (prop === "sidebar") {
          let sidebar = document.getElementById("sidebar");
          if (sidebar === null) {
            sidebar = document.createElement("aside");
            sidebar.id = "sidebar";
            document.body.appendChild(sidebar);
          }
          return sidebar;
        }
        if (prop === "promptInput") {
          let input = document.getElementById("prompt-input");
          if (input === null) {
            input = document.createElement("textarea");
            input.id = "prompt-input";
            document.body.appendChild(input);
          }
          return input;
        }
        return document.createElement("div");
      },
    },
  ),
  byId: (id: string) => {
    let el = document.getElementById(id);
    if (el === null) {
      el = document.createElement("span");
      el.id = id;
      document.body.appendChild(el);
    }
    return el;
  },
}));
import type * as TabsDrag from "./tabs-drag.js";
vi.mock("./tabs-drag.js", async (importOriginal) => ({
  ...(await importOriginal<typeof TabsDrag>()),
  attachDrag: vi.fn(),
  isDragHandled: vi.fn(() => false),
  setReorderCallback: vi.fn(),
}));
// `store.js` is deliberately NOT mocked. No case here stages chat state — the factory reads it only
// for a DISPLAY NAME, and every row below is addressed by its server-minted id — so the real module
// is both sufficient and one fewer thing to keep in step with `store.ts`'s own exports.
vi.mock("./run-store.js", () => ({ runLabelOf: vi.fn(() => "") }));
vi.mock("./composer-state.js", () => ({
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
}));
vi.mock("./context-menu.js", () => ({ showContextMenu: vi.fn() }));
vi.mock("./chat-export.js", () => ({ downloadChatExport: vi.fn() }));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
vi.mock("./transport.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => m.tabTransportMock()),
);
vi.mock("./api-client.js", () =>
  import("./__test-helpers__/tabs-server.js").then((m) => ({
    apiGetTyped: m.tabListRead(),
    apiGet: vi.fn(() => Promise.resolve(null)),
  })),
);

import { openTab, closeTab, hasTab, tabIdFor, setTabParent, _resetForTest } from "./tabs.js";
import { _resetForTest as _resetFreshnessForTest } from "./subject-versions.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import type { TabOpeners } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

// `materializeTab` refuses to build a spec with no openers registered, so every case needs these.
// The spec pair is the one this suite is about: the FEAT's stub `spec-view.ts` is what the
// composition root lazily imports, and the factory only ever reaches it through this seam.
interface Openers {
  chatShow: Mock<TabOpeners["chat"]["show"]>;
  chatRefresh: Mock<TabOpeners["chat"]["refresh"]>;
  chatClose: Mock<TabOpeners["chat"]["close"]>;
  editorShow: Mock<TabOpeners["editor"]["show"]>;
  editorRefresh: Mock<TabOpeners["editor"]["refresh"]>;
  editorClose: Mock<TabOpeners["editor"]["close"]>;
  runShow: Mock<TabOpeners["run"]["show"]>;
  runRefresh: Mock<TabOpeners["run"]["refresh"]>;
  subagentShow: Mock<TabOpeners["subagent"]["show"]>;
  subagentRefresh: Mock<TabOpeners["subagent"]["refresh"]>;
  specShow: Mock<TabOpeners["spec"]["show"]>;
  specRefresh: Mock<TabOpeners["spec"]["refresh"]>;
}

let openers: Openers;

function registerOpeners(): void {
  openers = {
    chatShow: vi.fn<TabOpeners["chat"]["show"]>(),
    chatRefresh: vi.fn<TabOpeners["chat"]["refresh"]>(),
    chatClose: vi.fn<TabOpeners["chat"]["close"]>(),
    editorShow: vi.fn<TabOpeners["editor"]["show"]>(),
    editorRefresh: vi.fn<TabOpeners["editor"]["refresh"]>(),
    editorClose: vi.fn<TabOpeners["editor"]["close"]>(),
    runShow: vi.fn<TabOpeners["run"]["show"]>(),
    runRefresh: vi.fn<TabOpeners["run"]["refresh"]>(),
    subagentShow: vi.fn<TabOpeners["subagent"]["show"]>(),
    subagentRefresh: vi.fn<TabOpeners["subagent"]["refresh"]>(),
    specShow: vi.fn<TabOpeners["spec"]["show"]>(),
    specRefresh: vi.fn<TabOpeners["spec"]["refresh"]>(),
  };
  registerTabOpeners({
    chat: {
      show: openers.chatShow,
      refresh: openers.chatRefresh,
      close: openers.chatClose,
      dot: () => "",
    },
    editor: {
      show: openers.editorShow,
      refresh: openers.editorRefresh,
      close: openers.editorClose,
    },
    run: { show: openers.runShow, refresh: openers.runRefresh },
    subagent: { show: openers.subagentShow, refresh: openers.subagentRefresh },
    spec: { show: openers.specShow, refresh: openers.specRefresh },
  });
}

const SPEC_DIR = ".kiro/specs/marotte-spec-tab";

/** A spec tab is always a VIEW, so `owns: false` rides every open. */
function openSpec(dir: string, parent?: string): Promise<unknown> {
  return openTab(
    parent === undefined
      ? { kind: "spec", ref: dir, owns: false }
      : { kind: "spec", ref: dir, parent, owns: false },
  );
}

/** renderDOM is rAF-deferred, so a DOM read has to wait for it. */
function paint(): Promise<void> {
  return new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

/** The rendered row for an id. It THROWS rather than asserting, so a case's own
 *  `expect.assertions` count stays the count of what that case checks. */
async function rowFor(id: string): Promise<HTMLElement> {
  await paint();
  const list = document.getElementById("tab-list");
  const row = list?.querySelector<HTMLElement>(`[data-tab-id="${id}"]`) ?? null;
  if (row === null) {
    throw new Error(`no row rendered for ${id}`);
  }
  return row;
}

/** The indent the row carries, read off the inline custom property renderDOM writes. The class
 *  says "is a child"; this says HOW DEEP, which is the fact a grandchild needs and the class
 *  cannot express. */
async function depthOf(id: string): Promise<string> {
  const row = await rowFor(id);
  return row.style.getPropertyValue("--tab-depth");
}

beforeEach(() => {
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerOpeners();
  resetActionFramework();
  _resetForTest();
  _resetFreshnessForTest();
  document.body.innerHTML = '<div id="tab-list"></div>';
});

afterEach(() => {
  _resetTabsSyncForTest();
});

describe("a spec tab nests under its chat", () => {
  it("hangs under its parent chat as a view it does not own", async () => {
    expect.assertions(4);
    await openTab({ kind: "chat", ref: "c-1" });
    const chat = tabIdFor("chat", "c-1");
    await openSpec(SPEC_DIR, chat);
    const spec = tabIdFor("spec", SPEC_DIR);

    const row = await rowFor(spec);
    expect(row.classList.contains("tab-child")).toBe(true);
    expect(row.dataset["parentId"]).toBe(chat);
    // `owns: false` is the close contract: the sub-tab's x stops watching the spec, the chat's x is
    // what ends the conversation.
    expect(tabServer.subjects().find((s) => s.id === spec)?.owns).toBe(false);
    expect(tabServer.subjects().find((s) => s.id === spec)?.parent).toBe(chat);
  });

  it("closes with the chat it hangs under", async () => {
    expect.assertions(3);
    await openTab({ kind: "chat", ref: "c-1" });
    const chat = tabIdFor("chat", "c-1");
    await openSpec(SPEC_DIR, chat);
    expect(hasTab("spec", SPEC_DIR)).toBe(true);

    await closeTab(chat);
    expect(hasTab("chat", "c-1")).toBe(false);
    expect(hasTab("spec", SPEC_DIR)).toBe(false);
  });

  // A spec directory the reader opened from a path link belongs to no conversation, so nothing
  // invents a parent for it.
  it("stays top-level with no parent", async () => {
    expect.assertions(2);
    await openSpec(SPEC_DIR);
    const row = await rowFor(tabIdFor("spec", SPEC_DIR));
    expect(row.classList.contains("tab-child")).toBe(false);
    expect(row.dataset["parentId"]).toBeUndefined();
  });
});

// The indent is per GENERATION rather than per child, which is what a spec under a tangent needs:
// the tangent is already indented, so its spec has to step in once more or the two read as
// siblings.
describe("--tab-depth", () => {
  it("counts the generations rather than answering is-a-child", async () => {
    expect.assertions(3);
    await openTab({ kind: "chat", ref: "c-parent" });
    const parent = tabIdFor("chat", "c-parent");
    await openTab({ kind: "chat", ref: "c-tangent", parent });
    const tangent = tabIdFor("chat", "c-tangent");
    await openSpec(SPEC_DIR, tangent);
    const spec = tabIdFor("spec", SPEC_DIR);

    expect(await depthOf(parent)).toBe("0");
    expect(await depthOf(tangent)).toBe("1");
    expect(await depthOf(spec)).toBe("2");
  });
});

describe("setTabParent", () => {
  it("dispatches reparent_tab and adopts the subject the reply carries", async () => {
    expect.assertions(4);
    await openTab({ kind: "chat", ref: "c-1" });
    await openTab({ kind: "chat", ref: "c-2" });
    const first = tabIdFor("chat", "c-1");
    const second = tabIdFor("chat", "c-2");
    await openSpec(SPEC_DIR, first);
    const spec = tabIdFor("spec", SPEC_DIR);

    await expect(setTabParent(spec, second)).resolves.toBe(true);
    expect(tabServer.sentOfType("reparent_tab")).toHaveLength(1);
    const row = await rowFor(spec);
    expect(row.dataset["parentId"]).toBe(second);
    expect(row.style.getPropertyValue("--tab-depth")).toBe("1");
  });

  // An unchanged parent commits nothing server-side and so emits no frame; the local check answers
  // before spending the round trip.
  it("sends nothing when the tab already hangs there", async () => {
    expect.assertions(2);
    await openTab({ kind: "chat", ref: "c-1" });
    const chat = tabIdFor("chat", "c-1");
    await openSpec(SPEC_DIR, chat);
    const spec = tabIdFor("spec", SPEC_DIR);

    await expect(setTabParent(spec, chat)).resolves.toBe(true);
    expect(tabServer.sentOfType("reparent_tab")).toHaveLength(0);
  });

  it("refuses a tab the projection does not hold", async () => {
    expect.assertions(2);
    await openTab({ kind: "chat", ref: "c-1" });
    await expect(setTabParent("tb_absent", tabIdFor("chat", "c-1"))).resolves.toBe(false);
    expect(tabServer.sentOfType("reparent_tab")).toHaveLength(0);
  });
});
