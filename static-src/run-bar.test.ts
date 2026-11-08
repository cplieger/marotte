// The composer band's run bar: one line per live workflow run the ACTIVE chat launched.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { page } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";

// The graph reaches the shared DOM registry, which throws on a missing element. Every id has to
// exist before the imports below are evaluated.
// The fold row and the list sit in their host as `index.html` has them, so the shipped stylesheet lays them out.
const runHost = document.createElement("div");
runHost.className = "run-bar-host";
runHost.append(
  Object.assign(document.createElement("div"), { id: "run-bar-fold", className: "run-bar-fold" }),
  Object.assign(document.createElement("ul"), { id: "run-bar", className: "run-bar hidden" }),
);
document.body.appendChild(runHost);
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const el = document.createElement("div");
  el.id = id;
  document.body.appendChild(el);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical mock is what every
// suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// Replaced rather than spied so the real run page is never linked into this file's graph.
const openRunView = vi.hoisted(() => vi.fn());
vi.mock("./run-view.js", () => ({
  openRunView,
}));

const announce = vi.hoisted(() => vi.fn());
vi.mock("@cplieger/ui/announce", () => ({ announce }));

/** Per-run inspect answers, consulted by the stubbed run read. A run absent from this map
 *  answers a failed read, which is the honest "nothing fetched yet" case. */
const inspect = new Map<string, unknown>();
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  // The OrError variant: the store spends a failed read's STATUS, so a run this map does not name
  // answers 0 — no request — rather than the settled 404 that would skip its retry ladder.
  apiGetOrError: vi.fn((path: string) => {
    const hit = /^\/api\/runs\/([^/?]+)$/.exec(path);
    if (hit !== null) {
      const id = decodeURIComponent(hit[1] ?? "");
      const fixture = inspect.get(id) as Record<string, unknown> | undefined;
      if (fixture === undefined) {
        return Promise.resolve({ ok: false, status: 0, data: null, error: "" });
      }
      // CLONED per call, because the real read parses fresh JSON: handing the same object back
      // twice makes the run cell's signal dedupe by identity, and the refetch cases below would
      // then pass with no re-render to protect. `display_name` is the server's key beside KAS's state.
      const { display_name: displayName, controls, ...state } = structuredClone(fixture);
      return Promise.resolve({
        ok: true,
        status: 200,
        data: { workflowId: id, display_name: displayName, controls, state },
        error: "",
      });
    }
    return Promise.resolve({ ok: false, status: 0, data: null, error: "" });
  }),
}));

// Called through, so the one toast a batch raises is countable.
vi.mock("./toast.js", { spy: true });

const { initRunBar, _resetRunBarForTest } = await import("./run-bar.js");
const { initActions } = await import("./actions/boot.js");
const { configureApi } = await import("@cplieger/ui/actions");
const toast = await import("./toast.js");
const { noteRunLive, noteRunSettled, invalidateRun } = await import("./run-store.js");
const { setSessions, setActive } = await import("./store.js");
const { dropDecisions } = await import("./decision-dock.js");
const { applyRunAsks, _resetRunAsksForTest } = await import("./run-asks.js");
const { apiGetOrError } = await import("./api-client.js");

const bar = document.getElementById("run-bar") as HTMLUListElement;
const fold = document.getElementById("run-bar-fold")!;

let seq = 0;
/** A fresh chat id per case, so no case inherits another's live rows. */
function chatID(): string {
  seq++;
  return `c-bar-${String(seq)}`;
}

let runSeq = 0;
/** A fresh run id per case. `run-store.ts`'s cells are module state with one bound (`forgetRun`,
 *  which only the transcript card may call), so reusing an id would let a case read the state a
 *  previous case fetched for it — and a stale SETTLED cell makes the row invisible, which is
 *  exactly what the filter is for. */
function runID(tag: string): string {
  runSeq++;
  return `wf-${tag}-${String(runSeq)}`;
}

/** A run state as `GET /api/runs/{id}` answers it, with the server's `display_name` beside it. One
 *  running leaf, so the counters and the elapsed clock have something to read. */
function state(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    workflowId: "wf",
    status: "running",
    display_name: "nightly sweep",
    root: {
      nodeId: "n1",
      type: "step",
      status: "running",
      startedAt: new Date(Date.now() - 5000).toISOString(),
    },
    ...over,
  };
}

async function activate(chat: string): Promise<void> {
  setSessions([{ id: chat, name: chat, messages: [] } as never]);
  setActive(chat);
  // One macrotask per fetch generation: the first render triggers `invalidateRun`, whose resolution
  // writes the cell the second render reads.
  await new Promise((r) => setTimeout(r, 0));
  await new Promise((r) => setTimeout(r, 0));
}

function rowStates(): string[] {
  return [...bar.querySelectorAll<HTMLElement>("li.run-bar-row")].map(
    (li) => li.dataset["state"] ?? "",
  );
}

function rowText(sel: string): string[] {
  return [...bar.querySelectorAll(sel)].map((n) => n.textContent ?? "");
}

/** A CSS length resolved to pixels inside the run bar's box. */
function lengthPx(expr: string): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = expr;
  runHost.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

/** A control's press target: its padding box grown by its `::after` expander's used insets. */
function hitBox(el: HTMLElement): DOMRect {
  const cs = getComputedStyle(el, "::after");
  const out = (v: string): number => Math.max(0, -(parseFloat(v) || 0));
  const b = el.getBoundingClientRect();
  const left = b.left + el.clientLeft - out(cs.left);
  const top = b.top + el.clientTop - out(cs.top);
  return new DOMRect(
    left,
    top,
    el.clientWidth + out(cs.left) + out(cs.right),
    el.clientHeight + out(cs.top) + out(cs.bottom),
  );
}

/** Run the list's own fold slide to its end, then let the settle it resolves run. */
async function finishSlides(): Promise<void> {
  await Promise.all(
    bar.getAnimations().map((a) => {
      a.finish();
      return a.finished;
    }),
  );
  await Promise.resolve();
}

/** A parked step's question as the run-asks store holds it, keyed to the launching chat. */
function pushRunAsk(chat: string, runID: string): void {
  applyRunAsks({
    workflow_id: runID,
    chat_id: chat,
    epoch: "e1",
    seq: 1,
    reachable: true,
    asks: [
      {
        ask_id: `${runID}:ask-1`,
        node_id: "n1",
        node_path: ["n1"],
        step_session_id: "",
        agent_name: "",
        text: "which branch?",
        text_source: "asked",
      },
    ],
  });
}

const chats: string[] = [];
const runs: string[] = [];

beforeEach(() => {
  _resetRunBarForTest();
  inspect.clear();
  openRunView.mockClear();
  announce.mockClear();
  vi.mocked(apiGetOrError).mockClear();
  initRunBar();
});

afterEach(() => {
  for (const id of runs.splice(0)) {
    noteRunSettled(id);
  }
  for (const c of chats.splice(0)) {
    dropDecisions(c);
  }
  _resetRunAsksForTest();
  setSessions([]);
  setActive("");
  _resetRunBarForTest();
});

/** Record a live run so the afterEach can settle it, whatever the case did. */
function live(runID: string, chat: string, executing = true): void {
  runs.push(runID);
  noteRunLive(runID, chat, executing);
}

