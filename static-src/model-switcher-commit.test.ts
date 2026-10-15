import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { ModelInfo } from "./types.js";
import type * as Strings from "./strings.js";
import type * as Store from "./store.js";
import type * as Picker from "./picker.js";
import type * as Actions from "./actions/index.js";
import type * as ChatActions from "./actions/chat.js";
import type * as SessionContext from "./session-context.js";

// The real pill-expand and reconcile: the send rides the popup's own close paths (a model
// click, Escape, a click away), so a mocked popup would prove nothing about them.

let cachedModels: ModelInfo[] = [];

const { switchDispatch, effortDispatch, thinkingDispatch } = vi.hoisted(() => ({
  switchDispatch: vi.fn(),
  effortDispatch: vi.fn(),
  thinkingDispatch: vi.fn(),
}));

interface SignalOf<T> {
  value: T;
}
type SignalFactory = <T>(initial: T) => SignalOf<T>;
interface TestSession {
  id: string;
  model: string;
  effort: string;
  effort_levels?: { id: string }[];
  thinking_active?: string;
  pending_model?: string;
  pending_effort?: string;
}

vi.mock("./store.js", async (importOriginal) => {
  const { signal } = (await vi.importActual("@cplieger/reactive")) as { signal: SignalFactory };
  const activeSession = signal<TestSession | undefined>(undefined);
  return {
    ...(await importOriginal<typeof Store>()),
    activeSession,
    get: () => activeSession.value,
    getActive: () => activeSession.value,
    isEmptyChat: () => false,
    setEffort: vi.fn(),
    setModel: vi.fn(),
    setThinkingChoice: vi.fn(),
  };
});
vi.mock("./picker.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Picker>()),
  getCachedModels: () => cachedModels,
  refreshPickerIfVisible: vi.fn(),
  catalogNotice: () => null,
}));
vi.mock("./actions/index.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Actions>()),
  bindLoadingState: vi.fn(),
  transportAction: (def: { name: string }) => ({
    dispatch: def.name === "chat.set_thinking" ? thinkingDispatch : effortDispatch,
  }),
}));
vi.mock("./actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  switchModel: { dispatch: switchDispatch },
}));
vi.mock("./session-context.js", async (importOriginal) => ({
  ...(await importOriginal<typeof SessionContext>()),
  setCurrentModel: vi.fn(),
  setLastModel: vi.fn(),
  getLastEffortFor: () => "",
}));
vi.mock("./strings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Strings>()),
  humanName: (s: string) => s,
}));

import { activeSession } from "./store.js";
import { initModelSwitcher } from "./model-switcher.js";
import { collapseAll } from "./pill-expand.js";

const FIVE = [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];

function model(id: string, toggleable = false): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    ...(toggleable ? { thinking_toggleable: true } : {}),
  };
}

function pill(): HTMLElement {
  const el = document.getElementById("switch-model-btn");
  expect(el, "the fixture lost its pill").not.toBeNull();
  return el as HTMLElement;
}

function knob(): HTMLElement {
  const el = document.querySelector<HTMLElement>('[role="slider"]');
  expect(el, "the open card rendered no slider").not.toBeNull();
  return el as HTMLElement;
}

function option(id: string): HTMLElement {
  const el = document.querySelector<HTMLElement>(`[data-model="${id}"]`);
  expect(el, `the open card rendered no ${id} row`).not.toBeNull();
  return el as HTMLElement;
}

