// Turn actions live in the footer, the one region that survives the fold, so they work on the whole turn.

// DOM lookups the types call non-null can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { Turn } from "./turns.js";
import { payloadOf } from "./turns.js";
import { entryRenders, firstPlanSeq } from "./block-window.js";
import { ICON_COPY, ICON_COPY_MD, ICON_LINK } from "./icons.js";
import { getActiveId } from "./store.js";
import { copyClipboard } from "./actions/messages.js";
import { buildPath } from "./route-path.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const copyTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

/** The turn each footer acts on, refreshed every paint so handlers read current data. */
const footerTurns = new WeakMap<HTMLElement, Turn>();

let dismissalWired = false;

// ---------------------------------------------------------------------------
// Dismissal
// ---------------------------------------------------------------------------

/**
 * Close every open overflow menu the event did not happen inside (`null` closes all). The exemption is
 * load-bearing: `pointerdown` precedes `click`, so closing on it would make `fromMenu` read false and drop the toast,
 * and it lets an open menu's trigger still close it.
 */
function closeOverflowMenus(inside: Node | null): void {
  for (const menu of document.querySelectorAll<HTMLDetailsElement>(".turn-actions-more[open]")) {
    if (inside !== null && menu.contains(inside)) {
      continue;
    }
    menu.removeAttribute("open");
  }
}

/**
 * Two document listeners rather than one per footer, since a footer has no teardown seam. Escape does not
 * `stopPropagation`, so the app's own Escape handling still sees it.
 */
function wireOverflowDismissal(): void {
  if (dismissalWired) {
    return;
  }
  dismissalWired = true;
  document.addEventListener(
    "pointerdown",
    (ev) => {
      const target = ev.target;
      closeOverflowMenus(target instanceof Node ? target : null);
    },
    // Capture, so a handler that stops propagation cannot leave a menu open; passive, as it is on the scroll path.
    { capture: true, passive: true },
  );
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape") {
      closeOverflowMenus(null);
    }
  });
}

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts
// ---------------------------------------------------------------------------

let _svgTemplate: (markup: string) => () => Node = () => () => document.createDocumentFragment();

export function initTurnActionCallbacks(cbs: {
  svgTemplate: (markup: string) => () => Node;
}): void {
  _svgTemplate = cbs.svgTemplate;
}

/**
 * Whether `card`'s mounted body is a complete answer for "copy as text"; injected since messages.ts imports this
 * module. True until wired.
 */
let bodyHoldsWholeTurn: (card: HTMLElement, t: Turn) => boolean = () => true;

export function initTurnActionsBodyProbe(holdsWholeTurn: typeof bodyHoldsWholeTurn): void {
  bodyHoldsWholeTurn = holdsWholeTurn;
}

/** `announce` is for a click from the collapsed overflow, whose `.copied` flash goes off screen with
 *  the menu; elsewhere the flash suffices. */
