// Tests for the Workflows sub-tab. Two halves.

import { vi, describe, it, expect, beforeEach, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";
import { expectAnchored, resizeViewport } from "./__test-helpers__/anchored-panel.js";
import { loadCSS, mountAppCSS, ruleBody } from "./__test-helpers__/css-rules.js";

const authoredSource = import.meta.glob<string>(
  ["./*.ts", "./actions/*.ts", "./handlers/*.ts", "./fundamentals/*.ts", "../static/index.html"],
  { query: "?raw", import: "default", eager: true },
);

const dispatched: { name: string; args: unknown }[] = [];

vi.mock("./actions/runs.js", () => ({
  loadRecipes: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "recipes", args });
      return Promise.resolve(recipesReply);
    }),
  },
  loadRuns: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "runs", args });
      return Promise.resolve(runsReply);
    }),
  },
  launchRun: {
    dispatch: vi.fn(
      (
        args: unknown,
        opts?: { onSuccess?: (d: { workflow_id: string; name: string }) => void },
      ) => {
        dispatched.push({ name: "launch", args });
        opts?.onSuccess?.({ workflow_id: "wf_new", name: "goal" });
        return Promise.resolve({ workflow_id: "wf_new", name: "goal" });
      },
    ),
  },
  cancelRun: {
    dispatch: vi.fn((args: unknown) => {
      dispatched.push({ name: "cancel", args });
      return Promise.resolve({ ok: true });
    }),
  },
}));
vi.mock("./run-view.js", () => ({ openRunView: vi.fn() }));
const catalog = vi.hoisted(() => ({ models: [] as ModelInfo[] }));
vi.mock("./picker.js", () => ({ getCachedModels: () => catalog.models }));
// The unattended note's auto-approve read-out. Unmocked, refreshAutoApprove reaches /api/settings
// through the actions transport (which the api-client mock does not cover), fire-and-forget, so the
// request was still open when the window tore down and printed an unhandled AbortError.
vi.mock("./persist.js", () => ({
  loadSettings: vi.fn(async () => ({})),
  refreshSettings: vi.fn(async () => ({})),
}));

// The Run when… popup's actions: unmocked they reach the network.
vi.mock("./actions/launch-rules.js", () => ({
  loadLaunchRules: { dispatch: vi.fn(async () => rulesReply) },
  saveLaunchRule: { dispatch: vi.fn(async () => null) },
  saveEventRule: { dispatch: vi.fn(async () => null) },
  deleteLaunchRule: { dispatch: vi.fn(async () => ({ ok: true })) },
  dismissGap: { dispatch: vi.fn(async () => null) },
}));
vi.mock("./actions/forge-list.js", () => ({
  listForges: { dispatch: vi.fn(async () => forgesReply) },
}));
vi.mock("./confirm.js", () => ({ confirm: vi.fn(async () => true) }));

const standing = vi.hoisted(() => ({ files: new Map<string, ConfigStanding>() }));
vi.mock("./config-standing.js", async (importOriginal) => {
  const { signal } = await import("@cplieger/reactive");
  const view = signal({ files: standing.files, servers: new Map(), loadErrors: new Map() });
  return {
    ...(await importOriginal<typeof ConfigStandingModule>()),
    configStanding: view,
  };
});

import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { BUS_CATALOG_MODELS, dispatch, emitBus } from "./bus.js";
import {
  deleteLaunchRule,
  loadLaunchRules,
  saveEventRule,
  saveLaunchRule,
} from "./actions/launch-rules.js";
import { confirm } from "./confirm.js";
import { openRunView } from "./run-view.js";
import { launchRun, cancelRun, loadRecipes } from "./actions/runs.js";
import type { ModelInfo, RecipesResponse, WorkflowRun, ResumableSession } from "./types.js";
import type { ConfigStanding, RunStatus } from "./wire/types.gen.js";
import type { LaunchRulesResponse, LaunchRuleView } from "./launch-rule-types.js";
import type { ForgesListResponse } from "./actions/forge-list.js";
import type * as ConfigStandingModule from "./config-standing.js";

let recipesReply: RecipesResponse = { recipes: [] };
let rulesReply: LaunchRulesResponse = { rules: [] };
let forgesReply: ForgesListResponse = { forges: [], kinds: [] };
let runsReply: { sessions: ResumableSession[]; runs: WorkflowRun[] } = {
  sessions: [],
  runs: [],
};

function recipe(name: string, inputs?: Record<string, string>): RecipesResponse["recipes"][0] {
  const base = { name, source: `bundled://${name}`, description: `${name} desc` };
  return inputs === undefined ? base : { ...base, inputs };
}

// `status` is a bare string, not `RunStatus`: one case spells a word an engine ahead of this build
// would send, which is exactly what the live/terminal read has to survive. The cast is the wire lie
// being modelled.
function run(recipe: string, id: string, status: string): WorkflowRun {
  return {
    workflow_id: id,
    name: recipe,
    workflow_name: recipe,
    status: status as RunStatus,
    updated_at: 0,
  };
}

async function render(filter = ""): Promise<HTMLElement> {
  const panel = document.createElement("div");
  document.body.appendChild(panel);
  renderRecipesPanel(panel, filter);
  // renderRecipesPanel awaits its two fetches before painting.
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  return panel;
}

function names(panel: HTMLElement): string[] {
  return [...panel.querySelectorAll(".entry-title")].map((e) => e.textContent ?? "");
}

function buttonFor(panel: HTMLElement, source: string): HTMLButtonElement | null {
  return panel.querySelector<HTMLButtonElement>(`[data-recipe="${source}"] .recipe-run-btn`);
}

/** A button's painted text, part by part: its parts are flex items, which `innerText` puts on lines. */
function shownText(btn: HTMLElement): string {
  return [...btn.childNodes]
    .filter((c) => !(c instanceof Element) || c.getBoundingClientRect().width > 0)
    .map((c) => (c.textContent ?? "").trim())
    .filter((t) => t !== "")
    .join(" ");
}

beforeEach(() => {
  document.body.replaceChildren();
  dispatched.length = 0;
  vi.mocked(openRunView).mockClear();
  vi.mocked(launchRun.dispatch).mockClear();
  vi.mocked(cancelRun.dispatch).mockClear();
  recipesReply = { recipes: [] };
  rulesReply = { rules: [] };
  forgesReply = { forges: [], kinds: [] };
  runsReply = { sessions: [], runs: [] };
});

describe("configuration standing", () => {
  const ROOT_A = "/workspace/a/.kiro/workflows/deploy.workflow.json";
  const ROOT_B = "/workspace/b/.kiro/workflows/deploy.workflow.json";

  function workspaceRecipe(source: string): RecipesResponse["recipes"][0] {
    return { name: "deploy", source, doc_key: source.slice(1) };
  }

  function badges(panel: HTMLElement, source: string): string[] {
    return [...panel.querySelectorAll(`[data-recipe="${source}"] .docs-badge-standing`)].map(
      (b) => b.textContent ?? "",
    );
  }

  beforeEach(() => {
    standing.files.clear();
  });

  it("badges only the row whose file kiro-cli replaced, not a same-named one in another root", async () => {
    standing.files.set(ROOT_A.slice(1), {
      type: "workflow",
      key: ROOT_A.slice(1),
      kind: "displaced",
      decided_by: "user-local",
      decided_by_name: "Local Settings (~/.kiro)",
    });
    recipesReply = { recipes: [workspaceRecipe(ROOT_A), workspaceRecipe(ROOT_B), recipe("goal")] };
    const panel = await render();
    expect(badges(panel, ROOT_A)).toEqual(["Replaced by Local Settings (~/.kiro)"]);
    expect(badges(panel, ROOT_B)).toEqual([]);
    expect(badges(panel, "bundled://goal")).toEqual([]);
    const rowB = panel.querySelector(`[data-recipe="${ROOT_B}"] .entry-badges`);
    expect(rowB?.classList.contains("hidden"), "a row with no badge shows no badge strip").toBe(
      true,
    );
  });

  it("adds and drops the badge on a kept row as the standing changes", async () => {
    recipesReply = { recipes: [workspaceRecipe(ROOT_A)] };
    const panel = await render();
    expect(badges(panel, ROOT_A)).toEqual([]);
    standing.files.set(ROOT_A.slice(1), {
      type: "workflow",
      key: ROOT_A.slice(1),
      kind: "disabled",
      decided_by: "workspace",
      decided_by_name: "Workspace",
    });
    renderRecipesPanel(panel);
    expect(badges(panel, ROOT_A)).toEqual(["Off by Workspace"]);
    const strip = panel.querySelector(`[data-recipe="${ROOT_A}"] .entry-badges`);
    expect(strip?.classList.contains("hidden")).toBe(false);
    standing.files.clear();
    renderRecipesPanel(panel);
    expect(badges(panel, ROOT_A)).toEqual([]);
  });
});

