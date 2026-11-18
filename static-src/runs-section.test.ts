// The sidebar's Runs section over the shipped markup and stylesheet, fed through the real live-run
// inventory, run store and tab projection: which runs it lists, when a row leaves, what its dot says,
// where the dot sits and where focus goes.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import type * as Router from "./router.js";
import type * as Toast from "./toast.js";
import type * as Transport from "./transport.js";
import type * as ApiClient from "./api-client.js";

const h = vi.hoisted(() => ({
  /** What `GET /api/runs/{id}?view=header` answers, by workflow id; absent is a 404. */
  headers: new Map<string, { status: string; name: string }>(),
}));

vi.mock("./router.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Router>()),
  pushRoute: vi.fn(),
  replaceRoute: vi.fn(),
}));
vi.mock("./toast.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Toast>()),
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
}));
vi.mock("./transport.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Transport>()),
  ...(await import("./__test-helpers__/tabs-server.js")).tabTransportMock(),
}));
vi.mock("./api-client.js", async (importOriginal) => {
  const tabs = (await import("./__test-helpers__/tabs-server.js")).tabListRead();
  return {
    ...(await importOriginal<typeof ApiClient>()),
    apiGetTyped: vi.fn((path: string) =>
      path.startsWith("/api/tabs") ? tabs(path) : Promise.resolve(null),
    ),
    apiGetOrError: vi.fn((path: string) => {
      const m = /^\/api\/runs\/([^/?]+)\?view=header$/.exec(path);
      const id = decodeURIComponent(m?.[1] ?? "");
      const run = h.headers.get(id);
      return Promise.resolve(
        run === undefined
          ? { ok: false, status: 404, data: null, error: "not found" }
          : {
              ok: true,
              status: 200,
              data: {
                workflowId: id,
                state: { workflowId: id, status: run.status },
                display_name: run.name,
                rev: 1,
              },
              error: "",
            },
      );
    }),
    apiGet: vi.fn(() => Promise.resolve(null)),
  };
});

import { applyLiveRunFrame, reviseRun } from "./run-store.js";
import { applyRunAsks } from "./run-asks.js";
import { setSectionFolded } from "./device-view.js";
import { openTab, closeTab, tabIdFor, _resetForTest } from "./tabs.js";
import { registerTabOpeners, _resetTabOpenersForTest } from "./tab-materialize.js";
import { ingestTabsChanged, listTabs, _resetTabsSyncForTest } from "./tabs-sync.js";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";
import { bindTabsSync, settleTabs, tabServer } from "./__test-helpers__/tabs-server.js";

bindTabsSync({ ingest: ingestTabsChanged, list: listTabs });

const shell = document.createElement("div");
{
  const doc = new DOMParser().parseFromString(indexHtml, "text/html");
  for (const s of doc.querySelectorAll("script")) {
    s.remove();
  }
  for (const n of [...doc.body.childNodes]) {
    shell.appendChild(document.importNode(n, true));
  }
  shell.querySelector("#tab-strip-skeleton")?.remove();
  document.body.appendChild(shell);
}
const { initRunsSection } = await import("./runs-section.js");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  initRunsSection();
});

afterAll(() => {
  shell.remove();
  style.remove();
});

/** Every run a case made live, so the next case starts from an empty inventory. */
const live = new Set<string>();
let version = 0;

/** One `live_run` frame, stamped past every frame before it as the server's `runs` subject is. */
function frame(id: string, row: { chat?: string; executing?: boolean } | null): void {
  version++;
  applyLiveRunFrame(
    {
      workflow_id: id,
      live: row !== null,
      chat_id: row?.chat ?? "",
      executing: row?.executing ?? false,
    },
    { kind: "runs", ref: "", version: String(version) },
  );
  if (row === null) {
    live.delete(id);
  } else {
    live.add(id);
  }
}

/** A run marotte holds a lease for, its header as the server answers it. */
function launched(
  id: string,
  name: string,
  over: { chat?: string; status?: string; executing?: boolean } = {},
): void {
  h.headers.set(id, { status: over.status ?? "running", name });
  frame(id, {
    chat: over.chat ?? "",
    executing: over.executing ?? (over.status ?? "running") === "running",
  });
}

