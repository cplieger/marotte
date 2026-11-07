// The Workflows sub-tab of the configuration browser (/docs/workflows): the launchable recipes,
// each with the app's ONE run affordance — Run ⇄ Cancel.

import { el } from "@cplieger/ui";
import { join as joinKey } from "@cplieger/keyenc";
import { onBus, onSSE, BUS_CATALOG_MODELS, BUS_RUNS_CHANGED } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { loadRecipes, loadRuns, launchRun, cancelRun } from "./actions/runs.js";
import {
  deleteLaunchRule,
  dismissGap,
  loadLaunchRules,
  saveLaunchRule,
  type LaunchRuleBody,
} from "./actions/launch-rules.js";
import { listForges } from "./actions/forge-list.js";
import { serverSentence } from "./actions/server-sentence.js";
import { confirm } from "./confirm.js";
import { buildSchedulePicker, defaultSpec } from "./schedule-picker.js";
import { buildEventForm, buildRuleList, ruleError, ruleLine } from "./run-when.js";
import {
  buildRunDefaultsPicker,
  type RunDefaults,
  type RunDefaultsPicker,
} from "./run-defaults-picker.js";
import { getCachedModels } from "./picker.js";
import type { LaunchRuleView } from "./launch-rule-types.js";
import { createPopup } from "@cplieger/ui/popup";
import { openRunView } from "./run-view.js";
import { loadSettings, refreshSettings } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { openSettingsView } from "./tabs.js";
import { forceReflow } from "./dom.js";
import type { Recipe, WorkflowRun } from "./types.js";
import { entryDetail, entryList, entryRow } from "./entry-row.js";
import { classifyRunStatus, runStatusTerminal } from "./run-status.js";
import { configStanding, standingBadge, standingLabel } from "./config-standing.js";
import type { ConfigStanding } from "./wire/types.gen.js";
import { planToExec } from "./run-exec-source.js";
import { buildExecTree, type ExecTreeView } from "./exec-view/tree.js";
import { workNodes } from "./exec-view/model.js";

/** Last fetched recipe list, kept so a repaint needs no refetch. */
let recipes: Recipe[] = [];

/** Whether the latest recipe fetch answered. A rule is called orphaned only against a current
 *  list: a failed refetch keeps the old rows on screen, but absence from them proves nothing. */
let recipesCurrent = false;

/** MODULE state, not a render parameter, and that distinction is load-bearing: this panel repaints
 *  itself on its own schedule (the run poll, the rules fetch, the settings SSE), and a parameter
 *  threaded only through the render call would be dropped by the next one — so the filter would
 *  silently lift seconds after it was typed. */
let filterText = "";

