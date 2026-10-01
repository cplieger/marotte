// ---------------------------------------------------------------------------
// The Kiro configuration browser: one page over everything in `.kiro/`.
//
// SUB-TABBED, one tab per category, not one table: the categories are not one
// kind of thing (a steering doc's most useful fact is its inclusion mode, an
// agent's its model, a spec has no front-matter and is identified by its feature
// directory, a hook is JSON with a trigger), and each tab names its own source,
// so a file no tab claims simply gets no row. The bar is the shared segmented
// switcher and the rows the shared `.entry` family on the three-line tier
// (11-page-lists.css). A row opens its document in the file editor; the server
// row already carries the path.
// ---------------------------------------------------------------------------

import { apiGetTyped, type Decoder } from "./api-client.js";
import { decodeKiroDocsResponse } from "./wire/decoders.gen.js";
import type { KiroDoc as WireKiroDoc, KiroDocsResponse } from "./wire/types.gen.js";
import { defineAction, ActionError, retryNetwork, registerCleanup } from "./actions/index.js";
import { setHookEnabled } from "./actions/hooks.js";
import { asObject, decodeArray, optStr, reqBool, reqStr } from "./validators.js";
import { onSSE } from "./bus.js";
import { $ } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_EDIT, ICON_TRASH } from "./icons.js";
import { confirm as confirmDialog } from "./confirm.js";
import { deleteDoc } from "./actions/docs.js";
import { onGitStatusChange, statusFor } from "./git-status-store.js";
import { describeStatus } from "./git-types.js";
import { openFile } from "./editor-openers.js";
import { openSpec } from "./navigate.js";
import { specDirOf } from "./spec-path.js";
import { relToWorkspace } from "./workspace.js";
import { reconcile } from "./reconcile.js";
import { join as joinKey } from "@cplieger/keyenc";
import { paintIfChanged, sigChanged } from "./paint-sig.js";
import { entryRow, entryList, entrySkeleton } from "./entry-row.js";
import type { EntryRowSpec, EntrySub } from "./entry-row.js";
import { signal, subscribe } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { paintPlaceholder } from "./skeleton.js";
import { pushRoute } from "./router.js";
import type { DocsTab } from "./route-path.js";
import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { setDocsTab as setTabRoute } from "./tabs.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";
import { setPageSubtitle } from "./page-title.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";

/** One document row: the wire's `KiroDoc` plus the one field this page adds to it.
 *
 *  `hook_scope` is the client's, because it can be: `kiroRoots()` walks the workspace's
 *  `.kiro` trees and nothing else, so a scanned row is workspace by construction and a
 *  synthesized one is global. Absent means workspace. It is the THIRD component of the
 *  join key and has to be — the two path shapes normalize to the same `.kiro/...` tail,
 *  so without it a workspace hook and a global hook sharing a relative path and a name
 *  share one key. */
type KiroDoc = WireKiroDoc & { hook_scope?: HookScope };

/** The two scopes a hook row belongs to. A workspace hook and a global hook are
 *  different files with different affordances even when their relative path and
 *  their name match, so scope is part of a hook's identity, not a label on it. */
type HookScope = "workspace" | "global";

// ---------------------------------------------------------------------------
// Hooks: the one tab that is not a pure projection of /api/workspace/kiro-docs.
// A hook has STATE the file scan cannot see (enabled, and why KAS disabled it), so
// the tab joins the scan's rows against GET /api/hooks; a GLOBAL hook lives under
// the container HOME, outside `kiroRoots()`, so its row exists only on that
// endpoint and is synthesized here. The join key is (scope, path, name), because
// `hookRows` in kiro_docs.go expands one file into one row PER HOOK (`hookKey`).
// ---------------------------------------------------------------------------

/** A hook's live state, from GET /api/hooks. Narrowed to what the tab needs:
 *  `enabled` + `disabled_reason` are the state no file scan can know, `scope`
 *  decides which affordances the row may offer, `file_path` is the join key and
 *  `id` is what setEnabled addresses.
 *
 *  `trigger` / `command` / `prompt` are display-only and exist for ONE case: a
 *  global hook has no docs row, so this is the only source for its whole row. */
interface HookState {
  id: string;
  name: string;
  scope?: string;
  enabled: boolean;
  disabled_reason?: string;
  file_path?: string;
  trigger?: string;
  command?: string;
  prompt?: string;
  /** The regex KAS tests this hook's trigger subject against. Display-only. */
  matcher?: string;
  /** What is wrong with the trigger-and-matcher pairing, computed SERVER-side
   *  (internal/marotte's ClassifyHookMatcher) so the trigger-to-subject table
   *  exists once. `missing_tool_matcher` = a tool trigger with no matcher, so the
   *  hook runs on every tool call; `ineffective` = a matcher on a trigger with
   *  nothing to match on, so it governs nothing. Absent = nothing to say.
   *
   *  Never derived here. A TypeScript copy of that table could disagree with the
   *  Go one about a trigger's subject, and the subject is the whole judgement. */
  matcher_warning?: string;
}

const HP = "$.hook";

const decodeHook: Decoder<HookState> = (v) => {
  const o = asObject(v, HP);
  const out: HookState = {
    id: reqStr(o, "id", HP),
    name: reqStr(o, "name", HP),
    enabled: reqBool(o, "enabled", HP),
  };
  for (const key of [
    "scope",
    "disabled_reason",
    "file_path",
    "trigger",
    "command",
    "prompt",
    "matcher",
    "matcher_warning",
  ] as const) {
    const val = optStr(o, key, HP);
    if (val !== undefined) {
      out[key] = val;
    }
  }
  return out;
};

const decodeHookList: Decoder<{ hooks: HookState[] }> = (v) => {
  const o = asObject(v, "$.hooks");
  return { hooks: decodeArray(o["hooks"], decodeHook, "$.hooks.hooks") };
};