describe("the run bar", () => {
  it("stays hidden and empty when the active chat has no live run", async () => {
    const c = chatID();
    chats.push(c);
    await activate(c);

    expect(bar.classList.contains("hidden")).toBe(true);
    expect(bar.children.length).toBe(0);
  });

  // THE SCOPE DECISION. Another chat's run is surfaced by its own tab's activity dot, and a
  // parentless run has no launching chat at all, so neither belongs on this chat's line.
  it("shows one row per live run of the ACTIVE chat and none of anyone else's", async () => {
    const mine = chatID();
    const theirs = chatID();
    chats.push(mine, theirs);
    const [a, b, orphan] = [runID("mine"), runID("theirs"), runID("orphan")];
    inspect.set(a, state({ display_name: "mine" }));
    inspect.set(b, state({ display_name: "theirs" }));
    inspect.set(orphan, state({ display_name: "orphan" }));
    live(a, mine);
    live(b, theirs);
    live(orphan, "");
    await activate(mine);

    expect(bar.classList.contains("hidden")).toBe(false);
    expect(rowText(".ev-sum-label")).toEqual(["mine"]);
  });

  it("reads running, waiting and an unanswered ask as three states", async () => {
    const c = chatID();
    chats.push(c);
    const [running, parked, asking] = [runID("run"), runID("park"), runID("ask")];
    inspect.set(running, state({ display_name: "a" }));
    inspect.set(parked, state({ display_name: "b", status: "paused" }));
    inspect.set(asking, state({ display_name: "c", status: "paused" }));
    live(running, c);
    live(parked, c);
    live(asking, c);
    // Driven through the dock, not by faking the reader: `runPendingAsks` is what the bar consults
    // and what the transcript card and the tab dot consult too.
    pushRunAsk(c, asking);
    await activate(c);

    // The two that need the reader first, in inventory order.
    expect(rowStates()).toEqual(["waiting", "input", "running"]);
    expect(rowText(".run-bar-row .ev-sum-state")).toEqual(["waiting", "needs input", "running"]);
  });

  // The chat's row reads the run dock's own summary, so a run stuck on a person says why here too,
  // and returns to its summary once it clears.
  // A fan-out folds behind one row, so the transcript and the composer stay on a phone.
  it("folds three or more runs behind one summary row that opens in place", async () => {
    localStorage.removeItem("marotte.ui-state");
    const c = chatID();
    chats.push(c);
    const ids = ["f1", "f2", "f3", "f4"].map((n) => runID(n));
    for (const id of ids) {
      inspect.set(id, state({ display_name: id }));
      live(id, c);
    }
    inspect.set(ids[2]!, state({ display_name: ids[2], status: "paused" }));
    await activate(c);
    const toggle = fold.querySelector<HTMLElement>(".run-bar-toggle")!;
    expect(fold.hidden).toBe(false);
    expect(toggle.textContent).toBe("4 workflows, 1 needs you");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.querySelector<HTMLElement>(".run-bar-glyph")?.dataset["status"]).toBe("input");
    const rows = (): HTMLElement[] => [...bar.querySelectorAll<HTMLElement>("li.run-bar-row")];
    expect(bar.hasAttribute("data-folded")).toBe(true);
    expect(rows()[0]?.querySelector(".ev-sum-label")?.textContent).toBe(ids[2]);

    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(bar.hasAttribute("data-folded")).toBe(false);
    expect(JSON.parse(localStorage.getItem("marotte.ui-state") ?? "{}")).toMatchObject({
      run_bar_open: true,
    });
    toggle.click();
    await finishSlides();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(bar.hasAttribute("data-folded")).toBe(true);

    const range = document.createRange();
    range.selectNodeContents(toggle.querySelector(".run-bar-count")!);
    document.getSelection()?.addRange(range);
    toggle.click();
    expect(
      toggle.getAttribute("aria-expanded"),
      "a click ending a selection leaves the list shut",
    ).toBe("false");
    expect(bar.hasAttribute("data-folded")).toBe(true);
    document.getSelection()?.removeAllRanges();
    localStorage.removeItem("marotte.ui-state");
  });

  it("lists one run without a summary row", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("p1");
    inspect.set(id, state({ display_name: id }));
    live(id, c);
    await activate(c);
    expect(fold.hidden).toBe(true);
    expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(1);
    expect(bar.hasAttribute("data-folded")).toBe(false);
  });

  it("shows the alert in place of the summary, and the summary again once it clears", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("alert");
    inspect.set(
      id,
      state({ display_name: "nightly", status: "paused", pauseReason: "host unreachable" }),
    );
    live(id, c);
    await activate(c);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
    expect(btn.dataset["alert"]).toBe("paused");
    expect(btn.querySelector(".ev-alert-text")?.textContent).toBe("Waiting: host unreachable");
    expect(btn.querySelector("[role]")).toBeNull();
    expect(btn.getAttribute("aria-label")).toBe(
      "Open workflow run nightly: Waiting: host unreachable",
    );

    inspect.set(id, state({ display_name: "nightly" }));
    invalidateRun(id);
    await vi.waitFor(() => {
      expect(btn.dataset["alert"]).toBeUndefined();
    });
    expect(btn.querySelector(".ev-alert-line")).toBeNull();
    expect(btn.getAttribute("aria-label")).toBe("Open workflow run nightly, running");
  });

  // The answer may come from the launching agent rather than the reader, so the alert names no one.
  it("says a step's question is waiting for answer, and opens the run to answer it", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("question");
    inspect.set(id, state({ display_name: "release", status: "paused" }));
    live(id, c);
    pushRunAsk(c, id);
    await activate(c);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
    expect(btn.dataset["alert"]).toBe("input");
    expect(btn.querySelector(".ev-alert-text")?.textContent).toBe(
      "Waiting for answer: which branch?",
    );

    btn.click();
    expect(openRunView).toHaveBeenCalledWith(id, "release", c);
  });

  // The row's name and status are quotable, so a drag selecting them ends in a click that must not open the run.
  it("opens no run for a click that ends a selection in its row", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("selected");
    inspect.set(id, state({ display_name: "release", status: "running" }));
    live(id, c);
    await activate(c);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
    const range = document.createRange();
    range.selectNodeContents(btn.querySelector(".ev-sum-label")!);
    document.getSelection()?.removeAllRanges();
    document.getSelection()?.addRange(range);
    btn.click();
    expect(openRunView).not.toHaveBeenCalled();
    document.getSelection()?.removeAllRanges();
    btn.click();
    expect(openRunView).toHaveBeenCalledWith(id, "release", c);
  });

  // A `role="button"` row, so the run's name stays selectable on WebKit; the keys a `<button>` takes still work.
  it("opens the run from its row with Enter or Space, as a focusable button", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("keys");
    inspect.set(id, state({ display_name: "release", status: "running" }));
    live(id, c);
    await activate(c);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open")!;
    expect(btn.tagName).not.toBe("BUTTON");
    expect(btn.getAttribute("role")).toBe("button");
    expect(btn.tabIndex).toBe(0);
    for (const key of ["Enter", " "]) {
      openRunView.mockClear();
      const press = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
      btn.dispatchEvent(press);
      expect(openRunView).toHaveBeenCalledExactlyOnceWith(id, "release", c);
      expect(press.defaultPrevented, `${key} does not also scroll or type`).toBe(true);
    }
    openRunView.mockClear();
    btn.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
    expect(openRunView).not.toHaveBeenCalled();
  });

  it("toggles the fold row with Enter or Space, keeping aria-expanded in step", async () => {
    localStorage.removeItem("marotte.ui-state");
    const c = chatID();
    chats.push(c);
    for (const n of ["k1", "k2", "k3"]) {
      const id = runID(n);
      inspect.set(id, state({ display_name: id }));
      live(id, c);
    }
    await activate(c);
    const toggle = fold.querySelector<HTMLElement>(".run-bar-toggle")!;
    expect(toggle.tagName).not.toBe("BUTTON");
    expect(toggle.getAttribute("role")).toBe("button");
    expect(toggle.tabIndex).toBe(0);
    toggle.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    toggle.dispatchEvent(
      new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }),
    );
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    localStorage.removeItem("marotte.ui-state");
  });

  it("names a run by the server's display name, else a fallback, never by its recipe", async () => {
    const c = chatID();
    chats.push(c);
    const [a, b] = [runID("a"), runID("b")];
    inspect.set(a, state({ display_name: "the label", workflowName: "workflow-from-prompt" }));
    inspect.set(b, state({ display_name: "", runLabel: "", workflowName: "workflow-from-prompt" }));
    live(a, c);
    live(b, c);
    await activate(c);

    expect(rowText(".ev-sum-label")).toEqual(["the label", "Workflow run"]);
  });

  it("says nothing about a run it has not fetched yet", async () => {
    const c = chatID();
    chats.push(c);
    // No inspect entry, so the cell stays undefined.
    live(runID("unknown"), c);
    await activate(c);

    // The row exists — the inventory says the run is live — and it claims no state rather than
    // reporting "not started" for a run that has demonstrably started.
    expect(rowStates()).toEqual(["unknown"]);
    expect(rowText(".ev-sum-state")).toEqual([""]);
    // No `data-status` at all, which is how the shared mark rule says "reserved box, nothing to
    // show" — the same answer a tab row gets for a run it has not fetched.
    expect(bar.querySelector(".run-bar-glyph")?.hasAttribute("data-status")).toBe(false);
  });

  // The bar's glyph IS the workflow mark, so what it carries is the mark's own status vocabulary
  // (`TabRunDotStatus`) rather than this row's `data-state`. The two differ for the state that
  // matters most: `running` on the row, `working` on the mark, which is the tab strip's word.
  it("marks its glyph with the tab strip's status for every live state", async () => {
    const c = chatID();
    chats.push(c);
    const [running, parked, asking] = [runID("mrun"), runID("mpark"), runID("mask")];
    inspect.set(running, state({ display_name: "a" }));
    inspect.set(parked, state({ display_name: "b", status: "paused" }));
    inspect.set(asking, state({ display_name: "c", status: "paused" }));
    live(running, c);
    live(parked, c);
    live(asking, c);
    pushRunAsk(c, asking);
    await activate(c);

    expect(rowStates()).toEqual(["waiting", "input", "running"]);
    expect(
      [...bar.querySelectorAll(".run-bar-row .run-bar-glyph")].map((g) =>
        g.getAttribute("data-status"),
      ),
    ).toEqual(["waiting", "input", "working"]);
  });

  it("reports the step counter and the elapsed clock", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("counters");
    inspect.set(id, state());
    live(id, c);
    await activate(c);

    // Withheld for one step, as the run dock does: "step 1 of 1" cannot progress.
    expect(rowText(".ev-sum-progress .sr-only")).toEqual([""]);
    // `formatElapsed`'s sub-minute spelling, a tenth of a second.
    expect(rowText(".ev-sum-clock")[0]).toMatch(/^\d+\.\ds$/);
  });

  it("opens the run's own tab on click, and nothing else", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("click");
    inspect.set(id, state({ display_name: "nightly" }));
    live(id, c);
    await activate(c);

    bar.querySelector<HTMLElement>(".run-bar-open")?.click();

    expect(openRunView).toHaveBeenCalledTimes(1);
    expect(openRunView).toHaveBeenCalledWith(id, "nightly", c);
  });

  // The visible span is ellipsized by CSS, responsively. A length cut in TS would travel into two
  // places a cut does not belong: `openRunView` makes this string the run TAB's name, and it is the
  // button's accessible name.
  it("passes the run's whole name to the opener, the accessible name and the title's tooltip", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("long");
    const label = "nightly dependency sweep across every repository in the organisation";
    inspect.set(id, state({ display_name: label }));
    live(id, c);
    await activate(c);

    const btn = bar.querySelector<HTMLElement>(".run-bar-open");
    expect(rowText(".ev-sum-label")).toEqual([label]);
    expect(bar.querySelector(".ev-sum-label")?.getAttribute("data-tooltip")).toBe(label);
    expect(btn?.getAttribute("aria-label")).toContain(label);

    btn?.click();
    expect(openRunView).toHaveBeenCalledWith(id, label, c);
  });

  it("drops a row when its run settles, and re-hides when it was the last", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("settle");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    expect(bar.children.length).toBe(1);

    noteRunSettled(id);

    expect(bar.children.length).toBe(0);
    expect(bar.classList.contains("hidden")).toBe(true);
  });

  // A settled state still sitting in the inventory means a `run_finished` this client missed. A
  // completed row in a live-runs bar is the one wrong thing the bar can say, so the store's own
  // answer wins over the inventory.
  it("drops a row the store can prove has finished", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("done");
    inspect.set(id, state({ status: "completed" }));
    live(id, c);
    await activate(c);

    expect(bar.children.length).toBe(0);
    expect(bar.classList.contains("hidden")).toBe(true);
  });

  // What the `untracked` render protects: a refetch that does not move the key must not rebuild the
  // row, or every `run_progress` re-fires the entry animation.
  it("keeps the same row node across a refetch that changes nothing", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("stable");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    const row = bar.firstElementChild;
    expect(row).not.toBeNull();

    const { invalidateRun } = await import("./run-store.js");
    invalidateRun(id);
    await new Promise((r) => setTimeout(r, 0));

    expect(bar.firstElementChild).toBe(row);
  });

  // A rendered run whose store cell is BLANKED under it.
  it("refetches a rendered run whose store cell was forgotten", async () => {
    const c = chatID();
    chats.push(c);
    const parked = runID("parked");
    inspect.set(parked, state({ display_name: "nightly sweep", status: "paused" }));
    live(parked, c);
    await activate(c);
    expect(rowText(".ev-sum-label")).toEqual(["nightly sweep"]);

    const { forgetRun } = await import("./run-store.js");
    forgetRun(parked);

    // `forgetRun` DELETES the cell's signal rather than writing it, so no subscriber wakes on the
    // forget itself: the blank is read by the next render, which any other live run of this chat
    // arriving is enough to cause.
    const second = runID("second");
    inspect.set(second, state({ display_name: "the other one" }));
    live(second, c);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));

    expect(rowText(".ev-sum-label")).toEqual(["nightly sweep", "the other one"]);
    expect(rowStates()).toEqual(["waiting", "running"]);
  });

  it("advances the clock on the shared tick without refetching", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("tick");
    inspect.set(id, state());
    live(id, c);
    // PRIMED before the bar ever sees the run, so the row is built on the FIRST render rather than
    // on a second one after the bar's own fetch lands.
    const { invalidateRun: prime } = await import("./run-store.js");
    prime(id);
    await new Promise((r) => setTimeout(r, 0));

    // Fake timers installed BEFORE the render that creates the hold, or the interval the bar joins
    // is a real one no `advanceTimersByTime` can reach. `shouldAdvanceTime` keeps the awaits below
    // resolving.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      await activate(c);
      const before = rowText(".ev-sum-clock")[0];
      expect(before).not.toBe("");
      const fetches = vi.mocked(apiGetOrError).mock.calls.length;

      // The 1s interval is `messages-blocks.ts`'s, shared with the transcript's cards; the bar
      // joins it as a holder rather than starting one of its own.
      vi.advanceTimersByTime(2000);

      expect(rowText(".ev-sum-clock")[0]).not.toBe(before);
      expect(vi.mocked(apiGetOrError).mock.calls.length).toBe(fetches);
    } finally {
      vi.useRealTimers();
    }
  });

  it("announces a count change on the same chat and stays quiet on a switch", async () => {
    const first = chatID();
    const second = chatID();
    chats.push(first, second);
    const [one, two, three] = [runID("ann1"), runID("ann2"), runID("ann3")];
    inspect.set(one, state());
    inspect.set(two, state());
    inspect.set(three, state());
    live(one, first);
    live(two, second);

    // ARRIVING at a chat that already has a run is a switch, not news — the same rule
    // `pending-steers.ts` applies to its own count.
    await activate(first);
    expect(announce).not.toHaveBeenCalled();

    // Same chat, count moves: news.
    live(three, first);
    expect(announce).toHaveBeenCalledTimes(1);
    expect(announce).toHaveBeenCalledWith("2 workflow runs in progress");
    announce.mockClear();

    // A chat switch is not, even though the count moves with it.
    await activate(second);
    expect(announce).not.toHaveBeenCalled();
  });
});

