// The sidebar's Runs section over the shipped markup and stylesheet, fed through the real live-run
// inventory, run store and tab projection: which runs it lists, when a row leaves, what its dot says,
// how the rows are ordered, where the section and the dot sit and where focus goes.
import { describe, it, expect, vi, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { page, userEvent } from "vitest/browser";

import indexHtml from "../static/index.html?raw";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { maybeEl, unpark } from "./dom.js";
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

import { applyLiveRunFrame, registerRunStateDemand, reviseRun } from "./run-store.js";
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
const { installRunDotSubscriber } = await import("./run-dots.js");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  initRunsSection();
  installRunDotSubscriber();
});

afterAll(() => {
  shell.remove();
  style.remove();
});

/** Every run a case made live, so the next case starts from an empty inventory. */
const live = new Set<string>();
let version = 0;
/** When the last run went on the wire, unix ms: each launch is newer than the one before it. */
let clock = 1_000;

/** One `live_run` frame, stamped past every frame before it as the server's `runs` subject is. */
function frame(
  id: string,
  row: { chat?: string; executing?: boolean; started?: number } | null,
): void {
  version++;
  applyLiveRunFrame(
    {
      workflow_id: id,
      live: row !== null,
      chat_id: row?.chat ?? "",
      executing: row?.executing ?? false,
      started_at: row === null ? 0 : (row.started ?? ++clock),
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
  over: { chat?: string; status?: string; executing?: boolean; started?: number } = {},
): void {
  h.headers.set(id, { status: over.status ?? "running", name });
  frame(id, {
    chat: over.chat ?? "",
    executing: over.executing ?? (over.status ?? "running") === "running",
    ...(over.started === undefined ? {} : { started: over.started }),
  });
}

/** An open ask on the run, as the `run_asks` frame says it; `asking(id, false)` withdraws it. */
function asking(id: string, open = true): void {
  applyRunAsks({
    workflow_id: id,
    chat_id: `run:${id}`,
    epoch: "e1",
    seq: open ? 1 : 2,
    reachable: true,
    asks: open
      ? [
          {
            ask_id: `a-${id}`,
            node_id: "pick",
            step_session_id: `s-${id}`,
            agent_name: "picker",
            text: "Which one?",
            text_source: "asked",
            node_path: ["pick"],
          },
        ]
      : [],
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
    asking("wf_asking");
    await settle();

    expect(dotOf(link("paused-run")).dataset["status"]).toBe("waiting");
    expect(dotOf(link("asking-run")).dataset["status"]).toBe("input");
    asking("wf_asking", false);
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

  it("orders the rows by need, waiting for you first, then failed, then running, newest first in each", async () => {
    launched("wf_old_run", "old-running", { started: 1 });
    launched("wf_paused", "paused-run", { status: "paused", started: 2 });
    launched("wf_ask", "asking-run", { status: "paused", started: 3 });
    launched("wf_failed", "failed-run", { status: "failed", executing: false, started: 4 });
    launched("wf_new_run", "new-running", { started: 5 });
    launched("wf_tie", "tied-running", { started: 5 });
    asking("wf_ask");
    try {
      await settle();

      expect(names()).toEqual([
        "asking-run",
        "paused-run",
        "failed-run",
        "new-running",
        "tied-running",
        "old-running",
      ]);
    } finally {
      asking("wf_ask", false);
    }
  });

  it("moves a row up when its run starts waiting for you", async () => {
    launched("wf_up_a", "running-a", { started: 2 });
    launched("wf_up_b", "running-b", { started: 1 });
    await settle();
    expect(names()).toEqual(["running-a", "running-b"]);

    reviseRun("wf_up_b", 2, "paused");
    await settle();
    expect(names()).toEqual(["running-b", "running-a"]);
  });

  it("names its list Runs and speaks each row's state with the run's name", async () => {
    launched("wf_said", "spoken-run", { status: "paused" });
    await settle();

    const list = el("runs-list");
    const label = (list.getAttribute("aria-labelledby") ?? "")
      .split(" ")
      .map((id) => document.getElementById(id)?.textContent ?? "")
      .join(" ");
    expect(label).toBe("Runs");
    expect(link("spoken-run").textContent).toBe("spoken-run, waiting for you");
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

    // A run that finished reports its outcome on its header first, and sorts after the running one;
    // the release frame is what removes it.
    reviseRun("wf_done", 2, "completed");
    await settle();
    expect(names()).toEqual(["staying-run", "finishing-run"]);

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
    asking("wf_f2");
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
    asking("wf_f2", false);
  });

  it("speaks for a paused run over a running one, as the rows' order does", async () => {
    launched("wf_fw", "working-run-2", { started: 2 });
    launched("wf_fp", "paused-run-2", { status: "paused", started: 1 });
    setSectionFolded("runs", true);
    launched("wf_fw2", "working-run-3", { started: 3 });
    await settle();

    expect(toggle().querySelector<HTMLElement>(".side-section-dot")?.dataset["status"]).toBe(
      "waiting",
    );
    expect(toggle().querySelector(".sr-only")?.textContent).toBe("3 runs, one waiting for you");
  });

  it("lists an older decision under a newer pause and folds to the first row's state", async () => {
    launched("wf_old_ask", "older-asking", { status: "paused", started: 1 });
    launched("wf_new_pause", "newer-paused", { status: "paused", started: 2 });
    asking("wf_old_ask");
    try {
      await settle();
      expect(names()).toEqual(["newer-paused", "older-asking"]);
      const first = dotOf(link("newer-paused")).dataset["status"];
      expect(first).toBe("waiting");

      setSectionFolded("runs", true);
      launched("wf_fold_turn", "folding-run", { started: 0 });
      await settle();

      expect(toggle().querySelector<HTMLElement>(".side-section-dot")?.dataset["status"]).toBe(
        first,
      );
      expect(toggle().querySelector(".sr-only")?.textContent).toBe("3 runs, one waiting for you");
    } finally {
      asking("wf_old_ask", false);
    }
  });

  it("slides shut to the height it then rests at, below its floor on the way", async () => {
    launched("wf_slide", "sliding-run");
    await settle();
    const floor = Number.parseFloat(getComputedStyle(section()).minBlockSize);
    toggle().click();
    const anims = section().getAnimations();
    expect(anims, "the section slides shut").not.toHaveLength(0);
    const anim = anims[0];
    const effect = anim?.effect as KeyframeEffect | null | undefined;
    if (anim === undefined || effect == null) {
      throw new Error("no slide");
    }
    const frames = effect.getKeyframes();
    const px = (v: unknown): number => Number.parseFloat(String(v));
    const [from, to] = [px(frames.at(0)?.["blockSize"]), px(frames.at(-1)?.["blockSize"])];

    anim.pause();
    try {
      anim.currentTime = 0.9 * Number(effect.getComputedTiming().duration);
      const eased = effect.getComputedTiming().progress ?? Number.NaN;
      const mid = section().getBoundingClientRect().height;
      expect(mid, "the floor holds no part of the slide").toBeLessThan(floor);
      expect(Math.abs(mid - (from + (to - from) * eased))).toBeLessThanOrEqual(1);
    } finally {
      anim.play();
    }

    await Promise.all(anims.map((a) => a.finished));
    expect(el("runs-list").hidden).toBe(true);
    expect(to).toBe(section().getBoundingClientRect().height);
  });

  it("slides open from the height it rested at folded", async () => {
    launched("wf_slide_open", "opening-run");
    setSectionFolded("runs", true);
    launched("wf_slide_open2", "opening-run-2");
    await settle();
    const folded = section().getBoundingClientRect().height;

    toggle().click();
    const anim = section().getAnimations()[0];
    if (anim === undefined) {
      throw new Error("the section does not slide open");
    }
    anim.pause();
    try {
      anim.currentTime = 0;
      expect(Math.abs(section().getBoundingClientRect().height - folded)).toBeLessThanOrEqual(1);
    } finally {
      anim.play();
    }
    await anim.finished;
    expect(el("runs-list").hidden).toBe(false);
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
    launched("wf_r1", "r-one", { started: 3 });
    launched("wf_r2", "r-two", { started: 2 });
    launched("wf_r3", "r-zed", { started: 1 });
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

  it("keeps focus on a row a state change moves up the list", async () => {
    launched("wf_m1", "alpha", { started: 2 });
    launched("wf_m2", "beta", { started: 1 });
    await settle();
    link("beta").focus();

    reviseRun("wf_m2", 2, "paused");
    await settle();

    expect(names()).toEqual(["beta", "alpha"]);
    expect(document.activeElement).toBe(link("beta"));
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

  it("moves focus to the run's page at phone width, where the open closes the drawer", async () => {
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
    // The run tab's show reveals its page, as run-view.ts does; the opener here is a stub.
    const view = maybeEl("run-view");
    if (view === null) {
      throw new Error("no #run-view");
    }
    unpark(view, () => {
      view.classList.remove("hidden");
    });
    try {
      await userEvent.keyboard("{Enter}");
      await settleTabs();
      await settle();
      expect(tabIdFor("run", "wf_phone")).not.toBe("");
      expect(focusedTab()).toBe("");
      expect(document.activeElement).toBe(view.querySelector(":scope > .page-content"));
    } finally {
      vi.unstubAllGlobals();
      view.classList.add("hidden");
    }
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

describe("where the section sits", () => {
  it("stays in view under the tab sections however many tabs are open", async () => {
    for (let i = 0; i < 20; i++) {
      await openTab({ kind: "chat", ref: `c-many-${String(i)}` });
    }
    await settleTabs();
    launched("wf_docked", "docked-run");
    await settle();

    const sections = el("tab-sections");
    expect(sections.scrollHeight, "the tabs overflow their scroller").toBeGreaterThan(
      sections.clientHeight,
    );
    expect(sections.contains(section())).toBe(false);
    const row = link("docked-run").getBoundingClientRect();
    expect(row.top).toBeGreaterThanOrEqual(sections.getBoundingClientRect().bottom - 0.5);
    expect(row.bottom).toBeLessThanOrEqual(window.innerHeight);
  });

  it("shows three rows and scrolls the others inside its list, where focus reaches them", async () => {
    for (let i = 1; i <= 4; i++) {
      launched(`wf_cap${String(i)}`, `capped-${String(i)}`, { started: 10 - i });
    }
    await settle();

    const list = el("runs-list");
    const bottom = (): number => list.getBoundingClientRect().bottom;
    const inView = links().filter((a) => a.getBoundingClientRect().bottom <= bottom() + 0.5);
    expect(inView.map((a) => a.querySelector("bdi")?.textContent)).toEqual([
      "capped-1",
      "capped-2",
      "capped-3",
    ]);

    link("capped-4").focus();
    expect(link("capped-4").getBoundingClientRect().bottom).toBeLessThanOrEqual(bottom() + 0.5);
  });

  it("draws a live run in the tab rows' ink", async () => {
    launched("wf_ink", "inked-run");
    await settle();
    const probe = document.createElement("span");
    probe.style.color = "var(--c-text-primary)";
    el("runs-list").append(probe);
    try {
      expect(getComputedStyle(link("inked-run")).color).toBe(getComputedStyle(probe).color);
    } finally {
      probe.remove();
    }
  });

  it("draws a run's dot and name where its tab draws them once opened", async () => {
    // The run's page holds its state while its tab shows, as run-view.ts does.
    const release = registerRunStateDemand((id) => id === "wf_same", "header");
    try {
      launched("wf_same", "same-run");
      await settle();
      const row = link("same-run");
      const centre = (e: Element): number => {
        const r = e.getBoundingClientRect();
        return r.left + r.width / 2;
      };
      const rowDot = centre(dotOf(row));
      const rowName = row.querySelector("bdi")?.getBoundingClientRect().left ?? Number.NaN;

      row.click();
      await settleTabs();
      await settle();
      const tab = shell.querySelector<HTMLElement>(`[data-tab-id="${tabIdFor("run", "wf_same")}"]`);
      if (tab === null) {
        throw new Error("no run tab row");
      }
      await Promise.all(tab.getAnimations().map((a) => a.finished));

      expect(dotOf(tab).dataset["status"]).toBe("working");
      expect(Math.abs(centre(dotOf(tab)) - rowDot)).toBeLessThan(0.5);
      const tabName = tab.querySelector(".tab-name")?.getBoundingClientRect().left ?? Number.NaN;
      expect(Math.abs(tabName - rowName)).toBeLessThan(0.5);
    } finally {
      release();
    }
  });
});

describe("on a short screen", () => {
  // Last in the file: it resizes the frame and puts back the size it found.
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
  });

  // The :root pointer tier `crowd` found, put back after each case.
  let pointerFound: { readonly value: string | undefined } | null = null;

  afterEach(() => {
    el("sidebar").classList.remove("open");
    if (pointerFound !== null) {
      const { value } = pointerFound;
      if (value === undefined) {
        delete document.documentElement.dataset["pointer"];
      } else {
        document.documentElement.dataset["pointer"] = value;
      }
      pointerFound = null;
    }
  });

  afterAll(async () => {
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  /** Whether `e` lies wholly inside `box`'s rect, and above the sidebar's footer. */
  const within = (e: Element, box: Element): boolean => {
    const r = e.getBoundingClientRect();
    const b = box.getBoundingClientRect();
    const footer = shell.querySelector(".sidebar-footer")?.getBoundingClientRect().top ?? 0;
    return (
      r.height > 0 &&
      r.top >= b.top - 0.5 &&
      r.bottom <= b.bottom + 0.5 &&
      r.bottom <= footer + 0.5 &&
      r.bottom <= window.innerHeight + 0.5
    );
  };

  /** Size the frame, set the pointer tier, open the drawer, 20 tabs and three live runs. */
  async function crowd(
    width: number,
    height: number,
    pointer: "fine" | "coarse",
    tag: string,
  ): Promise<HTMLElement> {
    await page.viewport(width, height);
    expect([window.innerWidth, window.innerHeight], "viewport actually resized").toEqual([
      width,
      height,
    ]);
    pointerFound ??= { value: document.documentElement.dataset["pointer"] };
    document.documentElement.dataset["pointer"] = pointer;
    el("sidebar").classList.add("open");
    for (let i = 0; i < 20; i++) {
      await openTab({ kind: "chat", ref: `c-${tag}-${String(i)}` });
    }
    await settleTabs();
    for (let i = 1; i <= 3; i++) {
      launched(`wf_${tag}${String(i)}`, `${tag}-${String(i)}`, { started: 10 - i });
    }
    await settle();
    await Promise.all(
      el("sidebar")
        .getAnimations()
        .map((a) => a.finished),
    );
    const middle = shell.querySelector<HTMLElement>(".sidebar-middle");
    if (middle === null) {
      throw new Error("no .sidebar-middle");
    }
    return middle;
  }

  const firstRun = (): HTMLAnchorElement => {
    const a = links()[0];
    if (a === undefined) {
      throw new Error("no Runs row");
    }
    return a;
  };

  for (const [width, height, posture] of [
    [844, 390, "a phone held sideways"],
    [390, 844, "a phone's drawer"],
  ] as const) {
    for (const pointer of ["fine", "coarse"] as const) {
      it(`keeps Runs' header and first row, and Open's header and a whole tab, in view on ${posture} (${pointer})`, async () => {
        const middle = await crowd(width, height, pointer, `short-${String(width)}-${pointer}`);

        expect(middle.scrollHeight, "the middle itself never scrolls").toBe(middle.clientHeight);
        expect(within(toggle(), section()), "Runs' header").toBe(true);
        const list = el("runs-list");
        expect(within(firstRun(), list), "Runs' first row").toBe(true);

        const sections = el("tab-sections");
        // The open scrolled the newest tab into view; the floor is measured from the scroller's top.
        sections.scrollTop = 0;
        const header = shell.querySelector("#open-section > h2");
        expect(header !== null && within(header, sections), "Open's header").toBe(true);
        const tabRows = [...el("tab-list").querySelectorAll<HTMLElement>(":scope > [data-tab-id]")];
        expect(tabRows.filter((r) => within(r, sections)).length).toBeGreaterThanOrEqual(1);

        for (const r of tabRows) {
          r.focus();
          expect(within(r, sections), `tab ${r.dataset["tabId"] ?? ""} scrolled into view`).toBe(
            true,
          );
        }
        for (const a of links()) {
          a.focus();
          expect(within(a, list), `${a.textContent ?? ""} scrolled into view on focus`).toBe(true);
        }
      });
    }
  }

  // Below both floors (a small phone held sideways), the tab sections give theirs up before Runs'.
  it("keeps Runs' header and a row in view where the tab sections' floor no longer fits", async () => {
    const middle = await crowd(568, 320, "coarse", "shortest");

    expect(middle.scrollHeight, "the middle itself never scrolls").toBe(middle.clientHeight);
    expect(within(toggle(), section()), "Runs' header").toBe(true);
    const list = el("runs-list");
    expect(within(firstRun(), list), "Runs' first row").toBe(true);
  });
});
