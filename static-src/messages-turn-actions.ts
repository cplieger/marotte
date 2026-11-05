// Turn actions live in the footer, the one region that survives the fold, so they work on the whole turn.

// DOM lookups the types call non-null can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { Turn } from "./turns.js";
import { payloadOf, projectTurn } from "./turns.js";
import { wakeText } from "./turn-wake.js";
import { senderWords } from "./sent-by.js";
import { entryRenders, turnFolds } from "./block-window.js";
import { MessageRun } from "./entry-ids.js";
import { ICON_COPY, ICON_COPY_MD, ICON_LINK, ICON_MORE, ICON_TANGENT } from "./icons.js";
import { blockquote } from "./message-quote.js";
import { get, getPaintedId } from "./store.js";
import { chatNotice } from "./notice-subject.js";
import { copyClipboard } from "./actions/messages.js";
import { buildPath } from "./route-path.js";
import { el } from "@cplieger/ui";
import { queryAll } from "./dom.js";

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
  for (const menu of queryAll<HTMLDetailsElement>(".turn-actions-more[open]")) {
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
 * the turn settles: the copy actions when it wrote prose, Branch when it can be branched from. Every action starts in
 * the collapsible group; the fit moves those it keeps beside `…` into `.turn-actions-inline`.
 */
export function mountTurnFooterActions(footer: HTMLElement, card: HTMLElement, t: Turn): void {
  // Before every early return, and idempotent.
  wireOverflowDismissal();
  footerTurns.set(footer, t);
  // Every paint, since the outcome word and Rewind can change without resizing the footer.
  scheduleFooterFit(footer);
  if (footer.querySelector(":scope > .turn-actions-buttons > .turn-actions-more") !== null) {
    return;
  }
  const hasProse = turnMarkdown(t).trim() !== "";
  if (t.outcome === "running" || (!hasProse && t.branchFrom === undefined)) {
    return;
  }

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
      // Behind `…` an action closes the disclosure. Read `open` first: while the actions fit inline the summary is
      // `display: none`, so `open` is itself the collapsed-layout test.
      const menu = btn.closest<HTMLDetailsElement>(".turn-actions-more");
      const fromMenu = menu?.open === true;
      onClick(btn, fromMenu);
      menu?.removeAttribute("open");
    });
    return btn;
  };

  // Inline when they fit, behind one native <details> once `fitFooterActions` writes `data-actions-menu`; one set of
  // buttons serves both.
  const group = el("span", { className: "turn-actions-group" });
  if (hasProse) {
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
        const chatID = getPaintedId();
        if (chatID !== "") {
          copyAndAnimate(btn, turnLink(chatID, current().n), fromMenu);
        }
      }),
    );
  }
  if (t.branchFrom !== undefined) {
    group.appendChild(
      makeBtn(ICON_TANGENT, "Branch from here", () => {
        branchFrom(current().id);
      }),
    );
  }

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
      _svgTemplate(ICON_MORE)(),
    ),
  );
  more.appendChild(group);
  // Prepended, so the destructive Rewind keeps the far edge whichever of the two mounts first.
  footerActionsSlot(footer).prepend(more);
}

// ---------------------------------------------------------------------------
// Fit: which actions sit inline and which go behind `…`
// ---------------------------------------------------------------------------

/** `tight` hides the outcome words to make room; `keep` is how many group actions stay inline beside `…`, and
 *  null means no `…` at all. */
interface FooterFit {
  readonly tight: boolean;
  readonly keep: number | null;
}

/**
 * The rule, over `n` actions (Rewind counted) of which `fits` fit beside the outcome words and `fitsTight` fit
 * without them: inline when all fit; otherwise hide the words when that is the only way to keep three or fewer
 * inline, or when one action is all that is short; otherwise `…` takes the slot of the first action that does not
 * fit and holds the rest, which is always two or more. Rewind keeps the far edge, so it is never held.
 */
