// ---------------------------------------------------------------------------
// Tests for the Workflows sub-tab. Two halves.
//
// The Run ⇄ Cancel row logic. Each case pins a piece of the single-run contract:
//   - the button names the recipe's ONE possible live run (Run ⇄ Cancel flips
//     on the run list, not on who launched it)
//   - paused is NOT terminal: a paused run still blocks a relaunch, so its row
//     must offer Cancel or the recipe wedges with no way out
//   - launching with declared inputs collects them inline (no modal)
//   - a launch opens the run tab that OWNS the run
//
// The row's SHAPE: a recipe row is the shared `.entry` builder's output like
// every other row on the page, and its input form is the `.entry-detail` region
// under it rather than a block inside it.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { loadCSS, ruleBody } from "./__test-helpers__/css-rules.js";

/** Every authored client source file plus the shipped page, inlined as text.
 *  `import.meta.glob` replaces the directory walk this used to do with `readdir`:
 *  the corpus is the same set of files, resolved at transform time instead. */
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
// The unattended note's auto-approve read-out. Unmocked, refreshAutoApprove
// reaches /api/settings through the actions transport (which the api-client mock
// does not cover), fire-and-forget, so the request was still open when the window
// tore down and printed an unhandled AbortError.
vi.mock("./persist.js", () => ({ loadSettings: vi.fn(async () => ({})) }));

// The Schedule button's actions: unmocked they reach the network, and a row's
// summary line is decoration this suite does not assert on.
vi.mock("./actions/schedules.js", () => ({
  loadSchedules: { dispatch: vi.fn(async () => ({ schedules: [] })) },
  saveSchedule: { dispatch: vi.fn(async () => null) },
  deleteSchedule: { dispatch: vi.fn(async () => null) },
}));

import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { openRunView } from "./run-view.js";
import { launchRun, cancelRun } from "./actions/runs.js";
import type { RecipesResponse, WorkflowRun, ResumableSession } from "./types.js";
import type { RunStatus } from "./wire/types.gen.js";

let recipesReply: RecipesResponse = { recipes: [] };
let runsReply: { sessions: ResumableSession[]; runs: WorkflowRun[] } = {
  sessions: [],
  runs: [],
};

function recipe(name: string, inputs?: Record<string, string>): RecipesResponse["recipes"][0] {
  const base = { name, source: `bundled://${name}`, description: `${name} desc` };
  return inputs === undefined ? base : { ...base, inputs };
}

