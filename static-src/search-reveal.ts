// The one chain of containers find-in-chat opened for its current hit: the turn fold, then every collapsed container
// between the hit's item and its turn body. Stepping opens the next hit's chain and closes what the previous one
// opened that the next does not share; closing find closes the rest. A container the reader toggled by hand while
// it was in the chain (a click, or Enter or Space on its control) is the reader's and stays as they left it.

import { refoldAfterReveal } from "./chat-search.js";
import { closeForSearch } from "./fold-state.js";
import { hideHookPopover, showHookPopover } from "./hook-badge.js";
import type { Hit, SegmentKind } from "./wire/types.gen.js";

type ContainerKind = "tool-group" | "steer-group" | "details" | "tool-details" | "hook-popover";

/** One container search opened, with the control a reader's hand would toggle it by. */
interface OpenedContainer {
  readonly kind: ContainerKind;
  readonly el: HTMLElement;
  readonly control: HTMLElement;
  touched: boolean;
  unlisten: () => void;
}

/** A turn search opened, by its fold-state key. */
interface OpenedTurn {
  readonly chatID: string;
  readonly turnID: string;
}

/** The current hit's chain. */
interface Chain {
  readonly turn: OpenedTurn | null;
  readonly containers: readonly OpenedContainer[];
}

const EMPTY: Chain = { turn: null, containers: [] };

let current: Chain = EMPTY;

/** True while search itself toggles a control, so its own click does not read as the reader's. */
let searchToggling = false;

/** Kinds rendered inside `.tool-details`, which the card's disclosure opens. */
const IN_TOOL_DETAILS: ReadonlySet<SegmentKind> = new Set([
  "tool_output",
  "tool_input",
  "tool_denial",
]);

/** Whether the hit's words need the card's disclosure open: they render inside it, or they are a diff the card has
 *  no preview of, which loads when it opens (`DEFERRED_PARTS` in tool-card.ts). */
function wordsInDetails(card: HTMLElement, segments: readonly SegmentKind[]): boolean {
  return (
    segments.some((k) => IN_TOOL_DETAILS.has(k)) ||
    (segments.includes("tool_diff") && card.querySelector(".tool-diff-preview") === null)
  );
}

/** Activate without the click reaching the document: find's outside-dismissal would close the search. */
function activateQuietly(control: HTMLElement): void {
  const stop = (e: Event): void => {
    e.stopPropagation();
  };
  control.addEventListener("click", stop);
  searchToggling = true;
  try {
    control.click();
  } finally {
    searchToggling = false;
    control.removeEventListener("click", stop);
  }
}

function record(kind: ContainerKind, el: HTMLElement, control: HTMLElement): OpenedContainer {
  const opened: OpenedContainer = {
    kind,
    el,
    control,
    touched: false,
    unlisten: () => undefined,
  };
  const onClick = (): void => {
    if (!searchToggling) {
      opened.touched = true;
    }
  };
  // A tool group's header toggles on its own keydown, with no click; the others turn Enter and Space into one.
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Enter" || e.key === " ") {
      onClick();
    }
  };
  control.addEventListener("click", onClick, { capture: true });
  control.addEventListener("keydown", onKey, { capture: true });
  opened.unlisten = () => {
    control.removeEventListener("click", onClick, { capture: true });
    control.removeEventListener("keydown", onKey, { capture: true });
  };
  return opened;
}

/**
 * Open every collapsed container from `itemEl` up to its turn body, plus the tool card's own disclosure when the hit's
 * words sit inside it, and a folded hook's badge list. Containers already open, or that search already holds open in
 * the current chain, are kept and not recorded twice.
 */
