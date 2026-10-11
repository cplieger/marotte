// The Kiro configuration browser: one page over `.kiro/`, sub-tabbed per category because the categories are
// different kinds of thing (inclusion mode, model, feature directory, trigger).

import { apiGetTyped, type Decoder } from "./api-client.js";
import { decodeKiroDocsResponse, decodeSteeringIssuesResponse } from "./wire/decoders.gen.js";
import type { KiroDoc as WireKiroDoc, KiroDocsResponse, SteeringIssue } from "./wire/types.gen.js";
import { defineAction, ActionError, retryNetwork, registerCleanup } from "./actions/index.js";
import { setHookEnabled } from "./actions/hooks.js";
import { asObject, decodeArray, optStr, reqBool, reqStr } from "./validators.js";
import { onSSE } from "./bus.js";
import { $, byId } from "./dom.js";
import { swapViews } from "./view-swap.js";
import { el } from "@cplieger/reactive";
import { iconEl } from "./icon-el.js";
import { ICON_EDIT, ICON_TRASH } from "./icons.js";
import { confirm as confirmDialog } from "./confirm.js";
import { deleteDoc } from "./actions/docs.js";
import { onGitStatusChange, statusForPath } from "./git-status-store.js";
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
import { pushRoute, replaceRoute } from "./router.js";
import { buildPath, type DocsTab } from "./route-path.js";
import { renderRecipesPanel, setRecipeCountsListener } from "./recipes.js";
import { agentRunButton } from "./agent-run.js";
import { mountNewDocButton } from "./docs-new.js";
import { renderMemoriesPanel, setMemoryCountsListener } from "./memories.js";
import { loadSettings } from "./persist.js";
import { renderPowersPanel, setPowerCountsListener } from "./powers.js";
import { setDocsTab as setTabRoute } from "./tabs.js";
import { initSegmentedBar, setSegmentHidden } from "./segmented-bar.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";
import { setPageSubtitle } from "./page-title.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";

/**
 * The wire's `KiroDoc` plus `hook_scope`, client-derived: `kiroRoots()` scans only the workspace, so a scanned row
 * is workspace and a synthesized one global. Absent means workspace. Part of the join key.
 */
type KiroDoc = WireKiroDoc & { hook_scope?: HookScope };

/** Scope is part of a hook's identity: two hooks can share relative path and name across scopes. */
type HookScope = "workspace" | "global";

// Hooks join the scan's rows against GET /api/hooks (state a scan cannot see) and synthesize global hooks.

/**
 * A hook's live state from GET /api/hooks, narrowed to what the tab needs; `trigger` / `command` / `prompt` exist
 * for the synthesized global row.
 */
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
  /** KAS's matcher string, shown verbatim: a regex, or on tool triggers also a tool id, glob or tag. */
  matcher?: string;
  /**
   * The trigger-and-matcher defect, computed server-side (internal/marotte's ClassifyHookMatcher):
   * `missing_tool_matcher` or `ineffective`. Absent means nothing wrong.
   */
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

/**
 * Global hooks live in ~/.kiro/hooks (kiro-cli 2.13+). Scope is derived server-side; absent counts as workspace,
 * the safe direction.
 */
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

/** Normalize either path shape to its `.kiro/...` tail; "" when there is no `.kiro` segment (no join). */
function hookPathKey(path: string): string {
  const idx = path.indexOf(".kiro/");
  return idx < 0 ? "" : path.slice(idx);
}

/**
 * The join key: (scope, path tail, name). keyenc, because a hook name is arbitrary text. Scope leads because
 * `hookPathKey` discards what told the scopes apart.
 */
function hookKey(scope: HookScope, path: string, name: string): string {
  return joinKey(scope, hookPathKey(path), name);
}

/** Tab order is fixed; it must match the `data-docs-tab` order in index.html. */
const DOCS_TABS: readonly DocsTab[] = [
  "steering",
  "skills",
  "prompts",
  "agents",
  "specs",
  "hooks",
  "workflows",
  "memories",
  "powers",
] as const;

/** The tabs whose rows come from an RPC rather than the `.kiro` scan. */
type RpcTab = "workflows" | "memories" | "powers";

