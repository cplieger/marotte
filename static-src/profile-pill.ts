// The composer's security profile pill: names the profile the active chat's session runs and switches it for that
// chat alone. The run tab's composer carries no pills, so a run never offers it.

import { el, effect, signal, untracked } from "@cplieger/reactive";
import { byId } from "./dom.js";
import { apiGetTyped } from "./api-client.js";
import { onSSE } from "./bus.js";
import { iconEl } from "./icon-el.js";
import { ICON_SHIELD } from "./icons.js";
import { makeExpandable, collapseAll } from "./pill-expand.js";
import { rovingFocus, type RovingFocusController } from "./legacy-ui/roving-focus.js";
import { activeSession, getActive, watchBusy } from "./store.js";
import { createSession } from "./chat.js";
import { setChatProfile } from "./actions/chat.js";
import { forgetSig, sigChanged, wireSignature } from "./paint-sig.js";
import {
  CUSTOM_PROFILE,
  LOOSEST_PROFILE_HINT,
  confirmLoosest,
  profileDescription,
  profileLabel,
  savedRulesStillApply,
} from "./security-profiles.js";
import { decodeSecurityProfileView } from "./wire/decoders.gen.js";
import type { SecurityProfileView } from "./wire/types.gen.js";
import type { Session } from "./types.js";

const BUSY_NOTE = "Applies after this turn";
const SAVED_RULES_NOTE = "Your saved rules still apply";

const view = signal<SecurityProfileView | null>(null);
/** A pick made during a turn, until that chat's turn ends or the server refuses it. */
const queued = signal<{ chatID: string; profile: string } | null>(null);
let reading: AbortController | null = null;
let nav: RovingFocusController | null = null;

/** permissions_changed arrives once per live session, so a newer read aborts the one in flight. A failed read keeps
 *  the last answer, and the next card open asks again. */
async function readView(): Promise<void> {
  reading?.abort();
  const ctrl = new AbortController();
  reading = ctrl;
  const answer = await apiGetTyped(
    "/api/permissions/profile",
    decodeSecurityProfileView,
    ctrl.signal,
  );
  if (reading !== ctrl) {
    return;
  }
  reading = null;
  if (answer !== null) {
    view.value = answer;
  }
}

/** A chat with no profile yet is pinned to the Settings profile at its first session, so that is what it runs. */
function profileOf(s: Session | undefined, v: SecurityProfileView | null): string {
  const own = s?.security_profile ?? "";
  return own !== "" ? own : (v?.default ?? "");
}

/** The profiles a chat is offered: Custom only where Settings starts new chats on it or the chat already runs it. */
function listedProfiles(v: SecurityProfileView | null, current: string): string[] {
  return (v?.profiles ?? [])
    .map((p) => p.id)
    .filter((id) => id !== CUSTOM_PROFILE || v?.default === CUSTOM_PROFILE || current === id);
}

/** The pill's choices as a native select, for the Chat options card when the row has no room for the pill. */
export function profileSelect(id: string): HTMLSelectElement {
  const select = el("select", { id, className: "chat-opt-select" }) as HTMLSelectElement;
  let listed = "";
  const current = (): string => profileOf(activeSession.peek(), view.peek());
  effect(() => {
    const s = activeSession.value;
    const v = view.value;
    const q = queued.value;
    const now = profileOf(s, v);
    const ids = listedProfiles(v, now);
    if (wireSignature(ids) !== listed) {
      listed = wireSignature(ids);
      select.replaceChildren(...ids.map((p) => el("option", { value: p }, profileLabel(p))));
    }
    select.value = now;
    select.classList.toggle("pending", q !== null && q.chatID === s?.id);
  });
  select.addEventListener("change", () => {
    // A declined Unrestricted, or a pick with nowhere to go, leaves the chat as it was.
    void choose(select.value).then(() => {
      select.value = current();
    });
  });
  return select;
}

/** What the profile select's row says under it: that a pick waits for the running turn. Tracked. */
export function profileNote(): string {
  const s = activeSession.value;
  return s !== undefined && watchBusy(s.id) ? BUSY_NOTE : "";
}

