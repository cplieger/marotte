// The sidebar's History section: the server's list of chats whose last tab closed lately, drawn as
// sent (`recentClosed()`), each row reopening its chat, and More, its last row, opening History.

import { effect, el, signal } from "@cplieger/reactive";
import { reopenChat } from "./chat.js";
import { showContextMenu } from "./context-menu.js";
import { $, setText } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { type Press, lastPress } from "./legacy-ui/scripted-focus.js";
import { bindContextPress } from "./context-hold.js";
import { ICON_TRASH } from "./icons.js";
import { deleteClosedChat } from "./chat-delete.js";
import { formatClock, formatShortDate, formatWeekdayStamp, watchClock } from "./locale-time.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { sideSection } from "./side-section.js";
import { watchSession } from "./store.js";
import {
  focusTabRow,
  getActiveTabId,
  historyPaneTab,
  recentClosed,
  refreshActiveView,
  showHistoryView,
  statusDotEl,
  tabIdFor,
} from "./tabs.js";
import type { RecentClose } from "./wire/types.gen.js";

let descSeq = 0;

interface Row {
  readonly li: HTMLLIElement;
  readonly button: HTMLButtonElement;
  readonly remove: HTMLButtonElement;
  readonly name: HTMLElement;
  readonly time: HTMLTimeElement;
  readonly closedAt: HTMLElement;
  entry: RecentClose;
  label: string;
  drawn: string;
}

/** The entries the section draws: none while retention is off, since a chat closed then is deleted.
 *  A closed Bot survives that close, so the Bots change narrows this to its entries. */
function drawnEntries(all: readonly RecentClose[], retentionOn: boolean): readonly RecentClose[] {
  return retentionOn ? all : [];
}

/** A row's leading mark: an open chat row's dot as an empty ring. A closed Bot's row takes the
 *  open Bot row's mark the same way once entries carry `bot`. */
function rowMark(_entry: RecentClose): Element {
  return statusDotEl("closed");
}