describe("the Run ⇄ Cancel row", () => {
  it("labels an idle recipe Run and a live one Cancel", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("investigate")] };
    runsReply.runs = [run("investigate", "wf_9", "running")];
    const panel = await render();

    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
    expect(buttonFor(panel, "bundled://investigate")?.textContent).toBe("Cancel");
  });

  it("keys a live run on its recipe, never on a display name that matches another recipe", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [{ workflow_id: "wf_prompt", name: "goal", status: "running", updated_at: 0 }];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
  });

  it("treats an unknown status as live and keeps Cancel available", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_future", "quiesced")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Cancel");
  });

  it("treats a PAUSED run as live — Cancel, or the recipe wedges", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "paused")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Cancel");
  });

  it("returns a TERMINAL run's row to Run — a fresh run, never a retry", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_1", "failed")];
    const panel = await render();
    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
  });

  it("opens the form for an input-less recipe, its Launch focused, and sends Default as nothing", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
    const launchBtn = panel.querySelector<HTMLButtonElement>(".recipe-launch-btn");
    expect(document.activeElement).toBe(launchBtn);
    launchBtn?.click();
    expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
      source: "bundled://goal",
      inputs: {},
    });
    expect(vi.mocked(openRunView)).toHaveBeenCalledWith("wf_new", "goal");
  });

  it("launches with the model and effort picked on the form", async () => {
    catalog.models = [{ model_id: "opus", model_name: "Opus", effort_tiers: ["low", "high"] }];
    recipesReply = { recipes: [recipe("picked")] };
    const panel = await render();
    buttonFor(panel, "bundled://picked")?.click();
    const [model, effort] = [...panel.querySelectorAll<HTMLSelectElement>(".run-pick-select")];
    if (model === undefined || effort === undefined) {
      throw new Error("the Run form has no Model or no Effort select");
    }
    model.value = "opus";
    model.dispatchEvent(new Event("change"));
    effort.value = "high";
    effort.dispatchEvent(new Event("change"));
    panel.querySelector<HTMLButtonElement>(".recipe-launch-btn")?.click();
    catalog.models = [];
    expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
      source: "bundled://picked",
      inputs: {},
      model: "opus",
      effort: "high",
    });
  });

  it("gives an open Run form the catalog that lands after it opened", async () => {
    catalog.models = [];
    recipesReply = { recipes: [recipe("late")] };
    const panel = await render();
    buttonFor(panel, "bundled://late")?.click();
    const model = panel.querySelector<HTMLSelectElement>(".run-pick-select");
    expect([...(model?.options ?? [])].map((o) => o.value)).toEqual([""]);
    catalog.models = [{ model_id: "opus", model_name: "Opus" }];
    emitBus(BUS_CATALOG_MODELS);
    catalog.models = [];
    expect([...(model?.options ?? [])].map((o) => o.value)).toEqual(["", "opus"]);
  });

  it("cancels the LIVE run on click, whoever launched it", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    runsReply.runs = [run("goal", "wf_7", "running")];
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(cancelRun.dispatch).mock.calls[0]?.[0]).toBe("wf_7");
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
  });

  it("collects declared inputs inline before launching — no modal", async () => {
    recipesReply = { recipes: [recipe("goal", { prompt: "prompt", max_iterations: "string" })] };
    const panel = await render();

    // First click expands the form instead of launching.
    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch)).not.toHaveBeenCalled();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    expect(form).not.toBeNull();

    // Fill one field, leave the other empty (allowed), submit.
    const field = form?.querySelector<HTMLInputElement>("input");
    if (field !== null && field !== undefined) {
      field.value = "make the tests pass";
    }
    form?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    const args = vi.mocked(launchRun.dispatch).mock.calls[0]?.[0] as {
      source: string;
      inputs: Record<string, string>;
    };
    expect(args.source).toBe("bundled://goal");
    expect(args.inputs).toEqual({ prompt: "make the tests pass" });
  });
});

