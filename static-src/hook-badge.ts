// The hooks badge on a tool group's header, or on a lone tool card's, and the popover it opens listing each hook it
// counts once with its run count. hook-fold.ts decides which hooks fold and tool-group.ts which header carries them; this
// draws them.

import { el } from "@cplieger/ui";
import { join } from "@cplieger/keyenc";
import { createPopover, type PopoverController } from "@cplieger/ui/popover";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { fileIcon, toolIcon } from "./icons.js";
import { kindNoun } from "./tool-kind-noun.js";

/** Set on the group or card that carries a hooks badge, which `14-tools.css` lays over its header's top-right corner. */
const CLS_HAS_HOOKS = "tool-group-has-hooks";

/** The badge count's digit count, from which `14-tools.css` sizes the badge and the header's reserved padding. */
const DIGITS_VAR = "--hooks-digits";

/** The open list's widest fit, which `14-tools.css` caps the panel at. */
const ROOM_VAR = "--hook-pop-room";

/** The server titles a hook card "Hook fired: <name>" (internal/translate hookToolCall). */
const HOOK_TITLE_PREFIX = "Hook fired: ";

/** A trigger in plain words, keyed by the canonical names of internal/marotte/hook_triggers.go. */
const TRIGGER_LABELS: ReadonlyMap<string, string> = new Map([
  ["SessionStart", "at agent spawn (session start)"],
  ["SessionEnd", "at session end"],
  ["UserPromptSubmit", "at turn start (prompt sent)"],
  ["Stop", "at turn end (agent stops)"],
  ["PreToolUse", "before a tool runs"],
  ["PostToolUse", "after a tool runs"],
  ["PreTaskExec", "before a spec task"],
  ["PostTaskExec", "after a spec task"],
  ["PreCompact", "before the context is compacted"],
  ["PostCompact", "after the context is compacted"],
  ["PostFileCreate", "after a file is created"],
  ["PostFileSave", "after a file is saved"],
  ["PostFileDelete", "after a file is deleted"],
  ["Manual", "run by hand"],
]);

/** A hook's trigger as the popover words it: a name outside the table shows as it came, and an unnamed one says so. */
export function hookTriggerLabel(trigger: string): string {
  return trigger === "" ? "trigger unknown" : (TRIGGER_LABELS.get(trigger) ?? trigger);
}

/** One hook as the popover lists it: all its folded runs, which share a name, a hook file and a trigger. */
interface HookEntry {
  readonly key: string;
  readonly name: string;
  readonly trigger: string;
  readonly path: string;
  readonly filename: string;
  /** The first run's card, which owns what the hook file opens. */
  readonly card: HTMLElement;
  runs: number;
}

/** The list row a folded hook card is counted in: its name, hook file and trigger. */
function hookKey(card: HTMLElement): string {
  return join(
    card.dataset["title"] ?? "",
    card.dataset["filePath"] ?? "",
    card.dataset["hookTrigger"] ?? "",
  );
}

/** The folded hooks grouped by name, hook file and trigger, in order of first appearance. */
function hookEntries(folded: readonly HTMLElement[]): HookEntry[] {
  const entries = new Map<string, HookEntry>();
  for (const card of folded) {
    const title = card.dataset["title"] ?? "";
    const path = card.dataset["filePath"] ?? "";
    const trigger = card.dataset["hookTrigger"] ?? "";
    const key = hookKey(card);
    const seen = entries.get(key);
    if (seen !== undefined) {
      seen.runs++;
      continue;
    }
    entries.set(key, {
      key,
      name: title.startsWith(HOOK_TITLE_PREFIX) ? title.slice(HOOK_TITLE_PREFIX.length) : title,
      trigger,
      path,
      filename: card.dataset["filename"] ?? "",
      card,
      runs: 1,
    });
  }
  return [...entries.values()];
}

/** What a hooks badge opens: the hooks it counts and its popover, which lives as long as the badge. */
interface HookPopover {
  folded: readonly HTMLElement[];
  readonly panel: HTMLElement;
  readonly ctl: PopoverController;
  /** Watches the open list's group: a resize refits the list, and a group leaving the page closes it. */
  readonly watch: ResizeObserver;
}

const popovers = new WeakMap<HTMLElement, HookPopover>();