/** Local midnight starting the day `ms` falls in. */
function dayStart(ms: number): number {
  const d = new Date(ms);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** How a row names its close: the clock for a close on the day `today` starts, else the date (the
 *  year only when it differs), with what a reader hears for it. */
function closedWhen(ms: number, today: number): { readonly short: string; readonly said: string } {
  if (dayStart(ms) === today) {
    const clock = formatClock(ms);
    return { short: clock, said: `Closed at ${clock}` };
  }
  const sameYear = new Date(ms).getFullYear() === new Date(today).getFullYear();
  return {
    short: formatShortDate(ms, { year: !sameYear }),
    said: `Closed ${formatWeekdayStamp(ms)}`,
  };
}

/** Wire `#history-section` once its markup is in the page. */
export function initHistorySection(): void {
  const side = sideSection("history-section", "history-list", {
    key: "history",
    foldedByDefault: true,
    noun: "chat",
    tail: [$.historyMore],
    foldDot: () => null,
    // More stays while its page shows, so the fold keeps the row you are on.
    kept: () => (historyPaneTab() === null ? null : $.historyMore.closest("li")),
  });
  if (side === null) {
    return;
  }
  $.historyMore.addEventListener("click", () => {
    showHistoryView();
  });
  const retentionOn = signal(isRetentionEnabled());
  onRetentionChange(() => {
    retentionOn.value = isRetentionEnabled();
  });
  const rows = new Map<string, Row>();
  const busy = new Map<string, "reopen" | "delete">();
  // A row says its clock only on its close's day, so each midnight redraws the list.
  const dayTurn = signal(0);
  const watchDay = (): number => dayTurn.value;
  let turnAt = 0;
  const armDayTurn = (today: number): void => {
    const next = new Date(today);
    next.setDate(next.getDate() + 1);
    if (next.getTime() === turnAt) {
      return;
    }
    turnAt = next.getTime();
    // An early fire re-arms for the same midnight, since turnAt is cleared first.
    setTimeout(
      () => {
        turnAt = 0;
        dayTurn.value++;
      },
      Math.max(0, turnAt - Date.now()),
    );
  };
  /** The press that asked for each delete in flight: its row's focus moves take that press's ring. */
  const deletePress = new Map<string, Press | null>();

  const reopen = async (row: Row): Promise<void> => {
    const ref = row.entry.ref;
    if (busy.has(ref)) {
      return;
    }
    busy.set(ref, "reopen");
    row.button.setAttribute("aria-busy", "true");
    const mark = side.noteFocus();
    try {
      const outcome = await reopenChat(ref, row.entry.name);
      const tabID = tabIdFor("chat", ref);
      const focus = document.activeElement;
      const ours = focus === null || focus === document.body || focus === row.button;
      if (outcome !== "opened" || !ours || tabID === "" || getActiveTabId() !== tabID) {
        return;
      }
      // The phone's open dismissed the drawer, so the row is out of sight there.
      if (matchMedia("(width <= 48rem)").matches || !focusTabRow(tabID)) {
        $.promptInput.focus();
      }
    } finally {
      busy.delete(ref);
      row.button.removeAttribute("aria-busy");
      side.restoreFocus(mark);
    }
  };

  /** Delete for good through the History page's own door; the server's frame then drops the row. */
  const remove = async (row: Row): Promise<void> => {
    const ref = row.entry.ref;
    if (busy.has(ref)) {
      return;
    }
    busy.set(ref, "delete");
    row.remove.setAttribute("aria-busy", "true");
    // A click that moves no focus (confirm.ts owns the WebKit case) can leave it outside the section: the row stands for it.
    const mark = side.noteFocus() ?? side.markRow(ref);
    deletePress.set(ref, lastPress());
    try {
      if (await deleteClosedChat(ref, row.label)) {
        // History refreshes when it shows, so the chat leaves it too.
        refreshActiveView();
      }
    } finally {
      busy.delete(ref);
      row.remove.removeAttribute("aria-busy");
      side.restoreFocus(mark, deletePress.get(ref));
      deletePress.delete(ref);
    }
  };

  function openMenu(row: Row, x: number, y: number): void {
    showContextMenu(
      [
        {
          label: "Reopen",
          action: () => {
            void reopen(row);
          },
        },
        {
          label: "Delete",
          action: () => {
            void remove(row);
          },
        },
      ],
      { x, y },
      { returnFocus: row.button },
    );
  }

  function bindRow(row: Row): void {
    row.button.addEventListener("click", () => {
      void reopen(row);
    });
    bindContextPress(row.button, (x, y) => {
      openMenu(row, x, y);
    });
    row.remove.addEventListener("click", () => {
      void remove(row);
    });
  }

  function makeRow(entry: RecentClose): Row {
    const name = el("bdi", { className: "history-row-name" });
    const time = el("time", { className: "history-row-time" }) as HTMLTimeElement;
    const button = el(
      "button",
      { className: "history-row", type: "button" },
      rowMark(entry),
      name,
      time,
    ) as HTMLButtonElement;
    descSeq++;
    const closedAt = el("span", {
      className: "sr-only",
      id: `history-row-desc-${String(descSeq)}`,
    });
    button.setAttribute("aria-describedby", closedAt.id);
    // A closed Bot's row takes this same Delete.
    const trash = iconEl(ICON_TRASH);
    trash.setAttribute("aria-hidden", "true");
    const remove = el(
      "button",
      { className: "history-row-delete", type: "button" },
      trash,
    ) as HTMLButtonElement;
    const li = el("li", { className: "history-item" }, button, remove, closedAt) as HTMLLIElement;
    const row: Row = { li, button, remove, name, time, closedAt, entry, label: "", drawn: "" };
    bindRow(row);
    return row;
  }

  function paintRow(row: Row, entry: RecentClose, today: number): void {
    row.entry = entry;
    // A rename after the close changes the record, not the entry.
    const named = watchSession(entry.ref)?.name ?? entry.name;
    const label = named === "" ? "Untitled chat" : named;
    const when = closedWhen(entry.closed_at, today);
    const drawn = `${label}\n${when.short}\n${when.said}`;
    if (row.drawn === drawn && row.time.dateTime !== "") {
      return;
    }
    row.drawn = drawn;
    row.label = label;
    setText(row.name, label);
    setText(row.time, when.short);
    row.time.dateTime = new Date(entry.closed_at).toISOString();
    row.button.setAttribute("aria-label", `Reopen ${label}`);
    setText(row.closedAt, when.said);
    row.button.dataset["tooltip"] = `Reopen ${label}\n${when.said}`;
    row.remove.setAttribute("aria-label", `Delete ${label}`);
    row.remove.dataset["tooltip"] = `Delete ${label}`;
  }

  effect(() => {
    // A Clock change redraws each row's time through paintRow's drawn key.
    watchClock();
    watchDay();
    const today = dayStart(Date.now());
    armDayTurn(today);
    const focused = document.activeElement;
    // A reopen in flight moves focus off its row itself when it settles.
    const reopening = [...busy].some(
      ([ref, job]) => job === "reopen" && rows.get(ref)?.li.contains(focused) === true,
    );
    const mark = reopening ? null : side.noteFocus();
    const entries = drawnEntries(recentClosed(), retentionOn.value);
    const keep = new Set(entries.map((e) => e.ref));
    for (const ref of rows.keys()) {
      if (!keep.has(ref)) {
        rows.delete(ref);
      }
    }
    const seated = entries.map((entry) => {
      let row = rows.get(entry.ref);
      if (row === undefined) {
        row = makeRow(entry);
        rows.set(entry.ref, row);
      }
      paintRow(row, entry, today);
      return { ref: entry.ref, li: row.li, controls: [row.button, row.remove] };
    });
    side.seat(seated);
    side.restoreFocus(mark, mark !== null && "ref" in mark ? deletePress.get(mark.ref) : undefined);
  });
}
