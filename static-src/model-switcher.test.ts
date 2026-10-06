import { beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { ModelInfo } from "./types.js";
import type * as Strings from "./strings.js";

let cachedModels: ModelInfo[] = [];

let notice: { text: string; busy: boolean } | null = null;

const { onExpand, effortDispatch, thinkingDispatch } = vi.hoisted(() => ({
  onExpand: { fn: null as null | (() => void) },
  effortDispatch: vi.fn(),
  thinkingDispatch: vi.fn(),
}));

/** Read-only as in production: the set_effort command writes the seed. */
let lastEffortByModel: Record<string, string> = {};

vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _content: HTMLElement, opts?: { onExpand?: () => void }) => {
    onExpand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));

/** Local, so the mock factory need not reach for an `import()` type. */
interface SignalOf<T> {
  value: T;
}
type SignalFactory = <T>(initial: T) => SignalOf<T>;
interface TestSession {
  id: string;
  model: string;
  effort: string;
  effort_active?: string;
  effort_levels?: { id: string; name?: string }[];
  thinking_choice?: string;
  thinking_active?: string;
}

// Real reactive store: the mark rides an effect over activeSession.
vi.mock("./store.js", async () => {
  const { signal } = (await vi.importActual("@cplieger/reactive")) as { signal: SignalFactory };
  const activeSession = signal<TestSession | undefined>(undefined);
  return {
    activeSession,
    get: () => activeSession.value,
    getActive: () => activeSession.value,
    getActiveId: () => activeSession.value?.id ?? "",
    isEmptyChat: () => false,
    isThinking: () => false,
    setEffort: vi.fn(),
    setModel: vi.fn(),
    setThinkingChoice: vi.fn(),
  };
});

vi.mock("./picker.js", () => ({
  getCachedModels: () => cachedModels,
  refreshPickerIfVisible: vi.fn(),
  catalogNotice: () => notice,
  // Browser Mode links ESM for real: a missing name fails collection, not one case.
  retryCatalog: vi.fn(),
  RETRY_LABEL: "Retry loading the model list",
}));
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(),
  // Two actions share this factory; the name routes each to its own spy.
  transportAction: (def: { name: string }) => ({
    dispatch: def.name === "chat.set_thinking" ? thinkingDispatch : effortDispatch,
  }),
  retryNetwork: vi.fn(),
  RETRY_STANDARD: {},
}));
vi.mock("./actions/chat.js", () => ({ switchModel: { dispatch: vi.fn() } }));
vi.mock("./reconcile.js", () => ({ reconcile: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./session-context.js", () => ({
  setCurrentModel: vi.fn(),
  setLastModel: vi.fn(),
  getLastEffortFor: (model: string) =>
    model !== "" && Object.hasOwn(lastEffortByModel, model) ? (lastEffortByModel[model] ?? "") : "",
}));
// Only `humanName` is stubbed; `rateLabel` stays real because it decides whether
// a credit readout exists at all.
vi.mock("./strings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Strings>()),
  humanName: (s: string) => s,
}));
vi.mock("./icon-el.js", () => ({ iconEl: () => document.createElement("span") }));
vi.mock("./icons.js", () => ({ ICON_MODEL: "" }));
vi.mock("@cplieger/ui-primitives/roving-focus", () => ({
  rovingFocus: () => ({ refresh: vi.fn(), focusFirst: vi.fn(), dispose: vi.fn() }),
}));

import { activeSession } from "./store.js";
import { initModelSwitcher } from "./model-switcher.js";
import { setCatalogEfforts } from "./effort.js";

function model(id: string, dflt?: string, toggleable?: boolean): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
    ...(toggleable === true ? { thinking_toggleable: true } : {}),
  };
}

/** Nothing draws tiers, so the vocabulary is read off the ARIA range and the knob's tier. */
function openCard(): void {
  onExpand.fn?.();
}

function tierCount(): number {
  openCard();
  return Number(knob().getAttribute("aria-valuemax")) + 1;
}

/** The walk leaves the knob at the top tier; re-open through `markedTier` afterwards. */
async function tierEnds(): Promise<readonly [string, string]> {
  openCard();
  knob().focus();
  await userEvent.keyboard("{Home}");
  const low = knob().dataset["level"] ?? "";
  await userEvent.keyboard("{End}");
  return [low, knob().dataset["level"] ?? ""] as const;
}

