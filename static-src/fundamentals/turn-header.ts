// TurnHeader: the turn card's top band for the turn's TRIGGER (a user prompt, what woke an
// agent-initiated turn, or a typed system line). A meta row (fold toggle, number, outcome dot,
// timestamp, hit count) over the request. The fold toggle opens the request with the body: a FOLDED
// turn's request text clamps to four lines in CSS, since folded rows are the session's navigation,
// and a labelled request's full text shows only while open; attachment chips stay outside the clamp.

import { join } from "@cplieger/keyenc";
import { el } from "@cplieger/ui";
import { chevronEl } from "../chevron.js";
import { selectableButton } from "../disclosure-row.js";
import { iconEl } from "../icon-el.js";
import { workflowIcon } from "../icons.js";
import { watchCut } from "../clamp-text.js";
import { linkifyPaths } from "../linkify.js";
import { buildAttachmentPill, type AttachmentRef } from "../attachment-pill.js";
import { leadQuote, type PromptPart, type Quote } from "../message-quote.js";
import { buildQuoteBand, drawParts } from "../quote-band.js";
// The dot's words, shared with the turn map — see turn-severity.ts.
// Colour is never the sole channel (WCAG 1.4.1), and these are that channel.
import { OUTCOME_LABEL, OUTCOME_TOOLTIP, severityOf } from "../turn-severity.js";
import type { TurnOutcome } from "../turns.js";
import { WAKE_FALLBACK_LINE, type WakeLine, type WakeRow, type WakeRun } from "../turn-wake.js";
import { senderWords, type Sender } from "../sent-by.js";
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
  /** marotte sent the request on its own: a "Sent by marotte" line over it, as an API caller's has. */
  automatic?: boolean | undefined;
  /** The turn's cancelled close is marotte's failsafe stop, not the reader's, so the dot says marotte stopped it. */
  stoppedToCompact?: boolean | undefined;
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

  const req = el("div", { className: "turn-req", "data-find-unit": "header" });
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
      queueWakeRowSync(header);
    },
  );

  updateTurnHeader(header, d);
  return header;
}

const FOLD_LABEL = "Expand or collapse this turn";
const STOPPED_TO_COMPACT_TOOLTIP = "marotte stopped this turn to compact the context";

/** The request band alone, for a turn drawn without card chrome: a run step's turn that a message resumed,
 *  headed by its Sent-by line when the reader did not send it. */
export function buildRequestHeader(request: string, from?: Sender): HTMLElement {
  const words = senderWords(from);
  const { lead, rest } = leadQuote(request);
  const text = el("div", { className: "turn-req-text" });
  setRequestParts(text, rest);
  const header = el(
    "div",
    { className: "turn-header", "data-trigger": words === undefined ? "user" : "caller" },
    el("div", { className: "turn-req", "data-find-unit": "header" }, text),
  );
  syncQuote(header, lead ?? undefined);
  syncSentBy(header, words);
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
    dot.setAttribute(
      "data-tooltip",
      d.stoppedToCompact === true ? STOPPED_TO_COMPACT_TOOLTIP : OUTCOME_TOOLTIP[d.outcome],
    );
  }

  const time = header.querySelector<HTMLTimeElement>(":scope > .turn-head-row > .turn-ts");
  if (time !== null && d.ts > 0) {
    const when = new Date(d.ts);
    time.dateTime = when.toISOString();
    time.dataset["clock"] = "short";
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
    queueWakeRowSync(header);
    return;
  }
  syncWakeLabel(header, undefined, undefined);
  text.hidden = false;

  const sender = senderWords(d);
  if (d.request === undefined && sender !== undefined) {
    // A caller's prompt with no words (attachments only): its Sent-by line and attachment row are the header.
    header.dataset["trigger"] = "caller";
    syncSentBy(header, sender);
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

  header.dataset["trigger"] = sender === undefined ? "user" : "caller";
  const body = d.request.trim();
  const label = d.label?.trim() ?? "";
  const { lead, rest } = label === "" ? leadQuote(body) : { lead: null, rest: [] };
  syncQuote(header, lead ?? undefined);
  syncSentBy(header, sender);
  if (label === "") {
    setRequestParts(text, rest);
  } else {
    setRequestText(text, label);
  }
  syncFullText(header, label === "" ? undefined : body);
}

/** The parts each request text was last drawn from, so a repaint of the same parts keeps their nodes. */
const drawnParts = new WeakMap<HTMLElement, string>();

function setRequestText(text: HTMLElement, shown: string): void {
  if (text.textContent !== shown || text.dataset["rows"] !== undefined || drawnParts.has(text)) {
    delete text.dataset["rows"];
    drawnParts.delete(text);
    text.textContent = shown;
    linkifyPaths(text);
  }
}