function footerFit(n: number, rewind: boolean, fits: number, fitsTight: number): FooterFit {
  const r = rewind ? 1 : 0;
  if (n <= fits) {
    return { tight: false, keep: null };
  }
  if (n <= 3 || (n <= fitsTight && (n - fits === 1 || fits < 1 + r))) {
    return { tight: true, keep: null };
  }
  if (fits >= 1 + r) {
    return { tight: false, keep: fits - 1 - r };
  }
  return { tight: true, keep: Math.max(0, fitsTight - 1 - r) };
}

function px(v: string): number {
  return Number.parseFloat(v) || 0;
}

/** The ledger button's width with its fact squeezed to nothing, which is the most the row may take from it. The
 *  word is measured through a Range, so the reading is the same whether `tight` is hiding it or not. */
function ledgerNeed(ledger: HTMLElement, withWords: boolean): number {
  const cs = getComputedStyle(ledger);
  const gap = px(cs.columnGap);
  const widths: number[] = [];
  for (const child of ledger.children) {
    const c = child as HTMLElement;
    if (c.classList.contains("sr-only") || c.hidden || getComputedStyle(c).display === "none") {
      continue;
    }
    const words = c.classList.contains("turn-ledger-text") || c.classList.contains("turn-fact");
    if (words && !withWords) {
      continue;
    }
    if (c.classList.contains("turn-fact")) {
      widths.push(0);
    } else if (c.classList.contains("turn-ledger-text")) {
      if ((c.textContent ?? "") === "") {
        continue;
      }
      const range = document.createRange();
      range.selectNodeContents(c);
      widths.push(range.getBoundingClientRect().width);
    } else {
      widths.push(c.getBoundingClientRect().width);
    }
  }
  const items = widths.reduce((a, b) => a + b, 0) + gap * Math.max(0, widths.length - 1);
  return px(cs.paddingInlineStart) + px(cs.paddingInlineEnd) + items;
}

/** The actions in their reading order: those kept inline beside `…`, then the group's. */
function footerActions(slot: HTMLElement): HTMLElement[] {
  const kept = slot.querySelector<HTMLElement>(":scope > .turn-actions-inline");
  const group = slot.querySelector<HTMLElement>(
    ":scope > .turn-actions-more > .turn-actions-group",
  );
  return [...(kept?.children ?? []), ...(group?.children ?? [])] as HTMLElement[];
}

/** Read the footer box and the ledger's text width, then write `data-actions-tight`, `data-actions-menu` and the inline
 *  split. Those writes do not change the inputs, so a resize they cause repeats the same fit. */
function fitFooterActions(footer: HTMLElement): void {
  const slot = footer.querySelector<HTMLElement>(":scope > .turn-actions-buttons");
  const ledger = footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary");
  const opener = slot?.querySelector<HTMLElement>(
    ":scope > .turn-actions-more > .turn-action-more",
  );
  const rewind = slot?.querySelector(":scope > .turn-rewind") != null;
  const actions = slot === null || slot === undefined ? [] : footerActions(slot);
  const n = actions.length + (rewind ? 1 : 0);
  let fit: FooterFit = { tight: false, keep: null };
  if (slot !== null && slot !== undefined && n > 0) {
    const fs = getComputedStyle(footer);
    const ss = getComputedStyle(slot);
    const content =
      footer.getBoundingClientRect().width -
      px(fs.paddingInlineStart) -
      px(fs.paddingInlineEnd) -
      px(fs.borderInlineStartWidth) -
      px(fs.borderInlineEndWidth);
    const room = content - px(ss.marginInlineStart);
    const gap = px(ss.columnGap);
    // Every footer control is the same square, so the hidden `…` reads the width any one of them paints at.
    const side = px(getComputedStyle(opener ?? slot.lastElementChild ?? slot).width);
    const slots = (need: number): number => Math.floor((room - need + gap + 0.5) / (side + gap));
    const need = (words: boolean): number => (ledger === null ? 0 : ledgerNeed(ledger, words));
    fit = footerFit(n, rewind, slots(need(true)), slots(need(false)));
  }
  footer.toggleAttribute("data-actions-tight", fit.tight);
  if (fit.keep === null) {
    footer.removeAttribute("data-actions-menu");
    slot?.querySelector<HTMLDetailsElement>(":scope > .turn-actions-more")?.removeAttribute("open");
  } else {
    footer.setAttribute("data-actions-menu", "");
  }
  if (slot !== null && slot !== undefined) {
    // Rewind can create the container after the paint that scheduled this fit.
    watchFit(slot);
    keepInline(slot, actions, fit.keep ?? 0);
  }
}

