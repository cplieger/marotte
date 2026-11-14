import { beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import { settingsPayload } from "./__test-helpers__/settings.js";
import type { EffectiveSettings } from "./persist.js";
import type { ModelInfo } from "./types.js";
import type { ModelFallback } from "./wire/types.gen.js";
import type * as Strings from "./strings.js";

let cachedModels: ModelInfo[] = [];

let notice: { text: string; busy: boolean } | null = null;

const { onExpand, onCollapse, effortDispatch, thinkingDispatch, fallbackDispatch } = vi.hoisted(
  () => ({
    onExpand: { fn: null as null | (() => void) },
    onCollapse: { fn: null as null | (() => void) },
    effortDispatch: vi.fn(),
    thinkingDispatch: vi.fn(),
    fallbackDispatch: vi.fn(),
  }),
);

/** Read-only as in production: the set_effort command writes the seed. */
let lastEffortByModel: Record<string, string> = {};

vi.mock("./pill-expand.js", () => ({
  makeExpandable: (
    _pill: HTMLElement,
    _content: HTMLElement,
    opts?: { onExpand?: () => void; onCollapse?: () => void },
  ) => {
    onExpand.fn = opts?.onExpand ?? null;
    onCollapse.fn = opts?.onCollapse ?? null;
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

// A real signal, as persist.ts's: the Fallback row repaints from it while the card is open. A write's
// answer enters the shown settings, as persist.ts's showFallbackAnswer does (persist.test.ts pins it).
vi.mock("./persist.js", async () => {
  const { signal } = (await vi.importActual("@cplieger/reactive")) as { signal: SignalFactory };
  const shownSettingsSignal = signal<EffectiveSettings | null>(null);
  return {
    shownSettingsSignal,
    settingsAsShown: () => shownSettingsSignal.value,
    showFallbackAnswer: (row: ModelFallback) => {
      const s = shownSettingsSignal.value;
      if (s !== null) {
        const rows = s.model_fallbacks.filter((f) => f.model !== row.model);
        shownSettingsSignal.value = { ...s, model_fallbacks: [...rows, row] };
      }
    },
  };
});
vi.mock("./settings.js", () => ({
  currentIdentity: () => ({
    state: "signed_in",
    email: "",
    method: "",
    startUrl: "",
    region: "",
    apiKey: false,
  }),
}));

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
  apiAction: () => ({ dispatch: vi.fn() }),
  retryNetwork: vi.fn(),
  RETRY_STANDARD: {},
}));
vi.mock("./actions/chat.js", () => ({ switchModel: { dispatch: vi.fn() } }));
vi.mock("./actions/model-fallback.js", () => ({ setModelFallback: { dispatch: fallbackDispatch } }));
vi.mock("./reconcile.js", () => ({ reconcile: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./session-context.js", () => ({
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
vi.mock("./legacy-ui/roving-focus.js", () => ({
  rovingFocus: () => ({ refresh: vi.fn(), focusFirst: vi.fn(), dispose: vi.fn() }),
}));

import { activeSession } from "./store.js";
import { shownSettingsSignal } from "./persist.js";
import { initModelSwitcher } from "./model-switcher.js";

function model(
  id: string,
  dflt?: string,
  toggleable?: boolean,
  tiers?: readonly string[],
): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
    ...(toggleable === true ? { thinking_toggleable: true } : {}),
    ...(tiers === undefined ? {} : { effort_tiers: tiers }),
  };
}

const FIVE = ["low", "medium", "high", "xhigh", "max"] as const;

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

/** The card's close, which is when its picks are sent. */
function closeCard(): void {
  onCollapse.fn?.();
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
    initModelSwitcher();
  });

  it("marks the tier the session reports running at when the chat chose none", () => {
    cachedModels = [model("model-a-4.7")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("xhigh");
  });

  it("marks the chat's own choice over the level the session reports", () => {
    cachedModels = [model("model-a-4.7")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
      effort: "low",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("low");
  });

  it("renders the tiers the session offers, not a fixed five", async () => {
    cachedModels = [model("model-b-5")];
    setSession({
      id: "c1",
      model: "model-b-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("rebuilds the tiers when the session's vocabulary changes", async () => {
    cachedModels = [model("model-a-4.7"), model("model-b-5")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });
    expect(tierCount()).toBe(5);

    setSession({
      id: "c1",
      model: "model-b-5",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "max" }],
    });

    expect(tierCount()).toBe(4);
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
    expect(markedTier()).toBe("medium");
  });

  it("uses the current model's own catalog tiers when no session catalog exists", async () => {
    cachedModels = [model("model-a-4.7", "high", false, ["low", "high"])];
    setSession({ id: "c1", model: "model-a-4.7", effort: "" });

    expect(tierCount()).toBe(2);
    await expect(tierEnds()).resolves.toEqual(["low", "high"]);
    expect(markedTier()).toBe("high");
  });

  it("offers a bridgeless chat on a model with fewer tiers that model's tiers", async () => {
    cachedModels = [
      model("model-a-5", "high", false, FIVE),
      model("model-b-5", "medium", false, ["low", "medium", "high"]),
    ];
    setSession({ id: "c1", model: "model-b-5", effort: "" });

    expect(tierCount()).toBe(3);
    await expect(tierEnds()).resolves.toEqual(["low", "high"]);
  });

  it("shows no effort row for a listed model with no tiers, even when no model has tiers", () => {
    cachedModels = [model("auto")];
    setSession({ id: "c1", model: "auto", effort: "" });

    openCard();
    expect(document.querySelector(".effort-row")).toBeNull();
    expect(document.querySelectorAll('[role="slider"]')).toHaveLength(0);
  });

  it("falls back to the canonical five with an empty catalog", async () => {
    cachedModels = [];
    setSession({ id: "c1", model: "older", effort: "" });

    expect(tierCount()).toBe(5);
    expect(markedTier()).toBe("low");
    // Last: the walk leaves the knob at the top tier and no source can rewind it here.
    await expect(tierEnds()).resolves.toEqual(["low", "max"]);
  });

  it("labels a tier by the catalog's name, else the house table", async () => {
    cachedModels = [model("model-a-4.7")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
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
    cachedModels = [model("model-a-4.7")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
      effort: "",
      effort_active: "medium",
      effort_levels: [{ id: "low" }, { id: "medium" }],
    });
    effortDispatch.mockClear();

    await press("{Home}");
    expect(
      effortDispatch,
      "a pick is previewed, never sent while the card is open",
    ).not.toHaveBeenCalled();
    closeCard();

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });

  it("steps one tier at a time, clamps at both ends and sends only where it stopped", async () => {
    cachedModels = [model("model-a-4.7")];
    setSession({
      id: "c1",
      model: "model-a-4.7",
      effort: "",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    const shown: string[] = [];
    openCard();
    knob().focus();
    for (const key of ["{ArrowLeft}", "{ArrowRight}", "{ArrowUp}", "{End}"]) {
      await userEvent.keyboard(key);
      shown.push(knob().dataset["level"] ?? "");
    }
    closeCard();

    expect(shown).toEqual(["low", "medium", "high", "max"]);
    expect(effortDispatch.mock.calls).toEqual([[{ chatID: "c1", level: "max" }]]);
  });

  it("does not mark a chosen level the current model does not offer", () => {
    cachedModels = [model("model-b-5", "medium")];
    setSession({
      id: "c1",
      effort: "max",
      model: "model-b-5",
      effort_active: "high",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    expect(markedTier()).toBe("high");
  });

  it("still marks a chosen level the model does offer", () => {
    cachedModels = [model("model-a-5", "high")];
    setSession({
      id: "c1",
      effort: "max",
      model: "model-a-5",
      effort_active: "high",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("max");
  });

  it("opens a new chat on the level the user last picked, under the same model", () => {
    lastEffortByModel = { "model-a-5": "max" };
    cachedModels = [model("model-a-5", "high", false, FIVE)];
    setSession({ id: "c1", model: "model-a-5", effort: "" });

    expect(markedTier()).toBe("max");
  });

  it("a level picked under ANOTHER model yields the current model's default", () => {
    lastEffortByModel = { "model-a-5": "max" };
    cachedModels = [model("model-e-mini", "medium")];
    setSession({ id: "c1", model: "model-e-mini", effort: "", effort_levels: fiveTiers() });

    expect(markedTier()).toBe("medium");
  });

  it("ignores a remembered level the current model does not offer", () => {
    lastEffortByModel = { "model-b-5": "max" };
    cachedModels = [model("model-b-5", "medium")];
    setSession({
      id: "c1",
      model: "model-b-5",
      effort: "",
      effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
    });

    expect(markedTier()).toBe("medium");
  });

  it("marks the level the session reports over the remembered pick", () => {
    lastEffortByModel = { "model-a-5": "low" };
    cachedModels = [model("model-a-5", "high")];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "",
      effort_active: "xhigh",
      effort_levels: fiveTiers(),
    });

    expect(markedTier()).toBe("xhigh");
  });

  it("sends nothing when the pick is the level this chat already chose", async () => {
    cachedModels = [model("model-a-5")];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "low",
      effort_active: "low",
      effort_levels: fiveTiers(),
    });
    effortDispatch.mockClear();

    await press("{Home}", "{Home}", "{Home}");
    closeCard();

    expect(effortDispatch).not.toHaveBeenCalled();
  });

  it("still sends when the pick is the marked tier but not this chat's choice", async () => {
    cachedModels = [model("model-a-5", "max")];
    setSession({ id: "c1", model: "model-a-5", effort: "", effort_levels: fiveTiers() });
    effortDispatch.mockClear();
    expect(markedTier()).toBe("max");

    await press("{End}");
    closeCard();

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
    initModelSwitcher();
    effortDispatch.mockClear();
    thinkingDispatch.mockClear();
  });

  it("leads the range with Off on a model whose thinking can be turned off", async () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({ id: "c1", model: "model-a-5", effort: "high", effort_levels: fiveTiers() });

    expect(tierCount()).toBe(6);
    expect(await tierEnds()).toEqual(["thinking:off", "max"]);
  });

  it("offers no Off stop on a model whose thinking cannot be turned off", async () => {
    cachedModels = [model("model-a-5", "high")];
    setSession({ id: "c1", model: "model-a-5", effort: "high", effort_levels: fiveTiers() });

    expect(tierCount()).toBe(5);
    expect((await tierEnds())[0]).toBe("low");
  });

  it("sits on Off when the session reports thinking off", () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    expect(markedTier()).toBe("thinking:off");
    expect(document.querySelector(".effort-value")?.textContent).toBe("off");
  });

  it("sits on Off for a default-off model the chat never chose", () => {
    cachedModels = [{ ...model("model-a-5", "high", true), thinking_default_off: true }];
    setSession({ id: "c1", model: "model-a-5", effort: "high", effort_levels: fiveTiers() });

    expect(markedTier()).toBe("thinking:off");
  });

  it("trusts the session's report over the chat's own choice", () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "high",
      effort_levels: fiveTiers(),
      thinking_choice: "off",
      thinking_active: "on",
    });

    expect(markedTier()).toBe("high");
  });

  it("sends set_thinking, not set_effort, when the reader picks Off", async () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "on",
    });

    await press("{Home}");
    closeCard();

    expect(thinkingDispatch).toHaveBeenCalledWith({ chatID: "c1" });
    expect(effortDispatch).not.toHaveBeenCalled();
  });

  it("sends nothing when Off is picked while thinking is already off", async () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    await press("{Home}");
    closeCard();

    expect(thinkingDispatch).not.toHaveBeenCalled();
  });

  it("still sends the chat's own tier while thinking is off, because that turns it back on", async () => {
    cachedModels = [model("model-a-5", "high", true)];
    setSession({
      id: "c1",
      model: "model-a-5",
      effort: "low",
      effort_levels: fiveTiers(),
      thinking_active: "off",
    });

    await press("{ArrowRight}");
    closeCard();

    expect(effortDispatch).toHaveBeenCalledWith({ chatID: "c1", level: "low" });
  });
});