describe("a recipe KAS cannot launch", () => {
  const reason =
    "Invalid workflow: include 'review' (./review-seats.workflow.json): recipe was not resolved";

  it("offers no Run, and says why on the row and the button", async () => {
    recipesReply = { recipes: [{ ...recipe("fix-and-review"), validation_error: reason }] };
    const panel = await render();
    const btn = buttonFor(panel, "bundled://fix-and-review");
    expect(btn?.disabled).toBe(true);
    expect(btn?.getAttribute("data-tooltip")).toBe(reason);
    const line = panel.querySelector<HTMLElement>(
      '[data-recipe="bundled://fix-and-review"] .entry-lines > .entry-sub:nth-child(1)',
    );
    expect(line?.textContent).toBe(reason);
    expect(line?.classList.contains("entry-sub-error")).toBe(true);
    const sched = panel.querySelector<HTMLButtonElement>(
      '[data-recipe="bundled://fix-and-review"] .recipe-sched-btn',
    );
    expect(sched?.disabled).toBe(true);
    btn?.click();
    expect(launchRun.dispatch).not.toHaveBeenCalled();
  });

  it("offers Run again, with its description, once a re-read finds it fixed", async () => {
    recipesReply = { recipes: [{ ...recipe("fix-and-review"), validation_error: reason }] };
    const panel = await render();
    recipesReply = { recipes: [recipe("fix-and-review")] };
    dispatch({ type: "recipes_changed", chat_id: "" });
    await Promise.resolve();
    await Promise.resolve();
    const btn = buttonFor(panel, "bundled://fix-and-review");
    expect(btn?.disabled).toBe(false);
    expect(btn?.hasAttribute("data-tooltip")).toBe(false);
    const line = panel.querySelector<HTMLElement>(
      '[data-recipe="bundled://fix-and-review"] .entry-lines > .entry-sub:nth-child(1)',
    );
    expect(line?.textContent).toBe("fix-and-review desc");
    expect(line?.classList.contains("entry-sub-error")).toBe(false);
  });

  describe("under an input form opened while it was valid", () => {
    async function openTypedForm(): Promise<{
      panel: HTMLElement;
      form: HTMLFormElement;
      field: HTMLInputElement;
      launchBtn: HTMLButtonElement;
    }> {
      recipesReply = { recipes: [recipe("fix-and-review", { prompt: "prompt" })] };
      const panel = await render();
      // The open form is module state, so an earlier case can leave it open.
      if (panel.querySelector(".recipe-input-form") === null) {
        buttonFor(panel, "bundled://fix-and-review")?.click();
      }
      const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
      if (form === null) {
        throw new Error("the input form did not open");
      }
      const field = form.querySelector<HTMLInputElement>("input");
      const launchBtn = form.querySelector<HTMLButtonElement>('button[type="submit"]');
      if (field === null || launchBtn === null) {
        throw new Error("the input form has no field or no Launch");
      }
      field.value = "make the tests pass";
      return { panel, form, field, launchBtn };
    }

    async function catalogChanges(next: RecipesResponse): Promise<void> {
      recipesReply = next;
      dispatch({ type: "recipes_changed", chat_id: "" });
      await Promise.resolve();
      await Promise.resolve();
    }

    function submit(form: HTMLFormElement): void {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    }

    it("turns Launch off with the reason, keeps what was typed, and refuses a submit", async () => {
      const { panel, form, field, launchBtn } = await openTypedForm();
      expect(launchBtn.disabled).toBe(false);

      await catalogChanges({
        recipes: [{ ...recipe("fix-and-review", { prompt: "prompt" }), validation_error: reason }],
      });

      expect(panel.querySelector(".recipe-input-form")).toBe(form);
      expect(field.value).toBe("make the tests pass");
      expect(launchBtn.disabled).toBe(true);
      expect(launchBtn.getAttribute("data-tooltip")).toBe(reason);
      submit(form);
      expect(launchRun.dispatch).not.toHaveBeenCalled();
    });

    it("still folds the form from Run, then turns Run off, launching nothing", async () => {
      const { panel } = await openTypedForm();
      await catalogChanges({
        recipes: [{ ...recipe("fix-and-review", { prompt: "prompt" }), validation_error: reason }],
      });
      const run = buttonFor(panel, "bundled://fix-and-review");
      expect(run?.disabled).toBe(false);
      expect(run?.getAttribute("aria-expanded")).toBe("true");
      expect(run?.getAttribute("data-tooltip")).toBe(reason);

      run?.click();
      expect(panel.querySelector(".recipe-input-form")).toBeNull();
      expect(run?.disabled).toBe(true);
      expect(run?.getAttribute("aria-expanded")).toBe("false");
      // Folded for good: a filter round trip does not bring it back.
      renderRecipesPanel(panel, "no such recipe");
      renderRecipesPanel(panel, "");
      expect(panel.querySelector(".recipe-input-form")).toBeNull();
      expect(launchRun.dispatch).not.toHaveBeenCalled();
    });

    it("mounts the form again with Launch off when a filter hides and re-shows it", async () => {
      const { panel } = await openTypedForm();
      await catalogChanges({
        recipes: [{ ...recipe("fix-and-review", { prompt: "prompt" }), validation_error: reason }],
      });
      renderRecipesPanel(panel, "no such recipe");
      expect(panel.querySelector(".recipe-input-form")).toBeNull();
      renderRecipesPanel(panel, "");

      const launchBtn = panel.querySelector<HTMLButtonElement>(
        '.recipe-input-form button[type="submit"]',
      );
      expect(launchBtn?.disabled).toBe(true);
      expect(launchBtn?.getAttribute("data-tooltip")).toBe(reason);
    });

    it("launches the typed inputs once a re-read finds it fixed", async () => {
      const { form, launchBtn } = await openTypedForm();
      await catalogChanges({
        recipes: [{ ...recipe("fix-and-review", { prompt: "prompt" }), validation_error: reason }],
      });
      await catalogChanges({ recipes: [recipe("fix-and-review", { prompt: "prompt" })] });

      expect(launchBtn.disabled).toBe(false);
      expect(launchBtn.hasAttribute("data-tooltip")).toBe(false);
      submit(form);
      expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
        source: "bundled://fix-and-review",
        inputs: { prompt: "make the tests pass" },
      });
    });
  });
});

describe("a recipes_changed invalidation", () => {
  it("re-reads the catalog while the list is on screen", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();
    expect(names(panel)).toEqual(["goal"]);

    recipesReply = { recipes: [recipe("goal"), recipe("review")] };
    dispatch({ type: "recipes_changed", chat_id: "" });
    await Promise.resolve();
    await Promise.resolve();
    expect(names(panel)).toEqual(["goal", "review"]);
  });

  it("does not re-read the catalog while the list is hidden", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();
    panel.classList.add("hidden");

    recipesReply = { recipes: [recipe("goal"), recipe("review")] };
    dispatch({ type: "recipes_changed", chat_id: "" });
    await Promise.resolve();
    await Promise.resolve();
    expect(names(panel)).toEqual(["goal"]);
  });
});

describe("the recipe row on the shared builder", () => {
  function rowFor(panel: HTMLElement, source: string): HTMLElement {
    const row = panel.querySelector<HTMLElement>(`[data-recipe="${source}"]`);
    if (row === null) {
      throw new Error(`no row rendered for ${source}`);
    }
    return row;
  }

  // Each case names its own recipe.

  it("is an inert entry whose two lines are the description and the rules", async () => {
    // Not a door: a recipe row's one destination is the run its button launches, and that run opens
    // its own tab.
    recipesReply = { recipes: [{ ...recipe("shape", { prompt: "prompt" }), built_in: true }] };
    const panel = await render();
    const row = rowFor(panel, "bundled://shape");

    expect(row.classList.contains("entry")).toBe(true);
    expect(row.querySelector(".entry-open")).toBeNull();
    expect(row.querySelector(".entry-body")?.tagName).toBe("DIV");
    expect(row.querySelector(".entry-title")?.textContent).toBe("shape");
    expect(
      [...(row.querySelector(".entry-badges")?.children ?? [])].map((b) => b.textContent),
    ).toEqual(["bundled"]);
    expect(
      [...(row.querySelector(".entry-lines")?.children ?? [])].map((l) => l.textContent),
    ).toEqual(["shape desc", "Not scheduled · Inputs: prompt"]);
  });

  it("puts Run when… then Run in the actions slot as text buttons", async () => {
    recipesReply = { recipes: [recipe("actions")] };
    const panel = await render();
    const actions = [
      ...(rowFor(panel, "bundled://actions").querySelector(".entry-actions")?.children ?? []),
    ];
    expect(actions.map((a) => a.className)).toEqual([
      "btn-small recipe-sched-btn",
      "btn-small recipe-run-btn",
    ]);
    expect(actions.map((a) => a.textContent)).toEqual(["Run when…", "Run"]);
  });

  it("keeps the second line for the rules when a recipe declares no inputs", async () => {
    recipesReply = { recipes: [recipe("plain")] };
    const panel = await render();
    const lines = [
      ...(rowFor(panel, "bundled://plain").querySelector(".entry-lines")?.children ?? []),
    ];
    expect(lines.map((l) => l.textContent)).toEqual(["plain desc", "Not scheduled"]);
  });

  it("mounts the input form as a detail region UNDER the row, never inside it", async () => {
    // The row's height is the list's tier and never grows; the only thing that may grow is a
    // sibling region below it.
    recipesReply = {
      recipes: [recipe("form-host", { prompt: "prompt", max_iterations: "string" })],
    };
    const panel = await render();
    const row = rowFor(panel, "bundled://form-host");
    buttonFor(panel, "bundled://form-host")?.click();

    const detail = row.nextElementSibling;
    expect(detail?.classList.contains("entry-detail")).toBe(true);
    expect(detail?.getAttribute("role")).toBe("listitem");
    expect(detail?.classList.contains("open")).toBe(true);
    expect(detail?.querySelector(".recipe-input-form")).not.toBeNull();
    expect(row.querySelector(".recipe-input-form")).toBeNull();
    expect(detail?.parentElement).toBe(row.parentElement);
    // A second press closes it.
    buttonFor(panel, "bundled://form-host")?.click();
    expect(panel.querySelector(".entry-detail")).toBeNull();
  });

  it("keeps what the reader typed across a repaint", async () => {
    // The panel repaints on its own schedule — the run poll, the rules fetch — so a form that
    // was rebuilt on each one would lose its values.
    recipesReply = { recipes: [recipe("typing", { prompt: "prompt" })] };
    const panel = await render();
    buttonFor(panel, "bundled://typing")?.click();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    const field = form?.querySelector<HTMLInputElement>("input");
    if (field !== null && field !== undefined) {
      field.value = "half typed";
    }

    renderRecipesPanel(panel);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();

    expect(panel.querySelector(".recipe-input-form")).toBe(form);
    expect(panel.querySelector<HTMLInputElement>(".recipe-input")?.value).toBe("half typed");
  });

  it("closes the form when it launches", async () => {
    recipesReply = { recipes: [recipe("launcher", { prompt: "prompt" })] };
    const panel = await render();
    buttonFor(panel, "bundled://launcher")?.click();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    form?.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    expect(panel.querySelector(".entry-detail")).toBeNull();
    expect(vi.mocked(launchRun.dispatch)).toHaveBeenCalledTimes(1);
  });
});