/** The popup arms its Escape and outside-click listeners a task after it opens. */
async function open(): Promise<void> {
  await userEvent.click(pill());
  expect(pill().getAttribute("aria-expanded"), "the card opened").toBe("true");
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function keys(...pressed: readonly string[]): Promise<void> {
  knob().focus();
  for (const key of pressed) {
    await userEvent.keyboard(key);
  }
}

function closed(): boolean {
  return pill().getAttribute("aria-expanded") === "false";
}

function noSend(): void {
  expect(switchDispatch).not.toHaveBeenCalled();
  expect(effortDispatch).not.toHaveBeenCalled();
  expect(thinkingDispatch).not.toHaveBeenCalled();
}

describe("the model card sends once, when it closes", () => {
  // One fixture: the controller and its popup are app-lifetime singletons.
  beforeAll(() => {
    const slot = document.createElement("div");
    slot.className = "pill-slot";
    const btn = document.createElement("button");
    btn.id = "switch-model-btn";
    btn.type = "button";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    slot.append(btn, card);
    document.body.append(slot);
    initModelSwitcher();
  });

  beforeEach(() => {
    // A failed case can leave the card open; its close must not count against the next case.
    collapseAll();
    cachedModels = [model("opus-5"), model("sonnet-5")];
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "opus-5",
      effort: "medium",
      effort_levels: FIVE,
    };
    switchDispatch.mockReset();
    effortDispatch.mockReset().mockResolvedValue(undefined);
    thinkingDispatch.mockReset().mockResolvedValue(undefined);
  });

  it("sends a tier and the model picked after it as one switch, on the model click", async () => {
    await open();
    await keys("{End}");
    expect(knob().dataset["level"], "the card previews the pick at once").toBe("max");
    noSend();

    await userEvent.click(option("sonnet-5"));

    expect(closed(), "a model click closes the card").toBe(true);
    expect(switchDispatch.mock.calls).toEqual([
      [{ chatID: "c1", model: "sonnet-5", effort: "max" }],
    ]);
    expect(
      effortDispatch,
      "the tier rides the switch, not a set_effort of its own",
    ).not.toHaveBeenCalled();
  });

  it("sends nothing when the card closes with nothing changed", async () => {
    await open();
    await userEvent.keyboard("{Escape}");

    expect(closed()).toBe(true);
    noSend();
  });

  it("sends nothing when the knob ends where the chat already is", async () => {
    await open();
    await keys("{ArrowRight}", "{ArrowLeft}");
    await userEvent.keyboard("{Escape}");

    expect(closed()).toBe(true);
    noSend();
  });

  it("sends a tier alone on Escape", async () => {
    await open();
    await keys("{ArrowRight}");
    noSend();
    await userEvent.keyboard("{Escape}");

    expect(effortDispatch.mock.calls).toEqual([[{ chatID: "c1", level: "high" }]]);
    expect(switchDispatch).not.toHaveBeenCalled();
  });

  it("sends a tier alone on a click away", async () => {
    await open();
    await keys("{Home}");
    await userEvent.click(document.body);

    expect(closed()).toBe(true);
    expect(effortDispatch.mock.calls).toEqual([[{ chatID: "c1", level: "low" }]]);
    expect(switchDispatch).not.toHaveBeenCalled();
  });

  it("sends a tier alone when the model clicked is the one the chat runs", async () => {
    await open();
    await keys("{End}");
    await userEvent.click(option("opus-5"));

    expect(effortDispatch.mock.calls).toEqual([[{ chatID: "c1", level: "max" }]]);
    expect(switchDispatch).not.toHaveBeenCalled();
  });

  it("sends a model alone, with no tier, when the knob did not move", async () => {
    await open();
    await userEvent.click(option("sonnet-5"));

    expect(switchDispatch.mock.calls).toEqual([[{ chatID: "c1", model: "sonnet-5" }]]);
    expect(effortDispatch).not.toHaveBeenCalled();
  });

  // Mid-turn the card stands on the parked pair: the knob shows the parked tier, and moving it
  // back to the chat's own tier is a pick the switch carries.
  it("opens on a parked tier and sends the chat's own tier picked back over it", async () => {
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "opus-5",
      effort: "medium",
      effort_levels: FIVE,
      pending_model: "sonnet-5",
      pending_effort: "high",
    };
    await open();
    expect(knob().dataset["level"], "the knob shows the parked tier").toBe("high");
    await keys("{ArrowLeft}");
    await userEvent.click(option("sonnet-5"));

    expect(switchDispatch.mock.calls).toEqual([
      [{ chatID: "c1", model: "sonnet-5", effort: "medium" }],
    ]);
    expect(effortDispatch).not.toHaveBeenCalled();
  });

  /** The close sent exactly this one switch: no stop of its own beside it. */
  function sentOnly(payload: { model: string; effort?: string }): void {
    expect(switchDispatch.mock.calls).toEqual([[{ chatID: "c1", ...payload }]]);
    expect(effortDispatch, "the stop rides the switch").not.toHaveBeenCalled();
    expect(thinkingDispatch, "the stop rides the switch").not.toHaveBeenCalled();
  }

  it("sends Off picked with a model as that one switch", async () => {
    cachedModels = [model("opus-5", true), model("sonnet-5", true)];
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "opus-5",
      effort: "medium",
      effort_levels: FIVE,
      thinking_active: "on",
    };
    await open();
    await keys("{Home}");
    expect(knob().dataset["level"]).toBe("thinking:off");
    await userEvent.click(option("sonnet-5"));

    sentOnly({ model: "sonnet-5", effort: "thinking:off" });
  });

  function parkHigh(thinkingActive: "on" | "off"): void {
    cachedModels = [model("opus-5", true), model("sonnet-5", true), model("haiku-5", true)];
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "opus-5",
      effort: "medium",
      effort_levels: FIVE,
      thinking_active: thinkingActive,
      pending_model: "sonnet-5",
      pending_effort: "high",
    };
  }

  it("opens on a parked tier while thinking is off, and sends nothing left there", async () => {
    parkHigh("off");
    await open();
    expect(knob().dataset["level"], "the knob shows the parked tier, not Off").toBe("high");
    await userEvent.keyboard("{Escape}");

    expect(closed()).toBe(true);
    noSend();
  });

  it("carries the parked tier the knob shows to another model clicked", async () => {
    parkHigh("on");
    await open();
    expect(knob().dataset["level"]).toBe("high");
    await userEvent.click(option("haiku-5"));

    sentOnly({ model: "haiku-5", effort: "high" });
  });

  it("drops a parked tier with one switch when the knob moves to Off", async () => {
    for (const thinking of ["on", "off"] as const) {
      switchDispatch.mockClear();
      parkHigh(thinking);
      await open();
      await keys("{Home}");
      expect(knob().dataset["level"]).toBe("thinking:off");
      await userEvent.keyboard("{Escape}");

      sentOnly({ model: "sonnet-5", effort: "thinking:off" });
    }
  });

  // One switch naming the running model carries the stop the reader kept; the server sets it in
  // place and clears the parked pair in the same request, so no turn close can land between them.
  it("cancels the parked switch with one switch carrying a stop kept on the running model", async () => {
    const cases: readonly { thinking: "on" | "off"; key: string; effort: string }[] = [
      { thinking: "on", key: "{ArrowRight}", effort: "xhigh" },
      { thinking: "on", key: "{ArrowLeft}", effort: "medium" },
      { thinking: "off", key: "{ArrowLeft}", effort: "medium" },
      { thinking: "on", key: "{Home}", effort: "thinking:off" },
      { thinking: "off", key: "{Home}", effort: "thinking:off" },
    ];
    for (const c of cases) {
      switchDispatch.mockClear();
      parkHigh(c.thinking);
      await open();
      await keys(c.key);
      expect(knob().dataset["level"], JSON.stringify(c)).toBe(c.effort);
      await userEvent.click(option("opus-5"));

      sentOnly({ model: "opus-5", effort: c.effort });
    }
  });
});