/** Global hooks live in ~/.kiro/hooks (kiro-cli 2.13+) and apply in every
 *  workspace. Scope is DERIVED server-side from the hook's file path, because the
 *  wire carries no scope field; an absent value (an older server) counts as
 *  workspace, which is the safe direction — it grants the file affordances a
 *  workspace file legitimately has. */
function isGlobalHook(h: HookState): boolean {
  return h.scope === "global";
}

/** A hook's scope as the join key needs it: the endpoint's own explicit value,
 *  with the same safe default `isGlobalHook` applies. */
function hookScopeOf(h: HookState): HookScope {
  return isGlobalHook(h) ? "global" : "workspace";
}

/** A ROW's scope. Absent means workspace, which is what every scanned row is. */
function rowScope(doc: KiroDoc): HookScope {
  return doc.hook_scope ?? "workspace";
}

/** Normalize either path shape to its `.kiro/...` tail, which is the only part
 *  the two endpoints agree on. Returns "" for a path with no `.kiro` segment
 *  (a global hook's `~/...` display path has one; an absolute fallback may not),
 *  and a row that cannot produce a key simply does not join. */
function hookPathKey(path: string): string {
  const idx = path.indexOf(".kiro/");
  return idx < 0 ? "" : path.slice(idx);
}

/** The join key: (scope, normalized path, name). keyenc rather than a template
 *  literal: a hook NAME is arbitrary text from a JSON file, so a separator inside
 *  one could forge another hook's key and hand it the wrong toggle. SCOPE leads
 *  because `hookPathKey` discards the one thing that told the two scopes apart
 *  (`~/.kiro/hooks/x.json` and `workspace/.kiro/hooks/x.json` share a tail), and
 *  the collision is not symmetric: the endpoint loads workspace then global, so
 *  the global hook would win the key and the workspace row inherit its gates. */
function hookKey(scope: HookScope, path: string, name: string): string {
  return joinKey(scope, hookPathKey(path), name);
}

/** Tab order is fixed: the page always reads Steering · Skills · Agents ·
 *  Specs · Hooks. Workflows is deliberately absent — a recipe is not a `.kiro`
 *  file (the bundled ones are compiled into KAS and agent-authored ones land
 *  under the sessions path), so it is sourced from an RPC and arrives with the
 *  run-launch work rather than here. */
export const DOCS_TABS: readonly DocsTab[] = [
  "steering",
  "skills",
  "agents",
  "specs",
  "hooks",
  "workflows",
] as const;

const TAB_LABELS: Readonly<Record<DocsTab, string>> = {
  steering: "Steering",
  skills: "Skills",
  agents: "Agents",
  specs: "Specs",
  hooks: "Hooks",
  workflows: "Workflows",
};

/** Wire category (the server's value) per tab. Workflows has NO category: it
 *  is RPC-sourced (a recipe is not a `.kiro` file — the bundled ones are
 *  compiled into KAS and agent-authored ones live under KAS's sessions tree),
 *  so renderActive hands its panel to recipes.ts instead of filtering docs. */
const TAB_CATEGORY: Readonly<Record<Exclude<DocsTab, "workflows">, string>> = {
  steering: "steering",
  skills: "skill",
  agents: "agent",
  specs: "spec",
  hooks: "hook",
};

const EMPTY_TEXT: Readonly<Record<Exclude<DocsTab, "workflows">, string>> = {
  steering: "No steering docs in .kiro/steering/.",
  skills: "No skills in .kiro/skills/.",
  agents: "No custom agents in .kiro/agents/.",
  specs: "No specs in .kiro/specs/.",
  hooks: "No hooks in .kiro/hooks/.",
};

// --- State ---

const activeTab = signal<DocsTab>("steering");
/** Last fetched inventory. Kept so a tab switch repaints from memory rather
 *  than refetching ~200 parsed documents. */
let docs: KiroDoc[] = [];
/** Last fetched hook state, keyed by (normalized path, name). Kept for the same
 *  reason `docs` is, and separately from it because the two have different
 *  invalidation triggers: the inventory refetches on `settings_updated`, hook
 *  state on `hooks_changed`. */
let hooks = new Map<string, HookState>();
let inited = false;
/** Whether the inventory has ANSWERED. `docs` initialises to `[]`, so a category with
 *  no documents is indistinguishable from one this client has never read. */
let inventoryAnswered = false;
/** Whether the server cut the inventory at its per-category or total cap. The one
 *  fact about coverage a filter over an in-memory list has to carry, because a
 *  reader filtering a cut list is filtering less than the page implies. */
let docsTruncated = false;
/** The folded query the metadata filter is applying. A FILTER, not a search:
 *  everything it matches on is in memory, so there is no request, and the only
 *  coverage it reports is `docsTruncated`. No match-case toggle, because every
 *  filter in this app folds both sides. Metadata only: the inventory carries a
 *  name, a description, a path, front-matter and a hook's trigger, never a
 *  document's BODY, which is the file browser's recursive grep one view away. */
let filterText = "";

/** The page's unit on both axes: a row is a document, and the filter reads
 *  documents. One noun for all six tabs because the box is labelled "Filter
 *  documents" and a recipe row sits under that label like every other. */
const NOUNS: Nouns = {
  match: { one: "document", many: "documents" },
  scanned: { one: "document", many: "documents" },
};

const loadDocsAction = defineAction<undefined, KiroDocsResponse>({
  name: "docs.load",
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, sig) => {
    const data = await apiGetTyped("/api/workspace/kiro-docs", decodeKiroDocsResponse, sig);
    if (sig.aborted) {
      throw new DOMException("aborted", "AbortError");
    }
    if (data === null) {
      throw new ActionError("Failed to load .kiro documents", { code: "network" });
    }
    return data;
  },
  error: false,
});

// --- Public API ---

