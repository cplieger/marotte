// The Model and Effort selects on a workflow's Run and Schedule forms. A pure form over the catalog
// its caller passes in, so either form and its test read the same rule.

import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { effortLabel } from "./effort.js";
import type { ModelInfo } from "./types.js";

/** A run's model and effort pick as its request carries it: an absent key is "Default", which sends
 *  nothing, so KAS's own order and the recipe's pins stand. */
export interface RunDefaults {
  readonly model?: string;
  readonly effort?: string;
}

export interface RunDefaultsPicker {
  readonly el: HTMLElement;
  readonly value: () => RunDefaults;
  /** Rebuild for a newer catalog, keeping the pick; a no-op when the catalog is unchanged. */
  readonly setModels: (models: readonly ModelInfo[]) => void;
}

const DEFAULT = "";

/** `initial` may name a model, or a tier of it, the catalog no longer offers: each stays listed
 *  under its id, so a save keeps it and the server says why it is refused, rather than the form
 *  dropping it. */
export function buildRunDefaultsPicker(opts: {
  readonly models: readonly ModelInfo[];
  readonly initial: RunDefaults;
}): RunDefaultsPicker {
  const modelSel = el("select", { className: "run-pick-select" }) as HTMLSelectElement;
  const effortSel = el("select", { className: "run-pick-select" }) as HTMLSelectElement;
  const effortField = el(
    "label",
    { className: "run-pick" },
    el("span", { className: "run-pick-label" }, "Effort"),
    effortSel,
  );
  let models: readonly ModelInfo[] = [];
  let signature: string | undefined;
  let kept: RunDefaults = opts.initial;

  function listed(): readonly ModelInfo[] {
    const model = kept.model ?? DEFAULT;
    if (model === DEFAULT || models.some((m) => m.model_id === model)) {
      return models;
    }
    return [...models, { model_id: model, model_name: model }];
  }

  function paintModels(): void {
    modelSel.replaceChildren(
      el("option", { value: DEFAULT }, "Default"),
      ...listed().map((m) => el("option", { value: m.model_id }, m.model_name)),
    );
    modelSel.value = kept.model ?? DEFAULT;
    paintEfforts();
  }

  function tiersOf(modelID: string): readonly string[] {
    return models.find((m) => m.model_id === modelID)?.effort_tiers ?? [];
  }

  /** The chosen model's tiers, plus a kept tier it no longer offers; hidden when that leaves none,
   *  Default included. */
  function paintEfforts(): void {
    const model = models.find((m) => m.model_id === modelSel.value);
    const effort = modelSel.value === DEFAULT ? DEFAULT : (kept.effort ?? DEFAULT);
    const offered = model?.effort_tiers ?? [];
    const tiers = effort === DEFAULT || offered.includes(effort) ? offered : [...offered, effort];
    effortField.hidden = tiers.length === 0;
    const def = model?.default_effort_level ?? "";
    effortSel.replaceChildren(
      el(
        "option",
        { value: DEFAULT },
        def === "" ? "Default" : `Default (${effortLabel({ id: def })})`,
      ),
      ...tiers.map((id) => el("option", { value: id }, effortLabel({ id }))),
    );
    effortSel.value = effort;
  }

  function read(): RunDefaults {
    const model = modelSel.value;
    if (model === DEFAULT) {
      return {};
    }
    const effort = effortField.hidden ? DEFAULT : effortSel.value;
    return effort === DEFAULT ? { model } : { model, effort };
  }

  function setModels(next: readonly ModelInfo[]): void {
    const sig = joinKey(
      ...next.map((m) =>
        joinKey(m.model_id, m.model_name, m.default_effort_level ?? "", ...(m.effort_tiers ?? [])),
      ),
    );
    if (sig === signature) {
      return;
    }
    signature = sig;
    // The reader's current pick survives the rebuild, not the one the form opened on.
    if (modelSel.options.length > 0) {
      kept = read();
    }
    models = next;
    paintModels();
  }

  modelSel.addEventListener("change", () => {
    // A tier picked for the last model is kept when this one offers it too.
    const effort = effortSel.value;
    kept = {
      model: modelSel.value,
      ...(effort !== DEFAULT && tiersOf(modelSel.value).includes(effort) ? { effort } : {}),
    };
    paintEfforts();
  });
  effortSel.addEventListener("change", () => {
    kept = read();
  });

  setModels(opts.models);
  return {
    el: el(
      "div",
      { className: "run-picks" },
      el(
        "label",
        { className: "run-pick" },
        el("span", { className: "run-pick-label" }, "Model"),
        modelSel,
      ),
      effortField,
    ),
    value: read,
    setModels,
  };
}
