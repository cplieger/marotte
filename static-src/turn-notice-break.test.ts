// A BROKEN TURN'S REASON IS THE FRAMED PROSE UNDER THE BODY'S LAST DIVIDER. A body-ending
// `.boundary` names the KIND, the card-level `.turn-notice` carries the server's PROSE; this pins
// the geometry. The divider is the frame's top edge, the footer BAND's fill change the bottom, and
// the reason sits at a SYMMETRIC inset with no second rule. Each half alone passes for a broken
// frame. The divider is a `compaction` (no `interrupted` event kind exists). `.boundary`'s
// `vk-slide-up` puts its rect 6px low until it runs, so the harness stops that animation.
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { buildEvent } from "./messages-events.js";
import type { EventEntry } from "./messages-events.js";

/** `--sp-2`, the compaction head's own inset and the notice's — so it is what
 *  "framed the way the compaction break is framed" measures against. */
const INSET_PX = 8;
/** `--sp-3` (01-tokens.css), `.turn-body`'s `row-gap` AND its padding. The air a
 *  break mid-body still keeps below it is larger than this; the break that ENDS a
 *  framed body keeps none. */
const ROW_GAP_PX = 12;

const REASON = "ACP bridge exited";

let style: HTMLStyleElement;
const made: HTMLElement[] = [];

interface Card {
  readonly card: HTMLElement;
  readonly body: HTMLElement;
  readonly boundary: HTMLElement | null;
  readonly notice: HTMLElement;
  readonly footer: HTMLElement;
}

interface CardOpts {
  /** Whether the body ENDS with a divider, which opens the frame. False is the differential control:
   *  a reason from the carrier, after ordinary prose. */
  readonly dividerLast?: boolean;
  /** Fold the card, which puts `.turn-face` between the body and the notice. */
  readonly folded?: boolean;
}

/** A `compaction` with an EMPTY summary, which `buildEvent` renders as a plain `.boundary`, so the
 *  first case compares two DIFFERENT elements. */
function compactionDivider(): EventEntry {
  return {
    id: "e-compaction",
    turn: "t1",
    kind: "compaction",
    seq: 2,
    ts: 0,
    payload: { summary: "" },
  };
}

function prose(text: string): HTMLElement {
  const row = document.createElement("div");
  row.className = "msg-row";
  const msg = document.createElement("div");
  msg.className = "message assistant";
  msg.textContent = text;
  row.append(msg);
  return row;
}

/** A broken turn's card in `buildTurn`'s child order (header, body, FACE if folded, notice, footer),
 *  with the real divider and the notice assembled as `syncTurnNotice` does. */
function interruptedCard({ dividerLast = true, folded = false }: CardOpts = {}): Card {
  const card = document.createElement("div");
  card.className = "turn";
  if (folded) {
    card.setAttribute("data-folded", "");
  }

  const header = document.createElement("div");
  header.className = "turn-header";
  const req = document.createElement("div");
  req.className = "turn-req-text";
  req.textContent = "run the build";
  header.append(req);

  const body = document.createElement("div");
  body.className = "turn-body";
  body.append(prose("Building."));
  let boundary: HTMLElement | null = null;
  if (dividerLast) {
    boundary = buildEvent(compactionDivider());
    // See the header note: the entry animation's backwards fill offsets the rect
    // by 6px. Stopping it changes no layout — the keyframes touch opacity and
    // transform.
    boundary.style.animation = "none";
    body.append(boundary);
  }

  const notice = document.createElement("div");
  notice.className = "turn-notice";
  notice.dataset["severity"] = "broken";
  notice.dataset["outcome"] = "interrupted";
  notice.setAttribute("role", "status");
  notice.textContent = REASON;

  const footer = document.createElement("div");
  footer.className = "turn-footer";
  footer.dataset["severity"] = "broken";
  const ledger = document.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  ledger.textContent = "2 cmds";
  footer.append(ledger);

  card.append(header, body);
  if (folded) {
    const face = document.createElement("div");
    face.className = "turn-face";
    face.append(prose("Building."));
    card.append(face);
  }
  card.append(notice, footer);
  document.body.replaceChildren(card);
  made.push(card);
  return { card, body, boundary, notice, footer };
}

/** A compaction break, from the same builder, as the reference frame: the skin the
 *  reason is meant to have copied. */
function compactionBreak(): HTMLElement {
  const node = buildEvent({
    id: "e-compacted",
    turn: "t1",
    kind: "compaction",
    seq: 3,
    ts: 0,
    // NON-empty, so this is the `<details class="compaction">` frame rather than a
    // second `.boundary`: it is the SKIN the reason is meant to have copied.
    payload: { summary: "a summary" },
  });
  document.body.append(node);
  made.push(node);
  return node;
}

/** Where the INK is, not where the box is: the notice's content is one text node,
 *  so a Range over it is the only way to say how much air sits above and below the
 *  sentence a reader sees. */