/** Each group's badge, wherever in the group it sits. */
const badges = new WeakMap<HTMLElement, HTMLElement>();

/** The badge counting each folded hook card, which may sit in another group than the card. */
const foldedInto = new WeakMap<HTMLElement, HTMLElement>();

/** Paint the group's hooks badge listing `folded` on `host`, the group or its lone card, absent with none. The
 *  badge is the one place the hook count lives and counts runs; it is a sibling of the host's header, since a button
 *  inside the group header's `role="button"` is not reachable by assistive technology. */
export function syncHookBadge(
  group: HTMLElement,
  folded: readonly HTMLElement[],
  host: HTMLElement,
): void {
  let badge = badges.get(group) ?? null;
  const was = badge?.parentElement ?? null;
  if (folded.length === 0) {
    if (badge !== null) {
      const state = popovers.get(badge);
      state?.watch.disconnect();
      state?.ctl.dispose();
      state?.panel.remove();
      badge.remove();
      badges.delete(group);
    }
    if (was !== null) {
      unmarkHost(was);
    }
    return;
  }
  badge ??= mountBadge(group);
  if (was !== host) {
    if (was !== null) {
      unmarkHost(was);
    }
    seatBadge(badge, host);
  }
  host.classList.add(CLS_HAS_HOOKS);
  const state = popovers.get(badge);
  if (state === undefined) {
    return;
  }
  state.folded = folded;
  for (const card of folded) {
    foldedInto.set(card, badge);
  }
  const count = String(folded.length);
  host.style.setProperty(DIGITS_VAR, String(count.length));
  const visible = badge.querySelector(".tool-group-hooks-n");
  if (visible !== null && visible.textContent !== count) {
    visible.textContent = count;
  }
  badge.setAttribute("aria-label", `${count} ${kindNoun("hook", folded.length)} ran`);
  if (state.ctl.isOpen) {
    renderHookList(state);
  }
}

function unmarkHost(host: HTMLElement): void {
  host.classList.remove(CLS_HAS_HOOKS);
  host.style.removeProperty(DIGITS_VAR);
}

/** After the host's header: the group's, or a card's summary row. Every host has one, so none is a broken caller. */
function seatBadge(badge: HTMLElement, host: HTMLElement): void {
  const header = host.querySelector(":scope > .tool-group-header, :scope > .tool-summary");
  if (header === null) {
    throw new Error("hooks badge host has no header");
  }
  header.after(badge);
}

function mountBadge(group: HTMLElement): HTMLElement {
  const badge = el(
    "button",
    {
      type: "button",
      className: "tool-group-hooks",
      "aria-haspopup": "dialog",
      "aria-expanded": "false",
      [CHROME_ATTR]: "",
    },
    el(
      "span",
      { className: "tool-group-hooks-icon", "aria-hidden": "true" },
      iconEl(toolIcon({ kind: "hook", toolName: "" })),
    ),
    el("span", { className: "tool-group-hooks-n", "aria-hidden": "true" }),
  );
  const panel = el("div", {
    className: "tool-hook-popover",
    role: "dialog",
    "aria-label": "Hooks that ran",
    tabindex: "-1",
  });
  const watch = new ResizeObserver(() => {
    if (!badge.isConnected) {
      ctl.hide();
      return;
    }
    fitList(panel, badge);
    ctl.reposition();
  });
  const ctl = createPopover(badge, panel, {
    placement: "bottom",
    align: "end",
    haspopup: "dialog",
    initialFocus: panel,
    returnFocus: badge,
    onOpen: () => {
      const g = badge.closest(".tool-group");
      if (g !== null) {
        watch.observe(g);
      }
    },
    onClose: () => {
      watch.disconnect();
      panel.remove();
    },
  });
  const state: HookPopover = { folded: [], panel, ctl, watch };
  popovers.set(badge, state);
  badge.addEventListener("click", () => {
    toggleHookPopover(state, badge);
  });
  badges.set(group, badge);
  return badge;
}

function toggleHookPopover(state: HookPopover, badge: HTMLElement): void {
  if (state.ctl.isOpen) {
    state.ctl.hide();
    return;
  }
  // A reader's open moves focus into the list and back; set on every show, since find's open sets none.
  state.ctl.setOptions({ initialFocus: state.panel, returnFocus: badge });
  showList(state, badge);
}