beforeEach(() => {
  localStorage.clear();
  tabServer.reset();
  _resetTabsSyncForTest();
  _resetTabOpenersForTest();
  registerTabOpeners({
    chat: {
      prefetch: vi.fn(),
      select: vi.fn(),
      loadFailed: vi.fn(),
      show: vi.fn(),
      refresh: vi.fn(() => Promise.resolve()),
      close: vi.fn(),
      dot: () => "",
    },
    editor: { show: vi.fn(), refresh: vi.fn(), close: vi.fn(), move: vi.fn() },
    run: { show: vi.fn(), refresh: vi.fn() },
    subagent: { show: vi.fn(), refresh: vi.fn() },
    spec: { show: vi.fn(), refresh: vi.fn() },
  });
  resetActionFramework();
  _resetForTest();
});

afterEach(async () => {
  for (const id of [...live]) {
    frame(id, null);
  }
  h.headers.clear();
  tabServer.flushFrames();
  for (const s of tabServer.subjects()) {
    await closeTab(s.id);
  }
  await settle();
  for (const r of shell.querySelectorAll("[data-tab-id]")) {
    r.remove();
  }
  _resetTabsSyncForTest();
  localStorage.clear();
});

/** Let the header reads, the effects over them and a tab round trip run out. */
async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => {
      setTimeout(r, 0);
    });
  }
}

function el<T extends HTMLElement>(id: string): T {
  const e = shell.querySelector<T>(`[id="${id}"]`);
  if (e === null) {
    throw new Error(`no #${id}`);
  }
  return e;
}

const section = (): HTMLElement => el("runs-section");
const toggle = (): HTMLButtonElement => {
  const b = section().querySelector<HTMLButtonElement>(":scope > h2 > button.side-section-toggle");
  if (b === null) {
    throw new Error("no toggle in #runs-section");
  }
  return b;
};
const links = (): HTMLAnchorElement[] => [
  ...el("runs-list").querySelectorAll<HTMLAnchorElement>(":scope > li > a.runs-row"),
];
const names = (): string[] => links().map((a) => a.querySelector("bdi")?.textContent ?? "");
function link(name: string): HTMLAnchorElement {
  const a = links().find((r) => r.querySelector("bdi")?.textContent === name);
  if (a === undefined) {
    throw new Error(`no row named ${name}; rows: ${names().join(", ")}`);
  }
  return a;
}
const dotOf = (a: HTMLElement): HTMLElement => {
  const d = a.querySelector<HTMLElement>(".tab-status-dot");
  if (d === null) {
    throw new Error("no dot");
  }
  return d;
};
const shown = (e: HTMLElement): boolean => e.getBoundingClientRect().height > 0;