function isRpcTab(t: DocsTab): t is RpcTab {
  return t === "workflows" || t === "memories" || t === "powers";
}

const TAB_LABELS: Readonly<Record<DocsTab, string>> = {
  steering: "Steering",
  skills: "Skills",
  prompts: "Prompts",
  agents: "Agents",
  specs: "Specs",
  hooks: "Hooks",
  workflows: "Workflows",
  memories: "Memories",
  powers: "Powers",
};

/** Wire category per tab. Workflows has none: recipes come from KAS, so its panel is recipes.ts's. */
const TAB_CATEGORY: Readonly<Record<Exclude<DocsTab, RpcTab>, string>> = {
  steering: "steering",
  skills: "skill",
  prompts: "prompt",
  agents: "agent",
  specs: "spec",
  hooks: "hook",
};

const EMPTY_TEXT: Readonly<Record<Exclude<DocsTab, RpcTab>, string>> = {
  steering: "No steering docs in .kiro/steering/.",
  skills: "No skills in .kiro/skills/.",
  prompts: "No saved prompts in .kiro/prompts/.",
  agents: "No custom agents in .kiro/agents/.",
  specs: "No specs in .kiro/specs/.",
  hooks: "No hooks in .kiro/hooks/.",
};

const FIRST_TAB: DocsTab = "steering";

const activeTab = signal<DocsTab>(FIRST_TAB);
/** The Memory setting is Off, which withdraws the Memories tab. Null until the settings answer, so
 *  a Memories deep link waits rather than opening a panel it may have to withdraw. */
const memoryOff = signal<boolean | null>(null);
/** Kept so a tab switch repaints from memory. */
let docs: KiroDoc[] = [];
/** Separate from `docs`: different invalidation triggers (`settings_updated` vs `hooks_changed`). */
let hooks = new Map<string, HookState>();
/** Own fetch and broadcast (`steering_issues_changed`), so a failure does not blank the list. */
let steeringIssues = new Map<string, SteeringIssue[]>();
let inited = false;
/** `docs` starts as `[]`, so an empty category is otherwise indistinguishable from one never read. */
let inventoryAnswered = false;
/** The server cut the inventory: a filter over a cut list filters less than the page implies. */
let docsTruncated = false;
/** The folded filter query: metadata only, in memory, folding both sides (no match-case toggle). */
let filterText = "";

/** One noun for every tab: the box is labelled "Filter documents". */
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

/** The docs tab's activation: one-shot init only; forces no sub-tab, fetches nothing. */
export function showDocsTab(): void {
  initDocsView();
}

/** Refetch at the active sub-tab. Runs the init first, since `loadDocs`' success path renders into the panels. */
export function refreshDocsView(): void {
  initDocsView();
  loadDocs();
}

/** Set the active tab without pushing a URL — the router's entry point when
 *  back/forward lands on /docs/<tab>. Returns the tab applied: a withdrawn one lands on the first. */
export function forceDocsTab(tab: DocsTab): DocsTab {
  const applied = tab === "memories" && memoryOff.peek() === true ? FIRST_TAB : tab;
  // Replaced, not left for the tab's own push, so Back cannot land on the withdrawn tab again.
  if (applied !== tab && location.pathname === buildPath({ kind: "docs", tab })) {
    replaceRoute({ kind: "docs", tab: applied });
  }
  setTabRoute(applied);
  activeTab.value = applied;
  return applied;
}

/** Numbers each settings read so only the newest answer lands: reads overlap and resolve in any order. */
let memoryRead = 0;

function refreshMemoryAvailability(): void {
  const read = ++memoryRead;
  void loadSettings().then((s) => {
    if (read !== memoryRead) {
      return;
    }
    if (s !== null) {
      memoryOff.value = s.memory_mode === "off";
    } else if (memoryOff.peek() === null) {
      // Never answered: offer the tab rather than leave a segment whose panel never renders.
      memoryOff.value = false;
    }
  });
}

/** The route is replaced only while this page is on screen; otherwise the URL belongs to another tab. */
function withdrawMemories(): void {
  if (activeTab.peek() !== "memories") {
    return;
  }
  if ($.docsView.offsetParent !== null) {
    replaceRoute({ kind: "docs", tab: FIRST_TAB });
  }
  setTabRoute(FIRST_TAB);
  activeTab.value = FIRST_TAB;
}

