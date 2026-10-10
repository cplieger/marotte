// The Workflows sub-tab of the configuration browser (/docs/workflows): the launchable recipes,
// each with the app's ONE run affordance — Run ⇄ Cancel.

import { el } from "@cplieger/reactive";
import { join as joinKey } from "@cplieger/keyenc";
import { onBus, onSSE, BUS_RUNS_CHANGED } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { loadRecipes, loadRuns, launchRun, cancelRun } from "./actions/runs.js";
import { loadSchedules, saveSchedule, deleteSchedule } from "./actions/schedules.js";
import { buildSchedulePicker, defaultSpec, summaryLine } from "./schedule-picker.js";
import type { ScheduleSpec, ScheduleView } from "./schedule-types.js";
import { createPopup } from "@cplieger/ui-primitives/popup";
import { openRunView } from "./run-view.js";
import { loadSettings } from "./persist.js";
import { openSettingsView } from "./tabs.js";
import { forceReflow } from "./dom.js";
import type { Recipe, WorkflowRun } from "./types.js";
import { entryDetail, entryList, entryRow } from "./entry-row.js";
import { classifyRunStatus, runStatusTerminal } from "./run-status.js";

/** Last fetched recipe list, kept so a repaint needs no refetch. */
let recipes: Recipe[] = [];

/** MODULE state, not a render parameter, and that distinction is load-bearing: this panel repaints
 *  itself on its own schedule (the run poll, the schedules fetch, the settings SSE), and a parameter
 *  threaded only through the render call would be dropped by the next one — so the filter would
 *  silently lift seconds after it was typed. */
let filterText = "";

/** Live (non-terminal) run per RECIPE; the single-run rule makes the recipe a sufficient key.
 *  Keyed on `workflow_name`, never `name` — `name` is the DISPLAY label (`runLabel ??
 *  workflowName` upstream), so a labelled run filed itself under a key no recipe row looks up
 *  and its row offered Run over a live run. */
let liveRuns = new Map<string, WorkflowRun>();

let container: HTMLElement | null = null;
let wired = false;

/** How many recipes exist and how many the filter is showing, so the page's one note reads the
 *  same on this tab as on the five document tabs. */
interface RecipeCounts {
  total: number;
  shown: number;
}

/** Every field of a recipe row a reader could plausibly type at, folded once. */
function filterHaystack(r: Recipe): string {
  return [
    r.name,
    r.description ?? "",
    r.source,
    ...Object.keys(r.inputs ?? {}),
    r.built_in === true ? "bundled" : "",
  ]
    .join("\n")
    .toLowerCase();
}

function visibleRecipes(): Recipe[] {
  if (filterText === "") {
    return recipes;
  }
  return recipes.filter((r) => filterHaystack(r).includes(filterText));
}

/** Render the Workflows tab into its panel. Called by docs.ts on tab switch and on its inventory
 *  refetches; fetches recipes + runs and repaints. Takes the page's filter, because the box
 *  lives on docs.ts and the rows live here. */
export function renderRecipesPanel(panel: HTMLElement, filter = ""): void {
  container = panel;
  // Folded HERE, so no caller has to know the convention. docs.ts already hands over a folded
  // query; taking it on trust would make this function's answer depend on a rule stated in another
  // module.
  filterText = filter.trim().toLowerCase();
  if (!wired) {
    wired = true;
    // A run starting or finishing flips a row's button. The bus event fires on the run SSE events,
    // so the tab tracks launches made anywhere — another device, the agent, the TUI via a later
    // list refresh.
    onBus(BUS_RUNS_CHANGED, () => {
      if (isShowing()) {
        void refreshRuns();
      }
    });
    // The schedule form's unattended note reads the auto-approve setting, and that setting is
    // changed on another page. Without this the note would be correct as of whenever this tab last
    // rendered, which is the boilerplate a live read-out exists to avoid.
    onSSE("settings_updated", () => {
      void refreshAutoApprove();
    });
    // KAS reports a recipe added, edited or removed; the catalog is not cached anywhere, so the
    // list is simply read again while it is on screen.
    onSSE("recipes_changed", () => {
      if (isShowing()) {
        void refreshRecipes();
      }
    });
  }
  if (recipes.length === 0) {
    panel.replaceChildren(el("div", { className: "list-empty" }, "Loading workflows…"));
    onCounts?.(counts());
  } else {
    // Already fetched: paint (and report) in this tick, so a tab switch does not leave the previous
    // tab's count sitting under the box until the refetch lands.
    paint();
  }
  void (async () => {
    const [r] = await Promise.all([loadRecipes.dispatch(undefined), refreshRuns()]);
    if (r !== null) {
      recipes = r.recipes;
    }
    paint();
    // Decoration, deliberately off the critical path: the workflow list must still render when the
    // schedule endpoint is unavailable.
    void refreshSchedules();
    void refreshAutoApprove();
  })();
}

