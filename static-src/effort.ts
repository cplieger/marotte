// Reasoning-effort vocabulary, a leaf so the model card and the model pill agree. The default always arrives per model
// on the catalog, never from a local table.

import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";

/**
 * The fallback vocabulary and label table, never the authority: that is the `effortLevel` option's own choices, per
 * session or pre-session.
 */
const EFFORT_LEVELS = [
  { id: "low", label: "low" },
  { id: "medium", label: "medium" },
  { id: "high", label: "high" },
  { id: "xhigh", label: "x-high" },
  { id: "max", label: "max" },
] as const;

/** Written once at boot: a bridgeless chat's only evidence. */
let catalogEfforts: readonly SessionEffortLevel[] = [];
let catalogEffortActive = "";

export function setCatalogEfforts(levels: readonly SessionEffortLevel[], active: string): void {
  catalogEfforts = levels;
  catalogEffortActive = active;
}

/** The catalog's name, else the house table (`xhigh` → "x-high"), else the id: hiding an offered tier is worse. */
export function effortLabel(level: SessionEffortLevel): string {
  if (level.name !== undefined && level.name !== "") {
    return level.name;
  }
  return EFFORT_LEVELS.find((l) => l.id === level.id)?.label ?? level.id;
}

/** The default vocabulary, for a control with no catalog behind it yet. */
function fallbackEffortLevels(): SessionEffortLevel[] {
  return EFFORT_LEVELS.map((l) => ({ id: l.id, name: l.label }));
}

/** The current model's own default tier, or "" when the catalog does not say. */
function modelDefaultEffort(session: Session | undefined, models: readonly ModelInfo[]): string {
  return models.find((m) => m.model_id === session?.model)?.default_effort_level ?? "";
}

/**
 * Tiers to render and the live tier. Levels: session catalog, else pre-session template, else the canonical five.
 * Live tier, highest first: the chat's choice, the session's reported level, the remembered pick, the model default.
 */
export function effortVocabulary(
  session: Session | undefined,
  models: readonly ModelInfo[],
  seed: string,
): { levels: readonly SessionEffortLevel[]; active: string } {
  const fromSession = session?.effort_levels ?? [];
  const levels =
    fromSession.length > 0
      ? fromSession
      : catalogEfforts.length > 0
        ? catalogEfforts
        : fallbackEffortLevels();
  const candidates: readonly string[] = [
    ifOffered(session?.effort ?? "", levels),
    session?.effort_active ?? "",
    ifOffered(seed, levels),
    modelDefaultEffort(session, models),
    catalogEffortActive,
  ];
  const active = candidates.find((level) => level !== "") ?? "";
  return { levels, active };
}

/** `level` when the current model offers it, else "" — the reconciliation both a
 *  chosen and a remembered level go through. */
function ifOffered(level: string, levels: readonly SessionEffortLevel[]): string {
  return level !== "" && levels.some((l) => l.id === level) ? level : "";
}

/** Whether two tier lists are the same sequence — the rebuild test. */
export function sameLevels(
  a: readonly SessionEffortLevel[],
  b: readonly SessionEffortLevel[],
): boolean {
  return a.length === b.length && a.every((l, i) => l.id === b[i]?.id && l.name === b[i].name);
}

/** A sentinel id no effort tier can carry, so a tier named "off" cannot collide. */
export const THINKING_OFF: SessionEffortLevel = { id: "thinking:off", name: "off" };

/**
 * Whether thinking is or will be off: session report, then the chat's choice, then the model default. Twin of
 * `marotte.Chat.ThinkingIsOff`; the two must agree.
 */
export function thinkingIsOff(session: Session | undefined, models: readonly ModelInfo[]): boolean {
  const active = session?.thinking_active ?? "";
  if (active !== "") {
    return active === "off";
  }
  const choice = session?.thinking_choice ?? "";
  if (choice !== "") {
    return choice === "off";
  }
  const model = session?.model ?? "";
  return models.some((m) => m.model_id === model && m.thinking_default_off === true);
}

/** Whether the current model lets thinking be turned off (`thinking_toggleable`). */
export function modelThinkingToggleable(models: readonly ModelInfo[], modelID: string): boolean {
  return models.some((m) => m.model_id === modelID && m.thinking_toggleable === true);
}

/** Whether the slider sits on Off: a toggleable model with thinking off. */
export function thinkingOffShown(
  session: Session | undefined,
  models: readonly ModelInfo[],
): boolean {
  return modelThinkingToggleable(models, session?.model ?? "") && thinkingIsOff(session, models);
}

/**
 * Whether the current model advertises effort (`_meta.kiro.hasEffort`). True when no entry carries the field: hiding
 * a working control on a missing field is worse.
 */
export function modelHasEffort(models: readonly ModelInfo[], modelID: string): boolean {
  let plumbed = false;
  let current = false;
  for (const m of models) {
    if (m.has_effort !== undefined) {
      plumbed = true;
      if (m.model_id === modelID) {
        current = m.has_effort;
      }
    }
  }
  return plumbed ? current : true;
}

/**
 * The tier to name on the model pill, a readout of the level in force. Empty when the model has no effort or nothing
 * resolved.
 */
export function effortPillLabel(
  session: Session | undefined,
  models: readonly ModelInfo[],
  seed: string,
): string {
  if (!modelHasEffort(models, session?.model ?? "")) {
    return "";
  }
  if (thinkingOffShown(session, models)) {
    return effortLabel(THINKING_OFF);
  }
  const { levels, active } = effortVocabulary(session, models, seed);
  if (active === "") {
    return "";
  }
  return effortLabel(levels.find((l) => l.id === active) ?? { id: active });
}
