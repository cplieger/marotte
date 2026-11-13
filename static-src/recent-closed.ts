// The sidebar's Recently closed section: the server's list of chats whose last tab closed lately,
// drawn as sent (`recentClosed()`), each row reopening its chat, and the History row's label under it.

import { effect, el, signal } from "@cplieger/reactive";
import { reopenChat } from "./chat.js";
import { showContextMenu } from "./context-menu.js";
import { sectionFolded, setSectionFolded } from "./device-view.js";
import { isSliding, slideBox } from "./disclosure-row.js";
import { $, maybeEl, setText } from "./dom.js";
import { iconEl } from "./icon-el.js";
import { ICON_TAB_CHAT } from "./icons.js";
import { formatClock } from "./locale-time.js";
import { isRetentionEnabled, onRetentionChange } from "./retention.js";
import { watchSession } from "./store.js";
import { dragOwnsStrip } from "./tabs-drag.js";
import { focusTabRow, getActiveTabId, recentClosed, tabIdFor } from "./tabs.js";
import type { RecentClose } from "./wire/types.gen.js";

const FOLD_KEY = "recent";
const HOLD_MS = 500;
/** How long a touch lift's click stays swallowed, as `tabs-drag.ts` expires its release suppression. */
const LIFT_CLICK_MS = 80;

interface Row {
  readonly li: HTMLLIElement;
  readonly button: HTMLButtonElement;
  readonly name: HTMLElement;
  readonly time: HTMLTimeElement;
  entry: RecentClose;
  drawn: string;
}

/** Wire `#recent-section` once its markup is in the page. */
export function initRecentClosed(): void {
  const section = maybeEl("recent-section");
  const list = maybeEl("recent-list");
  const header = section?.querySelector<HTMLElement>(":scope > h2.side-section-h") ?? null;
  const toggle = header?.querySelector<HTMLButtonElement>(".side-section-toggle") ?? null;
  if (section === null || list === null || header === null || toggle === null) {
    return;
  }
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
      const outcome = await reopenChat(ref, watchSession(ref)?.name ?? row.entry.name);
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

  function openMenu(row: Row, x: number, y: number): void {
    showContextMenu(
      [
        {
          label: "Reopen",
          action: () => {
            void reopen(row);
          },
        },
      ],
      { x, y },
      { returnFocus: row.button },
    );
  }

  function bindRow(row: Row): void {
    const { button } = row;
    // One press opens one menu, whichever of the hold timer and the platform's `contextmenu` comes first.
    let press: { id: number; timer: number | null; opened: boolean } | null = null;
    const endPress = (): void => {
      if (press?.timer != null) {
        clearTimeout(press.timer);
      }
      press = null;
      window.removeEventListener("pointerup", onLift, true);
      window.removeEventListener("pointercancel", onCancel, true);
    };
    const onCancel = (e: PointerEvent): void => {
      if (e.pointerId === press?.id) {
        endPress();
      }
    };
    const onLift = (e: PointerEvent): void => {
      if (e.pointerId !== press?.id) {
        return;
      }
      const opened = press.opened;
      endPress();
      if (opened) {
        swallowLiftClick();
      }
    };
    const menuFromPress = (): boolean => {
      if (press === null) {
        return true;
      }
      if (press.opened) {
        return false;
      }
      press.opened = true;
      if (press.timer !== null) {
        clearTimeout(press.timer);
        press.timer = null;
      }
      return true;
    };

    button.addEventListener("click", () => {
      void reopen(row);
    });
    button.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      if (!menuFromPress()) {
        return;
      }
      // A keyboard-opened menu carries no point of its own.
      const box = button.getBoundingClientRect();
      const keyed = e.clientX === 0 && e.clientY === 0;
      openMenu(row, keyed ? box.left : e.clientX, keyed ? box.bottom : e.clientY);
    });
    button.addEventListener("pointerdown", (e) => {
      if (e.pointerType !== "touch") {
        return;
      }
      endPress();
      const p = { id: e.pointerId, timer: null as number | null, opened: false };
      p.timer = window.setTimeout(() => {
        p.timer = null;
        if (press === p && menuFromPress()) {
          const box = button.getBoundingClientRect();
          openMenu(row, e.clientX || box.left, e.clientY || box.bottom);
        }
      }, HOLD_MS);
      press = p;
      window.addEventListener("pointerup", onLift, true);
      window.addEventListener("pointercancel", onCancel, true);
    });
  }

  function makeRow(entry: RecentClose): Row {
    const name = el("bdi", { className: "recent-row-name" });
    const time = el("time", { className: "recent-row-time" }) as HTMLTimeElement;
    const glyph = iconEl(ICON_TAB_CHAT);
    glyph.setAttribute("aria-hidden", "true");
    const button = el(
      "button",
      { className: "recent-row", type: "button" },
      glyph,
      name,
      time,
    ) as HTMLButtonElement;
    const li = el("li", {}, button) as HTMLLIElement;
    const row: Row = { li, button, name, time, entry, drawn: "" };
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
    setText(row.name, label);
    setText(row.time, clock);
    row.time.dateTime = new Date(entry.closed_at).toISOString();
    row.button.setAttribute("aria-label", `Reopen ${label}`);
    row.button.setAttribute("aria-description", `Closed at ${clock}`);
    row.button.dataset["tooltip"] = `Reopen ${label}\nClosed at ${clock}`;
  }

  effect(() => {
    const entries = retentionOn.value ? recentClosed() : [];
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
      // Seated only when out of place, since a re-seat blurs a focused row.
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
    // The label follows the section's visibility, set in the same step.
    section.hidden = count === 0;
    setText(
      $.historyBtn.querySelector(".side-history-label"),
      count === 0 ? "History" : "More history",
    );
    setText(toggle.querySelector(".side-section-count"), String(count));
    setText(toggle.querySelector(".sr-only"), `${String(count)} ${count === 1 ? "chat" : "chats"}`);
    const folded = sectionFolded(FOLD_KEY, true);
    paintFold(folded);
    if (!isSliding(section)) {
      list.hidden = folded;
    }
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

/** Swallow the click a held touch sends when it lifts, so neither the row nor the menu's outside dismissal sees it. */
function swallowLiftClick(): void {
  const swallow = (e: MouseEvent): void => {
    e.stopPropagation();
    e.preventDefault();
    done();
  };
  const timer = window.setTimeout(() => {
    done();
  }, LIFT_CLICK_MS);
  function done(): void {
    clearTimeout(timer);
    window.removeEventListener("click", swallow, true);
  }
  window.addEventListener("click", swallow, true);
}
