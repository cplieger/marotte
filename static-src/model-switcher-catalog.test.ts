import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelInfo } from "./types.js";
import type * as Strings from "./strings.js";

let cachedModels: ModelInfo[] = [];
let notice: { text: string; busy: boolean; retry: boolean } | null = null;

const { onExpand } = vi.hoisted(() => ({ onExpand: { fn: null as null | (() => void) } }));

vi.mock("./pill-expand.js", () => ({
  makeExpandable: (_pill: HTMLElement, _content: HTMLElement, opts?: { onExpand?: () => void }) => {
    onExpand.fn = opts?.onExpand ?? null;
  },
  collapseAll: vi.fn(),
}));

interface SignalOf<T> {
  value: T;
}
type SignalFactory = <T>(initial: T) => SignalOf<T>;
interface TestSession {
  id: string;
  model: string;
  effort: string;
}

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

// The mock records each read the card hands model-list's retryCatalog, which proves the Retry goes
// through it with the read initModelSwitcher was given. `bump` stands for a catalog read landing.
const { retried, catalogTick } = vi.hoisted(() => ({
  retried: { via: [] as unknown[] },
  catalogTick: { bump: (): void => undefined },
}));
vi.mock("./model-list.js", async () => {
  const { signal } = (await vi.importActual("@cplieger/reactive")) as { signal: SignalFactory };
  const revision = signal(0);
  catalogTick.bump = () => {
    revision.value += 1;
  };
  return {
    getCachedModels: () => cachedModels,
    watchCatalog: () => {
      void revision.value;
    },
    catalogNotice: () => notice,
    retryCatalog: (reread: () => Promise<void>) => {
      retried.via.push(reread);
    },
    RETRY_LABEL: "Retry loading the model list",
  };
});
vi.mock("./actions/index.js", () => ({
  bindLoadingState: vi.fn(),
  transportAction: () => ({ dispatch: vi.fn() }),
  retryNetwork: vi.fn(),
  RETRY_STANDARD: {},
}));
vi.mock("./actions/chat.js", () => ({ switchModel: { dispatch: vi.fn() } }));
vi.mock("./actions/model-fallback.js", () => ({ setModelFallback: { dispatch: vi.fn() } }));
// The Fallback row's inputs; no settings shown, so the card builds no row.
vi.mock("./persist.js", () => ({
  shownSettingsSignal: { value: null },
  settingsAsShown: () => null,
  showFallbackAnswer: vi.fn(),
}));
vi.mock("./settings.js", () => ({ currentIdentity: () => ({ state: "signed_out" }) }));
// Real reconcile: the unkeyed notice must coexist with keyed option rows.
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./session-context.js", () => ({
  setLastModel: vi.fn(),
  getLastEffortFor: () => "",
}));
// Only `humanName` is stubbed (see model-switcher.test.ts).
vi.mock("./strings.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Strings>()),
  humanName: (s: string) => s,
}));
import { activeSession } from "./store.js";
import { initModelSwitcher } from "./model-switcher.js";
import { ICON_MODEL_UI, modelIcon } from "./icons.js";
import { iconEl } from "./icon-el.js";

function model(id: string): ModelInfo {
  return { model_id: id, model_name: id, rate_multiplier: 1 };
}

const reread = (): Promise<void> => Promise.resolve();

function scroller(): HTMLElement {
  const el = document.querySelector<HTMLElement>(".pill-model-scroll");
  if (el === null) {
    throw new Error("the card built no model scroller");
  }
  return el;
}

