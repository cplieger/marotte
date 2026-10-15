import { describe, it, expect } from "vitest";
import { buildRunDefaultsPicker } from "./run-defaults-picker.js";
import type { ModelInfo } from "./types.js";

const MODELS: readonly ModelInfo[] = [
  {
    model_id: "opus",
    model_name: "Opus",
    effort_tiers: ["low", "high", "xhigh"],
    default_effort_level: "high",
  },
  { model_id: "sonnet", model_name: "Sonnet", effort_tiers: ["low", "medium"] },
  { model_id: "auto", model_name: "Auto" },
];

function selects(root: HTMLElement): { model: HTMLSelectElement; effort: HTMLSelectElement } {
  const [model, effort] = [...root.querySelectorAll<HTMLSelectElement>(".run-pick-select")];
  if (model === undefined || effort === undefined) {
    throw new Error("the picker has no Model or no Effort select");
  }
  return { model, effort };
}

function choose(sel: HTMLSelectElement, value: string): void {
  sel.value = value;
  sel.dispatchEvent(new Event("change"));
}

function effortField(effort: HTMLSelectElement): HTMLElement {
  const field = effort.closest<HTMLElement>(".run-pick");
  if (field === null) {
    throw new Error("the Effort select has no field");
  }
  return field;
}

describe("the run's Model and Effort picker", () => {
  it("opens on Default, hides Effort, and sends nothing", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: {} });
    const { model, effort } = selects(p.el);
    expect(model.value).toBe("");
    expect([...model.options].map((o) => o.textContent)).toEqual([
      "Default",
      "Opus",
      "Sonnet",
      "Auto",
    ]);
    expect(effortField(effort).hidden).toBe(true);
    expect(p.value()).toEqual({});
  });

  it("lists the chosen model's tiers, labelled, under its own default", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: {} });
    const { model, effort } = selects(p.el);
    choose(model, "opus");
    expect(effortField(effort).hidden).toBe(false);
    expect([...effort.options].map((o) => o.textContent)).toEqual([
      "Default (high)",
      "low",
      "high",
      "x-high",
    ]);
    expect(p.value()).toEqual({ model: "opus" });
    choose(effort, "xhigh");
    expect(p.value()).toEqual({ model: "opus", effort: "xhigh" });
  });

  it("keeps a tier the next model offers and drops one it does not", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: { model: "opus", effort: "low" } });
    const { model, effort } = selects(p.el);
    choose(model, "sonnet");
    expect(p.value()).toEqual({ model: "sonnet", effort: "low" });
    choose(model, "opus");
    choose(effort, "xhigh");
    choose(model, "sonnet");
    expect(effort.value).toBe("");
    expect(p.value()).toEqual({ model: "sonnet" });
  });

  it("hides Effort for a model with no tiers and sends none for it", () => {
    const p = buildRunDefaultsPicker({
      models: MODELS,
      initial: { model: "opus", effort: "high" },
    });
    const { model, effort } = selects(p.el);
    choose(model, "auto");
    expect(effortField(effort).hidden).toBe(true);
    expect(p.value()).toEqual({ model: "auto" });
  });

  it("keeps a stored model the catalog no longer offers, under its id", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: { model: "gone-5" } });
    const { model } = selects(p.el);
    expect(model.value).toBe("gone-5");
    expect(model.selectedOptions[0]?.textContent).toBe("gone-5");
    expect(p.value()).toEqual({ model: "gone-5" });
  });

  it("takes a catalog that lands after it opened, keeping the reader's pick", () => {
    const p = buildRunDefaultsPicker({ models: [], initial: {} });
    const { model, effort } = selects(p.el);
    expect([...model.options].map((o) => o.value)).toEqual([""]);
    p.setModels(MODELS);
    choose(model, "opus");
    choose(effort, "low");
    p.setModels([...MODELS, { model_id: "haiku", model_name: "Haiku" }]);
    expect(model.options).toHaveLength(5);
    expect(p.value()).toEqual({ model: "opus", effort: "low" });
  });

  it("repaints a catalog that changes only a model's default tier", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: { model: "opus" } });
    const { effort } = selects(p.el);
    expect(effort.options[0]?.textContent).toBe("Default (high)");
    p.setModels(
      MODELS.map((m) => (m.model_id === "opus" ? { ...m, default_effort_level: "low" } : m)),
    );
    expect(effort.options[0]?.textContent).toBe("Default (low)");
  });

  it("keeps a stored tier its model no longer offers, so the save sends it", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: { model: "opus", effort: "max" } });
    const { effort } = selects(p.el);
    expect(effortField(effort).hidden).toBe(false);
    expect(effort.value).toBe("max");
    expect(p.value()).toEqual({ model: "opus", effort: "max" });
    p.setModels([...MODELS, { model_id: "haiku", model_name: "Haiku" }]);
    expect(p.value()).toEqual({ model: "opus", effort: "max" });
  });

  it("keeps a stored tier under a stored model the catalog no longer offers", () => {
    const p = buildRunDefaultsPicker({
      models: MODELS,
      initial: { model: "gone-5", effort: "max" },
    });
    const { effort } = selects(p.el);
    expect(effortField(effort).hidden).toBe(false);
    expect(p.value()).toEqual({ model: "gone-5", effort: "max" });
  });

  it("drops a stored tier its model no longer offers once the reader picks another model", () => {
    const p = buildRunDefaultsPicker({ models: MODELS, initial: { model: "opus", effort: "max" } });
    const { model, effort } = selects(p.el);
    choose(model, "sonnet");
    expect([...effort.options].map((o) => o.value)).toEqual(["", "low", "medium"]);
    expect(p.value()).toEqual({ model: "sonnet" });
  });
});