describe("which runs the section lists", () => {
  it("lists a running parentless run as a link to its run tab, with its name and state", async () => {
    launched("wf_listed", "animap-backlog-questions");
    await settle();

    expect(shown(section())).toBe(true);
    expect(names()).toEqual(["animap-backlog-questions"]);
    const row = link("animap-backlog-questions");
    expect(new URL(row.href).pathname).toBe("/run/wf_listed");
    expect(dotOf(row).dataset["status"]).toBe("working");
    // The accessible name is the run's name, then its state.
    expect(row.textContent).toBe("animap-backlog-questions, working");
    expect(toggle().querySelector(".side-section-count")?.textContent).toBe("1");
    expect(toggle().querySelector(".sr-only")?.textContent).toBe("1 run");
  });

  it("lists a paused run and one waiting for an answer, each with its own state", async () => {
    launched("wf_paused", "paused-run", { status: "paused" });
    launched("wf_asking", "asking-run", { status: "paused" });
    applyRunAsks({
      workflow_id: "wf_asking",
      chat_id: "run:wf_asking",
      epoch: "e1",
      seq: 1,
      reachable: true,
      asks: [
        {
          ask_id: "a1",
          node_id: "pick",
          step_session_id: "s1",
          agent_name: "picker",
          text: "Which one?",
          text_source: "asked",
          node_path: ["pick"],
        },
      ],
    });
    await settle();

    expect(dotOf(link("paused-run")).dataset["status"]).toBe("waiting");
    expect(dotOf(link("asking-run")).dataset["status"]).toBe("input");
    applyRunAsks({
      workflow_id: "wf_asking",
      chat_id: "run:wf_asking",
      epoch: "e1",
      seq: 2,
      reachable: true,
      asks: [],
    });
  });

  it("does not list a run a chat launched while that chat's tab is open, and lists it once it closes", async () => {
    await openTab({ kind: "chat", ref: "c1" });
    await settleTabs();
    launched("wf_agent", "from-a-chat", { chat: "c1" });
    await settle();
    expect(names()).toEqual([]);
    expect(section().hidden).toBe(true);

    tabServer.closeRemotely(tabIdFor("chat", "c1"));
    await settleTabs();
    await settle();
    expect(names()).toEqual(["from-a-chat"]);
  });

  it("lists a run whose launching chat has no tab open", async () => {
    launched("wf_orphan", "orphaned-run", { chat: "c-closed" });
    await settle();

    expect(names()).toEqual(["orphaned-run"]);
  });

  it("orders the rows by name, the same on every device", async () => {
    launched("wf_b", "beta");
    launched("wf_c", "gamma");
    launched("wf_a", "Alpha");
    await settle();

    expect(names()).toEqual(["Alpha", "beta", "gamma"]);
  });

  it("orders the rows the same whatever language the browser uses", async () => {
    // A Swedish browser sorts ä after z; the rows keep English order there.
    const sv = new Intl.Collator("sv");
    const compare = vi.spyOn(String.prototype, "localeCompare").mockImplementation(function (
      this: string,
      that: string,
    ) {
      return sv.compare(this, that);
    });
    try {
      launched("wf_z", "zebra");
      launched("wf_ae", "äpple");
      await settle();

      expect(names()).toEqual(["äpple", "zebra"]);
    } finally {
      compare.mockRestore();
    }
  });

  it("follows a run's state as it moves", async () => {
    launched("wf_moves", "moving-run");
    await settle();
    expect(dotOf(link("moving-run")).dataset["status"]).toBe("working");

    reviseRun("wf_moves", 2, "paused");
    await settle();
    expect(dotOf(link("moving-run")).dataset["status"]).toBe("waiting");
  });
});

describe("when a run leaves the section", () => {
  it("drops the row when marotte releases the run's lease, and hides the empty section", async () => {
    launched("wf_done", "finishing-run");
    launched("wf_stays", "staying-run");
    await settle();

    // A run that finished reports its outcome on its header first; the release frame is what removes it.
    reviseRun("wf_done", 2, "completed");
    await settle();
    expect(names()).toEqual(["finishing-run", "staying-run"]);

    frame("wf_done", null);
    await settle();
    expect(names()).toEqual(["staying-run"]);

    frame("wf_stays", null);
    await settle();
    expect(names()).toEqual([]);
    expect(section().hidden).toBe(true);
  });

  it("moves a run to Open while a tab shows it, and back when that tab closes", async () => {
    launched("wf_tabbed", "tabbed-run");
    await settle();
    expect(names()).toEqual(["tabbed-run"]);

    const subject = tabServer.openElsewhere({ kind: "run", ref: "wf_tabbed", owns: false });
    tabServer.flushFrames();
    await settleTabs();
    await settle();
    expect(names()).toEqual([]);

    tabServer.closeRemotely(subject.id);
    await settleTabs();
    await settle();
    expect(names()).toEqual(["tabbed-run"]);
  });
});

describe("opening a run from its row", () => {
  it("opens the run's tab on a click, and the row moves to Open", async () => {
    launched("wf_click", "clicked-run");
    await settle();

    link("clicked-run").click();
    await settleTabs();
    await settle();

    const opens = tabServer.sentOfType("open_tab");
    expect(opens.map((o) => o.payload["ref"])).toEqual(["wf_click"]);
    expect(tabIdFor("run", "wf_click")).not.toBe("");
    expect(names()).toEqual([]);
  });

  it("leaves a modified click to the browser, which opens the address elsewhere", async () => {
    launched("wf_modified", "modified-run");
    await settle();
    let prevented: boolean | undefined;
    const record = (e: Event): void => {
      prevented = e.defaultPrevented;
      // Stops the real navigation once the section's listener has had its say.
      e.preventDefault();
    };
    document.addEventListener("click", record);
    try {
      link("modified-run").dispatchEvent(
        new MouseEvent("click", { bubbles: true, cancelable: true, ctrlKey: true }),
      );
    } finally {
      document.removeEventListener("click", record);
    }
    await settle();

    expect(prevented).toBe(false);
    expect(tabServer.sentOfType("open_tab")).toEqual([]);
  });
});