describe("the run bar's credit figure", () => {
  function billed(name: string, usage: number): Record<string, unknown> {
    const st = state({ display_name: name });
    const root = st["root"] as Record<string, unknown>;
    return { ...st, root: { ...root, credits: [{ usage, unit: "credit" }] } };
  }

  async function mountBilled(usages: readonly number[]): Promise<HTMLElement[]> {
    const c = chatID();
    chats.push(c);
    for (const [i, usage] of usages.entries()) {
      const id = runID(`cr${String(i)}`);
      inspect.set(id, billed(`run-${String(i)}`, usage));
      live(id, c);
    }
    await activate(c);
    return [...bar.querySelectorAll<HTMLElement>(".run-bar-open")];
  }

  const figure = (btn: HTMLElement | undefined): string | null =>
    btn?.querySelector(".ev-sum-credits")?.textContent ?? null;

  it("shows the one run's spend to the whole credit, its name keeping the cents", async () => {
    const [btn] = await mountBilled([230.28]);
    expect(figure(btn)).toBe("230 cr");
    expect(btn?.getAttribute("aria-label")).toContain("230.28 credits");
  });

  it("rounds each row of a fan-out to the nearest whole credit", async () => {
    const rows = await mountBilled([3210.64, 230.28, 0.31]);
    expect(rows.map(figure).sort()).toEqual(["230 cr", "3211 cr", "<1 cr"]);
    expect(rows.map((b) => b.getAttribute("aria-label") ?? "").join("|")).toContain(
      "3210.64 credits",
    );
  });

  it("shows a spend under half a credit as under one, never as free", async () => {
    const [btn] = await mountBilled([0.31]);
    expect(figure(btn)).toBe("<1 cr");
    expect(btn?.getAttribute("aria-label")).toContain("0.31 credits");
  });
});