/** The request's words with the quotes after its opening one drawn as bands in their places. */
function setRequestParts(text: HTMLElement, parts: readonly PromptPart[]): void {
  const only = parts.length === 1 && parts[0]?.kind === "text" ? parts[0].text : null;
  if (parts.length === 0 || only !== null) {
    setRequestText(text, only ?? "");
    return;
  }
  const key = join(
    ...parts.map((p) =>
      p.kind === "text" ? join("t", p.text) : join("q", p.source ?? "", p.quote),
    ),
  );
  if (drawnParts.get(text) === key) {
    return;
  }
  drawnParts.set(text, key);
  delete text.dataset["rows"];
  text.replaceChildren(...drawParts(parts, linkedWords));
}

function linkedWords(words: string): HTMLElement {
  const span = el("span", {}, words);
  linkifyPaths(span);
  return span;
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
  drawnParts.delete(text);
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

/** Who sent a request the user did not write, one line above the request text and outside its clamp, so a folded
 *  turn still says so; `words` undefined removes it. */
function syncSentBy(header: HTMLElement, words: string | undefined): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  let line = req.querySelector<HTMLElement>(":scope > .turn-sent-by");
  if (words === undefined) {
    line?.remove();
    return;
  }
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
  const btn = selectableButton("span", className, () => {
    open(run);
  });
  btn.dataset["runId"] = run.id;
  btn.dataset["tooltip"] = "Open the workflow run";
  btn.textContent = run.name;
  return btn;
}

/**
 * Make each re-delivery row's run button inert while the request text's clamp hides any of it, so it takes no focus,
 * and live while all of it shows. Queued after each header update, clamp measurement and fold change, and measured in
 * the next frame for every queued header at once, so a paint forces no layout. With no layout to measure, a folded
 * card keeps only the first row's live.
 */
export function queueWakeRowSync(header: HTMLElement): void {
  if (wakeRowRuns(header).length === 0) {
    return;
  }
  wakeRowQueue.add(header);
  if (!wakeRowFrame) {
    wakeRowFrame = true;
    requestAnimationFrame(flushWakeRowSync);
  }
}

const wakeRowQueue = new Set<HTMLElement>();
let wakeRowFrame = false;

function wakeRowRuns(header: HTMLElement): NodeListOf<HTMLElement> {
  return header.querySelectorAll<HTMLElement>(
    ':scope > .turn-req > .turn-req-text .turn-wake-row-run[role="button"]',
  );
}

function flushWakeRowSync(): void {
  wakeRowFrame = false;
  const headers = [...wakeRowQueue];
  wakeRowQueue.clear();
  // Every rect read before any `inert` write: interleaving forces a layout per header.
  const writes: { btn: HTMLElement; inert: boolean }[] = [];
  for (const header of headers) {
    const text = header.querySelector<HTMLElement>(":scope > .turn-req > .turn-req-text");
    if (text === null) {
      continue;
    }
    const folded = text.closest(".turn[data-folded]") !== null;
    const clip = header.isConnected ? text.getBoundingClientRect() : undefined;
    wakeRowRuns(header).forEach((btn, i) => {
      if (clip === undefined || clip.height <= 0) {
        writes.push({ btn, inert: folded && i > 0 });
        return;
      }
      const box = btn.getBoundingClientRect();
      const inert = box.top < clip.top - CLIP_EPSILON || box.bottom > clip.bottom + CLIP_EPSILON;
      writes.push({ btn, inert });
    });
  }
  for (const { btn, inert } of writes) {
    btn.inert = inert;
  }
}

/** Sub-pixel slack (px) for a run name whose box rounds onto the clip edge. */
const CLIP_EPSILON = 0.5;

/** The passage a prompt opened with (`message-quote.ts`), drawn as one band before the text, led by the line naming
 *  its source when it has one; `quote` undefined removes it. */
function syncQuote(header: HTMLElement, quote: Quote | undefined): void {
  const req = header.querySelector<HTMLElement>(":scope > .turn-req");
  if (req === null) {
    return;
  }
  const band = req.querySelector<HTMLElement>(":scope > .turn-req-quote");
  if (quote === undefined) {
    band?.remove();
    return;
  }
  const key = join(quote.source ?? "", quote.quote);
  if (band?.dataset["key"] === key) {
    return;
  }
  const next = buildQuoteBand(quote);
  next.dataset["key"] = key;
  if (band === null) {
    req.prepend(next);
  } else {
    band.replaceWith(next);
  }
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