async function refreshRecipes(): Promise<void> {
  const r = await loadRecipes.dispatch(undefined);
  if (r !== null) {
    recipes = r.recipes;
    paint();
  }
}

function counts(): RecipeCounts {
  return { total: recipes.length, shown: visibleRecipes().length };
}

/** Set once by docs.ts, because the recipe fetch lands long after `renderRecipesPanel` returned its
 *  first answer — without this, the tab's note would read "0 of 0 shown." until the next keystroke. */
let onCounts: ((c: RecipeCounts) => void) | null = null;

export function setRecipeCountsListener(fn: (c: RecipeCounts) => void): void {
  onCounts = fn;
}

/** Off is the safe default and the server's own: absent or unreadable settings mean off there too,
 *  so a failed read cannot make the note claim more permission than the run will get. */
let autoApprove = false;

async function refreshAutoApprove(): Promise<void> {
  const s = await loadSettings();
  // A failed read leaves the last known value rather than resetting to false: false is the SERVER's
  // default for an absent key, which the payload now states outright, so re-deriving it here on a
  // network failure would be this module guessing at a value it can just be told.
  if (s !== null) {
    autoApprove = s.scheduled_auto_approve;
  }
}

/** Schedules by recipe source. One per recipe, matching the single-run rule. */
let schedules = new Map<string, ScheduleView>();

async function refreshSchedules(): Promise<void> {
  const d = await loadSchedules.dispatch(undefined);
  if (d === null) {
    return;
  }
  const next = new Map<string, ScheduleView>();
  for (const v of d.schedules) {
    next.set(v.source, v);
  }
  schedules = next;
  paint();
}

/** Reflect a recipe's schedule onto its row's second line and its button. */
function syncSchedule(row: HTMLElement, r: Recipe): void {
  const line = scheduleLineOf(row);
  if (line === null) {
    return;
  }
  const view = schedules.get(r.source);
  line.textContent = scheduleLine(r);
  line.classList.toggle("is-scheduled", view?.enabled === true);
  const btn = row.querySelector<HTMLButtonElement>(".recipe-sched-btn");
  if (btn !== null) {
    btn.classList.toggle("on", view?.enabled === true);
  }
}

function wireSchedulePopup(btn: HTMLButtonElement, source: string): void {
  let popup: ReturnType<typeof createPopup> | null = null;
  let open = false;

  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    if (open && popup !== null) {
      popup.hide();
      open = false;
      return;
    }
    const view = schedules.get(source);
    const body = el("div", { className: "sched-popup" });
    // Rebuilt per open rather than cached: the picker renders the CURRENT schedule, and a cached
    // body would show whatever the set looked like the first time the button was pressed.
    popup?.hide();
    popup = createPopup(body, { trigger: btn, group: "recipe-schedule" });
    const close = (): void => {
      popup?.hide();
      open = false;
    };
    body.appendChild(
      buildSchedulePicker({
        spec: view?.spec ?? defaultSpec(),
        enabled: view?.enabled ?? false,
        exists: view !== undefined,
        autoApprove,
        onOpenPermissions: () => {
          close();
          void openSettingsView("permissions");
        },
        // The picker's flag is carried through rather than re-decided here: it was hardcoded true
        // at both ends, so a paused schedule was unreachable even once the form could express it.
        onSave: (spec: ScheduleSpec, enabled: boolean) => {
          close();
          void (async () => {
            await saveSchedule.dispatch({ source, spec, enabled });
            await refreshSchedules();
          })();
        },
        onRemove: () => {
          close();
          void (async () => {
            if (view !== undefined) {
              await deleteSchedule.dispatch(view.id);
            }
            await refreshSchedules();
          })();
        },
        onClose: close,
      }),
    );
    popup.show();
    open = true;
  });
}

