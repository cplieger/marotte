// The sidebar's Runs section: the live runs no chat launched that no open tab shows, each row a link
// to the run's tab. A run leaves when marotte releases its lease (`live_run`) or a tab for it opens.

import { effect, el } from "@cplieger/reactive";
import { liveRunStatus } from "./chat-run-dots.js";
import { sectionFolded, setSectionFolded } from "./device-view.js";
import { isSliding, slideBox } from "./disclosure-row.js";
import { maybeEl, setAttr, setText } from "./dom.js";
import { buildPath } from "./route-path.js";
import {
  drawRun,
  forgetRun,
  parentlessLiveRuns,
  registerRunStateDemand,
  runState,
} from "./run-store.js";
import { openRunView } from "./run-view.js";
import { paintSectionHeader, type SectionHeaderView } from "./section-header.js";
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
import { dragOwnsStrip } from "./tabs-drag.js";
import { runLabelText } from "./workflow-run-label.js";

const FOLD_KEY = "runs";

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

function isShown(e: Element | null | undefined): e is HTMLElement {
  return e instanceof HTMLElement && e.isConnected && e.getClientRects().length > 0;
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
function foldDot(listed: readonly Row[]): SectionHeaderView["dot"] {
  for (const status of FOLD_DOT_PRECEDENCE) {
    if (listed.some((r) => r.status === status)) {
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
  const ours = focus === null || focus === document.body || focus === row.link;
  // The phone's open dismissed the drawer, so the tab row is out of sight there.
  if (
    ours &&
    tabID !== "" &&
    getActiveTabId() === tabID &&
    !matchMedia("(width <= 48rem)").matches
  ) {
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
  const section = maybeEl("runs-section");
  const list = maybeEl("runs-list");
  const header = section?.querySelector<HTMLElement>(":scope > h2.side-section-h") ?? null;
  const toggle = header?.querySelector<HTMLButtonElement>(".side-section-toggle") ?? null;
  if (section === null || list === null || header === null || toggle === null) {
    return;
  }
  /** The ids as last drawn, in order. */
  let order: readonly string[] = [];

  /** After a repaint took the focused row away: the row after it, before it, the header, else the
   *  active tab's row. Focus anywhere shown, or a click's open in flight, is left alone. */
  const restoreFocus = (lost: string): void => {
    const now = document.activeElement;
    if (lost === "" || lost === opening || (now !== document.body && isShown(now))) {
      return;
    }
    const i = order.indexOf(lost);
    const near = [...order.slice(i + 1), ...order.slice(0, Math.max(i, 0)).reverse()];
    for (const to of [...near.map((id) => rows.get(id)?.link), toggle]) {
      if (isShown(to)) {
        to.focus();
        if (document.activeElement === to) {
          return;
        }
      }
    }
    focusTabRow(getActiveTabId());
  };

  const paintHeader = (listed: readonly Row[], folded: boolean): void => {
    paintSectionHeader(list, {
      noun: "run",
      count: listed.length,
      folded,
      dot: folded ? foldDot(listed) : null,
    });
  };

  effect(() => {
    // A tab opening or closing for a run moves its row out of the section or back into it.
    void tabSetVersion();
    const focused = document.activeElement;
    const was = [...rows.values()].find((r) => r.link === focused)?.id ?? "";
    const want = parentlessLiveRuns().filter((r) => !hasTab("run", r.id));
    const keep = new Set(want.map((r) => r.id));
    for (const [id, row] of rows) {
      if (!keep.has(id)) {
        row.li.remove();
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
      (a, b) => a.label.localeCompare(b.label) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
    );
    // Seated only when out of place, since a re-seat blurs a focused row.
    let prev: HTMLLIElement | null = null;
    for (const row of listed) {
      const at: Element | null = prev === null ? list.firstElementChild : prev.nextElementSibling;
      if (at !== row.li) {
        if (prev === null) {
          list.prepend(row.li);
        } else {
          prev.after(row.li);
        }
      }
      prev = row.li;
    }
    section.hidden = listed.length === 0;
    const folded = sectionFolded(FOLD_KEY);
    paintHeader(listed, folded);
    if (!isSliding(section)) {
      list.hidden = folded;
    }
    restoreFocus(rows.has(was) ? "" : was);
    order = listed.map((r) => r.id);
  });

  toggle.addEventListener("click", () => {
    if (dragOwnsStrip()) {
      return;
    }
    const folded = !sectionFolded(FOLD_KEY);
    setSectionFolded(FOLD_KEY, folded);
    const listed = order.flatMap((id) => rows.get(id) ?? []);
    paintHeader(listed, folded);
    // A click that moves no focus (WebKit) leaves it on a row the fold hides.
    const hadFocus = list.contains(document.activeElement);
    // The header is the section's whole height while its list is hidden, so the close needs no measuring layout.
    const closed = folded ? { closedHeight: header.getBoundingClientRect().height } : {};
    slideBox(
      section,
      !folded,
      (opened) => {
        list.hidden = !opened;
        if (hadFocus && !opened) {
          toggle.focus();
        }
      },
      closed,
    );
  });
}