// Object IDENTITY is the assertion in every case here: a rebuilt row is indistinguishable from a
// patched one by content.
describe("a render patches its rows rather than rebuilding them", () => {
  /** Change what the run read answers, then make the bar re-read it the way a `run_progress`
   *  frame does. */
  async function advance(id: string, over: Record<string, unknown>): Promise<void> {
    inspect.set(id, state(over));
    invalidateRun(id);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
  }

  function parts(): { row: Element; glyph: Element; clock: Element } {
    const row = bar.querySelector("li");
    const glyph = bar.querySelector(".run-bar-glyph");
    const clock = bar.querySelector(".ev-sum-clock");
    if (row === null || glyph === null || clock === null) {
      throw new Error("the bar has no row");
    }
    return { row, glyph, clock };
  }

  it("keeps the row, its mark and its clock across a step advance", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("patch");
    inspect.set(
      id,
      state({
        root: {
          nodeId: "r",
          type: "sequence",
          status: "running",
          startedAt: new Date(Date.now() - 5000).toISOString(),
          children: [
            { nodeId: "a", type: "step", status: "completed" },
            { nodeId: "b", type: "step", status: "running" },
          ],
        },
      }),
    );
    live(id, c);
    await activate(c);
    expect(rowText(".ev-sum-progress .sr-only")).toEqual(["step 2 of 2"]);
    const before = parts();

    await advance(id, {
      root: {
        nodeId: "r",
        type: "sequence",
        status: "running",
        startedAt: new Date(Date.now() - 5000).toISOString(),
        children: [
          { nodeId: "a", type: "step", status: "completed" },
          { nodeId: "b", type: "step", status: "completed" },
          { nodeId: "c", type: "step", status: "running" },
        ],
      },
    });

    // The text moved, so the render definitely ran.
    expect(rowText(".ev-sum-progress .sr-only")).toEqual(["step 3 of 3"]);
    const after = parts();
    expect(after.row).toBe(before.row);
    // The mark is what carries the beat, and the clock is the element the shared 1s tick holds a
    // pointer to, so neither may be replaced.
    expect(after.glyph).toBe(before.glyph);
    expect(after.clock).toBe(before.clock);
  });

  it("repaints a row in place when only the loop pass moves", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("pass");
    const pass = (n: number, code: string, review: string) => ({
      nodeId: `loop#${String(n)}`,
      type: "sequence",
      status: code === "completed" && review === "completed" ? "completed" : "running",
      iteration: n,
      children: [
        { nodeId: "code", type: "step", status: code, iteration: n },
        { nodeId: "review", type: "step", status: review, iteration: n },
      ],
    });
    const tree = (passes: unknown[]) => ({
      root: {
        nodeId: "r",
        type: "sequence",
        status: "running",
        children: [{ nodeId: "loop", type: "repeat", status: "running", children: passes }],
      },
    });
    inspect.set(id, state(tree([pass(0, "running", "pending")])));
    live(id, c);
    await activate(c);
    expect(rowText(".ev-sum-progress .sr-only")).toEqual(["loop pass 1, step 1 of 2"]);
    const before = parts();

    // The same totals, done count, position, state, label and alert: only the pass differs.
    await advance(id, tree([pass(0, "completed", "completed"), pass(1, "running", "pending")]));

    expect(rowText(".ev-sum-progress .sr-only")).toEqual(["loop pass 2, step 1 of 2"]);
    expect(parts().row).toBe(before.row);
  });

  it("keeps the keyboard's place on a row whose state changed", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("focus");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open");
    btn?.focus();
    expect(document.activeElement).toBe(btn);

    await advance(id, { status: "paused" });

    expect(rowStates()).toEqual(["waiting"]);
    expect(document.activeElement).toBe(btn);
  });

  it("keeps the keyboard on a row the waiting-first order moves", async () => {
    const c = chatID();
    chats.push(c);
    const [first, second] = [runID("first"), runID("second")];
    inspect.set(first, state({ display_name: "first" }));
    inspect.set(second, state({ display_name: "second" }));
    live(first, c);
    live(second, c);
    await activate(c);
    expect(rowText(".ev-sum-label")).toEqual(["first", "second"]);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open");
    btn?.focus();

    await advance(second, { display_name: "second", status: "paused" });

    expect(rowText(".ev-sum-label")).toEqual(["second", "first"]);
    expect(document.activeElement).toBe(btn);
  });

  // A patch is not licence to go stale: the mark is written from the run's state on every pass, on
  // the SAME element, so the row that keeps its glyph still tells the truth about what the run is
  // doing.
  it("writes nothing to the bar when the shared clock repaints an unchanged run", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const c = chatID();
    chats.push(c);
    const id = runID("still");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    expect(bar.querySelector(".run-bar-glyph")?.getAttribute("data-status")).toBe("working");

    const writes: string[] = [];
    const mo = new MutationObserver((records) => {
      for (const r of records) {
        const at = r.target instanceof Element ? r.target : r.target.parentElement;
        // The elapsed clock moves with time, not with the run.
        if (at?.closest(".ev-sum-clock") === null) {
          writes.push(`${r.type} ${at.className} ${r.attributeName ?? ""}`);
        }
      }
    });
    mo.observe(bar, { subtree: true, childList: true, attributes: true, characterData: true });
    vi.advanceTimersByTime(1000);
    for (const r of mo.takeRecords()) {
      const at = r.target instanceof Element ? r.target : r.target.parentElement;
      if (at?.closest(".ev-sum-clock") === null) {
        writes.push(`${r.type} ${at.className} ${r.attributeName ?? ""}`);
      }
    }
    mo.disconnect();
    vi.useRealTimers();

    expect(writes).toEqual([]);
  });

  it("rewrites the mark on the element it kept", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("mark");
    inspect.set(id, state());
    live(id, c);
    await activate(c);
    const glyph = bar.querySelector(".run-bar-glyph");
    expect(glyph?.getAttribute("data-status")).toBe("working");

    await advance(id, { status: "paused" });
    expect(bar.querySelector(".run-bar-glyph")).toBe(glyph);
    expect(glyph?.getAttribute("data-status")).toBe("waiting");

    pushRunAsk(c, id);
    expect(glyph?.getAttribute("data-status")).toBe("input");
  });

  // The name is resolved at CLICK time rather than captured at mount, because the row outlives its
  // first paint now: a run whose label arrives with its first fetch would otherwise open a tab
  // called "Workflow run" for the rest of the session.
  it("opens the tab under the label the run has NOW, not the one it was mounted with", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("late-label");
    live(id, c);
    await activate(c);
    // Nothing fetched yet, so the row carries the fallback name.
    expect(rowText(".ev-sum-label")).toEqual(["Workflow run"]);
    const btn = bar.querySelector<HTMLElement>(".run-bar-open");

    await advance(id, { display_name: "nightly sweep" });

    expect(bar.querySelector(".run-bar-open")).toBe(btn);
    btn?.click();
    expect(openRunView).toHaveBeenCalledWith(id, "nightly sweep", c);
  });
});

