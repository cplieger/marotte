// The sidebar's History section: the server's list of chats whose last tab closed lately, drawn as
// sent (`recentClosed()`), each row reopening its chat, and More, its last row, opening History.

import { effect, el, signal } from "@cplieger/reactive";
import { reopenChat } from "./chat.js";
import { showContextMenu } from "./context-menu.js";
import { sectionFolded, setSectionFolded } from "./device-view.js";
import { isSliding, slideBox } from "./disclosure-row.js";
import { $, maybeEl, queryEl, setText } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { bindContextPress } from "./context-hold.js";
import { ICON_TRASH } from "./icons.js";
import { deleteClosedChat } from "./chat-delete.js";
import { formatClock, watchClock } from "./locale-time.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { watchSession } from "./store.js";
import { dragOwnsStrip } from "./tabs-drag.js";
import {
  focusHistoryEntry,
  focusTabRow,
  getActiveTabId,
  recentClosed,
  refreshActiveView,
  showHistoryView,
  statusDotEl,
  tabIdFor,
} from "./tabs.js";
import type { RecentClose } from "./wire/types.gen.js";

const FOLD_KEY = "history";

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

/** Wire `#history-section` once its markup is in the page. */
export function initHistorySection(): void {
  const section = maybeEl("history-section");
  const list = maybeEl("history-list");
  const header = section?.querySelector<HTMLElement>(":scope > h2.side-section-h") ?? null;
  const toggle = header?.querySelector<HTMLButtonElement>(".side-section-toggle") ?? null;
  if (section === null || list === null || header === null || toggle === null) {
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
  const busy = new Set<string>();

  const paintFold = (folded: boolean): void => {
    toggle.setAttribute("aria-expanded", String(!folded));
  };

  async function reopen(row: Row): Promise<void> {
    const ref = row.entry.ref;
    if (busy.has(ref)) {
      return;
    }
    busy.add(ref);
    row.button.setAttribute("aria-busy", "true");
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
    }
  }

  /** Delete for good through the History page's own door; the server's frame then drops the row. */
  async function remove(row: Row): Promise<void> {
    const ref = row.entry.ref;
    if (busy.has(ref)) {
      return;
    }
    busy.add(ref);
    row.remove.setAttribute("aria-busy", "true");
    const land = focusLanding(row.remove);
    try {
      if (!(await deleteClosedChat(ref, row.label))) {
        return;
      }
      // History refreshes when it shows, so the chat leaves it too.
      refreshActiveView();
      // The confirm answers before its dialog has faded, and the page is inert until it closes.
      const modal = queryEl<HTMLDialogElement>("dialog:modal");
      if (modal === null) {
        land();
      } else {
        modal.addEventListener("close", land, { once: true });
      }
    } finally {
      busy.delete(ref);
      row.remove.removeAttribute("aria-busy");
    }
  }

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

  function paintRow(row: Row, entry: RecentClose): void {
    row.entry = entry;
    // A rename after the close changes the record, not the entry.
    const named = watchSession(entry.ref)?.name ?? entry.name;
    const label = named === "" ? "Untitled chat" : named;
    const clock = formatClock(entry.closed_at);
    const drawn = `${label}\n${clock}`;
    if (row.drawn === drawn && row.time.dateTime !== "") {
      return;
    }
    row.drawn = drawn;
    row.label = label;
    setText(row.name, label);
    setText(row.time, clock);
    row.time.dateTime = new Date(entry.closed_at).toISOString();
    row.button.setAttribute("aria-label", `Reopen ${label}`);
    setText(row.closedAt, `Closed at ${clock}`);
    row.button.dataset["tooltip"] = `Reopen ${label}\nClosed at ${clock}`;
    row.remove.setAttribute("aria-label", `Delete ${label}`);
    row.remove.dataset["tooltip"] = `Delete ${label}`;
  }

  /** Where focus goes once `from` is taken or hidden: the nearest row after it, else before it,
   *  else More, the header, History's entry, the composer or the active tab's row, so it never
   *  falls to `<body>`. Read before the change, since the rows around `from` go with it. */
  function focusLanding(from: Element): () => void {
    const li = from.closest("#history-list > li");
    const near: Element[] = [];
    for (let n = li?.nextElementSibling; n != null; n = n.nextElementSibling) {
      near.push(n);
    }
    for (let n = li?.previousElementSibling; n != null; n = n.previousElementSibling) {
      near.push(n);
    }
    return () => {
      const now = document.activeElement;
      const moved = now !== null && now !== document.body && now !== from;
      if (moved || (from.isConnected && from.getClientRects().length > 0)) {
        return;
      }
      const to = [
        ...near.map((n) => n.querySelector<HTMLElement>(":scope > button.history-row")),
        $.historyMore,
        toggle,
      ].find((b) => b?.isConnected === true && b.getClientRects().length > 0);
      if (to != null) {
        to.focus();
      } else if (!focusHistoryEntry()) {
        $.promptInput.focus();
        if (document.activeElement !== $.promptInput) {
          focusTabRow(getActiveTabId());
        }
      }
    };
  }

  effect(() => {
    // A Clock change redraws each row's time through paintRow's drawn key.
    watchClock();
    const focused = document.activeElement;
    const held = focused !== null && section.contains(focused);
    // A reopen in flight puts focus on its tab's row itself.
    const reopening = [...rows.values()].some(
      (r) => busy.has(r.entry.ref) && r.li.contains(focused),
    );
    const land = held && !reopening ? focusLanding(focused) : null;
    const entries = drawnEntries(recentClosed(), retentionOn.value);
    const keep = new Set(entries.map((e) => e.ref));
    for (const [ref, row] of rows) {
      if (!keep.has(ref)) {
        row.li.remove();
        rows.delete(ref);
      }
    }
    let prev: HTMLLIElement | null = null;
    for (const entry of entries) {
      let row = rows.get(entry.ref);
      if (row === undefined) {
        row = makeRow(entry);
        rows.set(entry.ref, row);
      }
      paintRow(row, entry);
      // Seated only when out of place, since a re-seat blurs a focused row; More stays last.
      const want: Element | null = prev === null ? list.firstElementChild : prev.nextElementSibling;
      if (want !== row.li) {
        if (prev === null) {
          list.prepend(row.li);
        } else {
          prev.after(row.li);
        }
      }
      prev = row.li;
    }
    const count = entries.length;
    section.hidden = count === 0;
    setText(toggle.querySelector(".side-section-count"), String(count));
    setText(toggle.querySelector(".sr-only"), `${String(count)} ${count === 1 ? "chat" : "chats"}`);
    const folded = sectionFolded(FOLD_KEY, true);
    paintFold(folded);
    if (!isSliding(section)) {
      list.hidden = folded;
    }
    land?.();
  });

  toggle.addEventListener("click", () => {
    if (dragOwnsStrip()) {
      return;
    }
    const folded = !sectionFolded(FOLD_KEY, true);
    setSectionFolded(FOLD_KEY, folded);
    paintFold(folded);
    // The header is the section's whole height while its list is hidden, so the close needs no measuring layout.
    const closed = folded ? { closedHeight: header.getBoundingClientRect().height } : {};
    slideBox(
      section,
      !folded,
      (open) => {
        list.hidden = !open;
      },
      closed,
    );
  });
}
