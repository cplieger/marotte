// The pill's readout end to end: no other spec covers the wiring (`effort.test.ts` calls the resolver directly,
// `context-ui.test.ts` stubs `./effort.js` and `./picker.js`).

import { describe, it, expect, beforeEach, vi } from "vitest";
import { userEvent } from "vitest/browser";
import type { ModelInfo, Session, SessionEffortLevel } from "./types.js";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";
import { makeSession } from "./__test-helpers__/model.js";

// Mutable module state, because the real modules are read through a live effect firing after any store write.
const staged = vi.hoisted(() => ({ models: [] as ModelInfo[], seed: "" }));

vi.mock("./picker.js", () => ({
  getCachedModels: () => staged.models,
  refreshPickerIfVisible: vi.fn(),
  catalogNotice: () => null,
  // Named anyway: Browser Mode links ESM for real, so a missing imported name fails collection.
  retryCatalog: vi.fn(),
  RETRY_LABEL: "Retry loading the model list",
}));
// Every export, because Browser Mode links ESM for real.
vi.mock("./session-context.js", () => ({
  getLastEffortFor: (model: string) => (model === "" ? "" : staged.seed),
  getCurrentModel: () => "",
  setCurrentModel: vi.fn(),
  getLastModel: () => "",
  setLastModel: vi.fn(),
  restoreLastModel: vi.fn(),
  restoreLastEffort: vi.fn(),
}));
vi.mock("./prompt-input.js", () => ({
  setSendState: vi.fn(),
  initPromptInput: vi.fn(),
  sendComposer: vi.fn(),
}));

// `onExpand` is what the pill's click calls, so capturing it is the click surface.
const expand = vi.hoisted(() => ({ fn: null as null | (() => void) }));
vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _card: HTMLElement, opts?: { onExpand?: () => void }) => {
    expand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));
// `./actions/chat.js` is not mocked: composer-state.ts pulls it in and a partial factory would fail collection.

// `updateContextBar` paints the whole bar in one pass, so a missing id throws.
document.body.innerHTML = `
  <button id="switch-model-btn">
    <span id="ctx-model-pill"></span><span id="ctx-effort-pill" class="hidden"></span>
  </button>
  <div id="model-switch-list"></div>
  <span id="context-indicator"></span>
  <span id="context-ring-fill"></span>
  <span id="context-ring-wedge"></span>
  <span id="context-label"></span>
  <span id="ctx-tokens"></span>
  <span id="ctx-credits"></span>
  <span id="ctx-turns"></span>
  <span id="ctx-last-turn"></span>
  <span id="ctx-entries"></span>
  <span id="ctx-tools"></span>
  <span id="ctx-metering"></span>`;

const { effect } = await import("@cplieger/reactive");
const store = await import("./store.js");
const { setCatalogEfforts } = await import("./effort.js");
const { refreshContextUI } = await import("./context-ui.js");
const { configure, configureTransport } = await import("@cplieger/actions");
const { initModelSwitcher } = await import("./model-switcher.js");

// `configure({})` is the framework's silent mode; the transport must answer or the rollback undoes the optimistic write.
configure({});
configureTransport(() => Promise.resolve({ ok: true, status: 200 }));
initModelSwitcher();

// The one subscriber under test, as installStoreSubscribers registers it.
effect(() => {
  const active = store.activeSession.value;
  if (active !== undefined) {
    refreshContextUI(active);
  }
});

function fiveTiers(): SessionEffortLevel[] {
  return [{ id: "low" }, { id: "medium" }, { id: "high" }, { id: "xhigh" }, { id: "max" }];
}

function model(id: string, dflt?: string, hasEffort = true): ModelInfo {
  return {
    model_id: id,
    model_name: id,
    rate_multiplier: 1,
    has_effort: hasEffort,
    ...(dflt === undefined ? {} : { default_effort_level: dflt }),
  };
}