function openContainers(itemEl: HTMLElement, hit: Hit): OpenedContainer[] {
  const out: OpenedContainer[] = [];
  const held = (el: HTMLElement): OpenedContainer | undefined =>
    current.containers.find((c) => c.el === el);
  const keep = (el: HTMLElement): boolean => {
    const c = held(el);
    if (c !== undefined) {
      out.push(c);
      return true;
    }
    return false;
  };
  for (let cur: HTMLElement | null = itemEl; cur !== null; cur = cur.parentElement) {
    if (cur instanceof HTMLDetailsElement && !keep(cur) && !cur.open) {
      const summary = cur.querySelector<HTMLElement>(":scope > summary") ?? cur;
      const opened = trackOpen("details", cur, summary, () => {
        cur.open = true;
      });
      out.push(opened);
    }
    if (
      cur.classList.contains("tool-group") &&
      !keep(cur) &&
      cur.classList.contains("tool-group-collapsed")
    ) {
      const header = cur.querySelector<HTMLElement>(":scope > .tool-group-header");
      if (header !== null) {
        out.push(
          trackOpen("tool-group", cur, header, () => {
            activateQuietly(header);
          }),
        );
      }
    }
    if (cur.classList.contains("steer-group") && !keep(cur)) {
      const header = cur.querySelector<HTMLElement>(
        ':scope > .steer-group-header[aria-expanded="false"]',
      );
      if (header !== null) {
        out.push(
          trackOpen("steer-group", cur, header, () => {
            activateQuietly(header);
          }),
        );
      }
    }
    if (cur.classList.contains("turn-body")) {
      break;
    }
  }
  // A lone card's hooks badge sits in that card, whose details hold none of the hook's words.
  const card = itemEl.classList.contains("tool-group-hooks")
    ? null
    : itemEl.closest<HTMLElement>(".tool-call");
  if (card !== null && wordsInDetails(card, hit.segments)) {
    openToolDetails(card, out, keep);
  }
  if (itemEl.classList.contains("tool-group-hooks") && !keep(itemEl) && showHookPopover(itemEl)) {
    out.push(record("hook-popover", itemEl, itemEl));
  }
  return out;
}

function openToolDetails(
  card: HTMLElement,
  out: OpenedContainer[],
  keep: (el: HTMLElement) => boolean,
): void {
  const toggle = card.querySelector<HTMLElement>(".tool-disclosure");
  if (toggle === null || keep(card)) {
    return;
  }
  if (toggle.getAttribute("aria-expanded") === "false") {
    out.push(
      trackOpen("tool-details", card, toggle, () => {
        activateQuietly(toggle);
      }),
    );
  }
}

/** Open a container, listening on its control from before the open so the record sees only the reader's clicks. */
function trackOpen(
  kind: ContainerKind,
  el: HTMLElement,
  control: HTMLElement,
  open: () => void,
): OpenedContainer {
  const opened = record(kind, el, control);
  open();
  return opened;
}

/** Close one container search opened, unless the reader took it over or it left the page. */
function close(c: OpenedContainer): void {
  c.unlisten();
  if (c.touched || !c.el.isConnected) {
    return;
  }
  switch (c.kind) {
    case "details":
      (c.el as HTMLDetailsElement).open = false;
      return;
    case "tool-group":
      if (!c.el.classList.contains("tool-group-collapsed")) {
        activateQuietly(c.control);
      }
      return;
    case "steer-group":
    case "tool-details":
      if (c.control.getAttribute("aria-expanded") === "true") {
        activateQuietly(c.control);
      }
      return;
    case "hook-popover":
      hideHookPopover(c.el);
      return;
  }
}

/**
 * Make `hit`'s chain the current one: open the containers between `itemEl` and its turn, then close what the previous
 * chain opened that this one does not hold. The turn itself the caller opened (`revealHitTurn`); a previous turn is
 * released and folded here.
 */
export function revealChain(chatID: string, hit: Hit, itemEl: HTMLElement): void {
  const containers = openContainers(itemEl, hit);
  replace({ turn: { chatID, turnID: hit.turn_id }, containers });
}

/** The chain for a hit landed with no element to open around: its turn only. */
export function revealTurnOnly(chatID: string, hit: Hit): void {
  replace({ turn: { chatID, turnID: hit.turn_id }, containers: [] });
}

function replace(next: Chain): void {
  const prev = current;
  current = next;
  for (const c of prev.containers) {
    if (!next.containers.includes(c)) {
      close(c);
    }
  }
  const turn = prev.turn;
  if (turn === null || (next.turn?.chatID === turn.chatID && next.turn.turnID === turn.turnID)) {
    return;
  }
  if (closeForSearch(turn.chatID, turn.turnID)) {
    refoldAfterReveal(turn.chatID, turn.turnID);
  }
}

/** Close the whole chain, as find closes. The turn is left to `resetServerSearch`, which re-folds every turn search
 *  opened. */
export function releaseChain(): void {
  const prev = current;
  current = EMPTY;
  for (const c of prev.containers) {
    close(c);
  }
}
