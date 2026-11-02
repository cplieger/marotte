// The model card's one decision: what its close sends, from the chat as the card opened, the knob
// and the model clicked. Pure, so every close path and its test read the same rule.

import { THINKING_OFF } from "./effort.js";

/** The chat as the card opened on it, any pair parked behind a turn included. */
export interface PillOpen {
  readonly chatModel: string;
  readonly effort: string;
  /** The slider stood on its Off stop. */
  readonly thinkingOff: boolean;
  /** `pending_model` and `pending_effort`, "" when nothing is parked. */
  readonly pendingModel: string;
  readonly pendingEffort: string;
}

/** The knob at close: never moved, or the stop it was left on. */
export type PillKnob =
  { readonly touched: false } | { readonly touched: true; readonly level: string };

/** A stop applied in place on the chat's own model. */
export type PillStop =
  | { readonly kind: "none" }
  | { readonly kind: "set_effort"; readonly level: string }
  | { readonly kind: "thinking_off" };

/** The one command a close sends. A switch's `effort` is absent (keep the parked tier, else the
 *  chat's level), a tier, or the Off stop's id (drop a parked tier); the server applies any stop it
 *  carries in place in the same request (SwitchModelCommand.Effort). */
export type PillCommand =
  PillStop | { readonly kind: "switch"; readonly model: string; readonly effort?: string };

const NONE: PillStop = { kind: "none" };

/** The model the card stands on: the parked pick, else the chat's own. */
function cardModel(open: PillOpen): string {
  return open.pendingModel === "" ? open.chatModel : open.pendingModel;
}

/** The tier parked with another model, which the knob opens on whatever the chat's thinking;
 *  undefined leaves the slider on the chat's own reading. */
export function parkedStop(open: PillOpen): string | undefined {
  return cardModel(open) === open.chatModel || open.pendingEffort === ""
    ? undefined
    : open.pendingEffort;
}

/** The stop the knob opened on, for a close with no click to compare against. */
function openStop(open: PillOpen): string {
  return parkedStop(open) ?? (open.thinkingOff ? THINKING_OFF.id : open.effort);
}

/** The stop moved to, applied in place on the chat's own model: it counts only when it differs
 *  from the chat's (a tier repeated while thinking is off turns thinking on). */
function stopInPlace(open: PillOpen, level: string): PillStop {
  if (level === THINKING_OFF.id) {
    return open.thinkingOff ? NONE : { kind: "thinking_off" };
  }
  return level !== open.effort || open.thinkingOff ? { kind: "set_effort", level } : NONE;
}

/** What the close sends. The chat's own model takes a moved stop in place; over a switch parked
 *  on another model it rides a switch naming the chat's model instead, which also cancels that pair.
 *  Any other model is one switch carrying the stop the knob shows, moved or parked; a close on a
 *  parked pair with no click sends only a stop moved off the one the knob opened on. */
export function resolvePillCommit(
  open: PillOpen,
  knob: PillKnob,
  clicked: string | undefined,
): PillCommand {
  const target = clicked ?? cardModel(open);
  const level = knob.touched ? knob.level : undefined;
  if (target === open.chatModel) {
    if (level === undefined) {
      return NONE;
    }
    return cardModel(open) === open.chatModel
      ? stopInPlace(open, level)
      : { kind: "switch", model: target, effort: level };
  }
  if (clicked === undefined && (level === undefined || level === openStop(open))) {
    return NONE;
  }
  const effort = level ?? parkedStop(open);
  return effort === undefined
    ? { kind: "switch", model: target }
    : { kind: "switch", model: target, effort };
}