describe("the section's fold", () => {
  it("folds to its header, which shows the most urgent hidden state", async () => {
    launched("wf_f1", "working-run");
    launched("wf_f2", "asking-run-2", { status: "paused" });
    applyRunAsks({
      workflow_id: "wf_f2",
      chat_id: "run:wf_f2",
      epoch: "e1",
      seq: 1,
      reachable: true,
      asks: [
        {
          ask_id: "a2",
          node_id: "pick",
          step_session_id: "s2",
          agent_name: "picker",
          text: "Which one?",
          text_source: "asked",
          node_path: ["pick"],
        },
      ],
    });
    setSectionFolded("runs", true);
    // Any repaint reads the stored fold; a run joining is one.
    launched("wf_f3", "third-run");
    await settle();

    expect(el("runs-list").hidden).toBe(true);
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    expect(toggle().querySelector<HTMLElement>(".side-section-dot")?.dataset["status"]).toBe(
      "input",
    );
    expect(toggle().querySelector(".sr-only")?.textContent).toBe("3 runs, one needs a decision");
    applyRunAsks({
      workflow_id: "wf_f2",
      chat_id: "run:wf_f2",
      epoch: "e1",
      seq: 2,
      reachable: true,
      asks: [],
    });
  });

  it("opens again from its header", async () => {
    setSectionFolded("runs", true);
    launched("wf_unfold", "unfolded-run");
    await settle();
    expect(el("runs-list").hidden).toBe(true);

    vi.stubGlobal(
      "matchMedia",
      (q: string): MediaQueryList =>
        ({
          matches: q.includes("prefers-reduced-motion"),
          media: q,
          onchange: null,
          addEventListener: () => undefined,
          removeEventListener: () => undefined,
          addListener: () => undefined,
          removeListener: () => undefined,
          dispatchEvent: () => false,
        }) as MediaQueryList,
    );
    try {
      toggle().click();
    } finally {
      vi.unstubAllGlobals();
    }

    expect(el("runs-list").hidden).toBe(false);
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    expect(shown(link("unfolded-run"))).toBe(true);
  });
});

describe("where a row draws its dot", () => {
  it("draws the dot left of the name, at the x a chat tab draws its own", async () => {
    await openTab({ kind: "chat", ref: "c-open" });
    await settleTabs();
    launched("wf_dot", "dotted-run");
    await settle();

    const chatRow = shell.querySelector<HTMLElement>(
      `[data-tab-id="${tabIdFor("chat", "c-open")}"]`,
    );
    if (chatRow === null) {
      throw new Error("no chat row");
    }
    // The tab's entry slides it in from the side; measured where it lands.
    await Promise.all(chatRow.getAnimations().map((a) => a.finished));
    const chatDot = dotOf(chatRow).getBoundingClientRect();
    const row = link("dotted-run");
    const dot = dotOf(row).getBoundingClientRect();
    const name = row.querySelector("bdi")?.getBoundingClientRect();
    const chatName = chatRow.querySelector(".tab-name")?.getBoundingClientRect();
    if (name === undefined || chatName === undefined) {
      throw new Error("no name");
    }

    expect(dot.right).toBeLessThanOrEqual(name.left);
    expect(Math.abs(dot.left + dot.width / 2 - (chatDot.left + chatDot.width / 2))).toBeLessThan(
      0.5,
    );
    expect(Math.abs(name.left - chatName.left)).toBeLessThan(0.5);
  });

  it("draws a run's square mark, not a chat's disc", async () => {
    launched("wf_square", "square-run");
    await settle();

    const dot = dotOf(link("square-run"));
    const size = dot.getBoundingClientRect().width;
    const corner = Number.parseFloat(getComputedStyle(dot).borderTopLeftRadius);
    expect(size).toBeGreaterThan(0);
    expect(corner).toBeLessThan(size / 2);
  });

  it("holds the name still while the run's first state has not landed", async () => {
    // No header to read yet and the inventory says the run is not executing: nothing to paint.
    frame("wf_blank", { executing: false });
    await settle();
    const row = links()[0];
    if (row === undefined) {
      throw new Error("no row");
    }
    const dot = dotOf(row);
    const name = row.querySelector<HTMLElement>("bdi");
    if (name === null) {
      throw new Error("no name");
    }
    expect(dot.hasAttribute("data-status")).toBe(false);
    const blank = name.getBoundingClientRect().left;

    frame("wf_blank", { executing: true });
    await settle();
    expect(dot.dataset["status"]).toBe("working");
    expect(Math.abs(name.getBoundingClientRect().left - blank)).toBeLessThan(0.5);
  });
});