function isShowing(): boolean {
  return (
    container !== null && container.childElementCount > 0 && !container.classList.contains("hidden")
  );
}

async function refreshRuns(): Promise<void> {
  const d = await loadRuns.dispatch(undefined);
  if (d === null) {
    return;
  }
  const next = new Map<string, WorkflowRun>();
  for (const run of d.runs) {
    // An older server sends no workflow_name; fall back to the display name so the row keeps its
    // pre-2.21.1 behaviour rather than losing every key.
    const recipe = run.workflow_name ?? run.name;
    const status = classifyRunStatus(run.status);
    if (recipe !== "" && (status === undefined || !runStatusTerminal(status))) {
      next.set(recipe, run);
    }
  }
  liveRuns = next;
  paint();
}

/** What the list reconciles: a recipe's row, and — for the one recipe whose input form is open —
 *  the `.entry-detail` region right under it. The detail is a KEYED member rather than a stray
 *  sibling, because `reconcile` re-seats every keyed row around an unkeyed node; keyed, it is
 *  placed with its row and its typed values survive every repaint. */
type RecipeEntry =
  | { readonly kind: "row"; readonly recipe: Recipe }
  | { readonly kind: "detail"; readonly recipe: Recipe };

/** The recipe whose input form is open, by source; at most one, like a `<details>` group. */
let openForm: string | null = null;

function paint(): void {
  if (container === null) {
    return;
  }
  const rows = visibleRecipes();
  // Every repaint path reports, not just the fetch: the note describes what is on screen, so
  // whichever caller changed what is on screen owes the update.
  onCounts?.(counts());
  if (rows.length === 0) {
    // Two different sentences, because they answer different questions. "No workflows available."
    // under an active filter is the same lie docs.ts records for its category text: the workflows
    // exist, they are one keystroke away.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        recipes.length === 0 ? "No workflows available." : "No workflows match the filter.",
      ),
    );
    return;
  }
  let list = container.querySelector<HTMLElement>(":scope > .list-container");
  if (list === null) {
    list = entryList();
    container.replaceChildren(list);
  }
  const entries: RecipeEntry[] = [];
  for (const recipe of rows) {
    entries.push({ kind: "row", recipe });
    if (recipe.source === openForm) {
      entries.push({ kind: "detail", recipe });
    }
  }
  // Keyed on `source`, which is stable across a filter change — so a keystroke removes and re-adds
  // the rows that left and arrived rather than rebuilding the list.
  reconcile(list, entries, {
    key: (e: RecipeEntry) => joinKey(e.kind, e.recipe.source),
    mount: (e: RecipeEntry) => (e.kind === "row" ? recipeRow(e.recipe) : inputForm(e.recipe)),
    update: (node: HTMLElement, e: RecipeEntry) => {
      // A kept detail is left alone: it holds what the reader has typed.
      if (e.kind === "row") {
        syncButton(node, e.recipe);
        syncSchedule(node, e.recipe);
      }
    },
  });
  // The region mounts closed and opens from a flushed layout, which is what turns
  const detail = list.querySelector<HTMLElement>(".entry-detail:not(.open)");
  if (detail !== null) {
    forceReflow(detail);
    detail.classList.add("open");
    detail.querySelector<HTMLInputElement>("input")?.focus({ preventScroll: true });
  }
}