function showList(state: HookPopover, badge: HTMLElement): void {
  delete state.panel.dataset["keys"];
  renderHookList(state);
  fitList(state.panel, badge);
  state.ctl.show();
}

/** Cap the list at the room between its group's start edge and the badge's end edge, the span an end-aligned list
 *  can take without leaving the turn card. Measured, since the panel is fixed to the viewport. */
function fitList(panel: HTMLElement, badge: HTMLElement): void {
  const group = badge.closest(".tool-group");
  if (group === null) {
    return;
  }
  const room = badge.getBoundingClientRect().right - group.getBoundingClientRect().left;
  panel.style.setProperty(ROOM_VAR, `${String(room)}px`);
}

/** Open the badge's list without moving focus, for find-in-chat landing on a folded hook while the reader types.
 *  False when `badge` is no hooks badge or the list is already open. */
export function showHookPopover(badge: HTMLElement): boolean {
  const state = popovers.get(badge);
  if (state === undefined || state.ctl.isOpen) {
    return false;
  }
  state.ctl.setOptions({ initialFocus: null, returnFocus: false });
  showList(state, badge);
  return true;
}

/** The open list's row counting the folded hook `card`, null while the list is closed. Every show rebuilds the
 *  list, so a mark find-in-chat puts in a row never outlives the list's close. */
export function hookRowOf(badge: HTMLElement, card: HTMLElement): HTMLElement | null {
  const state = popovers.get(badge);
  if (state?.ctl.isOpen !== true) {
    return null;
  }
  const key = hookKey(card);
  return (
    [...state.panel.querySelectorAll<HTMLElement>(".tool-hook-item")].find(
      (row) => row.dataset["hookKey"] === key,
    ) ?? null
  );
}

/** Close the badge's list; a list `showHookPopover` opened moved no focus, so none moves back. */
export function hideHookPopover(badge: HTMLElement): void {
  const state = popovers.get(badge);
  if (state?.ctl.isOpen === true) {
    state.ctl.hide();
  }
}

/** Rebuilt only when the entries change, so a focused chip keeps its focus. */
function renderHookList(state: HookPopover): void {
  const entries = hookEntries(state.folded);
  const keys = entries.map((e) => join(e.key, String(e.runs))).join("\n");
  if (state.panel.dataset["keys"] === keys) {
    return;
  }
  state.panel.dataset["keys"] = keys;
  state.panel.replaceChildren(
    el(
      "ul",
      { className: "tool-hook-list" },
      ...entries.map((e) =>
        hookItem(e, () => {
          state.ctl.hide();
        }),
      ),
    ),
  );
  if (state.ctl.isOpen) {
    state.ctl.reposition();
  }
}

function hookItem(entry: HookEntry, close: () => void): HTMLElement {
  const item = el(
    "li",
    { className: "tool-hook-item", "data-hook-key": entry.key },
    el("span", { className: "tool-hook-count" }, `${String(entry.runs)}\u00d7`),
    el(
      "span",
      { className: "tool-hook-text" },
      el("span", { className: "tool-title", "data-tooltip": entry.name }, entry.name),
      el("span", { className: "tool-hook-trigger" }, hookTriggerLabel(entry.trigger)),
    ),
  );
  if (entry.path !== "") {
    const chip = el(
      "button",
      {
        type: "button",
        className: "tool-file-link",
        "data-path": entry.path,
        "data-tooltip": entry.path,
        "aria-label": `Open the hook file ${entry.filename}`,
      },
      el("span", { className: "tool-file-icon" }, iconEl(fileIcon(entry.filename, false))),
      el("span", { className: "tool-file-name" }, entry.filename),
    );
    chip.addEventListener("click", (e: Event) => {
      e.stopPropagation();
      entry.card.querySelector<HTMLElement>(".tool-file-link")?.click();
      close();
    });
    item.appendChild(chip);
  }
  return item;
}

/** The hooks badge standing for a folded hook card, where find-in-chat lands a hit on that card. */
export function foldedHookBadge(card: HTMLElement): HTMLElement | null {
  if (!card.hasAttribute("data-hook-folded")) {
    return null;
  }
  const badge = foldedInto.get(card);
  return badge?.isConnected === true ? badge : null;
}