/** Wire the composer's profile pill. Call once at startup. */
export function initProfilePill(): void {
  byId("profile-pill-icon").replaceChildren(iconEl(ICON_SHIELD));
  let open = false;
  makeExpandable(byId("profile-pill"), byId("profile-list"), {
    haspopup: "listbox",
    onExpand: () => {
      open = true;
      forgetSig(byId("profile-list"));
      renderCard();
      void readView();
    },
    onCollapse: () => {
      open = false;
    },
  });
  nav = rovingFocus(byId("profile-list"), ".pill-role-item");
  onSSE("permissions_changed", () => {
    void readView();
  });
  effect(() => {
    const q = queued.value;
    if (q !== null && !watchBusy(q.chatID)) {
      queued.value = null;
    }
  });
  effect(() => {
    const s = activeSession.value;
    const q = queued.value;
    paintPill(profileOf(s, view.value), q !== null && q.chatID === s?.id);
  });
  // An open card follows the chat's turn and the server's answer.
  effect(() => {
    const s = activeSession.value;
    // eslint-disable-next-line @typescript-eslint/no-unused-expressions
    view.value;
    if (s !== undefined) {
      watchBusy(s.id);
    }
    if (open) {
      untracked(renderCard);
    }
  });
  void readView();
}

function paintPill(id: string, pending: boolean): void {
  const pill = byId<HTMLButtonElement>("profile-pill");
  pill.classList.toggle("hidden", id === "");
  pill.classList.toggle("pending", pending);
  const label = profileLabel(id);
  byId("profile-pill-label").textContent = label;
  // The tooltip repeats the accessible name because a phone's pill is icon-only.
  const name = pending
    ? `Security profile, ${label}, applies after this turn`
    : `Security profile, currently ${label}`;
  pill.setAttribute("aria-label", name);
  pill.setAttribute("data-tooltip", name);
}

/** Reads untracked: the effect above owns what repaints an open card. */
function renderCard(): void {
  const s = activeSession.value;
  const v = view.value;
  const busy = s !== undefined && watchBusy(s.id);
  const list = byId("profile-list");
  list.setAttribute("role", "listbox");
  list.setAttribute("aria-label", "Security profile");
  const current = profileOf(s, v);
  const saved = savedRulesStillApply(current, v?.saved_grants ?? false);
  const profiles = listedProfiles(v, current);
  // A repaint that changes nothing must not rebuild an open card: the rebuild takes focus off the row the user is on.
  if (!sigChanged(list, [current, String(busy), String(saved), wireSignature(profiles)])) {
    return;
  }
  const focused =
    document.activeElement instanceof HTMLElement && list.contains(document.activeElement)
      ? document.activeElement.dataset["profileId"]
      : undefined;
  const items: HTMLElement[] = [];
  if (busy) {
    items.push(el("span", { className: "pill-role-hint" }, BUSY_NOTE));
  }
  if (saved) {
    items.push(el("span", { className: "pill-role-hint" }, SAVED_RULES_NOTE));
  }
  for (const id of profiles) {
    items.push(profileOption(id, current));
  }
  list.replaceChildren(...items);
  nav?.refresh();
  if (focused !== undefined) {
    list.querySelector<HTMLElement>(`[data-profile-id="${CSS.escape(focused)}"]`)?.focus();
  }
}

function profileOption(id: string, current: string): HTMLButtonElement {
  const isCurrent = id === current;
  const opt = el(
    "button",
    {
      type: "button",
      className: isCurrent ? "pill-role-item active" : "pill-role-item",
      role: "option",
      "aria-selected": isCurrent ? "true" : "false",
      "data-tooltip": profileDescription(id),
      "data-profile-id": id,
    },
    el("span", { className: "pill-role-name" }, profileLabel(id)),
  ) as HTMLButtonElement;
  opt.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    collapseAll();
    if (!isCurrent) {
      void choose(id);
    }
  });
  return opt;
}

async function choose(id: string): Promise<void> {
  if (id === LOOSEST_PROFILE_HINT && !(await confirmLoosest())) {
    return;
  }
  const active = getActive();
  // No chat on screen: the id is the server's, so the chat is created before the pick can name it.
  const chatID = active !== undefined ? active.id : await createSession();
  if (chatID === "") {
    return;
  }
  const pick = { chatID, profile: id };
  if (untracked(() => watchBusy(chatID))) {
    queued.value = pick;
  }
  const { outcome } = setChatProfile.dispatch(pick);
  if ((await outcome).status === "error" && queued.peek() === pick) {
    queued.value = null;
  }
}