describe("the muted classes are gone rather than defined", () => {
  // `.text-muted` and `.text-sm` were on seven elements across four files and declared in no
  // stylesheet, so "Not scheduled" rendered at full primary ink and outshouted the `.is-scheduled`
  // accent meant to distinguish it. Each site takes its ink from its own component rule instead.

  it("names neither class anywhere in authored source", () => {
    const offenders: string[] = [];
    for (const [path, text] of Object.entries(authoredSource)) {
      // The test file naming the classes in its own prose is not a use site.
      if (path.endsWith("recipes.test.ts")) {
        continue;
      }
      if (/\btext-muted\b|\btext-sm\b/.test(text)) {
        offenders.push(path.replace(/^\.\//, ""));
      }
    }
    expect(offenders).toEqual([]);
  });

  it("lets a live schedule take the accent on the shared subtitle line", () => {
    // The dormant line is the builder's own `.entry-sub` and carries no rule of its own here; only
    // the live state earns one.
    const docs = loadCSS("28-docs.css");
    expect(
      /color:\s*var\(--c-accent\)/.test(ruleBody(docs, ".docs-panel .entry-sub.is-scheduled")),
    ).toBe(true);
    expect(docs).not.toMatch(/recipe-sched-summary/);
  });

  it("gives every former use site a component rule that carries its ink", () => {
    // `.run-id` and `.run-output-empty` were this page's own; the page renders the run CARD now, so
    // their equivalents live with the component. Both are still tertiary ink — that is what the
    // muted-class sweep is checking — they are just in the stylesheet that owns the element.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("27-run-card.css"), ".run-output-val-empty"),
      ),
    ).toBe(true);
    // The run PAGE's note lives in the exec view, a different component with its own vocabulary
    // than the transcript's card.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("31-exec-view.css"), ".ev-d-empty"),
      ),
    ).toBe(true);
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(ruleBody(loadCSS("24-find.css"), ".page-find-note")),
    ).toBe(true);
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("19-files.css"), ".fb-search-note"),
      ),
    ).toBe(true);
  });
});

// The filter, which this tab did not have.

describe("the filter", () => {
  it("narrows by name, case-insensitively", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    expect(names(await render())).toEqual(["goal", "triage"]);
    expect(names(await render("GOA"))).toEqual(["goal"]);
  });

  it("reaches a description, a source and a declared input name", async () => {
    recipesReply = {
      recipes: [
        { name: "one", source: "bundled://one", description: "reviews a pull request" },
        { name: "two", source: "workspace/.kiro/flows/deploy.workflow.json" },
        { name: "three", source: "bundled://three", inputs: { branch: "string" } },
      ],
    };
    expect(names(await render("pull request"))).toEqual(["one"]);
    expect(names(await render("deploy.workflow"))).toEqual(["two"]);
    expect(names(await render("branch"))).toEqual(["three"]);
  });

  it("matches the badge a bundled row DISPLAYS", async () => {
    // Same rule docs.ts applies to its own badges: a reader types at what they can see.
    recipesReply = {
      recipes: [
        { name: "one", source: "bundled://one", built_in: true },
        { name: "two", source: "b://two" },
      ],
    };
    expect(names(await render("bundled"))).toEqual(["one"]);
  });

  it("cannot reach the node PLAN, which is raw JSON nobody types at", async () => {
    // Folding it in would match on punctuation and internal key names, so the box would be
    // answering a different question than it appears to ask.
    recipesReply = {
      recipes: [
        {
          name: "one",
          source: "bundled://one",
          plan: JSON.stringify({ nodeId: "n1", agentName: "reviewer" }),
        } as RecipesResponse["recipes"][0],
      ],
    };
    expect(names(await render("nodeId"))).toEqual([]);
    expect(names(await render(""))).toEqual(["one"]);
  });

  it("says NO MATCHES rather than claiming there are no workflows", async () => {
    // "No workflows available." under an active filter is the same lie docs.ts records for its
    // category text: they exist, they are one keystroke away.
    recipesReply = { recipes: [recipe("goal")] };
    const filtered = await render("zzzz");
    expect(filtered.textContent).toContain("No workflows match the filter");
    expect(filtered.textContent).not.toContain("No workflows available");
    recipesReply = { recipes: [] };
    expect((await render()).textContent).toContain("No workflows available");
  });

  it("reports its counts on every repaint, not only on the fetch", async () => {
    // The note describes what is on screen, so whichever caller changed what is on screen owes the
    // update — the run poll and the schedules fetch repaint too.
    const seen: { total: number; shown: number }[] = [];
    setRecipeCountsListener((c) => {
      seen.push(c);
    });
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    await render("goal");
    expect(seen.at(-1)).toEqual({ total: 2, shown: 1 });
    setRecipeCountsListener(() => undefined);
  });

  it("keeps a row's click bound to the recipe it names, not to the filtered index", async () => {
    // recipeRow resolves at CLICK time against the UNFILTERED list, which is what survives
    // reconcile keeping a row across a keystroke.
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    const panel = await render("triage");
    buttonFor(panel, "bundled://triage")?.click();
    panel.querySelector<HTMLButtonElement>(".recipe-launch-btn")?.click();
    expect(dispatched.filter((d) => d.name === "launch")).toHaveLength(1);
    expect(dispatched.at(-1)?.args).toMatchObject({ source: "bundled://triage" });
  });
});