/** The docs tab's ACTIVATION: the one-shot init alone. Forces no sub-tab and fetches nothing. */
export function showDocsTab(): void {
  initDocsView();
}

/** Refetch the inventory at the ACTIVE sub-tab. Forces nothing.
 *
 *  Runs the one-shot init first so this does not depend on which of two dynamic
 *  imports of this module the host resolves first: `loadDocs`' success path calls
 *  `renderActive`, which needs the panels wired. */
export function refreshDocsView(): void {
  initDocsView();
  loadDocs();
}

/** Set the active tab without pushing a URL — the router's entry point when
 *  back/forward lands on /docs/<tab>. */
export function forceDocsTab(tab: DocsTab): void {
  setTabRoute(tab);
  activeTab.value = tab;
}

/** Fetch (or refetch) the inventory and repaint. */
export function loadDocs(): void {
  loadDocsAction.cancel();
  const skeleton = inventoryAnswered ? null : skeletonTiming(() => showSkeleton());
  void loadDocsAction.dispatch(undefined, {
    onSuccess: (d) => {
      skeleton?.cancel();
      docs = d.docs;
      docsTruncated = d.truncated;
      inventoryAnswered = true;
      renderActive();
    },
    onError: () => {
      skeleton?.cancel();
      panelFor(activeTab.peek())?.replaceChildren(
        el("div", { className: "list-empty" }, "Failed to load .kiro documents"),
      );
    },
  });
  loadHookState();
}

/** Fetch the hook state and repaint.
 *
 *  Best-effort and deliberately NOT folded into loadDocs's action: a hooks
 *  endpoint failure must not blank the Steering tab, and the four tabs that need
 *  nothing from it must not wait on it. When it fails, a workspace hook still
 *  renders from its docs row minus the toggle; a global hook has no row to fall
 *  back to and simply does not appear, which is the honest degradation — inventing
 *  a row for a file nothing reported would be worse. */
function loadHookState(): void {
  void apiGetTyped("/api/hooks", decodeHookList).then((d) => {
    if (d === null) {
      return;
    }
    const next = new Map<string, HookState>();
    for (const h of d.hooks) {
      next.set(hookKey(hookScopeOf(h), h.file_path ?? "", h.name), h);
    }
    hooks = next;
    if (activeTab.peek() === "hooks") {
      renderActive();
    }
  });
}

// --- Tab bar wiring (the Settings idiom) ---

