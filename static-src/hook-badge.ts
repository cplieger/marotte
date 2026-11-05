// The hooks badge on a tool group's header, and the popover it opens listing each folded hook once with its run
// count. hook-fold.ts decides which hooks fold; this draws them.

import { el } from "@cplieger/ui";
import { join } from "@cplieger/keyenc";
import { createPopover, type PopoverController } from "@cplieger/ui/popover";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { fileIcon, toolIcon } from "./icons.js";
import { kindNoun } from "./tool-kind-noun.js";

/** Set on a group that carries a hooks badge, which `14-tools.css` gives a column beside the header. */
const CLS_HAS_HOOKS = "tool-group-has-hooks";

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

/** The folded hooks grouped by name, hook file and trigger, in order of first appearance. */
function hookEntries(folded: readonly HTMLElement[]): HookEntry[] {
  const entries = new Map<string, HookEntry>();
  for (const card of folded) {
    const title = card.dataset["title"] ?? "";
    const path = card.dataset["filePath"] ?? "";
    const trigger = card.dataset["hookTrigger"] ?? "";
    const key = join(title, path, trigger);
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

/** What a hooks badge opens: the hooks it counts, and the popover while one is showing. */
interface HookPopover {
  folded: readonly HTMLElement[];
  panel: HTMLElement | null;
  ctl: PopoverController | null;
}

const popovers = new WeakMap<HTMLElement, HookPopover>();

/** Paint the group's hooks badge for its folded hooks, absent with none. The badge is the one place the hook count
 *  lives and counts runs; it is a sibling of the header, since a button inside the header's `role="button"` is not
 *  reachable by assistive technology. */
export function syncHookBadge(group: HTMLElement, folded: readonly HTMLElement[]): void {
  let badge = group.querySelector<HTMLElement>(":scope > .tool-group-hooks");
  if (folded.length === 0) {
    if (badge !== null) {
      popovers.get(badge)?.ctl?.hide();
      badge.remove();
    }
    group.classList.remove(CLS_HAS_HOOKS);
    return;
  }
  badge ??= mountBadge(group);
  group.classList.add(CLS_HAS_HOOKS);
  const state = popovers.get(badge);
  if (state === undefined) {
    return;
  }
  state.folded = folded;
  const count = String(folded.length);
  const visible = badge.querySelector(".tool-group-hooks-n");
  if (visible !== null && visible.textContent !== count) {
    visible.textContent = count;
  }
  badge.setAttribute("aria-label", `${count} ${kindNoun("hook", folded.length)} ran`);
  if (state.panel !== null) {
    renderHookList(state);
  }
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
  const state: HookPopover = { folded: [], panel: null, ctl: null };
  popovers.set(badge, state);
  badge.addEventListener("click", () => {
    toggleHookPopover(badge, state);
  });
  const header = group.querySelector(":scope > .tool-group-header");
  if (header === null) {
    group.appendChild(badge);
  } else {
    header.after(badge);
  }
  return badge;
}

function toggleHookPopover(badge: HTMLElement, state: HookPopover): void {
  if (state.ctl?.isOpen === true) {
    state.ctl.hide();
    return;
  }
  const panel = el("div", {
    className: "tool-hook-popover",
    role: "dialog",
    "aria-label": "Hooks that ran",
    tabindex: "-1",
  });
  state.panel = panel;
  renderHookList(state);
  state.ctl = createPopover(badge, panel, {
    placement: "bottom",
    align: "end",
    haspopup: "dialog",
    initialFocus: panel,
    returnFocus: badge,
    onClose: () => {
      const ctl = state.ctl;
      panel.remove();
      if (state.panel === panel) {
        state.panel = null;
        state.ctl = null;
      }
      ctl?.dispose();
    },
  });
  state.ctl.show();
}

/** Rebuilt only when the entries change, so a focused chip keeps its focus. */
function renderHookList(state: HookPopover): void {
  const panel = state.panel;
  if (panel === null) {
    return;
  }
  const entries = hookEntries(state.folded);
  const keys = entries.map((e) => join(e.key, String(e.runs))).join("\n");
  if (panel.dataset["keys"] === keys) {
    return;
  }
  panel.dataset["keys"] = keys;
  panel.replaceChildren(
    el(
      "ul",
      { className: "tool-hook-list" },
      ...entries.map((e) => hookItem(e, () => state.ctl?.hide())),
    ),
  );
  state.ctl?.reposition();
}

function hookItem(entry: HookEntry, close: () => void): HTMLElement {
  const item = el(
    "li",
    { className: "tool-hook-item" },
    el("span", { className: "tool-hook-count" }, `${String(entry.runs)}\u00d7`),
    el(
      "span",
      { className: "tool-hook-text" },
      el("span", { className: "tool-title" }, entry.name),
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
  return (
    card.closest(".tool-group")?.querySelector<HTMLElement>(":scope > .tool-group-hooks") ?? null
  );
}