describe("the Run when… popup", () => {
  const daily = { freq: "daily", hour: 3, minute: 0 } as const;

  function clockRule(id: string, over: Partial<LaunchRuleView> = {}): LaunchRuleView {
    return {
      id,
      source: "bundled://nightly",
      enabled: true,
      when: { clock: daily },
      held: 0,
      ...over,
    };
  }

  function eventRule(id: string, over: Partial<LaunchRuleView> = {}): LaunchRuleView {
    return {
      id,
      source: "bundled://nightly",
      enabled: true,
      when: {
        event: {
          connection: "github:github.com",
          repo: "o/r",
          kind: "run_concluded",
          filter: { result: ["success"] },
        },
      },
      cursor: { baselined: true },
      held: 0,
      ...over,
    };
  }

  async function openPopup(inputs?: Record<string, string>): Promise<HTMLButtonElement> {
    recipesReply = { recipes: [recipe("nightly", inputs)] };
    const panel = await render();
    await vi.waitFor(() => {
      expect(vi.mocked(loadLaunchRules.dispatch)).toHaveBeenCalled();
    });
    await new Promise((r) => setTimeout(r, 0));
    const btn = panel.querySelector<HTMLButtonElement>(".recipe-sched-btn");
    if (btn === null) {
      throw new Error("the row has no Run when… button");
    }
    btn.click();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    return btn;
  }

  function popupButton(text: string): HTMLButtonElement {
    // A closing popup stays in the document until its fade ends, so only the one opened last is
    // searched.
    const open = [...document.querySelectorAll<HTMLElement>(".sched-popup")].at(-1);
    const b = [...(open?.querySelectorAll<HTMLButtonElement>("button") ?? [])].find(
      (x) => x.textContent === text,
    );
    if (b === undefined) {
      throw new Error(`the popup has no ${text} button`);
    }
    return b;
  }

  /** Repaint the list holding `btn` under a filter that matches no recipe. */
  function filterAway(btn: HTMLButtonElement): void {
    const host = btn.closest(".list-container")?.parentElement;
    if (host == null) {
      throw new Error("the button is not in a recipe list");
    }
    renderRecipesPanel(host, "zzzz");
  }

  function scheduleModel(): HTMLSelectElement {
    const sel = document.querySelector<HTMLSelectElement>(".sched-picker .run-pick-select");
    if (sel === null) {
      throw new Error("the schedule form has no Model select");
    }
    return sel;
  }

  beforeEach(() => {
    vi.mocked(saveLaunchRule.dispatch).mockClear();
    vi.mocked(saveEventRule.dispatch).mockClear();
    vi.mocked(loadLaunchRules.dispatch).mockClear();
    vi.mocked(deleteLaunchRule.dispatch).mockClear();
    vi.mocked(confirm).mockClear();
  });

  it("counts enabled rules on the button", async () => {
    rulesReply = {
      rules: [clockRule("c1"), eventRule("e1"), eventRule("e2", { enabled: false })],
    };
    const btn = await openPopup();
    expect(shownText(btn)).toBe("Run when… 2");
    expect(btn.classList.contains("on")).toBe(true);
    expect(
      [...document.querySelectorAll(".rw-rule")].map((r) => r.getAttribute("data-rule-id")),
    ).toEqual(["c1", "e1", "e2"]);

    document.body.replaceChildren();
    rulesReply = { rules: [clockRule("c1"), eventRule("e2", { enabled: false })] };
    const one = await openPopup();
    expect(shownText(one)).toBe("Run when…");
    expect(one.classList.contains("on")).toBe(true);

    document.body.replaceChildren();
    rulesReply = { rules: [clockRule("c1", { enabled: false })] };
    const none = await openPopup();
    expect(shownText(none)).toBe("Run when…");
    expect(none.classList.contains("on")).toBe(false);
  });

  it("leaves no panel in the page once it closes and its row is filtered away", async () => {
    rulesReply = { rules: [clockRule("c1")] };
    const btn = await openPopup();
    btn.click();
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    filterAway(btn);
    await vi.waitFor(() => {
      expect(document.querySelectorAll(".sched-popup")).toHaveLength(0);
    });
  });

  it("closes the open panel when its row leaves the page", async () => {
    rulesReply = { rules: [clockRule("c1")] };
    const btn = await openPopup();
    // A recipes_changed refetch that drops the recipe repaints with no rules fetch behind it.
    btn.closest(".entry")!.remove();
    await vi.waitFor(() => {
      expect(document.querySelectorAll(".sched-popup")).toHaveLength(0);
    });
  });

  it("shows the first error state on the recipe row", async () => {
    rulesReply = {
      rules: [
        clockRule("c1"),
        eventRule("e1", { status: { blocked: "this rule's workflow file is gone" } }),
        eventRule("e2", { cursor: { baselined: true, blocked: "connection signed out" } }),
      ],
    };
    await openPopup();
    const line = document.querySelector<HTMLElement>(
      '[data-recipe="bundled://nightly"] .entry-lines > .entry-sub:nth-child(2)',
    );
    expect(line?.textContent).toBe("blocked: this rule's workflow file is gone. Edit the rule");
    expect(line?.classList.contains("entry-sub-error")).toBe(true);
    expect(line?.classList.contains("is-scheduled")).toBe(false);
  });

  it("shows the first enabled rule and how many more on the recipe row", async () => {
    rulesReply = { rules: [clockRule("c1", { enabled: false }), clockRule("c2"), clockRule("c3")] };
    await openPopup();
    const line = document.querySelector<HTMLElement>(
      '[data-recipe="bundled://nightly"] .entry-lines > .entry-sub:nth-child(2)',
    );
    expect(line?.textContent).toMatch(/^Every day at .+ · \+1 more rule$/);
    expect(line?.classList.contains("is-scheduled")).toBe(true);
  });

  it("takes the catalog that lands after the schedule form opened", async () => {
    catalog.models = [];
    await openPopup();
    popupButton("On a schedule").click();
    expect([...scheduleModel().options].map((o) => o.value)).toEqual([""]);
    catalog.models = [{ model_id: "opus", model_name: "Opus" }];
    emitBus(BUS_CATALOG_MODELS);
    catalog.models = [];
    expect([...scheduleModel().options].map((o) => o.value)).toEqual(["", "opus"]);
  });

  it("stays open with the reader's edits when the save is refused", async () => {
    const btn = await openPopup();
    popupButton("On a schedule").click();
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(1);
    });
    await Promise.resolve();
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(document.querySelector(".sched-picker")).not.toBeNull();
  });

  it("sends no id for a new rule, and the stored rule's id and inputs on an edit", async () => {
    rulesReply = { rules: [clockRule("r-nightly", { inputs: { tag: "v1" } })] };
    await openPopup({ tag: "string" });
    popupButton("On a schedule").click();
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[0]?.[0]).not.toHaveProperty("id");

    document.body.replaceChildren();
    await openPopup({ tag: "string" });
    popupButton("Edit").click();
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(2);
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[1]?.[0]).toMatchObject({
      id: "r-nightly",
      source: "bundled://nightly",
      when: { clock: daily },
      inputs: { tag: "v1" },
    });
  });

  it("lists the rule a create minted when the list read after it fails", async () => {
    const btn = await openPopup();
    vi.mocked(saveLaunchRule.dispatch).mockImplementationOnce((async () =>
      clockRule("r-minted")) as unknown as typeof saveLaunchRule.dispatch);
    vi.mocked(loadLaunchRules.dispatch).mockImplementationOnce(
      (async () => null) as unknown as typeof loadLaunchRules.dispatch,
    );
    popupButton("On a schedule").click();
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(btn.getAttribute("aria-expanded")).toBe("false");
    });
    btn.click();
    popupButton("Edit").click();
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(2);
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[1]?.[0]).toMatchObject({ id: "r-minted" });
  });

  it("lists the rules a fetch lands after the popup opened", async () => {
    let land: (r: LaunchRulesResponse) => void = () => undefined;
    vi.mocked(loadLaunchRules.dispatch).mockImplementationOnce(
      (() =>
        new Promise<LaunchRulesResponse>((resolve) => {
          land = resolve;
        })) as unknown as typeof loadLaunchRules.dispatch,
    );
    recipesReply = { recipes: [recipe("nightly")] };
    const panel = await render();
    panel.querySelector<HTMLButtonElement>(".recipe-sched-btn")?.click();
    expect(document.querySelector('[data-rule-id="r-late"]')).toBeNull();
    land({ rules: [clockRule("r-late")] });
    await vi.waitFor(() => {
      expect(document.querySelector(".rw-rule")?.getAttribute("data-rule-id")).toBe("r-late");
    });
  });

  it("pauses a rule from its switch, keeping the rest of it", async () => {
    rulesReply = { rules: [clockRule("c1", { inputs: { tag: "v1" }, model: "opus" })] };
    await openPopup({ tag: "string" });
    const box = document.querySelector<HTMLInputElement>(".rw-rule input[type=checkbox]");
    box!.checked = false;
    box!.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[0]?.[0]).toEqual({
      id: "c1",
      source: "bundled://nightly",
      enabled: false,
      when: { clock: daily },
      inputs: { tag: "v1" },
      model: "opus",
    });
  });

  it("deletes a rule after a confirm naming the events it holds", async () => {
    rulesReply = { rules: [eventRule("e1", { held: 3 })] };
    await openPopup();
    popupButton("Delete").click();
    await vi.waitFor(() => {
      expect(vi.mocked(deleteLaunchRule.dispatch)).toHaveBeenCalledWith("e1");
    });
    expect(vi.mocked(confirm).mock.calls[0]?.[0].body).toContain("3 events");

    vi.mocked(deleteLaunchRule.dispatch).mockClear();
    vi.mocked(confirm).mockResolvedValueOnce(false);
    document.body.replaceChildren();
    await openPopup();
    popupButton("Delete").click();
    await new Promise((r) => setTimeout(r, 0));
    expect(vi.mocked(deleteLaunchRule.dispatch)).not.toHaveBeenCalled();
  });

  it("deletes a rule holding nothing without asking", async () => {
    rulesReply = { rules: [clockRule("c1")] };
    await openPopup();
    popupButton("Delete").click();
    await vi.waitFor(() => {
      expect(vi.mocked(deleteLaunchRule.dispatch)).toHaveBeenCalledWith("c1");
    });
    expect(vi.mocked(confirm)).not.toHaveBeenCalled();
  });

  it("drops a stored input the recipe no longer declares when a switch saves the rule", async () => {
    rulesReply = { rules: [clockRule("c1", { inputs: { tag: "v1", gone: "x" } })] };
    await openPopup({ tag: "string" });
    const box = document.querySelector<HTMLInputElement>(".rw-rule input[type=checkbox]");
    box!.checked = false;
    box!.dispatchEvent(new Event("change"));
    await vi.waitFor(() => {
      expect(vi.mocked(saveLaunchRule.dispatch)).toHaveBeenCalledTimes(1);
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[0]?.[0]).toMatchObject({
      id: "c1",
      enabled: false,
      inputs: { tag: "v1" },
    });
    expect(vi.mocked(saveLaunchRule.dispatch).mock.calls[0]?.[0].inputs).not.toHaveProperty("gone");
  });

  describe("an edit that drops held events", () => {
    const github = {
      id: "github:github.com",
      kind: "github",
      host: "github.com",
      account: "a",
      status: "connected",
    } as const;

    function refuseWith409Once(): void {
      vi.mocked(saveEventRule.dispatch).mockImplementationOnce(((
        _args: unknown,
        opts?: { onError?: (e: { message: string; status: number }) => void },
      ) => {
        opts?.onError?.({
          message: "This change drops 3 held events. Send it again with `confirm_drop: 3`",
          status: 409,
        });
        return Promise.resolve(null);
      }) as unknown as typeof saveEventRule.dispatch);
    }

    async function editAndSave(): Promise<HTMLButtonElement> {
      forgesReply = { forges: [github], kinds: ["github"] };
      rulesReply = { rules: [eventRule("e1", { held: 3 })] };
      const btn = await openPopup();
      popupButton("Edit").click();
      await vi.waitFor(() => {
        expect(document.querySelector(".rw-form .rw-repo")).not.toBeNull();
      });
      const repo = document.querySelector<HTMLInputElement>(".rw-form .rw-repo")!;
      repo.value = "o/other";
      repo.dispatchEvent(new Event("input"));
      popupButton("Save").click();
      return btn;
    }

    it("resends the save with the held count once the confirm is accepted", async () => {
      refuseWith409Once();
      vi.mocked(saveEventRule.dispatch).mockImplementationOnce((async () =>
        eventRule("e1")) as unknown as typeof saveEventRule.dispatch);
      await editAndSave();
      await vi.waitFor(() => {
        expect(vi.mocked(saveEventRule.dispatch)).toHaveBeenCalledTimes(2);
      });
      expect(vi.mocked(confirm).mock.calls[0]?.[0].body).toContain("3 events");
      expect(vi.mocked(saveEventRule.dispatch).mock.calls[0]?.[0]).not.toHaveProperty(
        "confirm_drop",
      );
      expect(vi.mocked(saveEventRule.dispatch).mock.calls[1]?.[0]).toMatchObject({
        id: "e1",
        confirm_drop: 3,
        when: { event: { repo: "o/other" } },
      });
      // saveLaunchRule toasts every refusal; the confirm flow's expected 409 must not.
      expect(vi.mocked(saveLaunchRule.dispatch)).not.toHaveBeenCalled();
    });

    it("keeps the form open with no error when the confirm is cancelled", async () => {
      refuseWith409Once();
      vi.mocked(confirm).mockResolvedValueOnce(false);
      const btn = await editAndSave();
      await vi.waitFor(() => {
        expect(vi.mocked(confirm)).toHaveBeenCalledTimes(1);
      });
      await new Promise((r) => setTimeout(r, 0));
      expect(vi.mocked(saveEventRule.dispatch)).toHaveBeenCalledTimes(1);
      expect(btn.getAttribute("aria-expanded")).toBe("true");
      expect(document.querySelector(".rw-form")).not.toBeNull();
      expect(document.querySelector(".rw-error")?.classList.contains("hidden")).toBe(true);
    });
  });

  it("keeps the event form open with the server's sentence on a refusal", async () => {
    forgesReply = {
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          account: "a",
          status: "connected",
        },
      ],
      kinds: ["github"],
    };
    vi.mocked(saveEventRule.dispatch).mockImplementationOnce(((
      _args: unknown,
      opts?: { onError?: (e: { message: string; status: number }) => void },
    ) => {
      opts?.onError?.({ message: "Connect github.com in Sources first", status: 400 });
      return Promise.resolve(null);
    }) as unknown as typeof saveEventRule.dispatch);
    const btn = await openPopup();
    popupButton("When something happens on a forge").click();
    await vi.waitFor(() => {
      expect(document.querySelector(".rw-form .rw-repo")).not.toBeNull();
    });
    popupButton("Save").click();
    await vi.waitFor(() => {
      expect(document.querySelector(".rw-error")?.textContent).toBe(
        "Connect github.com in Sources first",
      );
    });
    expect(vi.mocked(saveEventRule.dispatch).mock.calls[0]?.[0]).toMatchObject({
      source: "bundled://nightly",
      when: { event: { connection: "github:github.com", kind: "run_concluded" } },
    });
    expect(btn.getAttribute("aria-expanded")).toBe("true");
  });
});