// `status` is a bare string, not `RunStatus`: one case spells a word an engine ahead
// of this build would send, which is exactly what the live/terminal read has to
// survive. The cast is the wire lie being modelled.
function run(name: string, id: string, status: string): WorkflowRun {
  return { workflow_id: id, name, status: status as RunStatus, updated_at: 0 };
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

beforeEach(() => {
  document.body.replaceChildren();
  dispatched.length = 0;
  vi.mocked(openRunView).mockClear();
  vi.mocked(launchRun.dispatch).mockClear();
  vi.mocked(cancelRun.dispatch).mockClear();
  recipesReply = { recipes: [] };
  runsReply = { sessions: [], runs: [] };
});

describe("the Run ⇄ Cancel row", () => {
  it("labels an idle recipe Run and a live one Cancel", async () => {
    recipesReply = { recipes: [recipe("goal"), recipe("investigate")] };
    runsReply.runs = [run("investigate", "wf_9", "running")];
    const panel = await render();

    expect(buttonFor(panel, "bundled://goal")?.textContent).toBe("Run");
    expect(buttonFor(panel, "bundled://investigate")?.textContent).toBe("Cancel");
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

  it("launches an input-less recipe on click and opens its run tab", async () => {
    recipesReply = { recipes: [recipe("goal")] };
    const panel = await render();

    buttonFor(panel, "bundled://goal")?.click();
    expect(vi.mocked(launchRun.dispatch).mock.calls[0]?.[0]).toEqual({
      source: "bundled://goal",
      inputs: {},
    });
    expect(vi.mocked(openRunView)).toHaveBeenCalledWith("wf_new", "goal");
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

describe("the recipe row on the shared builder", () => {
  function rowFor(panel: HTMLElement, source: string): HTMLElement {
    const row = panel.querySelector<HTMLElement>(`[data-recipe="${source}"]`);
    if (row === null) {
      throw new Error(`no row rendered for ${source}`);
    }
    return row;
  }

  // Each case names its own recipe. The module keeps the last fetched list, and
  // `reconcile` keys rows by source and runs only `update()` on a key it already
  // has — so reusing one name across cases would let a row MOUNTED from the
  // previous case's recipe survive into this one and be asserted against the new
  // one's fields. A unique key per case is what forces a fresh mount.

  it("is an inert entry whose two lines are the description and the schedule", async () => {
    // Not a door: a recipe row's one destination is the run its button launches,
    // and that run opens its own tab.
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

  it("puts Schedule then Run in the actions slot as text buttons", async () => {
    recipesReply = { recipes: [recipe("actions")] };
    const panel = await render();
    const actions = [
      ...(rowFor(panel, "bundled://actions").querySelector(".entry-actions")?.children ?? []),
    ];
    expect(actions.map((a) => a.className)).toEqual([
      "btn-small recipe-sched-btn",
      "btn-small recipe-run-btn",
    ]);
    expect(actions.map((a) => a.textContent)).toEqual(["Schedule", "Run"]);
  });

  it("keeps the second line for the schedule when a recipe declares no inputs", async () => {
    recipesReply = { recipes: [recipe("plain")] };
    const panel = await render();
    const lines = [
      ...(rowFor(panel, "bundled://plain").querySelector(".entry-lines")?.children ?? []),
    ];
    expect(lines.map((l) => l.textContent)).toEqual(["plain desc", "Not scheduled"]);
  });

  it("mounts the input form as a detail region UNDER the row, never inside it", async () => {
    // The row's height is the list's tier and never grows; the only thing that
    // may grow is a sibling region below it.
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
    // The panel repaints on its own schedule — the run poll, the schedules
    // fetch — so a form that was rebuilt on each one would lose its values.
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
  // `.text-muted` and `.text-sm` were on seven elements across four files and
  // declared in no stylesheet, so "Not scheduled" rendered at full primary ink
  // and outshouted the `.is-scheduled` accent meant to distinguish it. They were
  // REPLACED rather than defined: the utilities layer ranks below every
  // unlayered feature slice, so a `.text-muted` there would have lost to the
  // component rules at some of those very sites and won at others — a class that
  // works in some places is worse than one that works nowhere. Each site takes
  // its ink from its own component rule instead.

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
    // The dormant line is the builder's own `.entry-sub` and carries no rule of
    // its own here; only the live state earns one.
    const docs = loadCSS("28-docs.css");
    expect(
      /color:\s*var\(--c-accent\)/.test(ruleBody(docs, ".docs-panel .entry-sub.is-scheduled")),
    ).toBe(true);
    expect(docs).not.toMatch(/recipe-sched-summary/);
  });

  it("gives every former use site a component rule that carries its ink", () => {
    // `.run-id` and `.run-output-empty` were this page's own; the page renders the
    // run CARD now, so their equivalents live with the component. Both are still
    // tertiary ink — that is what the muted-class sweep is checking — they are just
    // in the stylesheet that owns the element.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("27-run-card.css"), ".run-output-val-empty"),
      ),
    ).toBe(true);
    // The run PAGE's own note moved to the exec view, which is a different component
    // with its own vocabulary — the page renders a delegated-execution view now rather
    // than a variant of the transcript's card.
    expect(
      /color:\s*var\(--c-text-tertiary\)/.test(
        ruleBody(loadCSS("31-exec-view.css"), ".ev-d-empty"),
      ),
    ).toBe(true);
    // The History search note moved with its box: the page search boxes are one
    // popup now (24-find.css `.page-find`), so `.hist-search-note` is gone and
    // `.page-find-note` carries the ink for all four of them.
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

// ---------------------------------------------------------------------------
// The filter, which this tab did not have.
//
// The configuration browser's box was HIDDEN on this tab, on the reasoning that
// Workflows is RPC-sourced and escapes here before any docs logic runs — true
// about where the rows come from, and not the same claim as "nothing to filter".
// A recipe has a name, a description, a source and declared inputs, so the box
// reaches this panel now instead of hiding from it.
// ---------------------------------------------------------------------------

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
    // Same rule docs.ts applies to its own badges: a reader types at what they can
    // see.
    recipesReply = {
      recipes: [
        { name: "one", source: "bundled://one", built_in: true },
        { name: "two", source: "b://two" },
      ],
    };
    expect(names(await render("bundled"))).toEqual(["one"]);
  });

  it("cannot reach the node PLAN, which is raw JSON nobody types at", async () => {
    // Folding it in would match on punctuation and internal key names, so the box
    // would be answering a different question than it appears to ask.
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
    // "No workflows available." under an active filter is the same lie docs.ts
    // records for its category text: they exist, they are one keystroke away.
    recipesReply = { recipes: [recipe("goal")] };
    const filtered = await render("zzzz");
    expect(filtered.textContent).toContain("No workflows match the filter");
    expect(filtered.textContent).not.toContain("No workflows available");
    recipesReply = { recipes: [] };
    expect((await render()).textContent).toContain("No workflows available");
  });

  it("reports its counts on every repaint, not only on the fetch", async () => {
    // The note describes what is on screen, so whichever caller changed what is on
    // screen owes the update — the run poll and the schedules fetch repaint too.
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
    // recipeRow resolves at CLICK time against the UNFILTERED list, which is what
    // survives reconcile keeping a row across a keystroke.
    recipesReply = { recipes: [recipe("goal"), recipe("triage")] };
    const panel = await render("triage");
    buttonFor(panel, "bundled://triage")?.click();
    expect(dispatched.filter((d) => d.name === "launch")).toHaveLength(1);
    expect(dispatched.at(-1)?.args).toMatchObject({ source: "bundled://triage" });
  });
});
