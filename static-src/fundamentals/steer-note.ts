// Fundamental: SteerNote — a message delivered into a turn already running, rendered at the
// `seq` where the agent read it. A CARD on the tool-card box vocabulary, not a left rail:
// `#marotte-ui` reserves a leading rail for work this agent did not do itself.
//
// IT HOLDS THE READER'S WORDS ALONE AND CARRIES NO CONTROL. The agent's acknowledgement is
// its own `steer_ack` entry rendering at its own position, so the note carries a MARKER for
// it — an attribute plus a clause on the label, never agent prose — and the label's other
// clauses come from recorded facts: why a drop went unread, and whether this one resends.

import { el } from "@cplieger/reactive";
import { attachClamp } from "../clamp-text.js";
import { iconEl } from "../icon-el.js";
import { ICON_SEND, ICON_TAB_RUN } from "../icons.js";
import type { SteerOrigin, SteerReason } from "../types.js";

export interface SteerNoteData {
  text: string;
  /** Whose words these are. Decides the label and the glyph. */
  origin: SteerOrigin;
  /** The agent never read it. */
  dropped: boolean;
  /** Why it was never read, verbatim from the entry. A wire ENUM, so the wording table
   *  below is total over it and a reason nobody worded cannot compile. */
  reason?: SteerReason;
  /** This message re-sends an earlier drop, which the entry states by naming it. The
   *  earlier note is not drawn, so this one carries its state — and only that state, because
   *  `resends` names the entry and never why it went unread. */
  resent?: boolean;
  /** The agent acknowledged it. The acknowledgement's own words are elsewhere. */
  acknowledged?: boolean;
  /** The DELEGATE lane that read it, empty when the turn's own agent did. The reader's
   *  words belong to the reader, so the note renders in the parent's flow either way and
   *  this is the marker for who consumed them: a clause plus `data-lane`, which carries
   *  the lane itself, because a lane id is a uuid and the label is for a person. */
  lane?: string;
}

/** The four labels, TOTAL over the origin so a third value cannot compile without wording
 *  of its own. KAS's steering buffer is the only inbound channel into a live turn, so a
 *  workflow's report arrives on it beside the reader's own corrections — and with one label
 *  the report read as something they had typed.
 *
 *  A DROPPED LABEL STATES WHAT IS KNOWN AND NOTHING MORE: what happened next is a recorded
 *  fact, so the resend's own note says it and no label may assert one. */
const LABELS: Record<SteerOrigin, { read: string; dropped: string }> = {
  user: { read: "Mid-turn message", dropped: "Not read" },
  agent: { read: "Workflow result", dropped: "Workflow result not delivered" },
};

/** The wording for each drop reason, TOTAL over the wire enum so a third reason cannot
 *  compile without wording of its own — which is why `SteerReason` is a registered enum
 *  rather than a free string: a reason with no clause renders the bare state, and the note
 *  then reads as if nothing had gone wrong. The `Object.hasOwn` guard below stays anyway,
 *  because the value arrives off the wire and totality is a claim about THIS build. */
const REASONS: Record<SteerReason, string> = {
  restart: "the session restarted",
  boundary: "the turn ended first",
  deleted: "you deleted it",
};

/** The second channel, never a hue (WCAG 1.4.1 would forbid one as the only
 *  channel anyway): the composer's send arrow, or the run tab's own glyph. */
const GLYPHS: Record<SteerOrigin, string> = {
  user: ICON_SEND,
  agent: ICON_TAB_RUN,
};

/** Lines the body shows before the opener appears. Four rather than the turn
 *  header's three: this is the message itself, not a navigation row. */
const CLAMP_LINES = 4;

export function buildSteerNote(d: SteerNoteData): HTMLElement {
  const label = labelFor(d);

  const root = el("div", {
    className: "steer-note",
    "data-state": d.dropped ? "dropped" : "read",
    // Read by CSS, so no class has to be kept in step with the label.
    "data-origin": d.origin,
    // The full text: an accessible name has no width, and neither the glyph nor
    // the clamp carries the state on its own.
    "aria-label": `${label}: ${oneLine(d.text)}`,
  });
  if (d.acknowledged === true) {
    root.setAttribute("data-acknowledged", "true");
  }
  if (readByDelegate(d)) {
    root.setAttribute("data-lane", d.lane);
  }

  root.appendChild(
    el(
      "div",
      { className: "steer-note-head" },
      el("span", { className: "tool-icon", "aria-hidden": "true" }, iconEl(GLYPHS[d.origin])),
      el("span", { className: "steer-note-label" }, label),
    ),
  );

  const body = el("div", { className: "steer-note-body" });
  // NOT collapsed to one line: the clamp is an opener rather than a cut, so the
  // message keeps the shape it was typed in (`white-space: pre-wrap`).
  const text = el("div", { className: "steer-note-text" }, d.text);
  body.appendChild(text);
  const more = el("button", {
    className: "steer-note-more",
    type: "button",
  }) as HTMLButtonElement;
  // OUTSIDE the clamped element, or the clamp hides its own opener.
  body.appendChild(more);
  root.appendChild(body);

  attachClamp(text, more, { lines: CLAMP_LINES });
  return root;
}

/** The base label plus one clause per recorded fact, in the order a reader needs them:
 *  what the state is, why, and what this message is. */
function labelFor(d: SteerNoteData): string {
  const parts = [d.dropped ? LABELS[d.origin].dropped : LABELS[d.origin].read];
  const why = d.dropped ? reasonWording(d.reason) : undefined;
  if (why !== undefined) {
    parts.push(why);
  }
  if (d.resent === true) {
    parts.push("not read earlier, resent");
  }
  if (readByDelegate(d)) {
    parts.push(d.acknowledged === true ? "acknowledged by a delegate" : "read by a delegate");
  } else if (d.acknowledged === true) {
    parts.push("acknowledged");
  }
  return parts.join(" · ");
}

/** Whether a DELEGATE read it, which is what the lane says when it is not the turn's own.
 *  One predicate, because the attribute and the clause must agree about it. */
function readByDelegate(d: SteerNoteData): d is SteerNoteData & { lane: string } {
  return d.lane !== undefined && d.lane !== "";
}

/** The wording for one drop reason, or nothing. `Object.hasOwn` because the value comes off
 *  the wire, and a bare index into a record answers `Object.prototype`'s members. */
function reasonWording(reason: SteerReason | undefined): string | undefined {
  return reason !== undefined && Object.hasOwn(REASONS, reason) ? REASONS[reason] : undefined;
}

/** Collapse whitespace without shortening, for the single-string surfaces only.
 *  The VISIBLE text keeps its newlines — see buildSteerNote. */
function oneLine(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}