describe("a rule whose recipe is gone", () => {
  const GONE = "/workspace/.kiro/workflows/weekly-smoke.workflow.json";

  function ruleOf(source: string, id: string, name: string): LaunchRuleView {
    const clock = { freq: "daily", hour: 3, minute: 0 } as const;
    return { id, source, name, enabled: true, when: { clock }, held: 0 };
  }

  function orphans(panel: HTMLElement): HTMLElement[] {
    return [...panel.querySelectorAll<HTMLElement>("[data-orphan-rule]")];
  }

  async function settled(): Promise<void> {
    await vi.waitFor(() => {
      expect(vi.mocked(loadLaunchRules.dispatch)).toHaveBeenCalled();
    });
    await new Promise((r) => setTimeout(r, 0));
  }

  beforeEach(() => {
    vi.mocked(loadLaunchRules.dispatch).mockClear();
    vi.mocked(deleteLaunchRule.dispatch).mockClear();
  });

  it("is listed under the recipes with a Delete that removes it", async () => {
    recipesReply = { recipes: [recipe("nightly")] };
    rulesReply = {
      rules: [ruleOf("bundled://nightly", "kept", "nightly"), ruleOf(GONE, "gone", "weekly-smoke")],
    };
    const panel = await render();
    await settled();

    const rows = orphans(panel);
    expect(rows.map((r) => r.dataset["orphanRule"])).toEqual(["gone"]);
    const row = rows[0];
    if (row === undefined) {
      throw new Error("no orphaned rule row");
    }
    expect(names(panel)).toEqual(["nightly", "weekly-smoke"]);
    expect(row.textContent).toContain(GONE);
    const del = row.querySelector<HTMLButtonElement>(".recipe-sched-delete-btn");
    expect(del?.textContent).toBe("Delete rule");

    rulesReply = { rules: [ruleOf("bundled://nightly", "kept", "nightly")] };
    del?.click();
    await vi.waitFor(() => {
      expect(orphans(panel)).toHaveLength(0);
    });
    expect(vi.mocked(deleteLaunchRule.dispatch).mock.calls.map((c) => c[0])).toEqual(["gone"]);
  });

  it("is listed when no recipe is left at all", async () => {
    rulesReply = { rules: [ruleOf(GONE, "gone", "weekly-smoke")] };
    const panel = await render();
    await settled();

    expect(orphans(panel)).toHaveLength(1);
    expect(panel.textContent).not.toContain("No workflows available");
  });

  it("is not claimed while the recipe list could not be read", async () => {
    vi.mocked(loadRecipes.dispatch).mockImplementationOnce(
      (async () => null) as unknown as typeof loadRecipes.dispatch,
    );
    rulesReply = { rules: [ruleOf(GONE, "gone", "weekly-smoke")] };
    const panel = document.createElement("div");
    document.body.appendChild(panel);
    renderRecipesPanel(panel);
    await settled();

    expect(orphans(panel)).toHaveLength(0);
  });

  it("answers the filter like a recipe row", async () => {
    recipesReply = { recipes: [recipe("nightly")] };
    rulesReply = { rules: [ruleOf(GONE, "gone", "weekly-smoke")] };
    expect(orphans(await render("nightly"))).toHaveLength(0);
    await settled();
    const panel = await render("smoke");
    await settled();
    expect(orphans(panel)).toHaveLength(1);
  });
});