describe("the model card with no models", () => {
  // One fixture: the controller keeps the scroller it builds.
  beforeAll(() => {
    const pill = document.createElement("button");
    pill.id = "switch-model-btn";
    const card = document.createElement("div");
    card.id = "model-switch-list";
    document.body.append(pill, card);
    initModelSwitcher(reread);
  });

  beforeEach(() => {
    cachedModels = [];
    notice = null;
    retried.via = [];
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "model-a-4.7",
      effort: "",
    };
  });

  function retryBtn(): HTMLButtonElement | null {
    return scroller().querySelector<HTMLButtonElement>("button.pill-model-retry");
  }

  it("says why the list is empty instead of showing nothing", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(scroller().textContent).toContain("No models available yet.");
  });

  it("marks the scroller busy only while an answer is still coming", () => {
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();
    expect(scroller().hasAttribute("aria-busy")).toBe(true);

    notice = { text: "Could not load the model list.", busy: false, retry: true };
    onExpand.fn?.();
    expect(scroller().hasAttribute("aria-busy")).toBe(false);
  });

  it("is not a listbox while it holds no options", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(scroller().getAttribute("role")).toBe(null);
    expect(scroller().getAttribute("aria-label")).toBe(null);
  });

  it("stops being a listbox when a re-read answers with no models", () => {
    cachedModels = [model("model-a-4.7")];
    onExpand.fn?.();
    expect(scroller().getAttribute("role")).toBe("listbox");

    cachedModels = [];
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(scroller().getAttribute("role")).toBe(null);
    expect(scroller().getAttribute("aria-label")).toBe(null);
  });

  it("becomes a labelled listbox again once a catalog arrives", () => {
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();
    cachedModels = [model("model-a-4.7")];
    notice = null;
    onExpand.fn?.();

    const el = scroller();
    expect(el.getAttribute("role")).toBe("listbox");
    expect(el.getAttribute("aria-label")).toBe("Available models");
    expect(el.textContent).not.toContain("Loading models");
    expect(el.querySelectorAll(".pill-model-item")).toHaveLength(1);
  });

  it("offers a way back when asking again can change the answer", () => {
    notice = { text: "Could not load the model list.", busy: false, retry: true };
    onExpand.fn?.();

    const btn = retryBtn();
    expect(btn).not.toBe(null);
    btn?.click();
    expect(retried.via).toEqual([reread]);
  });

  it("names what the Retry retries", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();

    expect(retryBtn()?.getAttribute("aria-label")).toBe("Retry loading the model list");
  });

  it("offers no Retry while an answer is still coming", () => {
    notice = { text: "Loading models…", busy: true, retry: false };
    onExpand.fn?.();

    expect(retryBtn()).toBe(null);
  });

  it("takes the Retry away with the notice once a catalog lands", () => {
    notice = { text: "Could not load the model list.", busy: false, retry: true };
    onExpand.fn?.();
    expect(retryBtn()).not.toBe(null);

    cachedModels = [model("model-a-4.7")];
    notice = null;
    onExpand.fn?.();

    expect(retryBtn()).toBe(null);
    expect(scroller().querySelector(".list-empty")).toBe(null);
  });

  it("moves focus from a Retry that loaded the list to the chat's model", () => {
    notice = { text: "Could not load the model list.", busy: false, retry: true };
    onExpand.fn?.();
    retryBtn()?.focus();
    expect(document.activeElement).toBe(retryBtn());

    cachedModels = [model("model-b-5"), model("model-a-4.7")];
    notice = null;
    catalogTick.bump();

    expect(document.activeElement).toBe(scroller().querySelector('[data-model="model-a-4.7"]'));
  });

  it("keeps focus on the Retry when the read fails again", () => {
    notice = { text: "No models available yet.", busy: false, retry: true };
    onExpand.fn?.();
    const btn = retryBtn();
    btn?.focus();

    notice = { text: "Could not load the model list.", busy: false, retry: true };
    catalogTick.bump();

    expect(scroller().textContent).toContain("Could not load the model list.");
    expect(retryBtn()).toBe(btn);
    expect(document.activeElement).toBe(btn);
  });

  it("lists a model that lands while the card is open, keeping focus on the focused model", () => {
    cachedModels = [model("model-a-4.7")];
    onExpand.fn?.();
    const row = scroller().querySelector<HTMLElement>('[data-model="model-a-4.7"]');
    row?.focus();
    expect(document.activeElement).toBe(row);

    cachedModels = [model("model-a-4.7"), model("model-b-5")];
    catalogTick.bump();

    expect(scroller().querySelector('[data-model="model-b-5"]')).not.toBe(null);
    expect(scroller().querySelector('[data-model="model-a-4.7"]')).toBe(row);
    expect(document.activeElement).toBe(row);
  });
});

describe("the model card's rows", () => {
  beforeAll(() => {
    if (document.getElementById("switch-model-btn") === null) {
      const pill = document.createElement("button");
      pill.id = "switch-model-btn";
      const card = document.createElement("div");
      card.id = "model-switch-list";
      document.body.append(pill, card);
      initModelSwitcher(() => Promise.resolve());
    }
  });

  beforeEach(() => {
    notice = null;
    (activeSession as unknown as { value: TestSession }).value = {
      id: "c1",
      model: "claude-opus-5.5",
      effort: "",
    };
  });

  /** Each row's glyph, which sits before its name and is hidden from assistive tech. */
  function rowGlyph(id: string): string {
    const row = scroller().querySelector<HTMLElement>(`[data-model="${id}"]`);
    const svg = row?.querySelector("svg");
    expect(svg, `${id}'s row has a glyph`).not.toBeNull();
    expect(svg?.closest("[aria-hidden='true']") ?? svg?.getAttribute("aria-hidden")).toBeTruthy();
    expect(row?.textContent, "the row still names the model").toContain(id);
    return svg?.innerHTML ?? "";
  }

  it("shows each model's vendor mark, the bot for Auto", () => {
    cachedModels = ["auto", "claude-opus-5.5", "claude-haiku-4.5", "gpt-5.6-sol", "glm-5"].map(
      model,
    );
    onExpand.fn?.();

    expect(rowGlyph("auto")).toBe(iconEl(ICON_MODEL_UI).innerHTML);
    expect(rowGlyph("claude-haiku-4.5")).toBe(rowGlyph("claude-opus-5.5"));
    expect(rowGlyph("gpt-5.6-sol")).not.toBe(rowGlyph("claude-opus-5.5"));
    expect(rowGlyph("glm-5")).not.toBe(iconEl(ICON_MODEL_UI).innerHTML);
  });

  it("draws the mark the pill shows for the same model", () => {
    cachedModels = [model("deepseek-3.2")];
    onExpand.fn?.();

    expect(rowGlyph("deepseek-3.2")).toBe(iconEl(modelIcon("deepseek-3.2", "inline")).innerHTML);
  });
});