/** Fetch (or refetch) the inventory and repaint. */
function loadDocs(): void {
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
  loadSteeringIssues();
}

function loadSteeringIssues(): void {
  void apiGetTyped("/api/steering/issues", decodeSteeringIssuesResponse).then((d) => {
    if (d === null) {
      return;
    }
    steeringIssues = new Map(Object.entries(d.issues));
    if (activeTab.peek() === "steering") {
      renderActive();
    }
  });
}

/**
 * Best-effort and separate from loadDocs: a hooks failure must not blank other tabs. A workspace hook still renders
 * without its toggle; a global hook does not render.
 */
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
  // Through the leaf registry, not the dispatcher: find-dispatch would pull scroll.ts's self-initialising singleton
  // into this lazy page. Every tab is filterable, so no `available` predicate.
  registerFind("docs", docsFilter);
  // The Workflows panel reports its own counts; wired once since it repaints on its own schedule.
  setRecipeCountsListener(({ total, shown }) => {
    // The panel's refetch can land after the reader left, and would stamp another tab's note.
    if (activeTab.peek() === "workflows") {
      docsFilter.shell?.setNote(noteFor("workflows", total, shown));
    }
  });
  setMemoryCountsListener(({ total, shown }) => {
    if (activeTab.peek() === "memories") {
      docsFilter.shell?.setNote(noteFor("memories", total, shown));
    }
  });
  setPowerCountsListener(({ total, shown }) => {
    if (activeTab.peek() === "powers") {
      docsFilter.shell?.setNote(noteFor("powers", total, shown));
    }
  });

  const syncNewDoc = mountNewDocButton(
    byId("docs-new-slot"),
    (path) => {
      loadDocs();
      openFile(path);
    },
    (prompt, shown) => {
      // Lazy: the chat module's graph is the app's, which this page has no other use for.
      void import("./chat.js").then(({ createSpecSession }) => createSpecSession(prompt, shown));
    },
  );
  subscribe(activeTab, (tab) => {
    syncTabChrome(tab, paintBar);
    syncNewDoc(tab);
    renderActive();
  });
  subscribe(memoryOff, (off) => {
    setSegmentHidden(bar, "data-docs-tab", "memories", off === true);
    if (off === true) {
      withdrawMemories();
    } else if (off === false && activeTab.peek() === "memories") {
      renderActive();
    }
  });
  refreshMemoryAvailability();

  // Git letters ride the shared status store; subscribing starts it.
  registerCleanup(
    onGitStatusChange(() => {
      renderActive();
    }),
  );
  registerCleanup(
    onSSE("settings_updated", () => {
      refreshMemoryAvailability();
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  // Hook state needs its own broadcast: `settings_updated` does not fire for a hook file, and the scan is memoized on
  // directory mtime and names, so an in-place edit needs `hooks_changed` (KAS's `_kiro/hooks/didChange`).
  registerCleanup(
    onSSE("hooks_changed", () => {
      if ($.docsView.offsetParent !== null) {
        loadDocs();
      }
    }),
  );
  registerCleanup(
    onSSE("steering_issues_changed", () => {
      if ($.docsView.offsetParent !== null) {
        loadSteeringIssues();
      }
    }),
  );
  // Delegated, since reconciled rows would rebind a per-row listener on every repaint.
  $.docsView.addEventListener("change", (e) => {
    const target = e.target as HTMLElement;
    if (!target.classList.contains("hook-toggle")) {
      return;
    }
    const id = target.closest<HTMLElement>("[data-hook-id]")?.getAttribute("data-hook-id") ?? "";
    if (id !== "") {
      // Server-canonical: refetch after the write so the checkbox reconciles, including on failure.
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

/** The note for the shown tab: silent with no filter unless the server truncated; an emptied tab names where matches went. */
function noteFor(tab: DocsTab, total: number, shown: number): string {
  // Workflows rows are not from the docs reply, so its cap does not apply.
  const truncated = isRpcTab(tab) ? false : docsTruncated;
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

/** The inventory-backed tabs; the RPC-backed panels' rows live in recipes.ts,
 *  memories.ts and powers.ts. */
const INVENTORY_TABS = DOCS_TABS.filter((t): t is Exclude<DocsTab, RpcTab> => !isRpcTab(t));

const TAB_LIST = new Intl.ListFormat("en-US", { type: "conjunction" });

function rowsFor(tab: Exclude<DocsTab, RpcTab>): KiroDoc[] {
  return tab === "hooks" ? hookRows() : docs.filter((d) => d.category === TAB_CATEGORY[tab]);
}

function matches(doc: KiroDoc): boolean {
  return filterHaystack(doc).includes(filterText);
}

/** Workflows is never a `where`: its rows are not in memory. */
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

/** The metadata filter, a popup: opens from the magnifier or Ctrl-F, closes on Escape, and closing clears the query. */
const docsFilter: SearchPopup = createSearchPopup<null>({
  id: "docs-filter",
  // A filter: it can only hide rows that are here.
  kind: "filter",
  label: "Filter documents",
  // Metadata only: a body is the file browser's grep.
  placeholder: "Filter by name, description or trigger\u2026",
  note: true,
  host: () => document.getElementById("docs-view"),
  // Applied in renderActive, the one place deciding which records a tab shows.
  query: (query) => {
    filterText = query.toLowerCase();
    return null;
  },
  render: () => {
    renderActive();
  },
});

/** A group of rows under one label: a spec's feature directory, a hook's file.
 *  The flat categories render as one unlabelled section. */
interface Section {
  readonly key: string;
  readonly label: string;
  readonly docs: KiroDoc[];
}

/** Every string a row shows, in one list: the filter haystack and the repaint signature both read it. */
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
    ...issueText(doc),
    MATCHER_WARNINGS[hook?.matcher_warning ?? ""]?.label ?? "",
    gates.explainDelete ? LINK_LABEL : "",
    gitLetter(doc, gates),
  ];
}

/** Per call, not cached: ~200 rows refetched whole would need `rowSig`'s invalidation for no gain. */
function filterHaystack(doc: KiroDoc): string {
  return rowText(doc, hookFor(doc)).join("\n").toLowerCase();
}

function renderActive(): void {
  const tab = activeTab.peek();
  const container = panelFor(tab);
  if (container === null) {
    return;
  }
  // The RPC panels filter their own rows and report counts through the listeners in initDocsView.
  if (tab === "workflows") {
    renderRecipesPanel(container, filterText);
    return;
  }
  if (tab === "memories") {
    // Before the settings answer, the memoryOff subscription renders or withdraws the panel.
    if (memoryOff.peek() === false) {
      renderMemoriesPanel(container, filterText);
    }
    return;
  }
  if (tab === "powers") {
    renderPowersPanel(container, filterText);
    return;
  }
  const all = rowsFor(tab);
  const rows = filterText === "" ? all : all.filter(matches);
  docsFilter.shell?.setNote(noteFor(tab, all.length, rows.length));
  if (rows.length === 0) {
    // The category's empty text would be a lie under an active filter.
    container.replaceChildren(
      el(
        "div",
        { className: "list-empty" },
        filterText === "" ? EMPTY_TEXT[tab] : "No documents match the filter.",
      ),
    );
    return;
  }

  // Drop non-keyed placeholders before reconcile.
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

/** Nested inside the panel's reconcile, safe because reconcile reads only keyed children. */
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

/**
 * Cut at every label change; the server already orders each category. "." marks the category root. The key carries
 * the section's ordinal among same-labelled sections.
 */
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

/** Path and name, because one hook file expands to one row per hook. */
function rowKey(doc: KiroDoc): string {
  return joinKey("d", doc.path, doc.name);
}

function buildRow(doc: KiroDoc): HTMLElement {
  const row = entryRow(rowSpec(doc));
  sigChanged(row, rowSig(doc));
  return row;
}

/**
 * Reconcile leaves a kept row alone and its key does not move on a state change, so repaint here; children only,
 * so reconcile keeps the node.
 */
function updateRow(row: HTMLElement, doc: KiroDoc): void {
  paintIfChanged(row, rowSig(doc), () => [...entryRow(rowSpec(doc)).childNodes]);
}

/**
 * Everything a row renders as signature parts, including state the controls render without text. `paint-sig.ts`
 * owns the join.
 */
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

/** Scanned workspace hooks, then globals, matching the server's own order (`hookScopeRank`). */
function hookRows(): KiroDoc[] {
  const scanned = docs.filter((d) => d.category === TAB_CATEGORY.hooks);
  const globals: KiroDoc[] = [];
  for (const h of hooks.values()) {
    // Global only: a synthesized workspace row would carry a workDir-relative path that neither openFile nor delete
    // accepts, while rowGates would offer both.
    if (!isGlobalHook(h)) {
      continue;
    }
    globals.push(synthesizedHookDoc(h));
  }
  return [...scanned, ...globals];
}

/**
 * A global hook's row, keyed by its display path (`~/.kiro/hooks/x.json`), which no endpoint accepts: it offers
 * neither open nor delete.
 */
function synthesizedHookDoc(h: HookState): KiroDoc {
  const path = h.file_path ?? "";
  const out: KiroDoc = {
    category: TAB_CATEGORY.hooks,
    name: h.name,
    path,
    group: path.slice(path.lastIndexOf("/") + 1),
    // Stamped so the row keys back to its own state, not a same-named workspace hook's.
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
    // Inert when not openable: no role, tabindex or listener for assistive tech to announce.
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

/** Most important first: the builder keeps two. Every label stays in `rowText`. */
function badgesFor(doc: KiroDoc, hook: HookState | undefined, gates: RowGates): HTMLElement[] {
  const out: HTMLElement[] = [];
  switch (doc.category) {
    case "steering":
    case "skill": {
      // First: an issue means the document does not do what it says.
      const issues = issuesFor(doc);
      if (issues.length > 0) {
        const badge = el(
          "span",
          { className: "docs-badge docs-badge-warn" },
          issueLabel(issues.length),
        );
        badge.setAttribute("data-tooltip", issueTooltip(issues));
        out.push(badge);
      }
      // Inclusion answers "does this cost tokens every session or only on matching files".
      const mode = doc.inclusion ?? "";
      if (mode !== "") {
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
    // Withheld and said: this row is a symlink, and deleting it would remove a file listed under its own name.
    const badge = el("span", { className: "docs-badge docs-badge-link" }, LINK_LABEL);
    badge.setAttribute(
      "data-tooltip",
      "A symlink. Editing it writes the file it points to. Deleting it would remove that file, so delete is disabled here",
    );
    out.push(badge);
  }
  return out;
}

function issuesFor(doc: KiroDoc): SteeringIssue[] {
  return doc.category === "steering" ? (steeringIssues.get(doc.path) ?? []) : [];
}

function issueLabel(n: number): string {
  return n === 1 ? "1 issue" : `${String(n)} issues`;
}

/** One sentence per issue: KAS's remediation, else its reason. */
function issueTooltip(issues: readonly SteeringIssue[]): string {
  return issues
    .map((i) => (i.remediation !== "" ? i.remediation : (i.reason ?? i.code)))
    .join(" · ");
}

function issueText(doc: KiroDoc): string[] {
  const issues = issuesFor(doc);
  return issues.length === 0 ? [] : [issueLabel(issues.length), issueTooltip(issues)];
}

/** A description, or a pair of facts; a spec's second line is its group, a hook's first its matcher (mono). */
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

/** The scan sets `action` from the command only, so an askAgent hook's prompt comes from the joined state. */
function hookAction(doc: KiroDoc, hook: HookState | undefined): string {
  return doc.action ?? hook?.command ?? hook?.prompt ?? "";
}

function gitLetterChip(letter: string): HTMLElement {
  const chip = el("span", { className: "docs-git-letter" }, letter);
  chip.setAttribute("data-tooltip", describeStatus(letter));
  chip.setAttribute("aria-label", `Git status: ${describeStatus(letter)}`);
  return chip;
}

/**
 * Three independent gates resolved once: global hook (container HOME is deny-listed by `internal/filebrowse`, so
 * nothing opens), `read_only` (body stays a door, controls go), `delete_protected` (a symlink).
 */
interface RowGates {
  openable: boolean;
  editable: boolean;
  deletable: boolean;
  /** Explain a withheld delete, only for the symlink case: an unreachable row's scope badge already says why. */
  explainDelete: boolean;
}

function rowGates(doc: KiroDoc, hook: HookState | undefined): RowGates {
  if (hook !== undefined && isGlobalHook(hook)) {
    return { openable: false, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.read_only === true) {
    // No controls and no claim: a row states what it can back up.
    return { openable: true, editable: false, deletable: false, explainDelete: false };
  }
  if (doc.delete_protected === true) {
    return { openable: true, editable: true, deletable: false, explainDelete: true };
  }
  return { openable: true, editable: true, deletable: true, explainDelete: false };
}

/** The confirm is the guard: a `.kiro` file has no undo. */
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

/** Siblings of the open control: a `<button>` cannot hold another. */
function rowActions(doc: KiroDoc, hook: HookState | undefined, gates: RowGates): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (doc.category === "agent") {
    const run = agentRunButton(doc.name);
    if (run !== null) {
      out.push(run);
    }
  }
  // Before every gate below, since none governs it.
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

/** Checked state is the server's; the row repaints after every write, so no optimistic flip. */
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
  // On the wrapper, since the delegated handler walks up from whatever the event hit.
  label.setAttribute("data-hook-id", h.id);
  return label;
}

/** Scope, matcher defect, disabled reason, in two-badge-cap order. The Global badge also carries the unreachable file's path. */
function hookBadges(h: HookState): HTMLElement[] {
  const out: HTMLElement[] = [];
  if (isGlobalHook(h)) {
    const badge = el("span", { className: "docs-badge docs-badge-global" }, GLOBAL_LABEL);
    badge.setAttribute(
      "data-tooltip",
      `${h.file_path ?? "~/.kiro/hooks"} applies in every workspace. It is outside the workspace, so it cannot be opened or deleted here`,
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

/** A lookup, so an unknown server enum value renders nothing instead of an empty badge. */
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

/** Named so the badge and `rowText` spell each literal once. */
const OVERRIDE_LABEL = "override";
const GLOBAL_LABEL = "global";
const DISABLED_LABEL = "disabled";
const LINK_LABEL = "link";

function toolCountLabel(n: number): string {
  return `${String(n)} tool${n === 1 ? "" : "s"}`;
}

/** Row paths are absolute with the leading `/` dropped. Skipped for an unreachable row: a global hook's
 *  `~/` path would read as a literal `/~` key, which a repository at `/` can hold. */
function gitLetter(doc: KiroDoc, gates: RowGates): string {
  if (!gates.openable) {
    return "";
  }
  return statusForPath("/" + doc.path);
}

/** On the list's own tier, so the swap to real rows moves nothing. */
function showSkeleton(): () => void {
  return paintPlaceholder(panelFor(activeTab.peek()), () => {
    const list = entryList();
    list.append(...entrySkeleton(4));
    return list;
  });
}

/** @internal Test seam: inject rows without a fetch. */
// deadset:ignore DS1004 -- test seam: seeds the docs inventory without a fetch
export function _setDocsForTest(list: KiroDoc[]): void {
  docs = list;
}

/** @internal Test seam: inject hook state without a fetch, keyed the way the
 *  join keys it. */
// deadset:ignore DS1004 -- test seam: seeds the hook state map without a fetch
export function _setHooksForTest(list: HookState[]): void {
  hooks = new Map(list.map((h) => [hookKey(hookScopeOf(h), h.file_path ?? "", h.name), h]));
}

/** @internal Test seam for the Hooks tab's row set — the one tab that is not a
 *  pure filter of the inventory. */
// deadset:ignore DS1004 -- test seam: reads the Hooks rows built from the docs inventory and the hook-state map
export function _hookRowsForTest(): KiroDoc[] {
  return hookRows();
}

/** @internal Test seam for one rendered row. */
// deadset:ignore DS1004 -- test seam: renders one row from the hook, steering-issue and repository-status state
export function _renderRowForTest(doc: KiroDoc): HTMLElement {
  return buildRow(doc);
}

/** @internal Test seam: repaint the active panel from the seeded state, without
 *  a fetch. */
// deadset:ignore DS1004 -- test seam: repaints from the active tab, docs inventory, hook map and filter state
export function _renderActiveForTest(): void {
  renderActive();
}