/** Seat the first `keep` actions in the inline holder before `…` and the rest in its group, in reading order.
 *  Moves nothing when they already sit there, since a move drops a button's hover and focus. */
function keepInline(slot: HTMLElement, actions: readonly HTMLElement[], keep: number): void {
  const menu = slot.querySelector<HTMLElement>(":scope > .turn-actions-more");
  const group = menu?.querySelector<HTMLElement>(":scope > .turn-actions-group");
  if (menu === null || group === null || group === undefined) {
    return;
  }
  let kept = slot.querySelector<HTMLElement>(":scope > .turn-actions-inline");
  if (kept?.childElementCount === keep || (kept === null && keep === 0)) {
    return;
  }
  if (kept === null) {
    kept = el("span", { className: "turn-actions-inline" });
    menu.before(kept);
  }
  kept.replaceChildren(...actions.slice(0, keep));
  group.replaceChildren(...actions.slice(keep));
  if (keep === 0) {
    kept.remove();
  }
}

/** Each footer's pending fit: `true` when a resize asked for it, which no unchanged input can skip. */
const pendingFits = new Map<HTMLElement, boolean>();
const fittedInputs = new WeakMap<HTMLElement, string>();
let fitFrame = 0;
let fitWatcher: ResizeObserver | undefined;
const fitWatched = new Set<Element>();

/**
 * Fit `footer` on the next frame, and again whenever it, its ledger or its actions container resizes (a viewport, a
 * sidebar, a pointer tier's gap). Deferred a frame: the fit resizes the container, which a ResizeObserver may not answer inside
 * its own delivery.
 */
function scheduleFooterFit(footer: HTMLElement): void {
  watchFit(footer);
  // The ledger too: a web font landing widens its words with no paint and no footer resize.
  for (const part of footer.querySelectorAll(
    ":scope > .turn-actions-buttons, :scope > .turn-ledger-summary",
  )) {
    watchFit(part);
  }
  queueFit(footer, false);
}

function watchFit(target: Element): void {
  if (fitWatched.has(target)) {
    return;
  }
  fitWatcher ??= new ResizeObserver((entries) => {
    for (const entry of entries) {
      const owner = entry.target.closest<HTMLElement>(".turn-footer");
      // Fallback only; `releaseFooterFitsIn` is the mechanism.
      if (!entry.target.isConnected || owner === null) {
        fitWatcher?.unobserve(entry.target);
        fitWatched.delete(entry.target);
        continue;
      }
      queueFit(owner, true);
    }
  });
  fitWatcher.observe(target);
  fitWatched.add(target);
}

function queueFit(footer: HTMLElement, resized: boolean): void {
  pendingFits.set(footer, resized || pendingFits.get(footer) === true);
  if (fitFrame !== 0) {
    return;
  }
  fitFrame = requestAnimationFrame(() => {
    fitFrame = 0;
    const due = [...pendingFits];
    pendingFits.clear();
    for (const [f, forced] of due) {
      if (!f.isConnected) {
        continue;
      }
      // A paint that changed none of the fit's inputs skips its geometry reads: a pass over a long chat repaints
      // every footer, and a read per card lays the transcript out once per card.
      const inputs = fitInputs(f);
      if (forced || fittedInputs.get(f) !== inputs) {
        fittedInputs.set(f, inputs);
        fitFooterActions(f);
      }
    }
  });
}

/** What a footer's fit depends on apart from its size, read with no layout: the actions, Rewind, and the ledger's
 *  parts and words. */
