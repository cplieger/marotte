// Mode / role picker: an expandable pill in the prompt bar that shows the active chat's current
// mode and lets the user switch to any other — a bundled workflow mode (Default / Spec / Quick Spec
// / Bug Fix / Plan / Autonomous) or a custom agent kiro-cli loads for this workspace.

import { $, byId } from "./dom.js";
import { el, effect, untracked } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { activeSession, getActive } from "./store.js";
import { createSession } from "./chat.js";
import { setMode } from "./actions/chat.js";
import {
  catalogBaseModes,
  displayModeName,
  iconForMode,
  isCustomSource,
  labelForMode,
  normalizeModeID,
  scopeLabel,
} from "./roles.js";
import { forgetSig, sigChanged, wireSignature } from "./paint-sig.js";
import type { SessionMode } from "./types.js";

// Roving-focus controller over the mode options — wired once in initRolePicker; renderOptions
// refreshes it after each re-render.
let roleNav: RovingFocusController | null = null;

/** Wire the prompt-bar mode pill. Call once at startup. `refreshCatalog` re-reads the workspace
 *  catalog; it runs on each open so a custom agent added on disk reaches the card. */
export function initRolePicker(refreshCatalog: () => void): void {
  const pill = $.rolePill;
  const list = $.roleList;
  let open = false;
  makeExpandable(pill, list, {
    onExpand: () => {
      open = true;
      forgetSig(list);
      renderOptions(list);
      refreshCatalog();
    },
    onCollapse: () => {
      open = false;
    },
  });
  // An open card follows the catalog: one opened before the boot fetch lands, or before the
  // on-open refresh answers, would otherwise keep the rows it opened with until it is reopened.
  effect(() => {
    catalogBaseModes();
    if (open) {
      untracked(() => {
        renderOptions(list);
      });
    }
  });
  // The list announces role=listbox, so it owes AT the composite-widget keyboard contract: one Tab
  // stop + arrow keys, via roving-focus (wired once; renderOptions calls refresh() after each
  // re-render).
  roleNav = rovingFocus(list, ".pill-role-item");
  // Keep the pill's icon + label in sync with the active chat's mode.
  effect(() => {
    const s = activeSession.value;
    refreshPill(s?.current_mode_id ?? "");
  });
}

function refreshPill(modeID: string): void {
  byId<HTMLElement>("role-pill-icon").replaceChildren(iconEl(iconForMode(normalizeModeID(modeID))));
  byId<HTMLSpanElement>("role-pill-label").textContent = labelForMode(modeID);
}

function renderOptions(list: HTMLElement): void {
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Chat mode");
  const currentMode = normalizeModeID(getActive()?.current_mode_id ?? "");
  const modes = catalogBaseModes();
  // A catalog answer that changes nothing must not rebuild an open card: the rebuild takes focus
  // off the row the user is on.
  if (!sigChanged(list, [currentMode, wireSignature(modes)])) {
    return;
  }
  const focusedID =
    document.activeElement instanceof HTMLElement && list.contains(document.activeElement)
      ? document.activeElement.dataset["modeId"]
      : undefined;
  // Group is "bundled" vs everything else. The server reports source as bundled | global |
  // workspace; both global and workspace custom agents belong under the "Custom agents" divider, so
  // only "bundled" (and any unset source, which the built-in catalog uses) stays in the top group.
  const isCustomAgent = (m: SessionMode): boolean => isCustomSource(m.source);
  const bundled = modes.filter((p) => !isCustomAgent(p));
  const workspace = modes.filter(isCustomAgent);

  // No "Switch mode for this chat" heading: the pill's own tooltip says that, and a menu that
  // opened from a control labelled with its purpose does not need to restate it in its first row.
  // The "Custom agents" divider below IS needed — it separates two groups.
  const items: HTMLElement[] = [];
  for (const m of bundled) {
    items.push(modeOption(m, currentMode));
  }
  if (workspace.length > 0) {
    items.push(el("span", { className: "pill-role-hint" }, "Custom agents"));
    for (const m of workspace) {
      items.push(modeOption(m, currentMode));
    }
  }
  list.replaceChildren(...items);
  // Restore the single-Tab-stop invariant over the fresh options.
  roleNav?.refresh();
  if (focusedID !== undefined) {
    list.querySelector<HTMLElement>(`[data-mode-id="${CSS.escape(focusedID)}"]`)?.focus();
  }
}

function modeOption(mode: SessionMode, currentMode: string): HTMLButtonElement {
  const isCurrent = mode.id === currentMode;
  const children: HTMLElement[] = [
    el("span", { className: "pill-role-item-icon" }, iconEl(iconForMode(mode.id))),
    el("span", { className: "pill-role-name" }, displayModeName(mode.name || mode.id)),
  ];
  // The shadowed entry, marked. There is one row per id, so without this the collision is
  // invisible: the user sees a custom agent and cannot tell that a same-named definition from
  // another scope exists and is NOT the one a run loads.
  const scope = scopeLabel(mode.source);
  let tooltip = mode.description ?? "";
  const shadows = mode.shadows ?? "";
  if (shadows !== "") {
    children.push(el("span", { className: "pill-role-shadow" }, "shadows " + shadows));
    const own = scope === "" ? "custom" : scope;
    const note = `This ${own} agent shadows the ${shadows} agent of the same name. The ${own} definition is the one a run uses.`;
    tooltip = tooltip === "" ? note : tooltip + " " + note;
  }
  // Scope on the row, so a user looking at two custom agents can tell which tree each came from.
  if (scope !== "") {
    children.push(el("span", { className: "pill-role-scope" }, scope));
  }
  const opt = el(
    "button",
    {
      type: "button",
      className: isCurrent ? "pill-role-item active" : "pill-role-item",
      role: "option",
      "aria-selected": isCurrent ? "true" : "false",
      "data-tooltip": tooltip,
      "data-mode-id": mode.id,
    },
    ...children,
  ) as HTMLButtonElement;
  opt.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    collapseAll();
    selectMode(mode.id);
  });
  return opt;
}

function selectMode(modeID: string): void {
  const active = getActive();
  if (active !== undefined) {
    dispatchMode(active.id, modeID);
    return;
  }
  // No active chat, so one has to be created first — and the id is the SERVER's now, so this cannot
  // be read back synchronously.
  void createSession().then((id) => {
    if (id === "") {
      return;
    }
    dispatchMode(id, modeID);
  });
}

/** No tab write: a chat tab's leading element is its activity dot, not a mode glyph, so the only
 *  optimistic surface is the pill — which the action's own update flips, confirmed by the server's
 *  mode_changed. */
function dispatchMode(chatID: string, modeID: string): void {
  void setMode.dispatch({ chatID, modeID });
}