describe("where focus goes", () => {
  /** The open tab row holding focus, by tab id, or "" when focus is not on one. */
  const focusedTab = (): string =>
    document.activeElement?.closest<HTMLElement>("[data-tab-id]")?.dataset["tabId"] ?? "";

  it("moves from a row that leaves to the next row, then the previous one, then the open tab", async () => {
    await openTab({ kind: "chat", ref: "c-here" });
    await settleTabs();
    launched("wf_r1", "r-one");
    launched("wf_r2", "r-two");
    launched("wf_r3", "r-zed");
    await settle();

    link("r-two").focus();
    frame("wf_r2", null);
    await settle();
    expect(document.activeElement).toBe(link("r-zed"));

    frame("wf_r3", null);
    await settle();
    expect(document.activeElement).toBe(link("r-one"));

    frame("wf_r1", null);
    await settle();
    expect(section().hidden).toBe(true);
    expect(focusedTab()).toBe(tabIdFor("chat", "c-here"));
  });

  it("keeps focus on a row a rename moves up the list", async () => {
    launched("wf_m1", "alpha");
    launched("wf_m2", "beta");
    await settle();
    link("beta").focus();

    h.headers.set("wf_m2", { status: "running", name: "aardvark" });
    reviseRun("wf_m2");
    await settle();

    expect(names()).toEqual(["aardvark", "alpha"]);
    expect(document.activeElement).toBe(link("aardvark"));
  });

  it("moves focus to the run's new tab row on Enter at desktop width", async () => {
    launched("wf_key", "keyed-run");
    await settle();
    link("keyed-run").focus();

    await userEvent.keyboard("{Enter}");
    await settleTabs();
    await settle();

    expect(tabIdFor("run", "wf_key")).not.toBe("");
    expect(focusedTab()).toBe(tabIdFor("run", "wf_key"));
  });

  it("leaves focus off the tab row at phone width, where the open closes the drawer", async () => {
    launched("wf_phone", "phone-run");
    await settle();
    link("phone-run").focus();
    vi.stubGlobal(
      "matchMedia",
      (q: string): MediaQueryList =>
        ({
          matches: q.includes("48rem"),
          media: q,
          onchange: null,
          addEventListener: () => undefined,
          removeEventListener: () => undefined,
          addListener: () => undefined,
          removeListener: () => undefined,
          dispatchEvent: () => false,
        }) as MediaQueryList,
    );
    try {
      await userEvent.keyboard("{Enter}");
      await settleTabs();
      await settle();
    } finally {
      vi.unstubAllGlobals();
    }

    expect(tabIdFor("run", "wf_phone")).not.toBe("");
    expect(focusedTab()).toBe("");
  });

  it("puts focus back on the row when the open is refused at the tab limit", async () => {
    launched("wf_full", "capped-run");
    await settle();
    link("capped-run").focus();
    tabServer.failNext("open_tab", 409, "too many tabs", "tabs_full");

    await userEvent.keyboard("{Enter}");
    await settleTabs();
    await settle();

    expect(tabIdFor("run", "wf_full")).toBe("");
    expect(names()).toEqual(["capped-run"]);
    expect(document.activeElement).toBe(link("capped-run"));
  });
});