function knob(): HTMLElement {
  const found = [...document.querySelectorAll<HTMLElement>('[role="slider"]')];
  expect(found, "the card renders exactly one slider").toHaveLength(1);
  return found[0] as HTMLElement;
}

/** Cross-checks both ARIA channels: one function writes them with the position. */
function markedTier(): string {
  openCard();
  const k = knob();
  const now = Number(k.getAttribute("aria-valuenow"));
  const max = Number(k.getAttribute("aria-valuemax"));
  expect(k.getAttribute("aria-valuemin")).toBe("0");
  expect(now, "the reported index is inside the reported range").toBeGreaterThanOrEqual(0);
  expect(now).toBeLessThanOrEqual(max);
  expect(k.getAttribute("aria-valuetext"), "the announced tier is the word the caption names").toBe(
    document.querySelector(".effort-value")?.textContent ?? "",
  );
  return k.dataset["level"] ?? "";
}

/** Runs inside ONE open: the mocked store would rewind the knob on a re-open. */
async function press(...keys: readonly string[]): Promise<void> {
  openCard();
  knob().focus();
  for (const key of keys) {
    await userEvent.keyboard(key);
  }
}

function setSession(s: TestSession): void {
  (activeSession as unknown as { value: TestSession }).value = s;
}

function fiveTiers(): { id: string; name?: string }[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

describe("the effort section", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    cachedModels = [];
    notice = null;
    onExpand.fn = null;
    lastEffortByModel = {};
    setCatalogEfforts([], "");
    initModelSwitcher();
  });

  it("marks the tier the session reports running at when the chat chose none", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("xhigh");
  });

  it("marks the chat's own choice over the level the session reports", () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "low",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("low");
  });

  it("renders the tiers the session offers, not a fixed five", async () => {
    cachedModels = [model("sonnet-5")];
    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("rebuilds the tiers when the session's vocabulary changes", async () => {
    cachedModels = [model("opus-4.7"), model("sonnet-5")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });
    expect(tierCount()).toBe(5);

    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("uses the pre-session catalog and the model default when no session catalog exists", async () => {
    setCatalogEfforts([{ id: "low" }, { id: "high" }], "low");
    cachedModels = [model("opus-4.7", "high")];
    setSession({ id: "c1", model: "opus-4.7", effort: "" });

    expect(tierCount()).toBe(2);
    await expect(tierEnds()).resolves.toEqual(["low", "high"]);
    expect(markedTier()).toBe("high");
  });

  it("falls back to the canonical five when nothing has landed yet", async () => {
    cachedModels = [model("older")];
    setSession({ id: "c1", model: "older", effort: "" });

    expect(tierCount()).toBe(5);
    expect(markedTier()).toBe("low");
    // Last: the walk leaves the knob at the top tier and no source can rewind it here.
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
  });

  it("labels a tier by the catalog's name, else the house table", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: [{ id: "low", name: "Low effort" }, { id: "xhigh" }],
    });

    expect(tierCount()).toBe(2);
    openCard();
    expect(knob().getAttribute("aria-valuetext")).toBe("x-high");
    await press("{Home}");
    expect(knob().getAttribute("aria-valuetext")).toBe("Low effort");
  });

  it("dispatches the tier a keyboard step names", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }],
    });
    effortDispatch.mockClear();

    await press("{Home}");

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });

  it("steps one tier at a time and clamps at both ends", async () => {
    cachedModels = [model("opus-4.7")];
    setSession({
      id: "c1",
      model: "opus-4.7",
      effort: "",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    await press("{ArrowLeft}", "{ArrowRight}", "{ArrowUp}", "{End}");

    expect(effortDispatch.mock.calls.map((c) => (c[0] as { level: string }).level)).toEqual([
      "low",
      "medium",
      "high",
      "max",
    ]);
  });

  it("does not mark a chosen level the current model does not offer", () => {
    cachedModels = [model("sonnet-5", "medium")];
    setSession({
      id: "c1",
      effort: "max",
      model: "sonnet-5",
      effort_active: "high",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    expect(markedTier()).toBe("high");
  });

  it("still marks a chosen level the model does offer", () => {
    cachedModels = [model("opus-5", "high")];
    setSession({
      id: "c1",
      effort: "max",
      model: "opus-5",
      effort_active: "high",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("max");
  });

  it("opens a new chat on the level the user last picked, under the same model", () => {
    lastEffortByModel = { "opus-5": "max" };
    setCatalogEfforts(fiveTiers(), "high");
    cachedModels = [model("opus-5", "high")];
    setSession({ id: "c1", model: "opus-5", effort: "" });

    expect(markedTier()).toBe("max");
  });

  it("a level picked under ANOTHER model yields the current model's default", () => {
    lastEffortByModel = { "opus-5": "max" };
    setCatalogEfforts(fiveTiers(), "high");
    cachedModels = [model("gpt-luna", "medium")];
    setSession({ id: "c1", model: "gpt-luna", effort: "", effort_levels: fiveTiers() });

    expect(markedTier()).toBe("medium");
  });

  it("ignores a remembered level the current model does not offer", () => {
    lastEffortByModel = { "sonnet-5": "max" };
    cachedModels = [model("sonnet-5", "medium")];
    setSession({
      id: "c1",
      model: "sonnet-5",
      effort: "",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    expect(markedTier()).toBe("medium");
  });

  it("marks the level the session reports over the remembered pick", () => {
    lastEffortByModel = { "opus-5": "low" };
    cachedModels = [model("opus-5", "high")];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("xhigh");
  });

  it("sends nothing when the pick is the level this chat already chose", async () => {
    cachedModels = [model("opus-5")];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "low",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    await press("{Home}", "{Home}", "{Home}");

    expect(effortDispatch).not.toHaveBeenCalled();
  });

  it("still sends when the pick is the marked tier but not this chat's choice", async () => {
    cachedModels = [model("opus-5", "max")];
    setSession({ id: "c1", model: "opus-5", effort: "", effort_levels: fiveTiers() });
    effortDispatch.mockClear();
    expect(markedTier()).toBe("max");

    await press("{End}");

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "max" });
  });
});

describe("the effort slider's thinking Off stop", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    cachedModels = [];
    notice = null;
    onExpand.fn = null;
    lastEffortByModel = {};
    setCatalogEfforts([], "");
    initModelSwitcher();
    effortDispatch.mockClear();
    thinkingDispatch.mockClear();
  });

  it("leads the range with Off on a model whose thinking can be turned off", async () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({ id: "c1", model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    expect(tierCount()).toBe(6);
    expect(await tierEnds()).toEqual(["thinking:off", "max"]);
  });

  it("offers no Off stop on a model whose thinking cannot be turned off", async () => {
    cachedModels = [model("opus-5", "high")];
    setSession({ id: "c1", model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    expect(tierCount()).toBe(5);
    expect((await tierEnds())[0]).toBe("low");
  });

  it("sits on Off when the session reports thinking off", () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    expect(markedTier()).toBe("thinking:off");
    expect(document.querySelector(".effort-value")?.textContent).toBe("off");
  });

  it("sits on Off for a default-off model the chat never chose", () => {
    cachedModels = [{ ...model("opus-5", "high", true), thinking_default_off: true }];
    setSession({ id: "c1", model: "opus-5", effort: "high", effort_levels: fiveTiers() });

    expect(markedTier()).toBe("thinking:off");
  });

  it("trusts the session's report over the chat's own choice", () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_choice: "off",
      thinking_active: "on",
    });

    expect(markedTier()).toBe("high");
  });

  it("sends set_thinking, not set_effort, when the reader picks Off", async () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "on",
    });

    await press("{Home}");

    expect(thinkingDispatch).toHaveBeenCalledWith({ chatID: "c1" });
    expect(effortDispatch).not.toHaveBeenCalled();
  });

  it("sends nothing when Off is picked while thinking is already off", async () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    await press("{Home}");

    expect(thinkingDispatch).not.toHaveBeenCalled();
  });

  it("still sends the chat's own tier while thinking is off, because that turns it back on", async () => {
    cachedModels = [model("opus-5", "high", true)];
    setSession({
      id: "c1",
      model: "opus-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    await press("{ArrowRight}");

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });
});