function session(id: string, over: Partial<Session> = {}): Session {
  // `refreshContextUI` walks `turn_order`, so a row without it throws inside the effect.
  return {
    ...makeSession({
      id,
      name: id,
      model: "claude-opus-5",
      effort: "",
      effort_levels: fiveTiers(),
      usage: {
        context_pct: 0,
        context_size: 200_000,
        credits: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
    }),
    ...over,
  };
}

function mount(s: Session): void {
  store.setSessions([s]);
  store.setActive(s.id);
}

/**
 * Overwritten by every paint: `updateContextBar` coalesces through rAF, so an untouched DOM would read the previous
 * case's paint.
 */
const UNPAINTED = "unpainted";

function arm(): void {
  const tierEl = document.getElementById("ctx-effort-pill")!;
  tierEl.textContent = UNPAINTED;
  tierEl.classList.remove("hidden");
  document.getElementById("ctx-model-pill")!.textContent = UNPAINTED;
}

async function nextFrame(): Promise<void> {
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

async function tier(): Promise<{ text: string; hidden: boolean }> {
  const el = document.getElementById("ctx-effort-pill")!;
  await vi.waitFor(
    () => {
      expect(el.textContent).not.toBe(UNPAINTED);
    },
    { timeout: FRAME_BUDGET_MS },
  );
  return { text: el.textContent ?? "", hidden: el.classList.contains("hidden") };
}

async function modelName(): Promise<string> {
  const el = document.getElementById("ctx-model-pill")!;
  await vi.waitFor(
    () => {
      expect(el.textContent).not.toBe(UNPAINTED);
    },
    { timeout: FRAME_BUDGET_MS },
  );
  return el.textContent ?? "";
}

beforeEach(async () => {
  // Let the previous case's frame land before staging, or it would read as this case's answer.
  await nextFrame();
  staged.models = [];
  staged.seed = "";
  setCatalogEfforts([], "");
  store.setSessions([]);
  arm();
});

describe("the model pill's reasoning tier", () => {
  // A bridgeless chat has no per-model catalog (GET /api/config-template may answer empty), so "no default known" is
  // the ordinary state of a new chat.
  it("names a tier the chat CHOSE even with nothing in the catalog", async () => {
    mount(session("a", { effort: "max" }));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // The remembered pick is what a new chat opens on; the server resolves the same seed into StartOpts.Effort.
  it("names the remembered pick even with nothing in the catalog", async () => {
    staged.seed = "max";
    mount(session("b"));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // Control: the path that already worked, proving the chain is the real one.
  it("names a chosen tier when the catalog knows a different default", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("c", { effort: "max" }));
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });

  // A readout of the level in force, whoever decided it.
  it("names a level the service resolved", async () => {
    mount(session("d", { effort_active: "high" }));
    expect(await tier()).toEqual({ text: "· high", hidden: false });
  });

  // A resolved tier equal to the model's default is still named.
  it("names the tier when the resolved tier EQUALS the model's own default", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("e", { effort: "high" }));
    expect(await tier()).toEqual({ text: "· high", hidden: false });
  });

  it("says nothing for a model that advertises no reasoning effort", async () => {
    staged.models = [model("auto", undefined, false)];
    mount(session("x", { model: "auto", effort: "max" }));
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  it("says nothing when no level resolves at all", async () => {
    // Nothing resolves, so naming a tier would invent one.
    staged.models = [model("claude-opus-5")];
    mount(session("y"));
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  // A level this model rejects would claim a tier the session cannot reach.
  it("says nothing about a chosen tier the current model does not offer", async () => {
    mount(
      session("f", {
        effort: "max",
        effort_levels: [{ id: "low" }, { id: "medium" }, { id: "high" }],
      }),
    );
    expect(await tier()).toEqual({ text: "", hidden: true });
  });

  it("repaints the tier when a later store write changes the chat's choice", async () => {
    mount(session("g"));
    expect(await tier()).toEqual({ text: "", hidden: true });

    arm();
    store.setEffort("g", "max");

    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });
});

describe("the model pill's model half", () => {
  // The model half reads `humanName(s.model)` with no catalog dependency; asserted since both halves paint together.
  it("names the model with nothing in the catalog", async () => {
    mount(session("h"));
    expect(await modelName()).toBe("claude opus 5");
  });

  it("repaints the model on a local pick, with no bridge and no message sent", async () => {
    mount(session("i", { model: "claude-sonnet-5" }));
    expect(await modelName()).toBe("claude sonnet 5");

    arm();
    store.setModel("i", "claude-opus-5");

    expect(await modelName()).toBe("claude opus 5");
  });

  it("paints the model and the tier together on selection", async () => {
    mount(session("j", { model: "claude-sonnet-5" }));
    await modelName();

    arm();
    store.setModel("j", "claude-opus-5");
    store.setEffort("j", "max");

    expect(await modelName()).toBe("claude opus 5");
    expect(await tier()).toEqual({ text: "· max", hidden: false });
  });
});

// The gesture end: `setEffortAction`'s optimistic callback is module-private, so only this reaches it.
describe("a gesture on the slider", () => {
  function knob(): HTMLElement {
    expand.fn?.();
    const found = document.querySelector<HTMLElement>('[role="slider"]');
    expect(found, "the card rendered no slider, so the gesture has no subject").not.toBeNull();
    return found as HTMLElement;
  }

  it("paints the pill, through the real action", async () => {
    staged.models = [model("claude-opus-5", "high")];
    mount(session("k", { model: "claude-opus-5" }));
    // The readout moves from the model's default.
    expect(await tier()).toEqual({ text: "· high", hidden: false });

    arm();
    const k = knob();
    k.focus();
    await userEvent.keyboard("{End}");
    expect(k.getAttribute("aria-valuetext"), "End lands on the highest tier").toBe("max");

    expect(await tier()).toEqual({ text: "· max", hidden: false });
    // The knob and the pill resolve through the same `effort.ts` chain.
    expect(k.dataset["level"]).toBe("max");
    // The pill reads the store, so the write must land there.
    expect(store.get("k")?.effort).toBe("max");
  });
});