describe("the Fallback row", () => {
  beforeEach(() => {
    document.body.replaceChildren();
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    cachedModels = [model("model-a", "high"), model("model-b", "high")];
    notice = null;
    shownSettingsSignal.value = null;
    initModelSwitcher();
    setSession({ id: "c1", model: "model-a", effort: "", effort_levels: fiveTiers() });
  });

  function answered(configured: string, vendorDefault = ""): ModelFallback {
    return { model: "model-a", configured, vendor_default: vendorDefault, read_only_reason: "" };
  }

  function shown(enabled: boolean, configured = "model-b", vendorDefault = ""): EffectiveSettings {
    return settingsPayload({
      model_fallback_enabled: enabled,
      model_fallbacks: [answered(configured, vendorDefault)],
    });
  }

  function fallbackSelect(): HTMLSelectElement | null {
    return document.querySelector<HTMLSelectElement>(
      '#model-switch-list select[aria-label="Fallback model"]',
    );
  }

  it("sits under the effort slider for the chat's model, showing the account's choice", () => {
    shownSettingsSignal.value = shown(true);
    openCard();
    const rows = [...document.querySelectorAll("#model-switch-list > .effort-row")];
    expect(rows).toHaveLength(2);
    expect(rows[0]?.querySelector('[role="slider"]')).not.toBeNull();
    expect(rows[1]?.contains(fallbackSelect())).toBe(true);
    expect(fallbackSelect()?.value).toBe("model-b");
  });

  it("appears and disappears on a live flip while the picker is open", () => {
    shownSettingsSignal.value = shown(false);
    openCard();
    expect(fallbackSelect()).toBeNull();
    shownSettingsSignal.value = shown(true);
    expect(fallbackSelect()?.value).toBe("model-b");
    shownSettingsSignal.value = shown(false);
    expect(fallbackSelect()).toBeNull();
  });

  /** Makes the next fallback write wait for its settle: answered with `answer`, or refused. */
  function pendingWrite(): { answer: (row: ModelFallback) => void; refuse: () => void } {
    const out = { answer: (_row: ModelFallback) => undefined as void, refuse: () => undefined as void };
    fallbackDispatch.mockImplementationOnce(
      (
        _args: unknown,
        opts: { onSuccess: (a: ModelFallback) => void; onError: (e: { code: string }) => void },
      ) =>
        new Promise<void>((resolve) => {
          out.answer = (row) => {
            opts.onSuccess(row);
            resolve();
          };
          out.refuse = () => {
            opts.onError({ code: "locked" });
            resolve();
          };
        }),
    );
    return out;
  }

  async function pick(target: string): Promise<HTMLSelectElement> {
    const s = fallbackSelect();
    if (s === null) {
      throw new Error("no Fallback select");
    }
    s.focus();
    await userEvent.selectOptions(s, target);
    return s;
  }

  async function settled(s: HTMLSelectElement): Promise<void> {
    await vi.waitFor(() => {
      expect(s.getAttribute("aria-busy")).toBeNull();
    });
  }

  it("keeps the focused select and the answered target through the write's settings refetch", async () => {
    cachedModels = [...cachedModels, model("model-c", "high")];
    const write = pendingWrite();
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = await pick("model-c");
    expect(fallbackDispatch).toHaveBeenCalledOnce();

    // While the write is pending, a pushed view still holding the old target; then the write's
    // settings_updated steps (announce, then the refetch's adopt) after the answer.
    shownSettingsSignal.value = shown(true, "model-b", "model-b");
    expect(s.value).toBe("model-c");
    write.answer(answered("model-c"));
    await settled(s);
    expect(s.value).toBe("model-c");
    shownSettingsSignal.value = shown(true, "model-c");

    expect(fallbackSelect()).toBe(s);
    expect(document.activeElement).toBe(s);
    expect(s.value).toBe("model-c");
  });

  it("removes the row for a switch-off that came during a refused write", async () => {
    cachedModels = [...cachedModels, model("model-c", "high")];
    const write = pendingWrite();
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = await pick("model-c");
    shownSettingsSignal.value = shown(false);
    write.refuse();
    await settled(s);
    expect(fallbackSelect()).toBeNull();
  });

  it("removes the row for a switch-off that came during an answered write", async () => {
    cachedModels = [...cachedModels, model("model-c", "high")];
    const write = pendingWrite();
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = await pick("model-c");
    shownSettingsSignal.value = shown(false);
    write.answer(answered("model-c"));
    await settled(s);
    expect(fallbackSelect()).toBeNull();
  });

  it("shows another device's target that came during a refused write", async () => {
    cachedModels = [...cachedModels, model("model-c", "high"), model("model-d", "high")];
    const write = pendingWrite();
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = await pick("model-c");
    shownSettingsSignal.value = shown(true, "model-d");
    write.refuse();
    await settled(s);
    expect(fallbackSelect()).toBe(s);
    expect(s.value).toBe("model-d");
  });

  it("paints another device's write-back of the old target before the refetch", async () => {
    cachedModels = [...cachedModels, model("model-c", "high")];
    const write = pendingWrite();
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = await pick("model-c");
    write.answer(answered("model-c"));
    await settled(s);
    expect(s.value).toBe("model-c");
    // The refetch adopts what KAS holds: another device wrote model-b back meanwhile.
    shownSettingsSignal.value = shown(true, "model-b");
    expect(s.value).toBe("model-b");
  });

  it("repaints in place when another device changes the choice", () => {
    cachedModels = [...cachedModels, model("model-c", "high")];
    shownSettingsSignal.value = shown(true);
    openCard();
    const s = fallbackSelect();
    shownSettingsSignal.value = shown(true, "model-c");
    expect(fallbackSelect()).toBe(s);
    expect(s?.value).toBe("model-c");
  });

  it("leaves a closed card alone on a settings change", () => {
    shownSettingsSignal.value = shown(false);
    openCard();
    closeCard();
    shownSettingsSignal.value = shown(true);
    expect(fallbackSelect()).toBeNull();
  });
});
