// Mode / role picker: an expandable pill in the prompt bar that shows the active chat's current
// mode and lets the user switch to any other — a bundled workflow mode (Default / Spec / Quick Spec
// / Bug Fix / Plan / Autonomous) or a workspace custom agent from .kiro/agents/.

import { $, byId } from "./dom.js";
import { el, effect } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { rovingFocus, type RovingFocusController } from "@cplieger/ui-primitives/roving-focus";
import { activeSession, getActive } from "./store.js";
import { createSession } from "./chat.js";
import { setMode } from "./actions/chat.js";
import { apiGet } from "./api-client.js";
import {
  type PickerMode,
  catalogBaseModes,
  displayModeName,
  iconForMode,
  isCustomSource,
  labelForMode,
  mergeCatalogAndWorkspace,
  normalizeModeID,
  scopeLabel,
} from "./roles.js";

// Cache of workspace custom-agent names; refreshed on each expand so the list renders instantly
// from cache, then folds in any changes.
let customAgents: string[] = [];

// Roving-focus controller over the mode options — wired once in initRolePicker; renderOptions
// refreshes it after each re-render.
let roleNav: RovingFocusController | null = null;

/** Wire the prompt-bar mode pill. Call once at startup. */
export function initRolePicker(): void {
  const pill = $.rolePill;
  const list = $.roleList;
  makeExpandable(pill, list, {
    onExpand: () => {
      renderList(list);
    },
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

/** The modes to offer: the workspace catalog merged with the workspace agents from kiro-config. */
function currentModes(): PickerMode[] {
  return mergeCatalogAndWorkspace(catalogBaseModes(), customAgents);
}

function renderList(list: HTMLElement): void {
  renderOptions(list);
  // Refresh the workspace-agent seed in the background; only matters before a live session exists
  // (once it does, availableModes already includes them).
  void apiGet<{ items: { name: string; type: string }[] }>("/api/workspace/kiro-config").then(
    (data) => {
      const next = (data?.items ?? []).filter((i) => i.type === "agent").map((i) => i.name);
      if (next.join("\u0000") !== customAgents.join("\u0000")) {
        customAgents = next;
        if (list.classList.contains("is-open")) {
          renderOptions(list);
        }
      }
    },
  );
}

function renderOptions(list: HTMLElement): void {
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Chat mode");
  const currentMode = normalizeModeID(getActive()?.current_mode_id ?? "");
  const modes = currentModes();
  // Group is "bundled" vs everything else. The server reports source as bundled | global |
  // workspace; both global and workspace custom agents belong under the "Custom agents" divider, so
  // only "bundled" (and any unset source, which the built-in catalog uses) stays in the top group.
  const isCustomAgent = (p: PickerMode): boolean => isCustomSource(p.mode.source);
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
}

function modeOption(entry: PickerMode, currentMode: string): HTMLButtonElement {
  const { mode } = entry;
  const isCurrent = mode.id === currentMode;
  const children: HTMLElement[] = [
    el("span", { className: "pill-role-item-icon" }, iconEl(iconForMode(mode.id))),
    el("span", { className: "pill-role-name" }, displayModeName(mode.name || mode.id)),
  ];
  // The shadowed entry, marked. There is one row per id, so without this the collision is
  // invisible: the user sees a "workspace" agent and cannot tell that a same-named global
  // definition exists and is NOT the one a run loads.
  let tooltip = mode.description ?? "";
  if (entry.shadowed !== undefined) {
    children.push(el("span", { className: "pill-role-shadow" }, "shadows " + entry.shadowed));
    const note = `This workspace agent shadows the ${entry.shadowed} agent of the same name. The workspace definition is the one a run uses.`;
    tooltip = tooltip === "" ? note : tooltip + " " + note;
  }
  // Scope on the row. It was already on the wire and already read (the grouping above keys on it)
  // and simply was not shown, so a user looking at two custom agents could not tell which tree each
  // came from.
  const scope = scopeLabel(mode.source);
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
