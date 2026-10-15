import { describe, expect, it } from "vitest";
import { THINKING_OFF } from "./effort.js";
import {
  parkedStop,
  resolvePillCommit,
  type PillCommand,
  type PillKnob,
  type PillOpen,
  type PillStop,
} from "./pill-commit.js";

const OFF = THINKING_OFF.id;

/** The commit rule restated case by case, written apart from resolvePillCommit. */
function spec(open: PillOpen, knob: PillKnob, clicked: string | undefined): PillCommand {
  const parked = open.pendingModel !== "" && open.pendingModel !== open.chatModel;
  const shownModel = parked ? open.pendingModel : open.chatModel;
  // The knob shows a tier parked with another model, whatever the chat's thinking.
  const parkedTier = parked && open.pendingEffort !== "" ? open.pendingEffort : null;
  const openedOn = parkedTier ?? (open.thinkingOff ? OFF : open.effort);
  const model = clicked ?? shownModel;
  const moved = knob.touched ? knob.level : null;
  if (model === open.chatModel) {
    if (moved === null) {
      return { kind: "none" };
    }
    // A stop against the chat's own state; a repeat while thinking is off turns it on.
    let inPlace: PillStop = { kind: "set_effort", level: moved };
    if (moved === OFF) {
      inPlace = open.thinkingOff ? { kind: "none" } : { kind: "thinking_off" };
    } else if (!open.thinkingOff && moved === open.effort) {
      inPlace = { kind: "none" };
    }
    // Over a switch parked on another model, the stop rides a switch naming the chat's model,
    // which the server applies in place and which cancels the parked pair.
    return parked ? { kind: "switch", model, effort: moved } : inPlace;
  }
  // Closing on the parked pair with no click sends only a stop moved off where the knob opened.
  if (clicked === undefined && (moved === null || moved === openedOn)) {
    return { kind: "none" };
  }
  // The switch carries what the knob shows: the stop moved to, else the parked tier.
  const shown = moved ?? parkedTier;
  return shown === null ? { kind: "switch", model } : { kind: "switch", model, effort: shown };
}

const efforts = ["", "medium", "high"];
const pendings: readonly (readonly [string, string])[] = [
  ["", ""],
  ["sonnet", ""],
  ["sonnet", "high"],
  ["sonnet", "low"],
  ["opus", "low"],
];
const knobs: readonly PillKnob[] = [
  { touched: false },
  ...[OFF, "low", "medium", "high"].map((level) => ({ touched: true, level }) as const),
];
const clicks = [undefined, "opus", "sonnet", "haiku"];

describe("resolvePillCommit", () => {
  it("matches the spec over every card, knob and click", () => {
    const reached = new Set<string>();
    let cases = 0;
    for (const effort of efforts) {
      for (const thinkingOff of [false, true]) {
        for (const [pendingModel, pendingEffort] of pendings) {
          const open: PillOpen = {
            chatModel: "opus",
            effort,
            thinkingOff,
            pendingModel,
            pendingEffort,
          };
          for (const knob of knobs) {
            for (const clicked of clicks) {
              cases++;
              const got = resolvePillCommit(open, knob, clicked);
              expect(got, JSON.stringify({ open, knob, clicked })).toEqual(
                spec(open, knob, clicked),
              );
              reached.add(
                got.kind === "switch"
                  ? `switch${got.effort === undefined ? "" : got.effort === OFF ? "+Off" : "+tier"}${got.model === open.chatModel ? "+own" : ""}`
                  : got.kind,
              );
            }
          }
        }
      }
    }
    // Every kind of send is reached, or a dimension is dead.
    expect([...reached].sort(), `over ${String(cases)} cases`).toEqual(
      [
        "none",
        "set_effort",
        "switch",
        "switch+Off",
        "switch+Off+own",
        "switch+tier",
        "switch+tier+own",
        "thinking_off",
      ].sort(),
    );
  });

  // A tier picked back to the chat's own, over a parked pair, is sent: the card showed the
  // parked tier, so moving off it is a change.
  it("sends the chat's own tier over a parked one when the knob moved to it", () => {
    const open: PillOpen = {
      chatModel: "opus",
      effort: "medium",
      thinkingOff: false,
      pendingModel: "sonnet",
      pendingEffort: "high",
    };

    expect(parkedStop(open), "the card opens on the parked tier").toBe("high");
    expect(resolvePillCommit(open, { touched: true, level: "medium" }, "sonnet")).toEqual({
      kind: "switch",
      model: "sonnet",
      effort: "medium",
    });
    expect(resolvePillCommit(open, { touched: true, level: "medium" }, undefined)).toEqual({
      kind: "switch",
      model: "sonnet",
      effort: "medium",
    });
  });

  it("sends a moved tier with the model clicked, even one equal to the chat's", () => {
    const open: PillOpen = {
      chatModel: "opus",
      effort: "medium",
      thinkingOff: false,
      pendingModel: "",
      pendingEffort: "",
    };

    expect(resolvePillCommit(open, { touched: true, level: "medium" }, "sonnet")).toEqual({
      kind: "switch",
      model: "sonnet",
      effort: "medium",
    });
  });

  const parkedHigh = (thinkingOff: boolean): PillOpen => ({
    chatModel: "opus",
    effort: "medium",
    thinkingOff,
    pendingModel: "sonnet",
    pendingEffort: "high",
  });

  it("opens on a parked tier while thinking is off", () => {
    expect(parkedStop(parkedHigh(true))).toBe("high");
    expect(resolvePillCommit(parkedHigh(true), { touched: false }, undefined)).toEqual({
      kind: "none",
    });
  });

  it("carries the parked tier it shows to another model clicked", () => {
    for (const thinkingOff of [false, true]) {
      expect(resolvePillCommit(parkedHigh(thinkingOff), { touched: false }, "haiku")).toEqual({
        kind: "switch",
        model: "haiku",
        effort: "high",
      });
    }
  });

  // A touched stop then a click on the running model replaces the parked switch: left parked, it
  // would land at the turn's close over the stop the reader just kept. The stop rides the one
  // switch, which the server applies in place before it clears the pair.
  it("cancels a parked switch with one switch carrying the stop kept on the chat's own model", () => {
    for (const thinkingOff of [false, true]) {
      for (const level of ["low", "medium", OFF]) {
        expect(
          resolvePillCommit(parkedHigh(thinkingOff), { touched: true, level }, "opus"),
          JSON.stringify({ thinkingOff, level }),
        ).toEqual({ kind: "switch", model: "opus", effort: level });
      }
    }
  });

  it("sends Off over a parked tier as one switch that drops it", () => {
    const off = { touched: true, level: OFF } as const;
    for (const thinkingOff of [false, true]) {
      expect(resolvePillCommit(parkedHigh(thinkingOff), off, undefined)).toEqual({
        kind: "switch",
        model: "sonnet",
        effort: OFF,
      });
    }
  });
});
