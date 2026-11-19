// The sidebar's Runs section, docked under the tab sections: the live runs no open tab shows, each
// row a link to the run's tab. A run leaves when marotte releases its lease (`live_run`) or a tab for
// it or its chat opens.

import { effect, el } from "@cplieger/reactive";
import { liveRunStatus } from "./chat-run-dots.js";
import { setAttr, setText } from "./dom.js";
import { buildPath } from "./route-path.js";
import {
  type LiveRunEntry,
  drawRun,
  forgetRun,
  liveRunList,
  registerRunStateDemand,
  runState,
} from "./run-store.js";
import { focusRunPage, openRunView } from "./run-view.js";
import type { SectionHeaderView } from "./section-header.js";
import { type SideSection, sideSection } from "./side-section.js";
import type { TabDotState } from "./store.js";
import {
  dotPhrase,
  focusTabRow,
  getActiveTabId,
  hasTab,
  statusDotEl,
  tabIdFor,
  tabSetVersion,
} from "./tabs.js";
import { runLabelText } from "./workflow-run-label.js";

interface Row {
  readonly id: string;
  readonly li: HTMLLIElement;
  readonly link: HTMLAnchorElement;
  readonly dot: HTMLElement;
  readonly name: HTMLElement;
  readonly said: HTMLElement;
  status: TabDotState;
  label: string;
  started: number;
}

/** The runs the section lists, by workflow id; each holds its run's header for the row's name and dot. */
const rows = new Map<string, Row>();
registerRunStateDemand((id) => rows.has(id), "header");

/** The run whose row was clicked, until its open settles: its row leaves when the tab opens, and the
 *  click, not the repaint, decides where focus goes then. */
let opening = "";

/** A row's place by need, the one order the rows and the folded header's dot share, so the rows past
 *  the list's fold never hide one that wants you: waiting for you (a decision or a resume) first,
 *  then failed, then running, then finished, then a run whose state has not landed. */
function need(status: TabDotState): number {
  switch (status) {
    case "input":
    case "waiting":
      return 0;
    case "failed":
      return 1;
    case "working":
    case "background":
      return 2;
    case "done":
      return 3;
    case "idle":
    case "":
      return 4;
  }
}

/** By need, then newest first, then by id: the same order on every device. */
function byNeed(a: Row, b: Row): number {
  return (
    need(a.status) - need(b.status) ||
    b.started - a.started ||
    (a.id < b.id ? -1 : a.id > b.id ? 1 : 0)
  );
}

/** The live runs no open tab shows: no tab for the run, and none for the chat that launched it,
 *  whose row would carry its mark. Subscribes to the inventory and the tab set. */
function untabbedLiveRuns(): LiveRunEntry[] {
  void tabSetVersion();
  return liveRunList().filter(
    (r) => !hasTab("run", r.id) && (r.chat === "" || !hasTab("chat", r.chat)),
  );
}

/** A row's dot and words: the run's own state, or nothing until its header or inventory says one. */
function paintRow(row: Row, status: TabDotState, label: string): void {
  row.status = status;
  row.label = label;
  setText(row.name, label);
  setAttr(row.link, "data-tooltip", label);
  const phrase = status === "" ? "" : dotPhrase("run", status);
  setAttr(row.dot, "data-status", status === "" ? null : status);
  setAttr(row.dot, "data-tooltip", phrase === "" ? null : phrase);
  setText(row.said, phrase === "" ? "" : `, ${phrase}`);
}

/** The state of the row a fold hides first: the most urgent by need, as the open list leads with it. */
function foldDot(): SectionHeaderView["dot"] {
  const first = [...rows.values()].sort(byNeed)[0];
  if (first === undefined || first.status === "" || first.status === "idle") {
    return null;
  }
  return { status: first.status, phrase: dotPhrase("run", first.status) };
}

/** Counts clicks, so only the latest one's open clears `opening` and moves focus. */
let openGen = 0;

async function open(row: Row, side: SideSection): Promise<void> {
  const gen = ++openGen;
  opening = row.id;
  const mark = side.markRow(row.id);
  try {
    await openRunView(row.id, row.label);
  } finally {
    if (gen === openGen) {
      opening = "";
    }
  }
  if (gen !== openGen) {
    return;
  }
  const tabID = tabIdFor("run", row.id);
  const focus = document.activeElement;
  const ours = focus === document.body || focus === row.link;
  if (tabID === "" || getActiveTabId() !== tabID) {
    // A refused open painted nothing; put focus back on the row, or the nearest stop if the row left meanwhile.
    if (ours) {
      side.restoreFocus(mark);
    }
    return;
  }
  if (!ours) {
    return;
  }
  // The phone's open dismissed the drawer, so the tab row is out of sight there.
  if (matchMedia("(width <= 48rem)").matches || !focusTabRow(tabID)) {
    focusRunPage();
  }
}

function makeRow(id: string, side: SideSection): Row {
  const dot = statusDotEl();
  const name = el("bdi", { className: "runs-row-name" });
  const said = el("span", { className: "sr-only" });
  const link = el(
    "a",
    { className: "runs-row", href: buildPath({ kind: "run", id }) },
    dot,
    name,
    said,
  ) as HTMLAnchorElement;
  const li = el("li", { className: "runs-item" }, link) as HTMLLIElement;
  const row: Row = { id, li, link, dot, name, said, status: "", label: "", started: 0 };
  // A modified or non-primary click follows the href, so a run opens in a new browser tab too.
  link.addEventListener("click", (e) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
      return;
    }
    e.preventDefault();
    void open(row, side);
  });
  return row;
}

/** Wire `#runs-section` once its markup is in the page. */
export function initRunsSection(): void {
  const side = sideSection("runs-section", "runs-list", {
    key: "runs",
    foldedByDefault: false,
    noun: "run",
    tail: [],
    foldDot,
    kept: () => null,
  });
  if (side === null) {
    return;
  }

  effect(() => {
    const focused = document.activeElement;
    // An open in flight moves focus off its row itself when it settles.
    const mark = rows.get(opening)?.link === focused ? null : side.noteFocus();
    const want = untabbedLiveRuns();
    const keep = new Set(want.map((r) => r.id));
    for (const id of rows.keys()) {
      if (!keep.has(id)) {
        rows.delete(id);
        // Out of this pass, so its writes cannot re-enter this effect; a demand still held keeps the state.
        queueMicrotask(() => {
          forgetRun(id);
        });
      }
    }
    for (const r of want) {
      let row = rows.get(r.id);
      if (row === undefined) {
        row = makeRow(r.id, side);
        rows.set(r.id, row);
      }
      if (runState(r.id) === undefined) {
        drawRun(r.id);
      }
      row.started = r.started;
      paintRow(row, liveRunStatus(r.id, r.executing), runLabelText(r.id));
    }
    const listed = [...rows.values()].sort(byNeed);
    side.seat(listed.map((r) => ({ ref: r.id, li: r.li, controls: [r.link] })));
    side.restoreFocus(mark);
  });
}