describe("the Run form's plan", () => {
  const PLAN = [
    { nodeId: "a", type: "step", agentName: "x", modelId: "m" },
    { nodeId: "r", type: "repeat", maxIterations: 3, steps: [{ nodeId: "b", type: "step" }] },
    {
      nodeId: "fan",
      type: "parallel",
      branches: [
        { nodeId: "c", type: "step" },
        { nodeId: "d", type: "step" },
      ],
    },
  ];
  const LABELS = ["a", "r", "pass 1", "b", "fan", "c", "d"];

  function planned(name: string, plan: unknown): RecipesResponse["recipes"][0] {
    return { ...recipe(name), plan };
  }

  async function openForm(r: RecipesResponse["recipes"][0]): Promise<{
    panel: HTMLElement;
    form: HTMLFormElement;
  }> {
    recipesReply = { recipes: [r] };
    const panel = await render();
    buttonFor(panel, r.source)?.click();
    const form = panel.querySelector<HTMLFormElement>(".recipe-input-form");
    if (form === null) {
      throw new Error("the input form did not open");
    }
    return { panel, form };
  }

  function rowLabels(form: HTMLElement): string[] {
    return [...form.querySelectorAll(".recipe-plan .ev-row > .ev-row-main .ev-label")].map(
      (l) => l.textContent ?? "",
    );
  }

  async function catalogChanges(next: RecipesResponse): Promise<void> {
    recipesReply = next;
    dispatch({ type: "recipes_changed", chat_id: "" });
    await Promise.resolve();
    await Promise.resolve();
  }

  it("shows the recipe's steps, all pending, in front of Launch", async () => {
    const { form } = await openForm(planned("planned", PLAN));
    const region = form.querySelector<HTMLElement>(".recipe-plan");

    expect(region?.nextElementSibling).toBe(form.querySelector(".recipe-launch-btn"));
    expect(region?.querySelector(".recipe-plan-head")?.textContent).toBe("Plan \u00b7 4 steps");
    expect(rowLabels(form)).toEqual(LABELS);
    const states = [...form.querySelectorAll<HTMLElement>(".recipe-plan .ev-row")].map(
      (r) => r.dataset["state"],
    );
    expect(new Set(states)).toEqual(new Set(["pending"]));
    expect(form.querySelector('.recipe-plan [role="tree"]')?.getAttribute("aria-label")).toBe(
      "Planned steps",
    );
  });

  it("shows no plan region for a recipe whose plan names no node", async () => {
    for (const [name, plan] of [
      ["unplanned", undefined],
      ["nameless", [{ type: "step" }]],
    ] as const) {
      const { form } = await openForm(planned(name, plan));
      expect(form.querySelector(".recipe-plan"), name).toBeNull();
      expect(form.textContent, name).not.toContain("Plan");
    }
  });

  it("follows a recipes_changed refetch under the kept form", async () => {
    const { panel, form } = await openForm(planned("replanned", PLAN));

    await catalogChanges({ recipes: [planned("replanned", [{ nodeId: "z", type: "step" }])] });
    expect(panel.querySelector(".recipe-input-form")).toBe(form);
    expect(rowLabels(form)).toEqual(["z"]);
    expect(form.querySelector(".recipe-plan-head")?.textContent).toBe("Plan \u00b7 1 step");

    await catalogChanges({ recipes: [planned("replanned", undefined)] });
    expect(form.querySelector(".recipe-plan")).toBeNull();

    await catalogChanges({ recipes: [planned("replanned", PLAN)] });
    expect(form.querySelector(".recipe-plan")?.nextElementSibling).toBe(
      form.querySelector(".recipe-launch-btn"),
    );
    expect(rowLabels(form)).toEqual(LABELS);
  });

  it("scrolls a long plan in a box about eight rows tall, keyboard-reachable", async () => {
    const style = document.createElement("style");
    style.textContent = `:root { --btn-h: 36px; --row-tap: 36px; }\n${loadCSS("28-docs.css")}\n${loadCSS("31-exec-view.css")}`;
    document.head.appendChild(style);
    try {
      const steps = Array.from({ length: 50 }, (_, i) => ({
        nodeId: `s${String(i)}`,
        type: "step",
      }));
      const { form } = await openForm(planned("long", steps));
      const box = form.querySelector<HTMLElement>(".recipe-plan-steps");
      if (box === null) {
        throw new Error("no plan scroll box");
      }
      expect(box.clientHeight).toBeLessThanOrEqual(8 * 36);
      expect(box.clientHeight).toBeGreaterThan(4 * 36);
      expect(box.scrollHeight).toBeGreaterThan(box.clientHeight);
      expect(box.tabIndex).toBe(0);
    } finally {
      style.remove();
    }
  });
});