function textRect(el: HTMLElement): DOMRect {
  const range = document.createRange();
  range.selectNodeContents(el);
  return range.getBoundingClientRect();
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  for (const el of made.splice(0)) {
    el.remove();
  }
});

describe("a broken turn's reason", () => {
  it("opens its frame with the divider's own rule and draws no second one", () => {
    const { boundary, notice, footer } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const frame = getComputedStyle(compactionBreak());

    // THE TOP EDGE is the divider's own dashed rule, which `.boundary` draws as
    // the `::before`/`::after` flanking its label. Read as a computed style rather
    // than assumed, because it is the edge the notice is now allowed to omit.
    const lead = getComputedStyle(boundary, "::before");
    expect(lead.borderTopStyle, "the divider's rule opens the frame").toBe(frame.borderTopStyle);
    expect(lead.borderTopWidth, "at the compaction break's weight").toBe(frame.borderTopWidth);
    expect(lead.borderTopColor, "in the same ink").toBe(frame.borderTopColor);

    // And the notice adds NOTHING to it. This is the second report: a rule here is
    // a second opening edge one row below the label that names the break.
    expect(
      getComputedStyle(notice).borderTopWidth,
      "the reason draws no rule of its own under that divider",
    ).toBe("0px");

    // THE BOTTOM EDGE is the footer BAND's fill change: two backgrounds that DIFFER, paired with the
    // width, since `0px` alone passes for a footer that stopped painting.
    const trailing = getComputedStyle(footer);
    expect(trailing.borderTopWidth, "the band closes the frame, so it draws no rule").toBe("0px");
    expect(trailing.backgroundColor, "and the band is what the reader sees").not.toBe(
      getComputedStyle(notice).backgroundColor,
    );
  });

  it("insets its text equally inside that frame", () => {
    // Measured across the WHOLE frame: the air below the divider's box against the air above the band.
    const { boundary, notice, footer } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const ink = textRect(notice);
    const above = ink.top - boundary.getBoundingClientRect().bottom;
    const below = footer.getBoundingClientRect().top - ink.bottom;
    expect(above, "the air between the divider and the sentence").toBeCloseTo(INSET_PX, 0);
    // WITHIN 1px: a Range rect is the INK box, which this font's metrics put 1px off centre.
    expect(
      Math.abs(below - above),
      "and the air between the sentence and the ledger",
    ).toBeLessThanOrEqual(1);
  });

  it("keeps its own rule when no divider opened a frame above it", () => {
    // THE DIFFERENTIAL: a notice with no frame rule also reports `0px`, so the carrier-reason turn,
    // where the notice draws its own line, is required.
    const { notice } = interruptedCard({ dividerLast: false });
    expect(getComputedStyle(notice).borderTopWidth, "one edge, drawn by the notice").toBe("1px");
  });

  it("keeps its own rule on a FOLDED card, where the face breaks the adjacency", () => {
    // The hidden body's divider is in the DOM, so `.turn-face` sitting between (`syncTurnFace` anchors
    // on the notice) keeps the frame rules off; the face's fill IS the body's, so this rule marks the
    // edge. The ORDER is pinned against real paint in turn-reason-once.test.ts.
    const { notice } = interruptedCard({ folded: true });
    expect(getComputedStyle(notice).borderTopWidth, "the notice draws the header's seam").toBe(
      "1px",
    );
  });

  it("leaves a divider that is NOT the body's last row alone", () => {
    // The control for the margin trims: a rule flattening every break's spacing would pass the rest.
    const { card, boundary } = interruptedCard();
    if (boundary === null) {
      throw new Error("no divider");
    }
    const body = card.querySelector<HTMLElement>(".turn-body");
    if (body === null) {
      throw new Error("no body");
    }
    const after = prose("Retrying.");
    body.append(after);
    const gap = after.getBoundingClientRect().top - boundary.getBoundingClientRect().bottom;
    expect(gap, "a break mid-body keeps its own trailing air").toBeGreaterThan(ROW_GAP_PX);
  });

  it("withdraws its rule against the header band on a card with no body", () => {
    // A prose-less stub puts the notice under the header, where the tinted band carries the edge, as
    // `.turn-face` does. Differential against the no-divider card.
    const bodied = interruptedCard({ dividerLast: false });
    expect(
      getComputedStyle(bodied.notice).borderTopWidth,
      "premise: a notice with no divider above it DOES carry the rule",
    ).toBe("1px");

    const { card } = interruptedCard({ dividerLast: false });
    card.querySelector(".turn-body")?.remove();
    const notice = card.querySelector<HTMLElement>(".turn-notice");
    if (notice === null) {
      throw new Error("no notice");
    }
    expect(getComputedStyle(notice).borderTopWidth, "one rule, not two").toBe("0px");
  });
});