/** Live (non-terminal) run per RECIPE; the single-run rule makes the recipe a sufficient key.
 *  Keyed on `workflow_name`, never `name`: `name` is the DISPLAY label, so a labelled run would
 *  file itself under a key no recipe row looks up and its row would offer Run over a live run. */
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
  const standing = standingOf(r);
  return [
    r.name,
    r.description ?? "",
    r.source,
    ...Object.keys(r.inputs ?? {}),
    r.built_in === true ? "bundled" : "",
    standing === undefined ? "" : standingLabel(standing),
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

/** Rules whose recipe is not in the current list: no recipe row carries them, so they get their
 *  own, or they could never be deleted. */
function orphanedRules(): LaunchRuleView[] {
  if (!recipesCurrent) {
    return [];
  }
  const known = new Set(recipes.map((r) => r.source));
  return [...rules.values()].flat().filter((v) => !known.has(v.source));
}

function visibleOrphans(): LaunchRuleView[] {
  return orphanedRules().filter(
    (v) =>
      filterText === "" ||
      [v.name ?? "", v.source, "orphaned"].join("\n").toLowerCase().includes(filterText),
  );
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
    // The rule forms' unattended note and event lines read the auto-approve setting, and that setting is
    // changed on another page. Without this the note would be correct as of whenever this tab last
    // rendered, which is the boilerplate a live read-out exists to avoid.
    onSSE("settings_updated", () => {
      void refreshAutoApprove(refreshSettings);
    });
    // KAS reports a recipe added, edited or removed; the catalog is not cached anywhere, so the
    // list is simply read again while it is on screen.
    onSSE("recipes_changed", () => {
      if (isShowing()) {
        void refreshRecipes();
      }
    });
    // An open Run or rule form holds the catalog it opened with, which can be empty when it
    // opened before the boot fetch landed.
    onBus(BUS_CATALOG_MODELS, () => {
      const models = getCachedModels();
      const detail = container?.querySelector<HTMLElement>(".entry-detail") ?? null;
      if (detail !== null) {
        formPicks.get(detail)?.setModels(models);
      }
      rulePicks?.setModels(models);
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
    recipesCurrent = r !== null;
    if (r !== null) {
      recipes = r.recipes;
    }
    paint();
    // Decoration, deliberately off the critical path: the workflow list must still render when the
    // launch-rule endpoint is unavailable.
    void refreshRules();
    void refreshAutoApprove(loadSettings);
  })();
}

async function refreshRecipes(): Promise<void> {
  const r = await loadRecipes.dispatch(undefined);
  recipesCurrent = r !== null;
  if (r !== null) {
    recipes = r.recipes;
  }
  paint();
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

async function refreshAutoApprove(read: () => Promise<EffectiveSettings | null>): Promise<void> {
  const s = await read();
  // A failed read leaves the last known value rather than resetting to false: false is the SERVER's
  // default for an absent key, which the payload now states outright, so re-deriving it here on a
  // network failure would be this module guessing at a value it can just be told.
  if (s !== null && s.scheduled_auto_approve !== autoApprove) {
    autoApprove = s.scheduled_auto_approve;
    // An event rule's line names auto-approve while it is on.
    paint();
  }
}

/** Launch rules by recipe source, in the server's order. */
let rules = new Map<string, LaunchRuleView[]>();

/** The open popup's rule list, repainted when a fetch lands; null while it shows a form. */
let popupList: { readonly source: string; readonly repaint: () => void } | null = null;

async function refreshRules(): Promise<void> {
  const d = await loadLaunchRules.dispatch(undefined);
  if (d === null) {
    return;
  }
  const next = new Map<string, LaunchRuleView[]>();
  for (const v of d.rules) {
    next.set(v.source, [...(next.get(v.source) ?? []), v]);
  }
  rules = next;
  popupList?.repaint();
  paint();
}

/** Put a rule the server just answered in place, before a refresh confirms it: a failed list read
 *  would otherwise leave no id, and the next save would mint a second rule. */
function storeRule(v: LaunchRuleView): void {
  const list = rules.get(v.source) ?? [];
  const i = list.findIndex((x) => x.id === v.id);
  rules.set(v.source, i < 0 ? [...list, v] : list.map((x, j) => (j === i ? v : x)));
  popupList?.repaint();
  paint();
}

function forgetRule(v: LaunchRuleView): void {
  rules.set(
    v.source,
    (rules.get(v.source) ?? []).filter((x) => x.id !== v.id),
  );
  popupList?.repaint();
  paint();
}

/** The stored inputs a save can send back: a save naming an input the recipe no longer declares
 *  is refused, so those go once the recipe is known. */
function keptInputs(v: LaunchRuleView): Record<string, string> | undefined {
  const r = recipes.find((x) => x.source === v.source);
  if (v.inputs === undefined || r === undefined) {
    return v.inputs;
  }
  const declared = r.inputs ?? {};
  const kept = Object.fromEntries(
    Object.entries(v.inputs).filter(([n]) => Object.hasOwn(declared, n)),
  );
  return Object.keys(kept).length === 0 ? undefined : kept;
}

/** A recipe's declared inputs, {} when it declares none; undefined only while it is unknown. */
function declaredOf(r: Recipe | undefined): Record<string, string> | undefined {
  return r === undefined ? undefined : (r.inputs ?? {});
}

/** A save of `v` with every field kept but those `over` names. */
function bodyOf(v: LaunchRuleView, over: Partial<LaunchRuleBody> = {}): LaunchRuleBody {
  const inputs = keptInputs(v);
  return {
    id: v.id,
    source: v.source,
    enabled: v.enabled,
    when: v.when,
    ...(inputs === undefined ? {} : { inputs }),
    ...(v.model === undefined ? {} : { model: v.model }),
    ...(v.effort === undefined ? {} : { effort: v.effort }),
    ...over,
  };
}

async function toggleRule(v: LaunchRuleView, enabled: boolean): Promise<void> {
  const saved = await saveLaunchRule.dispatch(bodyOf(v, { enabled }));
  if (saved === null) {
    // Puts the switch back where the stored rule has it.
    popupList?.repaint();
    return;
  }
  storeRule(saved);
}

/** A 409 is a held-event count the save did not confirm; the server's sentence names an API field. */
const HELD_MOVED = "The number of held events changed. Save again to confirm the new count.";

/** One save, with the refusal's sentence and status for a form that stays open. */
async function sendRule(
  body: LaunchRuleBody,
): Promise<{ saved: LaunchRuleView | null; refusal: string; status: number }> {
  let refusal = "Could not save the rule";
  let status = 0;
  const saved = await saveLaunchRule.dispatch(body, {
    onError: (err) => {
      status = err.status ?? 0;
      refusal = status === 409 ? HELD_MOVED : (serverSentence(err) ?? refusal);
    },
  });
  return { saved, refusal, status };
}

function heldNoun(held: number): string {
  return held === 1 ? "1 event" : `${String(held)} events`;
}

/** The rule's held count as the server now has it: the one the form opened with may have moved,
 *  and a confirm_drop that does not match it is refused again. */
async function heldNow(v: LaunchRuleView): Promise<number> {
  await refreshRules();
  return (rules.get(v.source) ?? []).find((x) => x.id === v.id)?.held ?? v.held;
}

/** The confirm an edit that drops held events shows; the count is the rule's, never the 409's prose. */
function confirmDrop(held: number): Promise<boolean> {
  return confirm({
    title: "Save this change?",
    body: `It drops ${heldNoun(held)} waiting to launch.`,
    confirmLabel: "Save",
    variant: "destructive",
  });
}

/** Delete a rule, after a confirm naming the events it holds when it holds any. */
async function removeRule(v: LaunchRuleView): Promise<void> {
  if (v.held > 0) {
    const ok = await confirm({
      title: "Delete this rule?",
      body: `It holds ${heldNoun(v.held)} waiting to launch, which are dropped with it.`,
      confirmLabel: "Delete",
      variant: "destructive",
    });
    if (!ok) {
      return;
    }
  }
  if ((await deleteLaunchRule.dispatch(v.id)) === null) {
    return;
  }
  forgetRule(v);
  await refreshRules();
}

/** Reflect a recipe's rules onto its row's second line and its button. */
function syncRules(row: HTMLElement, r: Recipe): void {
  const line = rulesLineOf(row);
  if (line === null) {
    return;
  }
  const list = rules.get(r.source) ?? [];
  const enabled = list.filter((v) => v.enabled).length;
  const failing = list.some((v) => ruleError(v) !== "");
  line.textContent = rulesLine(r);
  line.classList.toggle("is-scheduled", enabled > 0 && !failing);
  line.classList.toggle("entry-sub-error", failing);
  const btn = row.querySelector<HTMLButtonElement>(".recipe-sched-btn");
  if (btn !== null) {
    btn.classList.toggle("on", enabled > 0);
    btn.textContent = enabled > 1 ? `Run when… ${String(enabled)}` : "Run when…";
  }
}

/** The open rule form's Model and Effort selects; one popup of the group is open at a time. */
let rulePicks: RunDefaultsPicker | null = null;

function picksFor(v: LaunchRuleView | undefined): RunDefaultsPicker {
  rulePicks = buildRunDefaultsPicker({
    models: getCachedModels(),
    initial: {
      ...(v?.model === undefined ? {} : { model: v.model }),
      ...(v?.effort === undefined ? {} : { effort: v.effort }),
    },
  });
  return rulePicks;
}

function wireRulesPopup(btn: HTMLButtonElement, source: string): void {
  let popup: ReturnType<typeof createPopup> | null = null;
  let open = false;

  btn.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    if (open && popup !== null) {
      popup.hide();
      open = false;
      return;
    }
    const body = el("div", { className: "sched-popup" });
    // Rebuilt per open rather than cached: the popup renders the CURRENT rules.
    popup?.hide();
    popup = createPopup(body, { trigger: btn, group: "recipe-schedule" });
    const close = (): void => {
      popup?.hide();
      open = false;
      if (popupList?.source === source) {
        popupList = null;
      }
    };
    const view = (node: HTMLElement): void => {
      body.replaceChildren(node);
    };
    const recipe = (): Recipe | undefined => recipes.find((x) => x.source === source);
    const openPermissions = (): void => {
      close();
      void openSettingsView("permissions");
    };
    const saved = async (v: LaunchRuleView): Promise<void> => {
      close();
      storeRule(v);
      await refreshRules();
    };
    const idOf = (v: LaunchRuleView | undefined): { id?: string } =>
      v === undefined ? {} : { id: v.id };

    const showClock = (v?: LaunchRuleView): void => {
      popupList = null;
      view(
        buildSchedulePicker({
          spec: v?.when.clock ?? defaultSpec(),
          enabled: v?.enabled ?? true,
          exists: v !== undefined,
          autoApprove,
          picks: picksFor(v),
          declared: declaredOf(recipe()),
          inputs: v?.inputs,
          onOpenPermissions: openPermissions,
          // Closed only once saved: a refused pick keeps the edits open, and the server's
          // sentence shows in the action's error toast.
          onSave: (spec, enabled, defaults, inputs) => {
            void (async () => {
              const s = await saveLaunchRule.dispatch({
                ...idOf(v),
                source,
                enabled,
                when: { clock: spec },
                ...(inputs === undefined ? {} : { inputs }),
                ...defaults,
              });
              if (s !== null) {
                await saved(s);
              }
            })();
          },
          onRemove: () => {
            if (v !== undefined) {
              close();
              void removeRule(v);
            }
          },
          onClose: close,
        }),
      );
    };

    const showEvent = (v?: LaunchRuleView): void => {
      popupList = null;
      const loading = el("div", { className: "sched-hint" }, "Reading your connections…");
      view(loading);
      void listForges.dispatch(undefined).then((d) => {
        if (!body.contains(loading)) {
          return;
        }
        if (d === null) {
          view(
            el("div", { className: "sched-hint" }, "Could not read the connections in Sources."),
          );
          return;
        }
        view(
          buildEventForm({
            ...(v === undefined ? {} : { rule: v }),
            forges: d.forges,
            declared: declaredOf(recipe()),
            autoApprove,
            picks: picksFor(v),
            onOpenPermissions: openPermissions,
            onClose: close,
            onSave: async (draft, defaults) => {
              const body: LaunchRuleBody = {
                ...idOf(v),
                source,
                enabled: draft.enabled,
                when: { event: draft.event },
                ...(draft.inputs === undefined ? {} : { inputs: draft.inputs }),
                ...defaults,
              };
              let res = await sendRule(body);
              if (res.status === 409 && v !== undefined) {
                const held = await heldNow(v);
                if (held > 0 && !(await confirmDrop(held))) {
                  return "";
                }
                res = await sendRule(held > 0 ? { ...body, confirm_drop: held } : body);
              }
              if (res.saved === null) {
                return res.refusal;
              }
              await saved(res.saved);
              return null;
            },
          }),
        );
      });
    };

    const showList = (): void => {
      const repaint = (): void => {
        const r = recipe();
        view(
          buildRuleList({
            rules: rules.get(source) ?? [],
            autoApprove,
            declared: r === undefined ? undefined : Object.keys(r.inputs ?? {}),
            onToggle: (v, enabled) => {
              void toggleRule(v, enabled);
            },
            onEdit: (v) => {
              if (v.when.clock === undefined) {
                showEvent(v);
              } else {
                showClock(v);
              }
            },
            onDelete: (v) => {
              void removeRule(v);
            },
            onDismissGap: async (v) => {
              const next = await dismissGap.dispatch(v.id);
              if (next !== null) {
                storeRule(next);
              }
              return next;
            },
            onAddClock: () => {
              showClock();
            },
            onAddEvent: () => {
              showEvent();
            },
          }),
        );
      };
      popupList = { source, repaint };
      repaint();
    };

    showList();
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
    const recipe = run.workflow_name ?? "";
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
  | { readonly kind: "detail"; readonly recipe: Recipe }
  | { readonly kind: "orphan"; readonly rule: LaunchRuleView };

/** The recipe whose input form is open, by source; at most one, like a `<details>` group. */
let openForm: string | null = null;

function paint(): void {
  if (container === null) {
    return;
  }
  const rows = visibleRecipes();
  const orphans = visibleOrphans();
  // Every repaint path reports, not just the fetch: the note describes what is on screen, so
  // whichever caller changed what is on screen owes the update.
  onCounts?.(counts());
  if (rows.length === 0 && orphans.length === 0) {
    // Two different sentences, because they answer different questions. "No workflows available."
    // under an active filter is the same lie docs.ts records for its category text: the workflows
    // exist, they are one keystroke away.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        recipes.length === 0 && orphanedRules().length === 0
          ? "No workflows available."
          : "No workflows match the filter.",
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
  for (const rule of orphans) {
    entries.push({ kind: "orphan", rule });
  }
  // Keyed on `source`, which is stable across a filter change — so a keystroke removes and re-adds
  // the rows that left and arrived rather than rebuilding the list.
  reconcile(list, entries, {
    key: (e: RecipeEntry) => joinKey(e.kind, e.kind === "orphan" ? e.rule.id : e.recipe.source),
    mount: (e: RecipeEntry) => {
      switch (e.kind) {
        case "row":
          return recipeRow(e.recipe);
        case "detail":
          return inputForm(e.recipe);
        case "orphan":
          return orphanRow(e.rule);
      }
    },
    update: (node: HTMLElement, e: RecipeEntry) => {
      switch (e.kind) {
        case "row":
          syncButton(node, e.recipe);
          syncRules(node, e.recipe);
          syncStanding(node, e.recipe);
          break;
        case "detail":
          // Only Launch and the plan are synced: the fields hold what the reader has typed.
          syncLaunch(node, e.recipe);
          syncPlan(node, e.recipe);
          break;
        case "orphan":
          break;
      }
    },
  });
  // The region mounts closed and opens from a flushed layout, which is what turns
  const detail = list.querySelector<HTMLElement>(".entry-detail:not(.open)");
  if (detail !== null) {
    forceReflow(detail);
    detail.classList.add("open");
    // Launch when the recipe declares no inputs, so Enter still launches it on Default.
    (
      detail.querySelector<HTMLElement>("input") ??
      detail.querySelector<HTMLElement>(".recipe-launch-btn")
    )?.focus({ preventScroll: true });
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
    "aria-label": `Launch rules of ${r.name}`,
  }) as HTMLButtonElement;
  schedBtn.textContent = "Run when…";
  wireRulesPopup(schedBtn, r.source);

  // A slot, so a standing change updates a kept row; only a file-backed recipe has a file to join.
  const standingSlot =
    r.doc_key === undefined ? null : el("span", { className: "recipe-standing hidden" });
  // No `open`: a recipe row is not a door. Its one destination is the run its button launches, and
  // that run opens its own tab.
  const row = entryRow({
    key: r.source,
    title: r.name,
    badges: [
      ...(r.built_in === true ? [el("span", { className: "docs-badge" }, "bundled")] : []),
      ...(standingSlot === null ? [] : [standingSlot]),
    ],
    sub: {
      kind: "lines",
      lines: [{ text: r.description ?? "" }, { text: rulesLine(r) }],
    },
    actions: [schedBtn, btn],
    data: { "data-recipe": r.source },
  });
  syncButton(row, r);
  syncRules(row, r);
  syncStanding(row, r);
  return row;
}

/** A rule whose recipe file is gone: it cannot run or be edited, so Delete is its one control. */
function orphanRow(v: LaunchRuleView): HTMLElement {
  const title = v.name ?? v.source;
  const del = el("button", {
    type: "button",
    className: "btn-small recipe-sched-delete-btn",
    "aria-label": `Delete the rule of ${title}`,
  }) as HTMLButtonElement;
  del.textContent = "Delete rule";
  del.addEventListener("click", (e: MouseEvent) => {
    e.stopPropagation();
    void removeRule(v);
  });
  return entryRow({
    key: v.id,
    title,
    badges: [el("span", { className: "docs-badge" }, "orphaned")],
    sub: {
      kind: "lines",
      lines: [
        { text: v.source, mono: true },
        { text: "Its workflow file is gone, so this rule cannot run.", error: true },
      ],
    },
    actions: [del],
    data: { "data-orphan-rule": v.id },
  });
}

function standingOf(r: Recipe): ConfigStanding | undefined {
  return r.doc_key === undefined ? undefined : configStanding.peek().files.get(r.doc_key);
}

function syncStanding(row: HTMLElement, r: Recipe): void {
  const slot = row.querySelector<HTMLElement>(".recipe-standing");
  if (slot === null) {
    return;
  }
  const s = standingOf(r);
  slot.replaceChildren(...(s === undefined ? [] : [standingBadge(s)]));
  // `.hidden`, not the attribute: `.entry-badges` declares `display`, which beats the UA's `[hidden]`.
  slot.classList.toggle("hidden", s === undefined);
  const badges = slot.parentElement;
  badges?.classList.toggle(
    "hidden",
    [...badges.children].every((b) => b.classList.contains("hidden")),
  );
}

/** KAS's reason the recipe cannot launch, or "" for one it can. */
function invalidReason(r: Recipe): string {
  return (r.validation_error ?? "").trim();
}

/** The row's first line: KAS's reason a recipe cannot launch outranks its description, since it
 *  is why Run is off; re-run on every repaint, because a `recipes_changed` refetch can fix or
 *  break the recipe under a kept row. */
function syncValidity(row: HTMLElement, r: Recipe): void {
  const line = row.querySelector<HTMLElement>(".entry-lines > .entry-sub:nth-child(1)");
  if (line === null) {
    return;
  }
  const reason = invalidReason(r);
  line.textContent = reason === "" ? (r.description ?? "") : reason;
  line.classList.toggle("entry-sub-error", reason !== "");
  const sched = row.querySelector<HTMLButtonElement>(".recipe-sched-btn");
  if (sched !== null) {
    sched.disabled = reason !== "";
    toggleTooltip(sched, reason);
  }
}

/** A disabled control still gets hover, so its tooltip says why it is off. */
function toggleTooltip(btn: HTMLElement, text: string): void {
  if (text === "") {
    btn.removeAttribute("data-tooltip");
  } else {
    btn.setAttribute("data-tooltip", text);
  }
}

/** The row's second line: the first error any of the recipe's rules shows, else its first
 *  enabled rule's line and how many more there are; then the declared inputs. */
function rulesLine(r: Recipe): string {
  const list = rules.get(r.source) ?? [];
  const declared = Object.keys(r.inputs ?? {});
  let line = list.map(ruleError).find((e) => e !== "");
  if (line === undefined) {
    const on = list.filter((v) => v.enabled);
    const [first] = on;
    line = first === undefined ? "Not scheduled" : ruleLine(first, { autoApprove, declared });
    const more = on.length - 1;
    if (more > 0) {
      line += ` \u00b7 +${String(more)} more rule${more === 1 ? "" : "s"}`;
    }
  }
  return declared.length === 0 ? line : `${line} \u00b7 Inputs: ${declared.join(", ")}`;
}

/** The line `rulesLine` fills: the second of the row's two subtitle lines. */
function rulesLineOf(row: HTMLElement): HTMLElement | null {
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
  // A live run of a recipe KAS now calls invalid can still be cancelled, and an open form still
  // folds: Run is that form's only close.
  const reason = live === undefined ? invalidReason(r) : "";
  btn.disabled = reason !== "" && openForm !== r.source;
  toggleTooltip(btn, reason);
  if (live === undefined) {
    btn.setAttribute("aria-expanded", String(openForm === r.source));
  } else {
    btn.removeAttribute("aria-expanded");
  }
  syncValidity(row, r);
}

function onRunButton(r: Recipe): void {
  const live = liveRuns.get(r.name);
  if (live !== undefined) {
    void cancelRun.dispatch(live.workflow_id);
    return;
  }
  if (openForm === r.source) {
    openForm = null;
    paint();
    return;
  }
  if (invalidReason(r) !== "") {
    return;
  }
  // Always the form, inputs or not: it is the one place a run's model and effort are picked.
  openForm = r.source;
  paint();
}

/** Each open form's Model and Effort selects, for its repaint to hand a newer catalog. */
const formPicks = new WeakMap<HTMLElement, RunDefaultsPicker>();

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
  const picks = buildRunDefaultsPicker({ models: getCachedModels(), initial: {} });
  form.appendChild(picks.el);
  const launchBtn = el(
    "button",
    { type: "submit", className: "btn-small recipe-launch-btn" },
    "Launch",
  ) as HTMLButtonElement;
  form.appendChild(launchBtn);
  form.addEventListener("submit", (e: Event) => {
    e.preventDefault();
    // Resolved at submit time, like the row's Run: a refetch can invalidate the recipe under a
    // form kept open across repaints.
    const current = recipes.find((x) => x.source === r.source);
    if (current === undefined || invalidReason(current) !== "") {
      return;
    }
    const inputs: Record<string, string> = {};
    for (const [key, field] of fields) {
      if (field.value !== "") {
        inputs[key] = field.value;
      }
    }
    openForm = null;
    paint();
    launch(current, inputs, picks.value());
  });
  const detail = entryDetail(form);
  formPicks.set(detail, picks);
  formPlans.set(detail, buildPlanPreview(launchBtn));
  syncLaunch(detail, r);
  syncPlan(detail, r);
  return detail;
}

/** An open form's read-only plan, drawn by the run tab's own tree so the preview matches the run it
 *  launches. `launch` is the form's Launch, which the region sits in front of. */
interface PlanPreview {
  region: HTMLElement;
  head: HTMLElement;
  tree: ExecTreeView;
  launch: HTMLElement;
}

/** Each open form's plan preview, for its repaint to redraw from a refetched recipe. */
const formPlans = new WeakMap<HTMLElement, PlanPreview>();

function buildPlanPreview(launch: HTMLElement): PlanPreview {
  const head = el("div", { className: "recipe-plan-head" });
  // Selection means nothing before the run exists, so a row's click does nothing.
  const tree = buildExecTree(() => undefined);
  tree.root.setAttribute("aria-label", "Planned steps");
  // A tab stop, because no row is one and the box scrolls (WCAG 2.1.1).
  const steps = el("div", { className: "recipe-plan-steps", tabindex: "0" }, tree.root);
  return { region: el("div", { className: "recipe-plan" }, head, steps), head, tree, launch };
}

/** The open form's plan follows the recipe: a recipe whose plan folds to no node shows no region. */
function syncPlan(detail: HTMLElement, r: Recipe): void {
  const p = formPlans.get(detail);
  if (p === undefined) {
    return;
  }
  const nodes = planToExec(r.plan);
  if (nodes.length === 0) {
    p.region.remove();
    p.tree.render([], "");
    return;
  }
  const steps = workNodes(nodes).length;
  p.head.textContent = `Plan \u00b7 ${steps === 1 ? "1 step" : `${String(steps)} steps`}`;
  p.tree.render(nodes, "");
  if (p.region.nextElementSibling !== p.launch) {
    p.launch.before(p.region);
  }
}

/** The open form's Launch follows the row's Run: off, with KAS's reason, for an invalid recipe. */
function syncLaunch(detail: HTMLElement, r: Recipe): void {
  const btn = detail.querySelector<HTMLButtonElement>(".recipe-launch-btn");
  if (btn === null) {
    return;
  }
  const reason = invalidReason(r);
  btn.disabled = reason !== "";
  toggleTooltip(btn, reason);
  formPicks.get(detail)?.setModels(getCachedModels());
}

function launch(r: Recipe, inputs: Record<string, string>, defaults: RunDefaults): void {
  void launchRun.dispatch(
    { source: r.source, inputs, ...defaults },
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
