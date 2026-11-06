// TurnHeader: the turn card's top band for the turn's TRIGGER (a user prompt, what woke an
// agent-initiated turn, or a typed system line). A meta row (fold toggle, number, outcome dot,
// timestamp, hit count) over the request. The fold toggle opens the request with the body: a FOLDED
// turn's request text clamps to four lines in CSS, since folded rows are the session's navigation,
// and a labelled request's full text shows only while open; attachment chips stay outside the clamp.

import { join } from "@cplieger/keyenc";
import { el } from "@cplieger/ui";
import { chevronEl } from "../chevron.js";
import { iconEl } from "../icon-el.js";
import { workflowIcon } from "../icons.js";
import { watchCut } from "../clamp-text.js";
import { linkifyPaths } from "../linkify.js";
import { buildAttachmentPill, type AttachmentRef } from "../attachment-pill.js";
import { splitLeadingQuote } from "../message-quote.js";
// The dot's words, shared with the turn map — see turn-severity.ts.
// Colour is never the sole channel (WCAG 1.4.1), and these are that channel.
import { OUTCOME_LABEL, OUTCOME_TOOLTIP, severityOf } from "../turn-severity.js";
import type { TurnOutcome } from "../turns.js";
import { WAKE_FALLBACK_LINE, type WakeLine, type WakeRow, type WakeRun } from "../turn-wake.js";
import { sentBy } from "../sent-by.js";
import type { Caller } from "../types.js";
import { formatClock } from "../locale-time.js";

export interface TurnHeaderData {
  /** 1-based turn ordinal as rendered. */
  n: number;
  outcome: TurnOutcome;
  /** Turn start, ms epoch. */
  ts: number;
  /** The user's request. Undefined for a turn the user did not ask for. */
  request: string | undefined;
  /** The label of a request marotte wrote on the user's behalf, drawn in its place with the
   *  request one click away. Undefined for a typed request. */
  label?: string | undefined;
  /** The program that sent the request through the API: a "Sent by" line over it, and the header reads as that
   *  program's request, not the user's. Undefined for the user's own. */
  caller?: Caller | undefined;
  /** Files the user attached to this request. Drawn as the composer's own pill,
   *  read-only (a sent attachment cannot be un-sent), and OUTSIDE the clamp. */
  attachments: readonly AttachmentRef[];
  /** What woke a turn with no request: a one-line label over its message, which takes the
   *  request's place and clamp. Ignored when `request` is set or the wake is a fallback. */
  wake?: WakeLine | undefined;
  /** Opens a run the wake names; without it each name is plain text. */
  onOpenRun?: ((run: WakeRun) => void) | undefined;
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
        "aria-label": FOLD_LABEL,
        // `.turn-header` is a plain div — `aria-expanded` there is an axe
        // `aria-allowed-attr` violation, so the state lives on this button.
        "aria-expanded": "true",
      },
      chevronEl(),
    ),
  );
  const toggle = row.lastElementChild as HTMLElement;
  row.appendChild(el("span", { className: "turn-n" }, `#${String(d.n)}`));
  row.appendChild(el("span", { className: "turn-dot", role: "img" }));
  row.appendChild(el("time", { className: "turn-ts" }));
  // Filled while a search is active.
  row.appendChild(el("span", { className: "turn-hit-count" }));
  header.appendChild(row);

  const req = el("div", { className: "turn-req" });
  const text = el("div", { className: "turn-req-text" });
  req.appendChild(text);
  // Sibling of `.turn-req-text`, so the clamp cannot hide the attachments.
  req.appendChild(el("ul", { className: "turn-req-attachments attachment-row hidden" }));
  header.appendChild(req);
  watchCut(
    text,
    (cut) => {
      toggle.setAttribute("aria-label", cut ? `${FOLD_LABEL}, message truncated` : FOLD_LABEL);
    },
    () => {
      syncWakeRowRuns(header);
    },
  );

  updateTurnHeader(header, d);
  return header;
}

const FOLD_LABEL = "Expand or collapse this turn";

/** The request band alone, for a turn drawn without card chrome: a run step's turn that a message resumed,
 *  headed by its Sent-by line when `caller` sent it through the API. */