function fitInputs(footer: HTMLElement): string {
  const slot = footer.querySelector<HTMLElement>(":scope > .turn-actions-buttons");
  const parts = [
    String(slot === null ? -1 : footerActions(slot).length),
    String(slot?.querySelector(":scope > .turn-rewind") != null),
  ];
  for (const c of footer.querySelector(":scope > .turn-ledger-summary")?.children ?? []) {
    parts.push(c.className, String((c as HTMLElement).hidden), c.textContent ?? "");
  }
  return parts.join("\u0000");
}

/** Stop watching every footer inside `root`, `root` included. Call it where `root` is about to be discarded. */
export function releaseFooterFitsIn(root: Element): void {
  for (const target of [...fitWatched]) {
    if (root === target || root.contains(target)) {
      fitWatcher?.unobserve(target);
      fitWatched.delete(target);
    }
  }
}

/** The footer's one trailing container, created on first use: the action buttons and Rewind share its gap. */
export function footerActionsSlot(footer: HTMLElement): HTMLElement {
  let slot = footer.querySelector<HTMLElement>(":scope > .turn-actions-buttons");
  if (slot === null) {
    slot = el("span", { className: "turn-actions-buttons" });
    footer.appendChild(slot);
  }
  return slot;
}

/**
 * Open a tangent holding the conversation through `turnID`. The turn is re-projected from the live session, because
 * a compaction since the card's last paint leaves it nothing KAS can fork from. Files on disk stay as they are.
 */
function branchFrom(turnID: string): void {
  const chatID = getPaintedId();
  const session = get(chatID);
  if (session === undefined) {
    return;
  }
  const at = projectTurn(session, turnID)?.branchFrom;
  if (at === undefined) {
    chatNotice(
      chatID,
      "This turn was summarized or rewound since it was shown, so there is nothing to branch from.",
    );
    return;
  }
  // Lazy: chat.ts imports messages.ts, which imports this module.
  void import("./chat.js").then(({ openTangentChat }) => openTangentChat(chatID, at.id));
}

/** The full URL of one turn: the chat's route plus `#turn-<n>`. */
export function turnLink(chatID: string, n: number): string {
  return location.origin + buildPath({ kind: "chat", id: chatID, turn: n });
}

/** The turn's trigger as its header words it: the prompt under its "Sent by" line when the reader did not send it,
 *  or what woke the turn. */
function triggerText(t: Turn): string {
  const text = t.trigger?.text.trim();
  if (text === undefined) {
    return wakeText(t).trim();
  }
  const sender = senderWords(t.trigger);
  if (sender === undefined) {
    return text;
  }
  return text === "" ? sender : `${sender}\n${text}`;
}

/** "Copy as text": the reader's prompt or what woke the turn, then the reply (the reply alone when the
 *  turn names neither). */
function turnCopyText(card: HTMLElement, t: Turn): string {
  const reply = turnPlainText(card, t);
  const prompt = triggerText(t);
  return prompt === "" ? reply : `${prompt}\n\n${reply}`;
}

/** "Copy as markdown": the prompt or what woke the turn as a blockquote, then the reply's markdown. */
function turnCopyMarkdown(t: Turn): string {
  const reply = turnMarkdown(t);
  const prompt = triggerText(t);
  if (prompt === "") {
    return reply;
  }
  return `${blockquote(prompt)}\n\n${reply}`;
}

/**
 * The reply's markdown, one paragraph per prose run and per `steer_ack`, as the body draws a bubble for each. A run's
 * `text` entries concatenate since one parser renders them; `entryRenders` and `MessageRun` are the boundary tests,
 * as `proseRunAt`'s.
 */
export function turnMarkdown(t: Turn): string {
  const parts: string[] = [];
  let run = "";
  let msg = new MessageRun();
  const folds = turnFolds(t, "");
  const push = (s: string): void => {
    if (s.trim() !== "") {
      parts.push(s);
    }
  };
  const flush = (): void => {
    push(run);
    run = "";
    msg = new MessageRun();
  };
  for (const e of t.body) {
    if (!entryRenders(e, "", folds)) {
      continue;
    }
    const text = payloadOf(e, "text");
    if (text === undefined) {
      flush();
      push(payloadOf(e, "steer_ack")?.text ?? "");
      continue;
    }
    if (!msg.take(e)) {
      flush();
      msg.take(e);
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
