// TurnHeader: the turn card's top band for the turn's TRIGGER (a user prompt or a typed system
// line). A meta row (fold toggle, number, outcome dot, timestamp, hit count) over the request. A
// FOLDED turn's request text clamps to four lines in CSS, since folded rows are the session's
// navigation; attachment chips stay outside the clamp.

import { el } from "@cplieger/reactive";
import { chevronEl } from "../chevron.js";
import { linkifyPaths } from "../linkify.js";
import { buildAttachmentPill, type AttachmentRef } from "../attachment-pill.js";
// The dot's words, shared with the timeline rail's marker — see turn-severity.ts.
// Colour is never the sole channel (WCAG 1.4.1), and these are that channel.
import { OUTCOME_LABEL, OUTCOME_TOOLTIP, severityOf } from "../turn-severity.js";
import type { TurnOutcome } from "../turns.js";

export interface TurnHeaderData {
  /** 1-based turn ordinal as rendered. */
  n: number;
  outcome: TurnOutcome;
  /** Turn start, ms epoch. */
  ts: number;
  /** The user's request. Undefined for a turn the user did not ask for. */
  request: string | undefined;
  /** Files the user attached to this request. Drawn as the composer's own pill,
   *  read-only (a sent attachment cannot be un-sent), and OUTSIDE the clamp. */
  attachments: readonly AttachmentRef[];
}

export function buildTurnHeader(d: TurnHeaderData): HTMLElement {
  const header = el("div", { className: "turn-header" });

  const row = el("div", { className: "turn-head-row" });
  // The fold toggle leads the row, so the affordance sits where the eye starts
  // and is in the same place whether the turn is open or folded.
  row.appendChild(
    el(
      "button",
      {
        className: "turn-fold-toggle",
        type: "button",
        "aria-label": "Expand or collapse this turn",
        // `.turn-header` is a plain div — `aria-expanded` there is an axe
        // `aria-allowed-attr` violation, so the state lives on this button.
        "aria-expanded": "true",
      },
      chevronEl(),
    ),
  );
  row.appendChild(el("span", { className: "turn-n" }, `#${String(d.n)}`));
  row.appendChild(el("span", { className: "turn-dot", role: "img" }));
  row.appendChild(el("time", { className: "turn-ts" }));
  // Filled while a search is active.
  row.appendChild(el("span", { className: "turn-hit-count" }));
  header.appendChild(row);

  const req = el("div", { className: "turn-req" });
  req.appendChild(el("div", { className: "turn-req-text" }));
  // Sibling of `.turn-req-text`, so the clamp cannot hide the attachments.
  req.appendChild(el("ul", { className: "turn-req-attachments attachment-row hidden" }));
  header.appendChild(req);

  updateTurnHeader(header, d);
  return header;
}

/** Recompute the header from turn data. Idempotent. */
export function updateTurnHeader(header: HTMLElement, d: TurnHeaderData): void {
  header.dataset["outcome"] = d.outcome;
  // Hue comes off the shared severity table; `data-outcome` keeps the words and
  // the one stated exception. See turn-footer.ts's own write for the split.
  header.dataset["severity"] = severityOf(d.outcome);

  const num = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-n");
  if (num !== null) {
    num.textContent = `#${String(d.n)}`;
  }

  const dot = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-dot");
  if (dot !== null) {
    dot.setAttribute("aria-label", OUTCOME_LABEL[d.outcome]);
    dot.setAttribute("data-tooltip", OUTCOME_TOOLTIP[d.outcome]);
  }

  const time = header.querySelector<HTMLTimeElement>(":scope > .turn-head-row > .turn-ts");
  if (time !== null && d.ts > 0) {
    const when = new Date(d.ts);
    time.dateTime = when.toISOString();
    time.textContent = when.toLocaleTimeString(undefined, {
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  // Ahead of the early return below, so a repaint cannot leave a pill row
  // describing a request that is no longer here.
  syncAttachments(header, d.attachments);

  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  if (text === null) {
    return;
  }

  if (d.request === undefined) {
    // No user message: naming the trigger is honest, fabricating one is not.
    header.dataset["trigger"] = "system";
    text.textContent = "Agent-initiated turn";
    return;
  }

  header.dataset["trigger"] = "user";
  const body = d.request.trim();
  if (text.textContent !== body) {
    text.textContent = body;
    linkifyPaths(text);
  }
}

/** Draw the attachment pills, rebuilding only when the list changed (compared via each pill's
 *  `title`), so a focused pill survives a repaint. */
function syncAttachments(header: HTMLElement, atts: readonly AttachmentRef[]): void {
  const row = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-attachments");
  if (row === null) {
    return;
  }
  row.classList.toggle("hidden", atts.length === 0);
  if (row.children.length === atts.length) {
    let same = true;
    for (const [i, att] of atts.entries()) {
      if (row.children[i]?.getAttribute("title") !== att.path) {
        same = false;
        break;
      }
    }
    if (same) {
      return;
    }
  }
  row.replaceChildren(...atts.map((att) => buildAttachmentPill(att)));
}