export function buildRequestHeader(request: string, caller?: Caller): HTMLElement {
  const text = el("div", { className: "turn-req-text" }, request);
  linkifyPaths(text);
  const header = el(
    "div",
    { className: "turn-header", "data-trigger": caller === undefined ? "user" : "caller" },
    el("div", { className: "turn-req" }, text),
  );
  syncSentBy(header, caller);
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
    time.textContent = formatClock(d.ts);
  }

  // Ahead of the early return below, so a repaint cannot leave a pill row
  // describing a request that is no longer here.
  syncAttachments(header, d.attachments);

  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  if (text === null) {
    return;
  }

  if (d.request === undefined && d.wake !== undefined && !d.wake.fallback) {
    header.dataset["trigger"] = "wake";
    syncQuote(header, undefined);
    syncSentBy(header, undefined);
    syncWakeLabel(header, d.wake, d.onOpenRun);
    if (d.wake.rows === undefined) {
      setRequestText(text, d.wake.body);
    } else {
      setWakeRows(text, d.wake.rows, d.onOpenRun);
    }
    text.hidden = d.wake.body === "";
    syncFullText(header, undefined);
    syncWakeRowRuns(header);
    return;
  }
  syncWakeLabel(header, undefined, undefined);
  text.hidden = false;

  if (d.request === undefined && d.caller !== undefined) {
    // A caller's prompt with no words (attachments only): its Sent-by line and attachment row are the header.
    header.dataset["trigger"] = "caller";
    syncSentBy(header, d.caller);
    setRequestText(text, "");
    text.hidden = true;
    syncQuote(header, undefined);
    syncFullText(header, undefined);
    return;
  }

  if (d.request === undefined) {
    // No user message: naming the trigger is honest, fabricating one is not.
    header.dataset["trigger"] = "system";
    syncSentBy(header, undefined);
    setRequestText(text, WAKE_FALLBACK_LINE);
    syncQuote(header, undefined);
    syncFullText(header, undefined);
    return;
  }

  header.dataset["trigger"] = d.caller === undefined ? "user" : "caller";
  const body = d.request.trim();
  const label = d.label?.trim() ?? "";
  const quoted = label === "" ? splitLeadingQuote(body) : null;
  syncQuote(header, quoted ?? undefined);
  syncSentBy(header, d.caller);
  setRequestText(text, label !== "" ? label : (quoted?.rest ?? body));
  syncFullText(header, label === "" ? undefined : body);
}

function setRequestText(text: HTMLElement, shown: string): void {
  if (text.textContent !== shown || text.dataset["rows"] !== undefined) {
    delete text.dataset["rows"];
    text.textContent = shown;
    linkifyPaths(text);
  }
}

/** A re-delivery's messages in the request text, one per line as `body` joins them, each opening on its
 *  run's name; rebuilt only when the words or runs change, as the label is. */
function setWakeRows(
  text: HTMLElement,
  rows: readonly WakeRow[],
  open: ((run: WakeRun) => void) | undefined,
): void {
  const key = join(...rows.map((r) => join(r.run.id, r.run.name, r.rest, r.text)));
  if (text.dataset["rows"] === key) {
    return;
  }
  text.dataset["rows"] = key;
  const parts: (Node | string)[] = [];
  rows.forEach((r, i) => {
    if (i > 0) {
      parts.push("\n");
    }
    const message = el("span", { className: "turn-wake-row-text" }, r.text);
    linkifyPaths(message);
    parts.push(wakeRunEl(r.run, "turn-wake-row-run", open), r.rest, message);
  });
  text.replaceChildren(...parts);
}

/** Who sent a request an API caller posted, one line above the request text and outside its clamp, so a folded turn
 *  still says the user did not write it; `caller` undefined removes it. */
function syncSentBy(header: HTMLElement, caller: Caller | undefined): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  let line = req.querySelector<HTMLElement>(":scope > .turn-sent-by");
  if (caller === undefined) {
    line?.remove();
    return;
  }
  const words = sentBy(caller);
  line ??= el("div", { className: "turn-sent-by" });
  // First in the band, above a quote band drawn after it.
  if (req.firstElementChild !== line) {
    req.prepend(line);
  }
  if (line.textContent !== words) {
    line.textContent = words;
  }
}

/** The wake's label, one line above the request text and outside its clamp, rebuilt only when its
 *  words or run change so a focused run button survives a repaint; `wake` undefined removes it. */
