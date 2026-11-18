// The sidebar's Runs section: the live runs no open tab shows, each row a link to the run's tab. A
// run leaves when marotte releases its lease (`live_run`) or a tab for it or its chat opens.

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
import { openRunView } from "./run-view.js";
import type { SectionHeaderView } from "./section-header.js";
import { sideSection } from "./side-section.js";
import type { TabDotState } from "./store.js";
import {
  FOLD_DOT_PRECEDENCE,
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
}

/** The runs the section lists, by workflow id; each holds its run's header for the row's name and dot. */
const rows = new Map<string, Row>();
registerRunStateDemand((id) => rows.has(id), "header");

/** The run whose row was clicked, until its open settles: its row leaves when the tab opens, and the
 *  click, not the repaint, decides where focus goes then. */
let opening = "";

/** One collation for every browser language, so every device draws the same order. */
const byName = new Intl.Collator("en");

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

/** The first state, in the tab sections' precedence, among the rows a fold hides. */
function foldDot(): SectionHeaderView["dot"] {
  for (const status of FOLD_DOT_PRECEDENCE) {
    if ([...rows.values()].some((r) => r.status === status)) {
      return { status, phrase: dotPhrase("run", status) };
    }
  }
  return null;
}

async function open(row: Row): Promise<void> {
  opening = row.id;
  try {
    await openRunView(row.id, row.label);
  } finally {
    opening = "";
  }
  const tabID = tabIdFor("run", row.id);
  const focus = document.activeElement;
  const ours = focus === document.body || focus === row.link;
  // A refused open painted nothing, so the row and its focus are still there.
  if (!ours || tabID === "" || getActiveTabId() !== tabID) {
    return;
  }
  // The phone's open dismissed the drawer, so the tab row is out of sight there.
  if (!matchMedia("(width <= 48rem)").matches) {
    focusTabRow(tabID);
  }
}

function makeRow(id: string): Row {
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
  const row: Row = { id, li, link, dot, name, said, status: "", label: "" };
  // A modified or non-primary click follows the href, so a run opens in a new browser tab too.
  link.addEventListener("click", (e) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) {
      return;
    }
    e.preventDefault();
    void open(row);
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
        row = makeRow(r.id);
        rows.set(r.id, row);
      }
      if (runState(r.id) === undefined) {
        drawRun(r.id);
      }
      paintRow(row, liveRunStatus(r.id, r.executing), runLabelText(r.id));
    }
    const listed = [...rows.values()].sort(
      (a, b) => byName.compare(a.label, b.label) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
    );
    side.seat(listed.map((r) => ({ ref: r.id, li: r.li, controls: [r.link] })));
    side.restoreFocus(mark);
  });
}