function copyAndAnimate(btn: HTMLButtonElement, text: string, announce = false): void {
  void copyClipboard.dispatch(text, {
    silent: !announce,
    onSuccess: () => {
      btn.classList.add("copied");
      const prev = copyTimers.get(btn);
      if (prev !== undefined) {
        clearTimeout(prev);
      }
      copyTimers.set(
        btn,
        setTimeout(() => {
          btn.classList.remove("copied");
        }, 1500),
      );
    },
  });
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

/**
 * Mount the action buttons into the footer once and refresh the turn the handlers read. Buttons appear only once
 * the turn settles with something to copy; every action, Copy included, sits inside the collapsible group.
 */
export function mountTurnFooterActions(footer: HTMLElement, card: HTMLElement, t: Turn): void {
  // Before every early return, and idempotent.
  wireOverflowDismissal();
  footerTurns.set(footer, t);
  if (footer.querySelector(":scope > .turn-actions-buttons") !== null) {
    return;
  }
  if (t.outcome === "running" || turnMarkdown(t).trim() === "") {
    return;
  }

  const slot = el("span", { className: "turn-actions-buttons" });
  const current = (): Turn => footerTurns.get(footer) ?? t;

  const makeBtn = (
    svgMarkup: string,
    ariaLabel: string,
    onClick: (btn: HTMLButtonElement, fromMenu: boolean) => void,
  ): HTMLButtonElement => {
    const btn = el(
      "button",
      {
        type: "button",
        className: "turn-action-btn",
        "aria-label": ariaLabel,
        "data-tooltip": ariaLabel,
      },
      _svgTemplate(svgMarkup)(),
      el("span", { className: "turn-action-label" }, ariaLabel),
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      // On phone these sit inside the disclosure, and an action closes it. Read `open` first: on desktop the summary is
      // `display: none`, so `open` is itself the collapsed-layout test.
      const menu = btn.closest<HTMLDetailsElement>(".turn-actions-more");
      const fromMenu = menu?.open === true;
      onClick(btn, fromMenu);
      menu?.removeAttribute("open");
    });
    return btn;
  };

  // Inline on desktop, behind one native <details> on phone; one set of buttons serves both.
  const group = el("span", { className: "turn-actions-group" });
  group.appendChild(
    makeBtn(ICON_COPY, "Copy as text", (btn, fromMenu) => {
      copyAndAnimate(btn, turnCopyText(card, current()), fromMenu);
    }),
  );
  group.appendChild(
    makeBtn(ICON_COPY_MD, "Copy as markdown", (btn, fromMenu) => {
      copyAndAnimate(btn, turnCopyMarkdown(current()), fromMenu);
    }),
  );
  group.appendChild(
    makeBtn(ICON_LINK, "Copy link to this turn", (btn, fromMenu) => {
      const chatID = getActiveId();
      if (chatID !== "") {
        copyAndAnimate(btn, turnLink(chatID, current().n), fromMenu);
      }
    }),
  );

  const more = el("details", {
    className: "turn-actions-more",
    name: "turn-actions-overflow",
  }) as HTMLDetailsElement;
  more.appendChild(
    el(
      "summary",
      {
        className: "turn-action-btn turn-action-more",
        "aria-label": "More turn actions",
        "data-tooltip": "More turn actions",
      },
      "\u2026",
    ),
  );
  more.appendChild(group);
  slot.appendChild(more);

  // Before Rewind, so the destructive action keeps the far edge.
  const rewind = footer.querySelector<HTMLElement>(":scope > .turn-rewind");
  if (rewind !== null) {
    rewind.before(slot);
  } else {
    footer.appendChild(slot);
  }
}

/** The full URL of one turn: the chat's route plus `#turn-<n>`. */
export function turnLink(chatID: string, n: number): string {
  return location.origin + buildPath({ kind: "chat", id: chatID, turn: n });
}

/** "Copy as text": the reader's prompt, then the reply (the reply alone for an agent-initiated turn). */
function turnCopyText(card: HTMLElement, t: Turn): string {
  const reply = turnPlainText(card, t);
  const prompt = t.trigger?.text.trim() ?? "";
  return prompt === "" ? reply : `${prompt}\n\n${reply}`;
}

/** "Copy as markdown": the prompt as a blockquote, then the reply's markdown. */
function turnCopyMarkdown(t: Turn): string {
  const reply = turnMarkdown(t);
  const prompt = t.trigger?.text.trim() ?? "";
  if (prompt === "") {
    return reply;
  }
  const quoted = prompt
    .split("\n")
    .map((line) => (line === "" ? ">" : `> ${line}`))
    .join("\n");
  return `${quoted}\n\n${reply}`;
}

/**
 * The reply's markdown, one paragraph per prose run. A run's `text` entries concatenate since one parser renders
 * them; `entryRenders` is the boundary test.
 */
export function turnMarkdown(t: Turn): string {
  const parts: string[] = [];
  let run = "";
  const firstPlan = firstPlanSeq(t, "");
  const flush = (): void => {
    if (run.trim() !== "") {
      parts.push(run);
    }
    run = "";
  };
  for (const e of t.body) {
    if (!entryRenders(e, "", firstPlan)) {
      continue;
    }
    const text = payloadOf(e, "text");
    if (text === undefined) {
      flush();
      continue;
    }
    run += text.text;
  }
  flush();
  return parts.join("\n\n");
}

/**
 * The reply's rendered plain text: the mounted surface's bubbles, else the markdown. The body counts only while it
 * holds the whole turn.
 */
function turnPlainText(card: HTMLElement, t: Turn): string {
  const bubbles = [
    ...card.querySelectorAll(
      bodyHoldsWholeTurn(card, t)
        ? ":scope > .turn-body .message.assistant, :scope > .turn-face > .message.assistant"
        : ":scope > .turn-face > .message.assistant",
    ),
  ];
  if (bubbles.length > 0) {
    return bubbles.map((b) => b.textContent ?? "").join("\n\n");
  }
  return turnMarkdown(t);
}