function initDocsView(): void {
  if (inited) {
    return;
  }
  inited = true;
  const bar = $.docsTabBar;
  bar.setAttribute("aria-label", "Kiro document categories");
  const paintBar = initSegmentedBar(bar, {
    attr: "data-docs-tab",
    idPrefix: "docs",
    tabs: DOCS_TABS.map((id) => ({ id, label: TAB_LABELS[id] })),
    onSelect: selectTab,
  });
  // Hand Ctrl-F this page's entry point. Through the LEAF registry, not the
  // dispatcher: importing find-dispatch here would drag find-in-chat and
  // scroll.ts's self-initialising singleton into this lazily-loaded page.
  //
  // No `available` predicate: every one of the six tabs is filterable now that
  // Workflows narrows its recipes too, so the toolbar's magnifier always has a
  // destination here. It used to answer `activeTab.value !== "workflows"`.
  registerFind("docs", docsFilter);
  // The Workflows panel owns its own rows, so it reports what the filter is
  // showing rather than the page inferring it. Wired once, here, because the
  // panel repaints on its own schedule (its run poll, its schedules fetch).
  setRecipeCountsListener(({ total, shown }) => {
    // The panel's refetch can land after the reader left the tab, and its count
    // would then stamp another tab's note.
    if (activeTab.peek() === "workflows") {
      docsFilter.shell?.setNote(noteFor("workflows", total, shown));
    }
  });

  subscribe(activeTab, (tab) => {
    syncTabChrome(tab, paintBar);
    renderActive();
  });

  // Git letters ride the shared status store: no new server call and no timer,
  // which is the whole reason the store exists. Subscribing is what starts it, so
  // the page does not depend on which surface the user opened first.
  registerCleanup(
    onGitStatusChange(() => {
      renderActive();
    }),
  );
  // An edit — by the user or the agent — should reflect without a reopen.
  registerCleanup(
    onSSE("settings_updated", () => {
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  // Hook state has its OWN broadcast and needs it: `settings_updated` does not
  // fire for a hook file, and the docs scan is memoized behind a signature of each
  // category directory's mtime AND its entry names — so an in-place edit to a hook
  // file changes neither and that endpoint would serve the old trigger forever.
  // KAS watches the tree and emits `_kiro/hooks/didChange`, which the server turns
  // into this event, so a hook hand-edited AS A FILE reaches the tab. Both halves
  // refetch: the body edit is the inventory's, the enabled flag is the endpoint's.
  registerCleanup(
    onSSE("hooks_changed", () => {
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  // The toggle is delegated on the container, like the delete button: rows are
  // reconciled, so a per-row listener would be rebound on every repaint.
  $.docsView.addEventListener("change", (e) => {
    const target = e.target as HTMLElement;
    if (!target.classList.contains("hook-toggle")) {
      return;
    }
    const id = target.closest<HTMLElement>("[data-hook-id]")?.getAttribute("data-hook-id") ?? "";
    if (id !== "") {
      // Server-canonical: dispatch, then refetch so the checkbox reconciles from
      // authoritative state — which also re-syncs it when the write failed.
      void setHookEnabled
        .dispatch({ id, enabled: (target as HTMLInputElement).checked })
        .then(() => {
          loadHookState();
        });
    }
  });
}

function selectTab(tab: DocsTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setTabRoute(tab);
  pushRoute({ kind: "docs", tab });
  activeTab.value = tab;
}

function syncTabChrome(tab: DocsTab, paint: (tab: DocsTab) => void): void {
  paint(tab);
  swapViews(() => {
    let active: HTMLElement | null = null;
    for (const panel of document.querySelectorAll<HTMLDivElement>("[data-docs-panel]")) {
      const panelTab = panel.dataset["docsPanel"] ?? "";
      const isActive = panelTab === tab;
      panel.classList.toggle("hidden", !isActive);
      panel.setAttribute("role", "tabpanel");
      panel.id = `docs-panel-${panelTab}`;
      panel.setAttribute("aria-labelledby", `docs-tab-${panelTab}`);
      if (isActive) {
        active = panel;
      }
    }
    return active;
  });
  setPageSubtitle("docs", TAB_LABELS[tab]);
}

function panelFor(tab: DocsTab): HTMLDivElement | null {
  return document.querySelector<HTMLDivElement>(`[data-docs-panel="${tab}"]`);
}

/** The note under the box for the tab being shown: `total` rows on that tab,
 *  `shown` surviving the filter. Silent with no filter (a count restating the
 *  list is noise) unless the server cut the inventory, which the list cannot say
 *  about itself. A tab the filter emptied says where the matches went: the box
 *  sits in a page-level toolbar over six tabs, so "no matches" scoped to one of
 *  them reads as scoped to all six. */
function noteFor(tab: DocsTab, total: number, shown: number): string {
  // The Workflows rows are not from the docs reply, so its cap says nothing
  // about them.
  const truncated = tab === "workflows" ? false : docsTruncated;
  if (filterText === "" && !truncated) {
    return "";
  }
  if (shown > 0 || filterText === "") {
    return scanNote({ scanned: total, matched: shown, truncated }, shown, NOUNS);
  }
  const other = matchesElsewhere(tab);
  return emptyNote(
    classify({
      matched: other.matched,
      shown: 0,
      ...(other.where === undefined ? {} : { where: other.where }),
      scanned: total + other.scanned,
      truncated: docsTruncated,
    }),
    NOUNS,
  );
}

/** The five tabs whose rows this module holds; the sixth's live in recipes.ts. */
const INVENTORY_TABS = DOCS_TABS.filter(
  (t): t is Exclude<DocsTab, "workflows"> => t !== "workflows",
);

const TAB_LIST = new Intl.ListFormat("en-US", { type: "conjunction" });

function rowsFor(tab: Exclude<DocsTab, "workflows">): KiroDoc[] {
  return tab === "hooks" ? hookRows() : docs.filter((d) => d.category === TAB_CATEGORY[tab]);
}

function matches(doc: KiroDoc): boolean {
  return filterHaystack(doc).includes(filterText);
}

/** What the filter matched on the OTHER inventory-backed tabs, and the label of
 *  each tab holding a match. Workflows is never a `where`: its rows are fetched
 *  when its panel renders and live in recipes.ts, so from here they are neither
 *  in memory nor honestly countable. */
function matchesElsewhere(tab: DocsTab): {
  matched: number;
  scanned: number;
  where: string | undefined;
} {
  let matched = 0;
  let scanned = 0;
  const labels: string[] = [];
  for (const t of INVENTORY_TABS) {
    if (t === tab) {
      continue;
    }
    const rows = rowsFor(t);
    scanned += rows.length;
    const hits = rows.filter(matches).length;
    if (hits > 0) {
      matched += hits;
      labels.push(TAB_LABELS[t]);
    }
  }
  return {
    matched,
    scanned,
    where: labels.length === 0 ? undefined : TAB_LIST.format(labels),
  };
}

/** Build and mount the metadata filter.
 *
 *  A POPUP since the search-box audit, not the permanent in-flow field it was:
 *  the page's box now looks and behaves like the transcript's, opens from the
 *  toolbar's magnifier or Ctrl-F, and closes on Escape — and the close clears the
 *  query, because a hidden box holding `redis` would leave this page showing
 *  three of forty rows with nothing on screen saying why. */
const docsFilter: SearchPopup = createSearchPopup<null>({
  id: "docs-filter",
  // A FILTER, so it carries the funnel: everything it matches on is already in
  // memory, and it can only hide rows that are here.
  kind: "filter",
  label: "Filter documents",
  // Names what it reaches, and it reaches metadata only: never a document's
  // body, which is the file browser's recursive grep one view away.
  placeholder: "Filter by name, description or trigger\u2026",
  note: true,
  host: () => document.getElementById("docs-view"),
  // Synchronous by nature: the inventory is already here. The filter is
  // applied in renderActive, which is the ONE place that decides which records
  // a tab shows, so this hands the work there rather than keeping a second
  // copy of the decision.
  query: (query) => {
    filterText = query.toLowerCase();
    return null;
  },
  render: () => {
    renderActive();
  },
});

// --- Rendering ---

/** A group of rows under one label: a spec's feature directory, a hook's file.
 *  The flat categories render as one unlabelled section. */
interface Section {
  readonly key: string;
  readonly label: string;
  readonly docs: KiroDoc[];
}

/** Every string a row shows a reader, in ONE list: name, badge labels with the
 *  literals included (`override` is what a reader hunting overrides types), the
 *  matcher, the git letter, the subtitle lines, and the data a badge carries in
 *  its tooltip (the fileMatch pattern, the tool names, a disabled reason). The
 *  filter's haystack and the repaint signature both read it, so a rendered string
 *  is never unreachable and never stale; the census in docs-filter.test.ts types
 *  every rendered string back into the box. */
function rowText(doc: KiroDoc, hook: HookState | undefined): string[] {
  const gates = rowGates(doc, hook);
  const tools = doc.tools ?? [];
  const reason = hook?.disabled_reason ?? "";
  return [
    doc.name,
    doc.description ?? "",
    doc.path,
    doc.group ?? "",
    doc.inclusion ?? "",
    doc.file_match ?? "",
    doc.steering_override === true ? OVERRIDE_LABEL : "",
    doc.model ?? "",
    tools.length === 0 ? "" : toolCountLabel(tools.length),
    tools.join(", "),
    doc.trigger ?? "",
    hookAction(doc, hook),
    hook?.matcher ?? "",
    hook !== undefined && isGlobalHook(hook) ? GLOBAL_LABEL : "",
    reason === "" ? "" : DISABLED_LABEL,
    reason,
    MATCHER_WARNINGS[hook?.matcher_warning ?? ""]?.label ?? "",
    gates.explainDelete ? LINK_LABEL : "",
    gitLetter(doc, gates),
  ];
}

/** Built per call rather than cached on the record: the inventory is refetched
 *  whole on `settings_updated`, so a cache would need the same invalidation
 *  `rowSig` already has and would buy nothing over ~200 rows. */
function filterHaystack(doc: KiroDoc): string {
  return rowText(doc, hookFor(doc)).join("\n").toLowerCase();
}

function renderActive(): void {
  const tab = activeTab.peek();
  const container = panelFor(tab);
  if (container === null) {
    return;
  }
  // Workflows filters too, and it reaches recipes.ts to do it. That tab is
  // RPC-sourced and escapes before any docs logic runs, so the filter cannot be
  // applied HERE — but "no inventory of its own" was never the same claim as
  // "nothing to filter", and hiding the box was answering the second with the
  // first. The panel reports its own counts through the listener wired in
  // initDocsView, so the note reads the same on all six tabs.
  if (tab === "workflows") {
    renderRecipesPanel(container, filterText);
    return;
  }
  const all = rowsFor(tab);
  const rows = filterText === "" ? all : all.filter(matches);
  docsFilter.shell?.setNote(noteFor(tab, all.length, rows.length));
  if (rows.length === 0) {
    // The category's empty text is a LIE under an active filter — "No steering
    // docs in .kiro/steering/." when 47 of them are one keystroke away. The
    // git changes tab already makes this distinction and for the same reason.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        filterText === "" ? EMPTY_TEXT[tab] : "No documents match the filter.",
      ),
    );
    return;
  }

  // Drop any non-keyed placeholder (skeleton / empty state) before reconcile.
  for (const child of [...container.children]) {
    if (child.getAttribute("data-reconcile-key") === null) {
      child.remove();
    }
  }
  reconcile(container, sectionsFor(rows), {
    key: (s: Section) => s.key,
    mount: (s: Section) => {
      const node = el(
        "div",
        { className: "docs-section" },
        s.label === ""
          ? null
          : el("div", { className: "entry-section-label" }, s.label, specDoor(s)),
        entryList(),
      );
      fillSection(node, s);
      return node;
    },
    update: fillSection,
  });
}

/** Reconcile a section's rows into its own container. Nested inside the panel's
 *  reconcile, which is safe because `reconcile` reads only the children carrying
 *  its key attribute — a section's label is invisible to it. */
function fillSection(node: HTMLElement, s: Section): void {
  const list = node.querySelector<HTMLElement>(".list-container");
  if (list === null) {
    return;
  }
  reconcile(list, s.docs, {
    key: rowKey,
    mount: buildRow,
    update: updateRow,
  });
}

/** Cut the rows at every group boundary. The server already returns each category
 *  in its intended order (specs sorted requirements → design → tasks → lexical),
 *  so this only has to notice where the label changes; "." is its marker for a
 *  document directly in the category root. The key carries the section's ORDINAL
 *  among same-labelled sections, so two unlabelled runs cannot share one. */
function sectionsFor(rows: KiroDoc[]): Section[] {
  const out: Section[] = [];
  const seen = new Map<string, number>();
  for (const doc of rows) {
    const group = doc.group ?? "";
    const label = group === "." ? "" : group;
    const last = out.at(-1);
    if (last?.label === label) {
      last.docs.push(doc);
      continue;
    }
    const n: number = seen.get(label) ?? 0;
    seen.set(label, n + 1);
    out.push({ key: joinKey("g", label, String(n)), label, docs: [doc] });
  }
  return out;
}

/** A spec group's door onto its tab, parentless: the page's Run controls become a
 *  picker over the open chats. Null for any other section. */
function specDoor(s: Section): HTMLElement | null {
  const first = s.docs[0];
  if (first?.category !== "specs") {
    return null;
  }
  const dir = specDirOf(relToWorkspace("/" + first.path));
  if (dir === null) {
    return null;
  }
  const btn = el(
    "button",
    {
      type: "button",
      className: "btn-small docs-open-spec",
      "data-spec-dir": dir,
      "data-tooltip": `Open ${s.label} as a spec tab`,
    },
    "Open spec",
  );
  btn.addEventListener("click", () => {
    void openSpec(dir);
  });
  return btn;
}

/** The row's identity for the reconcile and for the builder's `data-key`. Path
 *  AND name, because one hook file expands to one row per hook. */
function rowKey(doc: KiroDoc): string {
  return joinKey("d", doc.path, doc.name);
}

function buildRow(doc: KiroDoc): HTMLElement {
  const row = entryRow(rowSpec(doc));
  sigChanged(row, rowSig(doc));
  return row;
}

/** Repaint a kept row when what it renders changed. `reconcile` leaves a kept row's
 *  content alone, and a row's key is its path plus its name — neither of which moves
 *  when a hook is enabled, an inclusion mode is edited or the git poll changes a
 *  letter — so the repaint has to come from here. Rebuilds the row's CHILDREN
 *  rather than replacing the row, so reconcile keeps tracking the node it placed. */
function updateRow(row: HTMLElement, doc: KiroDoc): void {
  paintIfChanged(row, rowSig(doc), () => [...entryRow(rowSpec(doc)).childNodes]);
}

/** Everything a row renders, as signature PARTS: `rowText` plus the state the
 *  controls render without text (the toggle's hook and position, the two provenance
 *  bits that decide the pencil and the delete). A stale toggle is the bug this
 *  exists to prevent. `paint-sig.ts` owns the join and the attribute. */
function rowSig(doc: KiroDoc): string[] {
  const hook = hookFor(doc);
  return [
    ...rowText(doc, hook),
    doc.read_only === true ? "1" : "0",
    doc.delete_protected === true ? "1" : "0",
    hook?.id ?? "",
    hook === undefined ? "" : hook.enabled ? "1" : "0",
  ];
}

/** The Hooks tab's rows: the scanned workspace hooks, then the global ones the
 *  scan cannot see.
 *
 *  Global rows come LAST rather than interleaved, matching the server's own order
 *  on GET /api/hooks (workspace before global, `hookScopeRank`), so the two
 *  surfaces present the same list in the same sequence. */
function hookRows(): KiroDoc[] {
  const scanned = docs.filter((d) => d.category === TAB_CATEGORY.hooks);
  const globals: KiroDoc[] = [];
  for (const h of hooks.values()) {
    // GLOBAL only, and that is the WHOLE test: a workspace hook the scan did not
    // report means the two surfaces disagree about the workspace, and synthesizing
    // it would build a row carrying the hooks endpoint's workDir-RELATIVE path —
    // which neither openFile nor the delete action accepts — while rowGates would
    // hand it both. No already-claimed check either: the scan's only reach is the
    // workspace (`kiroRoots()`), so no scanned row can ever BE this global hook.
    if (!isGlobalHook(h)) {
      continue;
    }
    globals.push(synthesizedHookDoc(h));
  }
  return [...scanned, ...globals];
}

/** A row for a global hook the docs scan never saw. Its `path` is the hook's
 *  DISPLAY path (`~/.kiro/hooks/x.json`), which no endpoint accepts — correct,
 *  because the row offers neither open nor delete; it is here to key the reconcile
 *  and to carry the file name into the group label. */
function synthesizedHookDoc(h: HookState): KiroDoc {
  const path = h.file_path ?? "";
  const out: KiroDoc = {
    category: TAB_CATEGORY.hooks,
    name: h.name,
    path,
    group: path.slice(path.lastIndexOf("/") + 1),
    // Global by construction: this function is only reached for a hook the scan
    // cannot see, and the scan sees the whole workspace. Stamped so the row keys
    // back to the state it was built from rather than to a same-named workspace
    // hook's.
    hook_scope: "global",
  };
  if (h.trigger !== undefined && h.trigger !== "") {
    out.trigger = h.trigger;
  }
  const action = h.command ?? h.prompt ?? "";
  if (action !== "") {
    out.action = action;
  }
  return out;
}

/** The hook state a row joins to, or undefined for a row the endpoint did not
 *  report (its own fetch failed, or the file changed under the inventory). */
function hookFor(doc: KiroDoc): HookState | undefined {
  if (doc.category !== TAB_CATEGORY.hooks) {
    return undefined;
  }
  return hooks.get(hookKey(rowScope(doc), doc.path, doc.name));
}

/** One row, from the shared builder, which owns the row's shape and its height;
 *  this fills the slots a category has a fact for. */
function rowSpec(doc: KiroDoc): EntryRowSpec {
  const hook = hookFor(doc);
  const gates = rowGates(doc, hook);
  const letter = gitLetter(doc, gates);
  return {
    key: rowKey(doc),
    title: doc.name,
    mark: letter === "" ? undefined : gitLetterChip(letter),
    badges: badgesFor(doc, hook, gates),
    sub: subFor(doc, hook),
    actions: rowActions(doc, hook, gates),
    // INERT when the row is not openable: without the button there is no role, no
    // tabindex and no listener, so assistive tech announces no control and a
    // keyboard user lands on none.
    open: gates.openable
      ? {
          name: doc.name,
          onOpen: () => {
            openFile(doc.path);
          },
        }
      : undefined,
  };
}

/** The title-line badges, most important first: the builder keeps two and drops
 *  the rest, so the order here is what decides which fact survives a crowded row.
 *  Every label stays in `rowText` whether or not it is painted. */
function badgesFor(doc: KiroDoc, hook: HookState | undefined, gates: RowGates): HTMLElement[] {
  const out: HTMLElement[] = [];
  switch (doc.category) {
    case "steering":
    case "skill": {
      // Inclusion is the most useful fact here: it answers "is this doc costing
      // me tokens on every session, or only when I touch its files".
      const mode = doc.inclusion ?? "";
      if (mode !== "") {
        // The class is lowercased ("fileMatch" → filematch): CSS class names are
        // kebab/lower by convention and the label keeps the camelCase spelling.
        const badge = el(
          "span",
          { className: `docs-badge docs-badge-${mode.toLowerCase()}` },
          mode,
        );
        if (doc.file_match !== undefined && doc.file_match !== "") {
          badge.setAttribute("data-tooltip", doc.file_match);
        }
        out.push(badge);
      }
      if (doc.steering_override === true) {
        const marker = el("span", { className: "docs-badge docs-badge-override" }, OVERRIDE_LABEL);
        marker.setAttribute("data-tooltip", "Replaces the steering set while this skill runs");
        out.push(marker);
      }
      break;
    }
    case "agent": {
      if (doc.model !== undefined && doc.model !== "") {
        out.push(el("span", { className: "docs-badge docs-badge-model" }, doc.model));
      }
      const tools = doc.tools ?? [];
      if (tools.length > 0) {
        const chip = el("span", { className: "docs-badge" }, toolCountLabel(tools.length));
        chip.setAttribute("data-tooltip", tools.join(", "));
        out.push(chip);
      }
      break;
    }
    case "hook": {
      if (doc.trigger !== undefined && doc.trigger !== "") {
        out.push(el("span", { className: "docs-badge docs-badge-trigger" }, doc.trigger));
      }
      if (hook !== undefined) {
        out.push(...hookBadges(hook));
      }
      break;
    }
    default:
      break;
  }
  if (gates.explainDelete) {
    // The delete is withheld and SAID, because an absent control with no reason
    // reads as a bug: this row is an alias, and deleting it would remove the file
    // it points at — which is listed under its own name on this same page.
    const badge = el("span", { className: "docs-badge docs-badge-link" }, LINK_LABEL);
    badge.setAttribute(
      "data-tooltip",
      "A symlink. Editing it writes the file it points to; deleting it would remove that file, so delete is disabled here",
    );
    out.push(badge);
  }
  return out;
}

/** The two-line region: a description where a kind has one, a pair of facts where
 *  it has two instead. A spec's second line is its group, a hook's first line the
 *  regex KAS tests the trigger's subject against — mono, because both are read
 *  character for character. */
function subFor(doc: KiroDoc, hook: HookState | undefined): EntrySub {
  switch (doc.category) {
    case "spec": {
      const group = doc.group ?? "";
      return {
        kind: "lines",
        lines: [{ text: doc.path, mono: true }, { text: group === "." ? "" : group }],
      };
    }
    case "hook":
      return {
        kind: "lines",
        lines: [
          { text: hook?.matcher ?? "", mono: true },
          { text: hookAction(doc, hook), mono: true },
        ],
      };
    default:
      return { kind: "clamp", text: doc.description ?? "" };
  }
}

/** A hook's command or prompt. The scan sets `action` from the command only, so a
 *  scanned askAgent hook's row reads its prompt off the joined state. */
function hookAction(doc: KiroDoc, hook: HookState | undefined): string {
  return doc.action ?? hook?.command ?? hook?.prompt ?? "";
}

function gitLetterChip(letter: string): HTMLElement {
  const chip = el("span", { className: "docs-git-letter" }, letter);
  chip.setAttribute("data-tooltip", describeStatus(letter));
  chip.setAttribute("aria-label", `Git status: ${describeStatus(letter)}`);
  return chip;
}

/** Split a document path into (repo, repo-relative path) for the git lookup.
 *
 *  Server paths look like `<workdir>/<repo>/.kiro/...` or `<workdir>/.kiro/...`,
 *  and git status reports a repo NAME plus a repo-relative path. The first
 *  segment after the work directory is the repo — for the workspace-root tree
 *  that is `.kiro` itself, which is its own repo. */
function splitRepoPath(path: string): { repo: string; rel: string } {
  const idx = path.indexOf("/.kiro");
  if (idx < 0) {
    return { repo: "", rel: "" };
  }
  // Everything before /.kiro, last segment = the containing directory.
  const before = path.slice(0, idx);
  const afterKiro = path.slice(idx + 1); // ".kiro/..."
  const parent = before.slice(before.lastIndexOf("/") + 1);
  // A per-repo tree: repo is the directory holding .kiro, and .kiro is part of
  // the repo-relative path. The workspace-root tree has no such directory
  // inside the workspace, so .kiro is itself the repo.
  if (parent === "" || before.endsWith("/workspace") || !before.includes("/")) {
    return { repo: ".kiro", rel: afterKiro.slice(".kiro/".length) };
  }
  return { repo: parent, rel: afterKiro };
}

/** What a row may do, resolved ONCE from three independent gates. A GLOBAL hook's
 *  file is under the container HOME, which `internal/filebrowse` deny-lists, so
 *  nothing there opens, edits or deletes. `read_only`: reading is legitimate, so
 *  the body stays a door and the controls go. `delete_protected`: the row is a
 *  symlink, so deleting it would unlink the file it points at. The enable toggle
 *  is outside all three: it goes through POST /api/hooks and KAS writes the file.
 *  An unopenable row's body is INERT rather than a disabled button, because a
 *  button that cannot act still announces itself as one. */
interface RowGates {
  openable: boolean;
  editable: boolean;
  deletable: boolean;
  /** Say why the delete is withheld. Only for the symlink case: an unreachable
   *  row's own scope badge already carries its reason, and a second explanation
   *  beside it would state one fact twice. */
  explainDelete: boolean;
}

function rowGates(doc: KiroDoc, hook: HookState | undefined): RowGates {
  if (hook !== undefined && isGlobalHook(hook)) {
    return { openable: false, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.read_only === true) {
    // Neither control, and no claim either: a row states what it can back up or
    // it states nothing.
    return { openable: true, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.delete_protected === true) {
    return { openable: true, editable: true, deletable: false, explainDelete: true };
  }
  return { openable: true, editable: true, deletable: true, explainDelete: false };
}

/** Delete the document behind a row, after a confirm.
 *
 *  The confirm is not ceremony: there is no undo path for a `.kiro` file — no
 *  trash, no snapshot — so the dialog IS the guard. */
async function deleteRow(doc: KiroDoc): Promise<void> {
  const ok = await confirmDialog(
    `Delete ${doc.name}? This cannot be undone.`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return;
  }
  const res = await deleteDoc.dispatch({ path: doc.path, name: doc.name });
  if (res === null) {
    return; // the action reported it; the row stays until the refetch says otherwise
  }
  loadDocs();
}

/** The row's actions: the hook's toggle first, then Edit, then Delete. Siblings
 *  of the open control rather than children of it, because a `<button>` cannot
 *  hold another. */
function rowActions(doc: KiroDoc, hook: HookState | undefined, gates: RowGates): HTMLElement[] {
  const out: HTMLElement[] = [];
  // Added before every gate below because none of them govern it.
  if (hook !== undefined) {
    out.push(hookToggle(hook));
  }
  if (gates.editable) {
    out.push(
      el(
        "button",
        {
          type: "button",
          className: "icon-btn docs-edit",
          "aria-label": `Edit ${doc.name}`,
          "data-tooltip": "Edit",
          onclick: () => {
            openFile(doc.path);
          },
        },
        iconEl(ICON_EDIT),
      ),
    );
  }
  if (gates.deletable) {
    out.push(
      el(
        "button",
        {
          type: "button",
          className: "icon-btn entry-delete",
          "aria-label": `Delete ${doc.name}`,
          "data-tooltip": "Delete",
          onclick: () => {
            void deleteRow(doc);
          },
        },
        iconEl(ICON_TRASH),
      ),
    );
  }
  return out;
}

/** The enable switch. Its checked state is the SERVER's — the row repaints from a
 *  refetch after every write — so there is no optimistic flip to reconcile. */
function hookToggle(h: HookState): HTMLElement {
  const input = el("input", {
    type: "checkbox",
    className: "hook-toggle",
    "aria-label": `${h.enabled ? "Disable" : "Enable"} hook ${h.name}`,
  }) as HTMLInputElement;
  input.checked = h.enabled;
  const label = el(
    "label",
    { className: "toggle toggle-inline" },
    input,
    el("span", { className: "toggle-slider" }),
  );
  // The id rides the wrapper, not the input, because the delegated handler walks
  // up from whatever the event hit.
  label.setAttribute("data-hook-id", h.id);
  return label;
}

/** The badges only a joined hook can carry, in the order the two-badge cap
 *  keeps them: its scope, a matcher defect, and the reason KAS disabled it.
 *
 *  The Global badge does double duty and that is deliberate — it names the scope
 *  AND carries the file path its row cannot open, so an unreachable row still
 *  tells the reader where the file is. */
function hookBadges(h: HookState): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (isGlobalHook(h)) {
    const badge = el("span", { className: "docs-badge docs-badge-global" }, GLOBAL_LABEL);
    badge.setAttribute(
      "data-tooltip",
      `${h.file_path ?? "~/.kiro/hooks"} — applies in every workspace. Outside the workspace, so it cannot be opened or deleted here`,
    );
    out.push(badge);
  }
  const warn = MATCHER_WARNINGS[h.matcher_warning ?? ""];
  if (warn !== undefined) {
    const badge = el("span", { className: "docs-badge docs-badge-warn" }, warn.label);
    badge.setAttribute("data-tooltip", warn.detail);
    out.push(badge);
  }
  const reason = h.disabled_reason ?? "";
  if (reason !== "") {
    const badge = el("span", { className: "docs-badge docs-badge-disabled" }, DISABLED_LABEL);
    badge.setAttribute("data-tooltip", reason);
    out.push(badge);
  }
  return out;
}

/** The two matcher defects the server reports, and the copy for each. A LOOKUP
 *  rather than a branch on the string, so an unrecognised value renders NOTHING
 *  instead of an empty badge: the field is a server-side enum, and a marotte build
 *  older than the server that added a third value should stay quiet. Both are
 *  warnings, not errors, so one badge style covers them: `every tool` may be a
 *  deliberate choice, and `no effect` cannot be created through marotte at all. */
const MATCHER_WARNINGS: Record<string, { label: string; detail: string }> = {
  missing_tool_matcher: {
    label: "every tool",
    detail:
      "This hook has no matcher, so it runs on EVERY tool call. Add a matcher to scope it to the tools you meant.",
  },
  ineffective: {
    label: "no effect",
    detail:
      "This trigger has nothing to match against, so its matcher is ignored and the hook fires every time. Remove the matcher, or pick a trigger whose matcher is tested against a tool name or a file path.",
  },
};

/** The four badge literals a row can render, named so the badge and `rowText`
 *  spell each one once. */
const OVERRIDE_LABEL = "override";
const GLOBAL_LABEL = "global";
const DISABLED_LABEL = "disabled";
const LINK_LABEL = "link";

function toolCountLabel(n: number): string {
  return `${String(n)} tool${n === 1 ? "" : "s"}`;
}

/** The row's git letter, or "" when it has none.
 *
 *  Skipped for an unreachable row: a global hook's `~/...` display path is not in
 *  any repo the status poll walks, and splitRepoPath would resolve it to a
 *  plausible repo name and look up a file that does not exist there. */
function gitLetter(doc: KiroDoc, gates: RowGates): string {
  if (!gates.openable) {
    return "";
  }
  const { repo, rel } = splitRepoPath(doc.path);
  return repo === "" ? "" : statusFor(repo, rel);
}

/** Placeholder rows on the list's own tier, so the swap to real rows moves nothing. */
function showSkeleton(): () => void {
  return paintPlaceholder(panelFor(activeTab.peek()), () => {
    const list = entryList();
    list.append(...entrySkeleton(4));
    return list;
  });
}

/** @internal Test seam: inject rows without a fetch. */
export function _setDocsForTest(list: KiroDoc[]): void {
  docs = list;
}

/** @internal Test seam: inject hook state without a fetch, keyed the way the
 *  join keys it. */
export function _setHooksForTest(list: HookState[]): void {
  hooks = new Map(list.map((h) => [hookKey(hookScopeOf(h), h.file_path ?? "", h.name), h]));
}

/** @internal Test seam for the Hooks tab's row set — the one tab that is not a
 *  pure filter of the inventory. */
export function _hookRowsForTest(): KiroDoc[] {
  return hookRows();
}

/** @internal Test seam for the repo/path split — the piece that decides whether
 *  a git letter resolves at all. */
export function _splitRepoPathForTest(path: string): { repo: string; rel: string } {
  return splitRepoPath(path);
}

/** @internal Test seam for one rendered row. */
export function _renderRowForTest(doc: KiroDoc): HTMLElement {
  return buildRow(doc);
}

/** @internal Test seam: repaint the active panel from the seeded state, without
 *  a fetch. */
export function _renderActiveForTest(): void {
  renderActive();
}