// The fold opens and shuts with the transcript's one disclosure slide (`disclosure-row.ts` `slideBox`), on the
// shipped stylesheet so the list has heights to slide between.
describe("the run bar's fold motion", () => {
  let style: HTMLStyleElement;

  beforeEach(() => {
    localStorage.removeItem("marotte.ui-state");
    style = mountAppCSS();
  });

  afterEach(() => {
    // A case can leave the list scrolled. With the stylesheet gone the list stops scrolling, so the
    // root afterEach empties it without clamping, and Chromium applies the kept offset again once a
    // later case's rows overflow it.
    bar.scrollTop = 0;
    style.remove();
    vi.unstubAllGlobals();
    localStorage.removeItem("marotte.ui-state");
  });

  function heightSlides(): Animation[] {
    return bar.getAnimations().filter((a) => {
      const fx = a.effect;
      return fx instanceof KeyframeEffect && fx.getKeyframes().some((k) => "blockSize" in k);
    });
  }

  async function folded(n: number): Promise<{ chat: string; toggle: HTMLElement }> {
    const chat = chatID();
    chats.push(chat);
    for (let i = 0; i < n; i++) {
      const id = runID(`m${String(i)}`);
      inspect.set(id, state({ display_name: id }));
      live(id, chat);
    }
    await activate(chat);
    const toggle = fold.querySelector<HTMLElement>(".run-bar-toggle");
    if (toggle === null) {
      throw new Error("no fold row");
    }
    return { chat, toggle };
  }

  it("slides the list open and shut at the disclosure tokens, fading as it goes", async () => {
    const { toggle } = await folded(3);
    toggle.click();
    const [open] = heightSlides();
    expect(open, "an open slide").toBeDefined();
    const timing = open?.effect?.getTiming();
    const root = getComputedStyle(document.documentElement);
    expect(Number(timing?.duration)).toBe(
      parseFloat(root.getPropertyValue("--uip-disclosure-duration")) * 1000,
    );
    expect(timing?.easing).toBe(root.getPropertyValue("--uip-disclosure-easing").trim());
    const frames = (open?.effect as KeyframeEffect).getKeyframes();
    expect(frames[0]?.["opacity"]).toBe("0");
    expect(frames.at(-1)?.["opacity"]).toBe("1");
    await finishSlides();

    toggle.click();
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    const [shut] = heightSlides();
    expect(shut, "a close slide").toBeDefined();
    const back = (shut?.effect as KeyframeEffect).getKeyframes();
    expect(parseFloat(String(back.at(-1)?.["blockSize"]))).toBe(0);
    expect(back.at(-1)?.["opacity"]).toBe("0");
    // The rows stay rendered until the slide ends.
    expect(bar.hasAttribute("data-folded")).toBe(false);
    await finishSlides();
    expect(bar.hasAttribute("data-folded")).toBe(true);
  });

  it("never lays the folded list out before a close's slide ends", async () => {
    // A layout of the shut list grows the transcript's scroller, which clamps a reader at its end off the live edge.
    const { toggle } = await folded(3);
    toggle.click();
    await finishSlides();
    const flips: boolean[] = [];
    const watch = new MutationObserver(() => {
      flips.push(bar.hasAttribute("data-folded"));
    });
    watch.observe(bar, { attributes: true, attributeFilter: ["data-folded"] });
    toggle.click();
    await Promise.resolve();
    expect(flips).toEqual([]);
    expect(heightSlides()).toHaveLength(1);
    await finishSlides();
    await Promise.resolve();
    watch.disconnect();
    expect(flips).toEqual([true]);
  });

  it("flips at once under reduced motion", async () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce") }));
    const { toggle } = await folded(3);
    toggle.click();
    expect(bar.hasAttribute("data-folded")).toBe(false);
    expect(heightSlides()).toHaveLength(0);
    toggle.click();
    expect(bar.hasAttribute("data-folded")).toBe(true);
    expect(heightSlides()).toHaveLength(0);
  });

  it("starts no slide when a row changes, arrives or leaves while the list is open", async () => {
    const { chat, toggle } = await folded(3);
    toggle.click();
    await finishSlides();
    const first = bar.querySelector<HTMLElement>("li.run-bar-row");
    const id = runs.at(-1) ?? "";

    inspect.set(id, state({ display_name: id, status: "paused" }));
    invalidateRun(id);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(heightSlides(), "a repaint").toHaveLength(0);

    const late = runID("late");
    inspect.set(late, state({ display_name: late }));
    live(late, chat);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(4);
    expect(heightSlides(), "an arrival").toHaveLength(0);

    noteRunSettled(late);
    await new Promise((r) => setTimeout(r, 0));
    expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(3);
    expect(heightSlides(), "a departure").toHaveLength(0);
    // The paused run moved to the front, so the row first in line is checked by identity, not position.
    expect(first?.isConnected).toBe(true);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
  });

  it("keeps a scrolled list's offset through a close and a reopen", async () => {
    const { toggle } = await folded(12);
    // The fold row's gap transition runs beside the slide and sets the list's room, so it ends first.
    const settle = async (): Promise<void> => {
      for (const a of fold.getAnimations()) {
        a.finish();
      }
      await finishSlides();
    };
    toggle.click();
    await settle();
    // A row's entry rise (`translateY(4px)`) widens the scroll range until it ends.
    for (const row of bar.querySelectorAll(".run-bar-open")) {
      for (const a of row.getAnimations()) {
        a.finish();
      }
    }
    expect(getComputedStyle(fold).marginBlockEnd, "the open gap").not.toBe("0px");
    expect(bar.scrollHeight, "the list overflows its cap").toBeGreaterThan(bar.clientHeight);
    bar.scrollTop = bar.scrollHeight;
    const top = bar.scrollTop;
    expect(top).toBeGreaterThan(0);
    const midway = (): void => {
      const [slide] = heightSlides();
      if (slide === undefined) {
        throw new Error("no slide");
      }
      slide.pause();
      slide.currentTime = Number(slide.effect?.getTiming().duration) / 2;
    };

    toggle.click();
    midway();
    expect(bar.scrollTop, "mid-close").toBe(top);
    // The slide reaches its end with the fold row's gap already shut, and a layout runs before the fold: `finish()`
    // would resolve the fold first.
    for (const a of fold.getAnimations()) {
      a.finish();
    }
    const [closing] = heightSlides();
    closing?.play();
    if (closing !== undefined) {
      closing.currentTime = Number(closing.effect?.getTiming().duration);
    }
    expect(bar.getBoundingClientRect().height, "the slide's end").toBe(0);
    expect(bar.scrollTop, "the slide's end").toBe(top);
    await closing?.finished;
    await new Promise((r) => requestAnimationFrame(r));
    expect(bar.hasAttribute("data-folded"), "closed").toBe(true);
    expect(bar.scrollTop, "closed").toBe(top);
    await settle();
    toggle.click();
    midway();
    expect(bar.scrollTop, "mid-reopen").toBe(top);
    await settle();
    expect(bar.scrollTop, "reopened").toBe(top);
  });

  it("retargets an open under way when a run arrives, so it ends at the list's height", async () => {
    const { chat, toggle } = await folded(3);
    toggle.click();
    heightSlides()[0]?.pause();
    const late = runID("late");
    inspect.set(late, state({ display_name: late }));
    live(late, chat);
    await new Promise((r) => setTimeout(r, 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(4);
    const [slide] = heightSlides();
    const end = (slide?.effect as KeyframeEffect | undefined)?.getKeyframes().at(-1)?.["blockSize"];
    await finishSlides();
    expect(parseFloat(String(end))).toBe(bar.getBoundingClientRect().height);
  });

  it("cuts a close under way when the fold row leaves, and shows the run left", async () => {
    const { toggle } = await folded(2);
    expect(toggle.getAttribute("aria-expanded"), "two runs start open").toBe("true");
    toggle.click();
    heightSlides()[0]?.pause();
    noteRunSettled(runs.at(-1) ?? "");
    await new Promise((r) => setTimeout(r, 0));
    expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(1);
    expect(fold.hidden).toBe(true);
    expect(heightSlides()).toHaveLength(0);
    expect(bar.hasAttribute("data-folded")).toBe(false);
    await new Promise((r) => setTimeout(r, 0));
    expect(bar.hasAttribute("data-folded")).toBe(false);
  });

  it("opens with no slide when the fold row first appears with the list already open", async () => {
    localStorage.setItem("marotte.ui-state", JSON.stringify({ run_bar_open: true }));
    const { toggle } = await folded(3);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(bar.hasAttribute("data-folded")).toBe(false);
    expect(heightSlides()).toHaveLength(0);
  });
});

// The box's one batch control: Pause all while a listed run runs, Resume all once none runs and one is a plain pause.
// Each run goes through its own pause or resume route.
describe("Pause all and Resume all", () => {
  const PAUSABLE = { verbs: ["pause", "cancel"] };
  const RESUMABLE = { verbs: ["resume", "cancel"] };
  // KAS's reason on the owning parent's pause (pinned by internal/kascap's census fixture).
  const PARENT_PAUSE = "Pause requested by owning parent/orchestrator; human intent not verified.";
  // marotte's sign-in gate pause (internal/agent/testdata/pause_reasons.json).
  const SIGN_IN_HOLD = "sign in to Kiro to continue; marotte resumes the run when you do";

  const pausedRoot = { nodeId: "n1", type: "step", status: "paused" };
  const running = (name: string): Record<string, unknown> =>
    state({ display_name: name, controls: PAUSABLE });
  const pausedBy = (name: string, reason = ""): Record<string, unknown> =>
    state({
      display_name: name,
      status: "paused",
      stopInitiator: "user",
      stopReason: reason,
      root: pausedRoot,
      controls: RESUMABLE,
    });

  /** Every POST the batch sent, as `<id> <verb>`, in send order. */
  let posts: string[] = [];
  /** Runs whose POST the server refuses. */
  let refuse = new Set<string>();
  /** The state a run reads once its POST lands. */
  let after = new Map<string, Record<string, unknown>>();
  /** Holds every POST's answer until opened, so a test can see which were sent together. */
  let gate: Promise<void> = Promise.resolve();

  beforeEach(() => {
    posts = [];
    refuse = new Set();
    after = new Map();
    gate = Promise.resolve();
    initActions();
    vi.mocked(toast.error).mockClear();
    configureApi({
      fetchFn: async (input) => {
        const url = typeof input === "string" ? input : (input as Request).url;
        const hit = /\/api\/runs\/([^/]+)\/(pause|resume)$/.exec(url);
        if (hit === null) {
          return new Response("{}", { status: 404 });
        }
        const id = decodeURIComponent(hit[1] ?? "");
        posts.push(`${id} ${hit[2] ?? ""}`);
        await gate;
        if (refuse.has(id)) {
          return new Response(JSON.stringify({ error: "refused" }), {
            status: 409,
            headers: { "content-type": "application/json" },
          });
        }
        const next = after.get(id);
        if (next !== undefined) {
          inspect.set(id, next);
        }
        return new Response(JSON.stringify({ ok: true }), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      },
    });
  });

  /** The batch button on screen, asserting there is never more than one. */
  function batchButton(): HTMLButtonElement | null {
    const shown = [...document.querySelectorAll<HTMLButtonElement>(".run-bar-all")].filter(
      (b) => b.isConnected && !b.hidden,
    );
    expect(shown.length, "one batch button at most").toBeLessThanOrEqual(1);
    return shown[0] ?? null;
  }

  async function mount(chat: string, runs: Record<string, Record<string, unknown>>): Promise<void> {
    for (const [id, st] of Object.entries(runs)) {
      inspect.set(id, st);
      live(id, chat);
    }
    await activate(chat);
  }

  it("offers Pause all, with a tooltip, while a listed run is running", async () => {
    const c = chatID();
    chats.push(c);
    await mount(c, { [runID("one")]: running("one"), [runID("two")]: running("two") });
    const btn = batchButton();
    expect(btn?.getAttribute("aria-label")).toBe("Pause all");
    expect(btn?.dataset["tooltip"] ?? "").not.toBe("");
  });

  it("moves the button from the header to the run left when a second run leaves, and back when one arrives", async () => {
    localStorage.removeItem("marotte.ui-state");
    const c = chatID();
    chats.push(c);
    const [a, b] = [runID("a"), runID("b")];
    await mount(c, { [a]: running("a"), [b]: running("b") });
    const btn = batchButton();
    expect(fold.hidden).toBe(false);
    expect(fold.contains(btn)).toBe(true);
    expect(bar.querySelector(".run-bar-all")).toBeNull();

    noteRunSettled(b);
    await vi.waitFor(() => {
      expect(fold.hidden).toBe(true);
    });
    expect(batchButton(), "the same button").toBe(btn);
    expect(bar.querySelector("li.run-bar-row")?.contains(btn)).toBe(true);
    expect(btn?.getAttribute("aria-label")).toBe("Pause");

    inspect.set(b, running("b"));
    live(b, c);
    await vi.waitFor(() => {
      expect(fold.hidden).toBe(false);
    });
    expect(fold.contains(batchButton())).toBe(true);
    expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
    localStorage.removeItem("marotte.ui-state");
  });

  it("hands the keyboard from the header's toggle to the run left when the header goes", async () => {
    localStorage.removeItem("marotte.ui-state");
    const c = chatID();
    chats.push(c);
    const [a, b] = [runID("fa"), runID("fb")];
    await mount(c, { [a]: running("a"), [b]: running("b") });
    const toggle = fold.querySelector<HTMLElement>(".run-bar-toggle");
    toggle?.focus();
    expect(document.activeElement, "focused on the header").toBe(toggle);

    noteRunSettled(b);
    await vi.waitFor(() => {
      expect(fold.hidden).toBe(true);
    });
    expect(document.activeElement).toBe(bar.querySelector(".run-bar-open"));
    localStorage.removeItem("marotte.ui-state");
  });

  it("keeps the keyboard on the batch button as it moves between the header and the run's row", async () => {
    localStorage.removeItem("marotte.ui-state");
    const c = chatID();
    chats.push(c);
    const [a, b] = [runID("ka"), runID("kb")];
    await mount(c, { [a]: running("a"), [b]: running("b") });
    const btn = batchButton();
    btn?.focus();
    expect(document.activeElement, "focused on the header").toBe(btn);

    noteRunSettled(b);
    await vi.waitFor(() => {
      expect(fold.hidden).toBe(true);
    });
    expect(bar.contains(btn)).toBe(true);
    expect(document.activeElement, "on the button in the run's row").toBe(btn);

    inspect.set(b, running("b"));
    live(b, c);
    await vi.waitFor(() => {
      expect(fold.hidden).toBe(false);
    });
    expect(fold.contains(btn)).toBe(true);
    expect(document.activeElement, "on the button in the header").toBe(btn);
    localStorage.removeItem("marotte.ui-state");
  });

  it("pauses the one run from the button at its row's right, without opening its tab", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("solo");
    await mount(c, { [id]: running("solo") });
    after.set(id, pausedBy("solo"));
    const btn = batchButton();
    expect(fold.hidden, "no header").toBe(true);
    expect(bar.querySelector("li.run-bar-row")?.contains(btn)).toBe(true);
    expect(btn?.getAttribute("aria-label")).toBe("Pause");

    btn?.click();
    await vi.waitFor(() => {
      expect(batchButton()?.getAttribute("aria-label")).toBe("Resume");
    });
    expect(posts).toEqual([`${id} pause`]);
    expect(rowStates()).toEqual(["waiting"]);
    expect(openRunView).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  });

  // The one run's button resumes exactly what Resume all would.
  for (const [name, make] of [
    ["paused by the reader", (n: string) => pausedBy(n)],
    ["paused by the parent", (n: string) => pausedBy(n, PARENT_PAUSE)],
    ["paused after rate-limit retries", (n: string) => throttled(n)],
  ] as const) {
    it(`resumes the one run ${name}`, async () => {
      const c = chatID();
      chats.push(c);
      const id = runID("solo-parked");
      await mount(c, { [id]: make("solo") });
      after.set(id, running("solo"));
      const btn = batchButton();
      expect(btn?.getAttribute("aria-label")).toBe("Resume");
      btn?.click();
      await vi.waitFor(() => {
        expect(batchButton()?.getAttribute("aria-label")).toBe("Pause");
      });
      expect(posts).toEqual([`${id} resume`]);
    });
  }

  for (const [name, make] of [
    [
      "paused while its step waits for an answer",
      (n: string) => ({ ...pausedBy(n), controls: { verbs: ["cancel"] } }),
    ],
    [
      "at a loop's pass limit",
      (n: string) => ({
        ...pausedBy(n),
        pause: { kind: "maxIterations", nodeId: "loop", maxIterations: 4, iterationsRun: 4 },
        controls: { verbs: ["extend", "finish_loop", "cancel"], pause_node_id: "loop" },
      }),
    ],
  ] as const) {
    it(`offers no resume on the one run ${name}`, async () => {
      const c = chatID();
      chats.push(c);
      const id = runID("solo-stuck");
      await mount(c, { [id]: make("solo") });
      expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(1);
      expect(batchButton()).toBeNull();
    });
  }

  it("pauses every running listed run at once, and no other run", async () => {
    const c = chatID();
    const other = chatID();
    chats.push(c, other);
    const [r1, r2, pending, parked, theirs] = [
      runID("r1"),
      runID("r2"),
      runID("pending"),
      runID("parked"),
      runID("theirs"),
    ];
    inspect.set(theirs, running("theirs"));
    live(theirs, other);
    // A run whose pause is already asked for: the server withholds a second one.
    const pausing = state({
      display_name: "pausing",
      pausePending: { initiator: "user" },
      controls: { verbs: ["cancel"] },
    });
    await mount(c, {
      [r1]: running("r1"),
      [r2]: running("r2"),
      [pending]: pausing,
      [parked]: pausedBy("parked"),
    });
    after.set(r1, pausedBy("r1"));
    after.set(r2, pausedBy("r2"));
    let open = (): void => undefined;
    gate = new Promise((r) => {
      open = r;
    });

    batchButton()?.click();
    await vi.waitFor(() => {
      expect(posts.length).toBe(2);
    });
    expect(posts.toSorted(), "sent together, before either answered").toEqual(
      [`${r1} pause`, `${r2} pause`].toSorted(),
    );
    open();
    // The real states after the reread: only the run already pausing still runs, and it offers no pause.
    await vi.waitFor(() => {
      expect(rowStates().filter((s) => s === "running")).toEqual(["running"]);
    });
    expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
    expect(batchButton()?.disabled).toBe(true);
    // Its pause lands, and nothing runs.
    inspect.set(pending, pausedBy("pausing"));
    invalidateRun(pending);
    await vi.waitFor(() => {
      expect(batchButton()?.getAttribute("aria-label")).toBe("Resume all");
    });
    expect(batchButton()?.disabled).toBe(false);
    expect(posts.some((p) => p.startsWith(theirs))).toBe(false);
    expect(posts.some((p) => p.startsWith(parked))).toBe(false);
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("resumes only the runs a person or the parent chat paused", async () => {
    const c = chatID();
    const other = chatID();
    chats.push(c, other);
    const [mine, parents, asking, capped, held, theirs] = [
      runID("mine"),
      runID("parents"),
      runID("asking"),
      runID("capped"),
      runID("held"),
      runID("theirs"),
    ];
    inspect.set(theirs, pausedBy("theirs"));
    live(theirs, other);
    await mount(c, {
      [mine]: pausedBy("mine"),
      [parents]: pausedBy("parents", PARENT_PAUSE),
      // Paused by the reader while its step waits for an answer: the server offers no resume.
      [asking]: { ...pausedBy("asking"), controls: { verbs: ["cancel"] } },
      // A loop at its cap: only extend or finish moves it.
      [capped]: {
        ...pausedBy("capped"),
        pause: { kind: "maxIterations", nodeId: "loop", maxIterations: 4, iterationsRun: 4 },
        controls: { verbs: ["extend", "finish_loop", "cancel"], pause_node_id: "loop" },
      },
      // marotte's own sign-in hold, which marotte resumes itself.
      [held]: pausedBy("held", SIGN_IN_HOLD),
    });
    pushRunAsk(c, asking);
    after.set(mine, running("mine"));
    after.set(parents, running("parents"));
    const btn = batchButton();
    expect(btn?.getAttribute("aria-label")).toBe("Resume all");

    btn?.click();
    await vi.waitFor(() => {
      expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
    });
    expect(posts.toSorted()).toEqual([`${mine} resume`, `${parents} resume`].toSorted());
  });

  // A rate limit is why the button exists: KAS pauses a run itself once its retries run out, names
  // no initiator, and marotte may have stopped resuming it. That is a plain pause.
  const throttled = (name: string): Record<string, unknown> =>
    state({
      display_name: name,
      status: "paused",
      pauseDetail: { class: "transient-error", code: "ThrottlingException" },
      autoResumeStoppedAt: "2026-11-06T20:00:00Z",
      root: pausedRoot,
      controls: RESUMABLE,
    });

  it("resumes the runs KAS paused itself after rate-limit retries", async () => {
    const c = chatID();
    chats.push(c);
    const [limited, mine, exhausted, asking] = [
      runID("limited"),
      runID("mine"),
      runID("exhausted"),
      runID("asking"),
    ];
    await mount(c, {
      [limited]: throttled("limited"),
      [mine]: pausedBy("mine"),
      // KAS's other self-pause class is no rate limit, so it stays out.
      [exhausted]: {
        ...throttled("exhausted"),
        pauseDetail: { class: "continuation-exhausted", code: "ContinuationExhausted" },
      },
      // A rate-limited run whose step waits for an answer: the server offers no resume.
      [asking]: { ...throttled("asking"), controls: { verbs: ["cancel"] } },
    });
    pushRunAsk(c, asking);
    after.set(limited, running("limited"));
    after.set(mine, running("mine"));
    const btn = batchButton();
    expect(btn?.getAttribute("aria-label")).toBe("Resume all");

    btn?.click();
    await vi.waitFor(() => {
      expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
    });
    expect(posts.toSorted()).toEqual([`${limited} resume`, `${mine} resume`].toSorted());
  });

  it("offers Resume all when only a rate-limited run is paused", async () => {
    const c = chatID();
    chats.push(c);
    await mount(c, {
      [runID("limited")]: throttled("limited"),
      [runID("limited-2")]: throttled("limited-2"),
    });
    expect(batchButton()?.getAttribute("aria-label")).toBe("Resume all");
  });

  // An empty class is read as a transient error, as the pause's own detail line reads it.
  it("offers Resume all for a self-paused run whose detail names an empty class", async () => {
    const c = chatID();
    chats.push(c);
    const blank = (name: string): Record<string, unknown> => ({
      ...throttled(name),
      pauseDetail: { class: "", code: "ServiceUnavailable" },
    });
    await mount(c, { [runID("blank")]: blank("blank"), [runID("blank-2")]: blank("blank-2") });
    expect(batchButton()?.getAttribute("aria-label")).toBe("Resume all");
  });

  it("raises one toast naming the runs that failed, and keeps to the real states", async () => {
    const c = chatID();
    chats.push(c);
    const [a, b, d] = [runID("a"), runID("b"), runID("d")];
    await mount(c, { [a]: running("alpha"), [b]: running("beta"), [d]: running("gamma") });
    refuse = new Set([a, b]);
    after.set(d, pausedBy("gamma"));

    batchButton()?.click();
    await vi.waitFor(() => {
      expect(toast.error).toHaveBeenCalledTimes(1);
    });
    const message = String(vi.mocked(toast.error).mock.calls[0]?.[0]);
    expect(message).toContain("alpha");
    expect(message).toContain("beta");
    expect(message).not.toContain("gamma");
    // Two runs still run, so the button still pauses; the one that paused reads paused.
    await vi.waitFor(() => {
      expect(rowStates().toSorted()).toEqual(["running", "running", "waiting"]);
    });
    expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
  });

  it("shows no button when no listed run runs and none is a plain pause", async () => {
    const c = chatID();
    chats.push(c);
    const [asking, capped, held] = [runID("asking"), runID("capped"), runID("held")];
    await mount(c, {
      [asking]: state({
        display_name: "asking",
        status: "paused",
        root: pausedRoot,
        controls: { verbs: ["cancel"] },
      }),
      [capped]: state({
        display_name: "capped",
        status: "paused",
        root: pausedRoot,
        pause: { kind: "maxIterations", nodeId: "loop", maxIterations: 4, iterationsRun: 4 },
        controls: { verbs: ["extend", "finish_loop", "cancel"], pause_node_id: "loop" },
      }),
      [held]: pausedBy("held", SIGN_IN_HOLD),
    });
    pushRunAsk(c, asking);
    expect(bar.classList.contains("hidden")).toBe(false);
    expect(batchButton()).toBeNull();
  });

  it("prefers Pause all while any listed run runs, even beside a paused one", async () => {
    const c = chatID();
    chats.push(c);
    await mount(c, { [runID("p")]: pausedBy("p"), [runID("r")]: running("r") });
    expect(batchButton()?.getAttribute("aria-label")).toBe("Pause all");
  });

  it("drops the button once the last run stops running and none is a plain pause", async () => {
    const c = chatID();
    chats.push(c);
    const id = runID("ends-pause");
    const stuck = (name: string): Record<string, unknown> =>
      state({
        display_name: name,
        status: "paused",
        root: pausedRoot,
        controls: { verbs: ["cancel"] },
      });
    await mount(c, { [id]: running("x"), [runID("ends-held")]: stuck("y") });
    expect(batchButton()).not.toBeNull();
    inspect.set(id, stuck("x"));
    invalidateRun(id);
    await vi.waitFor(() => {
      expect(rowStates()).toEqual(["waiting", "waiting"]);
    });
    expect(batchButton()).toBeNull();
  });

  // One run needs no recap and no batch control. From two runs the header row shows the recap, the fold toggle and the
  // batch button at its right end.
  describe("the header row", () => {
    let style: HTMLStyleElement;

    beforeEach(() => {
      localStorage.removeItem("marotte.ui-state");
      style = mountAppCSS();
    });

    afterEach(() => {
      style.remove();
      localStorage.removeItem("marotte.ui-state");
    });

    // A live run reports credits, which gives the rows their credits column.
    const billed =
      (base: (name: string) => Record<string, unknown>) =>
      (name: string): Record<string, unknown> => {
        const st = base(name);
        const root = st["root"] as Record<string, unknown>;
        return { ...st, root: { ...root, credits: [{ usage: 1.25, unit: "credit" }] } };
      };

    async function mountN(
      n: number,
      make: (name: string) => Record<string, unknown>,
    ): Promise<void> {
      const c = chatID();
      chats.push(c);
      const set: Record<string, Record<string, unknown>> = {};
      for (let i = 0; i < n; i++) {
        set[runID(`h${String(i)}`)] = make(`header-run-${String(i)}`);
      }
      await mount(c, set);
    }

    function toggleEl(): HTMLElement {
      const t = fold.querySelector<HTMLElement>(".run-bar-toggle");
      if (t === null || fold.hidden) {
        throw new Error("no header row");
      }
      return t;
    }

    /** The batch button sits on the header's line, right of the recap, at the box's top right, above every run. */
    function expectOnHeader(btn: HTMLButtonElement | null): void {
      if (btn === null) {
        throw new Error("no batch button");
      }
      expect(fold.contains(btn), "inside the header row").toBe(true);
      const t = toggleEl().getBoundingClientRect();
      const r = btn.getBoundingClientRect();
      const mid = (r.top + r.bottom) / 2;
      expect(r.left, "right of the recap").toBeGreaterThanOrEqual(t.right - 0.5);
      expect(mid, "on the recap's line").toBeGreaterThanOrEqual(t.top - 0.5);
      expect(mid, "on the recap's line").toBeLessThanOrEqual(t.bottom + 0.5);
      expect(
        runHost.getBoundingClientRect().right - r.right,
        "at the box's end inset",
      ).toBeLessThan(12);
      for (const row of bar.querySelectorAll<HTMLElement>("li.run-bar-row")) {
        if (row.checkVisibility()) {
          expect(row.getBoundingClientRect().top, "below the header").toBeGreaterThanOrEqual(
            r.bottom - 0.5,
          );
        }
      }
    }

    for (const [pointer, width] of [
      ["fine", 1280],
      ["coarse", 1280],
      ["coarse", 390],
    ] as const) {
      it(`renders 1 run with no header, and a Pause/Resume button at the right of its row, ${pointer} at ${String(width)}px`, async () => {
        await page.viewport(width, 844);
        document.documentElement.setAttribute("data-pointer", pointer);
        try {
          for (const [make, label] of [
            [billed(running), "Pause"],
            [billed(pausedBy), "Resume"],
          ] as const) {
            await mountN(1, make);
            const rows = bar.querySelectorAll<HTMLElement>("li.run-bar-row");
            expect(rows).toHaveLength(1);
            expect(fold.checkVisibility(), "no header row").toBe(false);
            const btn = batchButton();
            expect(btn?.getAttribute("aria-label")).toBe(label);
            expect(btn?.checkVisibility(), label).toBe(true);
            const row = rows[0]!;
            expect(row.contains(btn), "in the run's own row").toBe(true);
            const r = btn!.getBoundingClientRect();
            const li = row.getBoundingClientRect();
            const open = row.querySelector<HTMLElement>(".run-bar-open")!.getBoundingClientRect();
            expect(r.height, "painted at --btn-h").toBeCloseTo(lengthPx("var(--btn-h)"), 0);
            expect(r.left, "right of the run's line").toBeGreaterThanOrEqual(open.right - 0.5);
            expect(li.right - r.right, "at the row's right end").toBeLessThan(5);
            expect(r.top, "on the run's line").toBeGreaterThanOrEqual(li.top - 0.5);
            expect(r.bottom, "on the run's line").toBeLessThanOrEqual(li.bottom + 0.5);
            expect(
              Math.abs((r.top + r.bottom) / 2 - (li.top + li.bottom) / 2),
              "centred in the row",
            ).toBeLessThanOrEqual(0.5);
            const hit = hitBox(btn!);
            const floor = lengthPx("var(--hit-floor)");
            expect(hit.width, "target width").toBeGreaterThanOrEqual(floor - 0.5);
            expect(hit.height, "target height").toBeGreaterThanOrEqual(floor - 0.5);
            // The list clips at its edges, so the whole target must sit inside it to be pressable.
            const list = bar.getBoundingClientRect();
            expect(hit.right).toBeLessThanOrEqual(list.right + 0.5);
            expect(hit.top).toBeGreaterThanOrEqual(list.top - 0.5);
            expect(hit.bottom).toBeLessThanOrEqual(list.bottom + 0.5);
            expect(hit.left, "clear of the run's line").toBeGreaterThanOrEqual(open.right - 0.5);
            for (const id of runs.splice(0)) {
              noteRunSettled(id);
            }
            await new Promise((r) => setTimeout(r, 0));
          }
        } finally {
          document.documentElement.removeAttribute("data-pointer");
          await page.viewport(1280, 720);
        }
      });
    }

    for (const n of [2, 3]) {
      it(`heads ${String(n)} runs with the recap, its toggle and Pause all at its right`, async () => {
        await mountN(n, billed(running));
        const t = toggleEl();
        expect(t.querySelector(".run-bar-count")?.textContent).toBe(
          `${String(n)} workflows running`,
        );
        expect(t.getAttribute("aria-expanded"), "open below three runs").toBe(String(n < 3));
        const btn = batchButton();
        expect(btn?.getAttribute("aria-label")).toBe("Pause all");
        expectOnHeader(btn);
        t.click();
        await finishSlides();
        expectOnHeader(batchButton());
      });
    }

    const rowsShown = (): boolean =>
      [...bar.querySelectorAll<HTMLElement>("li.run-bar-row")].every((r) => r.checkVisibility());

    it("lists two runs open under the header when this screen never chose", async () => {
      await mountN(2, billed(running));
      const t = toggleEl();
      expect(t.getAttribute("aria-expanded")).toBe("true");
      expect(bar.hasAttribute("data-folded")).toBe(false);
      expect(bar.querySelectorAll("li.run-bar-row")).toHaveLength(2);
      expect(rowsShown(), "both rows visible").toBe(true);
      expectOnHeader(batchButton());
    });

    it("folds and opens two runs from the header's toggle, and keeps the fold", async () => {
      await mountN(2, billed(running));
      const t = toggleEl();
      t.click();
      expect(t.getAttribute("aria-expanded")).toBe("false");
      await finishSlides();
      expect(bar.hasAttribute("data-folded")).toBe(true);
      expect(bar.getBoundingClientRect().height).toBe(0);
      expect(JSON.parse(localStorage.getItem("marotte.ui-state") ?? "{}")).toMatchObject({
        run_bar_open: false,
      });
      t.click();
      expect(t.getAttribute("aria-expanded")).toBe("true");
      await finishSlides();
      expect(bar.hasAttribute("data-folded")).toBe(false);
      expect(rowsShown()).toBe(true);
    });

    it("keeps two runs folded when the reader last folded the list", async () => {
      localStorage.setItem("marotte.ui-state", JSON.stringify({ run_bar_open: false }));
      await mountN(2, billed(running));
      expect(toggleEl().getAttribute("aria-expanded")).toBe("false");
      expect(bar.hasAttribute("data-folded")).toBe(true);
      expectOnHeader(batchButton());
    });

    it("folds three runs when this screen never chose", async () => {
      await mountN(3, billed(running));
      expect(toggleEl().getAttribute("aria-expanded")).toBe("false");
      expect(bar.hasAttribute("data-folded")).toBe(true);
      expect(bar.getBoundingClientRect().height).toBe(0);
    });

    it("offers Resume all on the header when every run is paused by hand", async () => {
      await mountN(2, billed(pausedBy));
      const btn = batchButton();
      expect(btn?.getAttribute("aria-label")).toBe("Resume all");
      expectOnHeader(btn);
    });
  });
});
