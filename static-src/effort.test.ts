// `effortPillLabel` names the tier a chat runs at, withholding only when the model has no effort or nothing resolved.
import { beforeEach, describe, expect, it } from "vitest";
import { effortPillLabel, setCatalogEfforts, thinkingIsOff } from "./effort.js";
import { makeSession } from "./__test-helpers__/model.js";
import type { ModelInfo, Session } from "./types.js";

function model(id: string, dflt?: string, hasEffort?: boolean): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
    ...(hasEffort === undefined ? {} : { has_effort: hasEffort }),
  };
}

function session(fields: {
  model: string;
  effort?: string;
  effort_active?: string;
  effort_levels?: { id: string; name?: string }[];
  thinking_choice?: string;
  thinking_active?: string;
}): Session {
  return makeSession({ id: "c1", effort: "", ...fields });
}

function fiveTiers(): { id: string; name?: string }[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

describe("the pill's reasoning-tier readout", () => {
  beforeEach(() => {
    setCatalogEfforts([], "");
  });

  it("names the tier even when it IS the model's own default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    // The default is only knowable from the per-model catalog, empty on any bridgeless chat; the pill is a readout.
    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the tier when the chat chose something other than the default", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("max");
  });

  it("names the chat's own choice over the level the session reports", () => {
    const models = [model("opus-5", "high")];
    const s = session({
      model: "opus-5",
      effort: "max",
      effort_active: "high",
      effort_levels: fiveTiers(),
    });

    // Same precedence as the card's mark: one resolution order is why this is one module. The choice leads so a click
    // shows through the optimistic write.
    expect(effortPillLabel(s, models, "")).toBe("max");
  });

  it("falls through to what the session reports when the choice does not fit", () => {
    // Chosen on a model that had max; this one stops at high.
    const models = [model("sonnet-5", "medium")];
    const s = session({
      model: "sonnet-5",
      effort: "max",
      effort_active: "high",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // Naming max would claim an unreachable tier; it reports the session's level.
    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the level the session reports, default or not", () => {
    const models = [model("opus-5", "high")];
    const s = session({ model: "opus-5", effort_active: "high", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("high");
  });

  it("names the remembered pick on a chat with no session yet", () => {
    setCatalogEfforts(fiveTiers(), "high");
    const models = [model("opus-5", "high")];
    // A new chat: the server resolves the same seed into StartOpts.Effort.
    const s = session({ model: "opus-5" });

    expect(effortPillLabel(s, models, "max")).toBe("max");
  });

  it("falls back to the model's own default when the remembered pick does not fit", () => {
    const models = [model("sonnet-5", "medium")];
    const s = session({
      model: "sonnet-5",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    // A remembered max this model refuses falls through to the model's default.
    expect(effortPillLabel(s, models, "max")).toBe("medium");
  });

  it("names a chosen tier even when the catalog carries no default", () => {
    const models = [model("older")];
    const s = session({ model: "older", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("max");
  });

  it("names a chosen tier with an EMPTY catalog", () => {
    // The pre-session feed answers with no models on failure or before KAS resolves its list.
    const s = session({ model: "opus-5", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "")).toBe("max");
  });

  it("names the remembered pick with an EMPTY catalog", () => {
    const s = session({ model: "opus-5", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "max")).toBe("max");
  });

  it("names a service-resolved tier with no default to compare it to", () => {
    // The level the service resolved is still the level in force.
    const s = session({ model: "opus-5", effort_active: "high", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, [], "")).toBe("high");
  });

  it("says nothing when NO level resolves at all", () => {
    // Every rung is empty, so naming a tier would invent one.
    const models = [model("opus-5", undefined, true)];
    const s = session({ model: "opus-5", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("");
  });

  it("says nothing for a model that advertises no reasoning effort", () => {
    // `auto` has no tiers (KAS hasEffort:false).
    const models = [model("auto", "", false), model("opus-5", "high", true)];
    const s = session({ model: "auto", effort: "max" });

    expect(effortPillLabel(s, models, "max")).toBe("");
  });

  it("says nothing for a model whose capability is plumbed false even with a chosen tier", () => {
    // Hands the session its own tiers, so the gate is provably what withholds.
    const models = [model("auto", "", false), model("opus-5", "high", true)];
    const s = session({ model: "auto", effort: "max", effort_levels: fiveTiers() });

    expect(effortPillLabel(s, models, "")).toBe("");
  });

  it("says nothing for a model the catalog does not know", () => {
    const models = [model("opus-5", "high", true)];
    const s = session({ model: "some-new-model", effort: "max", effort_levels: fiveTiers() });

    // The `hasEffort` gate answers: the catalog says nothing about this model.
    expect(effortPillLabel(s, models, "")).toBe("");
  });

  it("labels the tier by the catalog's own name, else the house table", () => {
    const models = [model("opus-5", "high")];
    const named = session({
      model: "opus-5",
      effort: "xhigh",
      effort_levels: [{ id: "high" }, { id: "xhigh", name: "Extra high" }],
    });
    const unnamed = session({
      model: "opus-5",
      effort: "xhigh",
      effort_levels: [{ id: "high" }, { id: "xhigh" }],
    });

    expect(effortPillLabel(named, models, "")).toBe("Extra high");
    // The house table reads a bare `xhigh` as "x-high".
    expect(effortPillLabel(unnamed, models, "")).toBe("x-high");
  });

  it("says nothing for a chat that has no session at all", () => {
    expect(effortPillLabel(undefined, [model("opus-5", "high", true)], "")).toBe("");
  });
});

describe("the thinking Off readout", () => {
  beforeEach(() => {
    setCatalogEfforts([], "");
  });

  function toggleable(id: string): ModelInfo {
    return { ...model(id, "high", true), thinking_toggleable: true };
  }

  it("names Off on the pill for a toggleable model running with thinking off", () => {
    const s = session({
      model: "opus-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    expect(effortPillLabel(s, [toggleable("opus-5")], "")).toBe("off");
  });

  it("keeps naming the tier when the model's thinking cannot be turned off", () => {
    const s = session({
      model: "opus-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    expect(effortPillLabel(s, [model("opus-5", "high", true)], "")).toBe("high");
  });

  it("reads the session's report before the chat's own choice", () => {
    expect(
      thinkingIsOff(session({ model: "m", thinking_choice: "off", thinking_active: "on" }), []),
    ).toBe(false);
    expect(
      thinkingIsOff(session({ model: "m", thinking_choice: "on", thinking_active: "off" }), []),
    ).toBe(true);
    expect(thinkingIsOff(session({ model: "m", thinking_choice: "off" }), [])).toBe(true);
    expect(thinkingIsOff(session({ model: "m" }), [])).toBe(false);
  });

  it("falls back to the model's own default when nothing chose or reported", () => {
    const off: ModelInfo = { ...toggleable("m"), thinking_default_off: true };

    expect(thinkingIsOff(session({ model: "m" }), [off])).toBe(true);
    expect(thinkingIsOff(session({ model: "m", thinking_choice: "on" }), [off])).toBe(false);
    expect(thinkingIsOff(session({ model: "m", thinking_active: "on" }), [off])).toBe(false);
  });

  it("names Off on the pill for a default-off model the chat never touched", () => {
    const s = session({ model: "opus-5", effort: "high", effort_levels: fiveTiers() });
    const off: ModelInfo = { ...toggleable("opus-5"), thinking_default_off: true };

    expect(effortPillLabel(s, [off], "")).toBe("off");
  });
});