describe("the Run when… popup's placement", () => {
  let style: HTMLStyleElement;
  let entry: { readonly width: number; readonly height: number } | null = null;

  beforeAll(() => {
    entry = { width: window.innerWidth, height: window.innerHeight };
    style = mountAppCSS();
  });

  afterAll(async () => {
    style.remove();
    if (entry !== null) {
      await page.viewport(entry.width, entry.height);
    }
  });

  async function openAt(width: number, height: number): Promise<HTMLButtonElement> {
    await resizeViewport(width, height);
    recipesReply = { recipes: [recipe("nightly", { tag: "A tag to stamp on the release" })] };
    forgesReply = {
      forges: [
        {
          id: "github:github.com",
          kind: "github",
          host: "github.com",
          username: "cplieger",
          account: "a1",
          status: "connected",
        },
      ],
      kinds: [],
    };
    const panel = await render();
    await vi.waitFor(() => {
      expect(panel.querySelector(".recipe-sched-btn")).not.toBeNull();
    });
    const btn = panel.querySelector<HTMLButtonElement>(".recipe-sched-btn")!;
    btn.click();
    return btn;
  }

  function openPanel(): HTMLElement {
    const open = [...document.querySelectorAll<HTMLElement>(".sched-popup")].filter((p) =>
      p.classList.contains("is-open"),
    );
    expect(open, "one open Run when… panel").toHaveLength(1);
    return open[0]!;
  }

  describe.each([
    [390, 844],
    [1440, 900],
  ])("at %dx%d", (width, height) => {
    it("opens the menu anchored to its button", async () => {
      const btn = await openAt(width, height);
      expectAnchored(openPanel(), btn);
    });

    it("keeps the event form anchored and capped like the rule list", async () => {
      const btn = await openAt(width, height);
      const panel = openPanel();
      const add = [...panel.querySelectorAll<HTMLButtonElement>("button")].find(
        (b) => b.textContent === "When something happens on a forge",
      );
      add!.click();
      await vi.waitFor(() => {
        expect(panel.querySelector(".rw-repo")).not.toBeNull();
      });
      await new Promise((r) => requestAnimationFrame(r));
      expectAnchored(panel, btn);
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize);
      const sp4 = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--sp-4"));
      expect(panel.getBoundingClientRect().width).toBeLessThanOrEqual(
        Math.min(32 * rem, width - 4 * sp4) + 0.5,
      );
      expect(panel.scrollHeight, "the tall form scrolls inside the panel").toBeGreaterThan(
        panel.clientHeight,
      );
    });
  });

  describe("a bundled recipe row with three rules watching", () => {
    async function renderWatched(width: number, height: number): Promise<HTMLElement> {
      await resizeViewport(width, height);
      const checked = new Date();
      checked.setHours(13, 55, 0, 0);
      recipesReply = { recipes: [{ ...recipe("watched"), built_in: true }] };
      const watched = (id: string): LaunchRuleView => ({
        id,
        source: "bundled://watched",
        enabled: true,
        when: {
          event: {
            connection: "github:github.com",
            repo: "cplieger/animap-source",
            kind: "run_concluded",
            filter: { result: ["success"] },
          },
        },
        cursor: { baselined: true },
        status: { checked_at: checked.toISOString() },
        held: 0,
      });
      rulesReply = { rules: [watched("e1"), watched("e2"), watched("e3")] };
      const panel = await render();
      const row = await vi.waitFor(() => {
        const r = panel.querySelector<HTMLElement>('[data-recipe="bundled://watched"]');
        expect(r?.querySelector(".recipe-sched-btn.on")).not.toBeNull();
        return r!;
      });
      return row;
    }

    function shown(e: Element): boolean {
      return e.getBoundingClientRect().width > 0;
    }

    it("gives the state its whole width on a phone", async () => {
      const row = await renderWatched(390, 844);
      const body = row.querySelector<HTMLElement>(".entry-body")!;
      expect(body.getBoundingClientRect().width, "the text column").toBeGreaterThanOrEqual(210);
      const line = row.querySelector<HTMLElement>(".entry-lines > .entry-sub:nth-child(2)")!;
      const state = /^watching, checked \S+/.exec(line.textContent)?.[0];
      expect(state, "the line leads with the state").toBeDefined();
      const range = document.createRange();
      range.setStart(line.firstChild!, 0);
      range.setEnd(line.firstChild!, state!.length);
      expect(range.getBoundingClientRect().right, "the state is not cut").toBeLessThanOrEqual(
        line.getBoundingClientRect().right,
      );
      const title = row.querySelector<HTMLElement>(".entry-title")!.getBoundingClientRect();
      const badge = row.querySelector<HTMLElement>(".docs-badge")!.getBoundingClientRect();
      expect(
        [badge.left >= title.right, badge.top < title.bottom && badge.bottom > title.top],
        "the badge beside the title",
      ).toEqual([true, true]);
    });

    it("shows Run when… as an icon with its count on a phone, at the row's control size", async () => {
      const row = await renderWatched(390, 844);
      const sched = row.querySelector<HTMLButtonElement>(".recipe-sched-btn")!;
      const run = row.querySelector<HTMLButtonElement>(".recipe-run-btn")!;
      expect(shownText(sched), "the count is the only text").toBe("3");
      expect(sched.querySelector("svg") !== null && shown(sched.querySelector("svg")!)).toBe(true);
      expect(sched.getAttribute("aria-label")).toMatch(/^Run when… /);
      expect(sched.getAttribute("data-tooltip")).toBe("Run when…");
      const b = sched.getBoundingClientRect();
      expect(b.height, "the row's control height").toBe(run.getBoundingClientRect().height);
      expect(b.width, "at least square").toBeGreaterThanOrEqual(b.height);
    });

    it("keeps the words and the badge's own place on a desktop", async () => {
      const row = await renderWatched(1440, 900);
      const sched = row.querySelector<HTMLButtonElement>(".recipe-sched-btn")!;
      expect(shownText(sched)).toBe("Run when… 3");
      expect(sched.querySelector("svg") === null || !shown(sched.querySelector("svg")!)).toBe(true);
      expect(sched.hasAttribute("data-tooltip")).toBe(false);
      const body = row.querySelector<HTMLElement>(".entry-body")!.getBoundingClientRect();
      const badge = row.querySelector<HTMLElement>(".docs-badge")!.getBoundingClientRect();
      expect(badge.left, "the badge after the text column").toBeGreaterThanOrEqual(body.right);
    });
  });

  it("leads the recipe row with the rule's state and keeps its whole line readable", async () => {
    await resizeViewport(390, 844);
    const checked = new Date();
    checked.setHours(13, 55, 0, 0);
    recipesReply = { recipes: [recipe("nightly")] };
    rulesReply = {
      rules: [
        {
          id: "e1",
          source: "bundled://nightly",
          enabled: true,
          when: {
            event: {
              connection: "github:github.com",
              repo: "cplieger/animap-source",
              kind: "run_concluded",
              filter: { result: ["success"] },
            },
          },
          cursor: { baselined: true },
          status: { checked_at: checked.toISOString() },
          held: 0,
        },
      ],
    };
    const panel = await render();
    const row = '[data-recipe="bundled://nightly"]';
    const line = await vi.waitFor(() => {
      const l = panel.querySelector<HTMLElement>(`${row} .entry-lines > .entry-sub:nth-child(2)`);
      expect(l?.textContent).toMatch(/^watching, checked /);
      return l!;
    });
    expect(line.getAttribute("data-tooltip")).toBe(line.textContent);
    expect(line.scrollHeight, "the row keeps its one line").toBeLessThanOrEqual(line.clientHeight);
    panel.querySelector<HTMLButtonElement>(`${row} .recipe-sched-btn`)!.click();
    const full = await vi.waitFor(() => {
      const l = openPanel().querySelector<HTMLElement>(".rw-line");
      expect(l).not.toBeNull();
      return l!;
    });
    expect(full.textContent).toContain("cplieger/animap-source");
    expect(full.textContent).toContain("watching, checked ");
    expect(full.scrollWidth, "the list shows the whole line").toBeLessThanOrEqual(full.clientWidth);
  });
});