function syncWakeLabel(
  header: HTMLElement,
  wake: WakeLine | undefined,
  open: ((run: WakeRun) => void) | undefined,
): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  let line = req.querySelector<HTMLElement>(":scope > .turn-wake");
  if (wake === undefined) {
    line?.remove();
    return;
  }
  const run = wake.run?.id ?? "";
  const message = String(wake.message);
  if (
    line !== null &&
    line.dataset["label"] === wake.label &&
    line.dataset["run"] === run &&
    line.dataset["message"] === message
  ) {
    return;
  }
  if (line === null) {
    line = el("div", { className: "turn-wake" });
    req.prepend(line);
  }
  line.dataset["label"] = wake.label;
  line.dataset["run"] = run;
  line.dataset["message"] = message;
  // The glyph a step-message note carries, so the turn it started reads as the same message.
  const glyph = wake.message
    ? [
        el(
          "span",
          { className: "turn-wake-icon", "aria-hidden": "true" },
          iconEl(workflowIcon("message")),
        ),
      ]
    : [];
  const rest = el("span", { className: "turn-wake-rest" }, wake.rest);
  if (wake.run === undefined) {
    line.replaceChildren(...glyph, rest);
    return;
  }
  line.replaceChildren(...glyph, wakeRunEl(wake.run, "turn-wake-run", open), rest);
}

function wakeRunEl(
  run: WakeRun,
  className: string,
  open: ((run: WakeRun) => void) | undefined,
): HTMLElement {
  const attrs = { className, "data-run-id": run.id };
  if (open === undefined) {
    return el("span", attrs, run.name);
  }
  const btn = el(
    "button",
    { ...attrs, type: "button", "data-tooltip": "Open the workflow run" },
    run.name,
  );
  btn.addEventListener("click", () => {
    open(run);
  });
  return btn;
}

/**
 * Make each re-delivery row's run button inert while the request text's clamp hides any of it, so it takes no focus,
 * and live while all of it shows. Runs after each header update and each clamp measurement; a fold change calls
 * it. With no layout to measure, a folded card keeps only the first row's live.
 */
export function syncWakeRowRuns(header: HTMLElement): void {
  const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
  if (text === null) {
    return;
  }
  const clip = text.getBoundingClientRect();
  const folded = text.closest(".turn[data-folded]") !== null;
  const buttons = text.querySelectorAll<HTMLButtonElement>("button.turn-wake-row-run");
  buttons.forEach((btn, i) => {
    if (clip.height <= 0) {
      btn.inert = folded && i > 0;
      return;
    }
    const box = btn.getBoundingClientRect();
    // `inert`, not `disabled`: a disabled button dims (40-a11y.css), and a shown row stays at full ink.
    btn.inert = box.top < clip.top - CLIP_EPSILON || box.bottom > clip.bottom + CLIP_EPSILON;
  });
}

/** Sub-pixel slack (px) for a button whose box rounds onto the clip edge. */
const CLIP_EPSILON = 0.5;

/** The passage a prompt opened with (`message-quote.ts`), drawn as one band before the text, led by the line naming
 *  its source when it has one; `quote` undefined removes it. */
function syncQuote(
  header: HTMLElement,
  quote: { source: string | null; quote: string } | undefined,
): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  let band = req.querySelector<HTMLElement>(":scope > .turn-req-quote");
  if (quote === undefined) {
    band?.remove();
    return;
  }
  if (band === null) {
    band = el("blockquote", { className: "turn-req-quote" });
    req.prepend(band);
  }
  const key = join(quote.source ?? "", quote.quote);
  if (band.dataset["key"] === key) {
    return;
  }
  band.dataset["key"] = key;
  const text = el("span", { className: "turn-req-quote-text" }, quote.quote);
  linkifyPaths(text);
  band.replaceChildren(...(quote.source === null ? [] : [sourceEl(quote.source), " "]), text);
}

/** A quote's source line as the reader sees it: its code spans (CommonMark 6.1) drawn as code. */
function sourceEl(source: string): HTMLElement {
  const parts: (Node | string)[] = [];
  let at = 0;
  for (const m of source.matchAll(/(`+)(?!`)(.+?)(?<!`)\1(?!`)/g)) {
    parts.push(source.slice(at, m.index));
    const inner = m[2] ?? "";
    const code = /^ .* $/.test(inner) && inner.trim() !== "" ? inner.slice(1, -1) : inner;
    parts.push(el("code", {}, code));
    at = m.index + m[0].length;
  }
  parts.push(source.slice(at), ":");
  return el("span", { className: "turn-req-quote-from" }, ...parts);
}

/** A labelled request's full text, a sibling of the text so the folded clamp cannot hide it, shown
 *  while the turn is open (29-turns.css); `body` undefined removes it. */
function syncFullText(header: HTMLElement, body: string | undefined): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  let full = req.querySelector<HTMLElement>(":scope > .turn-req-full");
  if (body === undefined) {
    full?.remove();
    return;
  }
  if (full === null) {
    full = el("div", { className: "turn-req-full" });
    // After the text and before the attachments, in reading order.
    req.querySelector(":scope > .turn-req-text")?.after(full);
  }
  if (full.textContent !== body) {
    full.textContent = body;
    linkifyPaths(full);
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