function recipeRow(r: Recipe): HTMLElement {
  const btn = el("button", {
    type: "button",
    className: "btn-small recipe-run-btn",
  }) as HTMLButtonElement;
  // The recipe is resolved at CLICK time, not captured at mount: reconcile keeps a row whose key
  // matches and only runs update(), so a mount-time closure would keep serving the recipe as it
  // looked on FIRST paint — a refetched input set or description would never reach the click.
  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    const current = recipes.find((x) => x.source === r.source);
    if (current !== undefined) {
      onRunButton(current);
    }
  });

  const schedBtn = el("button", {
    type: "button",
    className: "btn-small recipe-sched-btn",
    "aria-label": `Schedule ${r.name}`,
  }) as HTMLButtonElement;
  schedBtn.textContent = "Schedule";
  wireSchedulePopup(schedBtn, r.source);

  // No `open`: a recipe row is not a door. Its one destination is the run its button launches, and
  // that run opens its own tab.
  const row = entryRow({
    key: r.source,
    title: r.name,
    badges: r.built_in === true ? [el("span", { className: "docs-badge" }, "bundled")] : undefined,
    sub: {
      kind: "lines",
      lines: [{ text: r.description ?? "" }, { text: scheduleLine(r) }],
    },
    actions: [schedBtn, btn],
    data: { "data-recipe": r.source },
  });
  syncButton(row, r);
  syncSchedule(row, r);
  return row;
}

/** The row's second line: the schedule's state, then the declared inputs. */
function scheduleLine(r: Recipe): string {
  const inputs = Object.keys(r.inputs ?? {});
  const sched = summaryLine(schedules.get(r.source));
  return inputs.length === 0 ? sched : `${sched} · Inputs: ${inputs.join(", ")}`;
}

/** The line `scheduleLine` fills: the second of the row's two subtitle lines. */
function scheduleLineOf(row: HTMLElement): HTMLElement | null {
  return row.querySelector<HTMLElement>(".entry-lines > .entry-sub:nth-child(2)");
}

function syncButton(row: HTMLElement, r: Recipe): void {
  const btn = row.querySelector<HTMLButtonElement>(".recipe-run-btn");
  if (btn === null) {
    return;
  }
  const live = liveRuns.get(r.name);
  btn.textContent = live === undefined ? "Run" : "Cancel";
  btn.classList.toggle("danger", live !== undefined);
  btn.setAttribute(
    "aria-label",
    live === undefined ? `Run ${r.name}` : `Cancel the running ${r.name}`,
  );
}

function onRunButton(r: Recipe): void {
  const live = liveRuns.get(r.name);
  if (live !== undefined) {
    void cancelRun.dispatch(live.workflow_id);
    return;
  }
  if (Object.keys(r.inputs ?? {}).length === 0) {
    launch(r, {});
    return;
  }
  openForm = openForm === r.source ? null : r.source;
  paint();
}

/** Inline input collection in the `.entry-detail` region under the row — deliberately not a
 *  modal. Empty values are allowed (KAS accepts them; templates resolve empty), so this collects
 *  rather than validates. */
function inputForm(r: Recipe): HTMLElement {
  const fields = new Map<string, HTMLInputElement>();
  const form = el("form", { className: "recipe-input-form" });
  for (const key of Object.keys(r.inputs ?? {})) {
    const input = el("input", {
      type: "text",
      className: "recipe-input",
      placeholder: r.inputs?.[key] ?? "string",
      "aria-label": `${r.name} input ${key}`,
    }) as HTMLInputElement;
    fields.set(key, input);
    form.appendChild(el("label", { className: "recipe-input-label" }, `${key}: `, input));
  }
  form.appendChild(el("button", { type: "submit", className: "btn-small" }, "Launch"));
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    const inputs: Record<string, string> = {};
    for (const [key, field] of fields) {
      if (field.value !== "") {
        inputs[key] = field.value;
      }
    }
    openForm = null;
    paint();
    launch(r, inputs);
  });
  return entryDetail(form);
}

function launch(r: Recipe, inputs: Record<string, string>): void {
  void launchRun.dispatch(
    { source: r.source, inputs },
    {
      onSuccess: (d) => {
        // Optimistic flip: the run_started event confirms it, and the refetch corrects a failed
        // launch the server accepted but KAS then refused.
        liveRuns.set(r.name, { workflow_id: d.workflow_id, name: d.name, updated_at: Date.now() });
        paint();
        void openRunView(d.workflow_id, d.name);
      },
    },
  );
}
