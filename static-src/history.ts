// Previous chats and workflow runs from KAS, on two panes (`/history`, `/history/runs`). marotte archives nothing: a
// picker over `GET /api/sessions`. A chat row opens its chat or adopts it via `resume_session`; a run row opens the
// read-only run view.

import { el, signal, subscribe, effect } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { $ } from "./dom.js";
import { hasTab, setHistoryTab as setHistoryTabRoute, tabSetVersion } from "./tabs.js";
import type { TabRunDotStatus } from "./tabs.js";
import { pushRoute } from "./router.js";
import type { HistoryTab } from "./route-path.js";
import { onBus, BUS_RUNS_CHANGED } from "./bus.js";
import { reconcile } from "./reconcile.js";
import { paintPlaceholder } from "./skeleton.js";
import { swapViews } from "./view-swap.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { setPageSubtitle } from "./page-title.js";
import { entryRow, entrySkeleton } from "./entry-row.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { loadSessions } from "./actions/chat.js";
import { registerCleanup } from "./actions/index.js";
import { openPreviousSession, openChatTab } from "./chat.js";
import { searchChats } from "./actions/chat-search.js";
import { openChatFindAt } from "./find-in-chat.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import type { Match } from "./wire/types.gen.js";
import { openRunView } from "./run-view.js";
import { runChatID } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { deleteRun } from "./actions/runs.js";
import { deleteChat as deleteChatAction } from "./actions/chat.js";
import { confirm } from "./confirm.js";
import { get } from "./store.js";
import { labelForMode } from "./roles.js";
import { applyOutcome } from "./tool-card.js";
import type { ToolRenderInfo } from "./tool-schema.js";
import { ICON_TAB_RUN, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";
import type { PageFind } from "./find-registry.js";
import type { ResumableSession, SessionListResponse, WorkflowRun } from "./types.js";
import { classifyRunStatus, type ClassifiedRunStatus } from "./run-status.js";

const HISTORY_TABS: readonly HistoryTab[] = ["chats", "runs"] as const;

const TAB_LABELS: Readonly<Record<HistoryTab, string>> = {
  chats: "Chats",
  runs: "Runs",
};

/** Every field the row renders is here, which makes `wireSignature(row)` a total repaint guard. */
interface HistoryRow {
  /** Prefixed per kind so a session and a run never collide. */
  key: string;
  kind: "chat" | "run";
  title: string;
  updatedAt: number;
  /** The one subtitle line, already joined. */
  sub: string;
  /** The verdict stated as a glyph, or null: any chat, a moving run, or a status with no verdict. */
  outcome: RunVerdict | null;
  /** The workflow mark for a moving run, `""` otherwise. `input` is an ask the dock holds (`runPendingAsks`). */
  mark: TabRunDotStatus | "";
  /** The listed runs this chat launched, which deleting it deletes too; 0 on a
   *  run row. */
  runCount: number;
  session?: ResumableSession;
  run?: WorkflowRun;
}

/**
 * Read from the store, not `/api/sessions`: `/api/chats` already holds every header, so the join costs nothing. An
 * unknown chat yields an empty list, since placeholders would read as fact.
 */
function chatFacts(chatID: string): string[] {
  const s = get(chatID);
  if (s === undefined) {
    return [];
  }
  const facts: string[] = [];
  if (s.model !== "") {
    facts.push(s.model);
  }
  if (s.current_mode_id !== "") {
    facts.push(labelForMode(s.current_mode_id));
  }
  const turns = s.turn_count;
  if (turns > 0) {
    facts.push(`${String(turns)} ${turns === 1 ? "turn" : "turns"}`);
  }
  // Credits only when metered: 0.00 on every unmetered row is noise.
  if (s.usage.credits > 0) {
    facts.push(`${s.usage.credits.toFixed(2)} cr`);
  }
  return facts;
}

/** What marotte did when a bound stopped it, the recipe when the label differs, the duration, and the launching chat. */
function runSub(r: WorkflowRun, endReason: string): string {
  const parts: string[] = [];
  const reason = END_REASON_TEXT[endReason];
  if (reason !== undefined) {
    parts.push(reason);
  }
  const recipe = r.workflow_name ?? "";
  if (recipe !== "" && recipe !== r.name) {
    parts.push(recipe);
  }
  // From creation, not `started_at`: KAS restamps that each time it resumes a run, which would drop every
  // stretch before the last resume.
  const started = r.started_at === undefined ? 0 : (r.created_at ?? r.started_at);
  if (started > 0 && r.updated_at > started) {
    parts.push(formatDuration(r.updated_at - started));
  }
  const parent = get(r.parent_chat_id ?? "")?.name ?? "";
  if (parent !== "") {
    parts.push(`from ${parent}`);
  }
  return parts.join(" · ");
}

/** An order of magnitude, not a stopwatch. */
function formatDuration(ms: number): string {
  const secs = Math.round(ms / 1000);
  if (secs < 60) {
    return `${String(secs)}s`;
  }
  const mins = Math.round(secs / 60);
  if (mins < 60) {
    return `${String(mins)}m`;
  }
  const hours = Math.floor(mins / 60);
  return `${String(hours)}h ${String(mins % 60)}m`;
}

/**
 * Two are `ToolStatus` members and the third widens `applyOutcome`, so these pass into the shared outcome
 * vocabulary untranslated.
 */
type RunVerdict = "completed" | "failed" | "aborted";

/**
 * Keys are the server's vocabulary (marotte.WorkflowRun.EndReason). Bounds cancel like a person does, so KAS says
 * `aborted` for both; this is where the difference is stated.
 */
const END_REASON_TEXT: Readonly<Record<string, string>> = {
  overran: "stopped: it ran past its time limit",
  stalled: "stopped: no activity and no live shell within its idle window",
  orphaned: "stopped: the server restarted while it was running",
};

/**
 * A live run's liveness is the mark's, and an unknown status is never a green check. A recognised `end_reason`
 * outranks the status (KAS says `aborted` at best, `running` mid-frame). Recognised, not non-empty, so one vocabulary
 * decides sentence and verdict.
 */
function runVerdict(status: ClassifiedRunStatus | undefined, endReason: string): RunVerdict | null {
  if (END_REASON_TEXT[endReason] !== undefined) {
    return "aborted";
  }
  switch (status) {
    case "completed":
    case "failed":
    case "aborted":
      return status;
    case "cancelled":
      // A user stop reads as the stop a bound produces.
      return "aborted";
    case undefined:
    case "running":
    case "paused":
    case "unknown":
      return null;
  }
}

/**
 * Written as `run-bar.ts` writes it. A recognised `end_reason` outranks everything, as in `runVerdict`; an ask
 * outranks the status, since KAS leaves an asking run `running`.
 */
function runMark(
  status: ClassifiedRunStatus | undefined,
  endReason: string,
  asking: boolean,
): TabRunDotStatus | "" {
  if (END_REASON_TEXT[endReason] !== undefined) {
    return "";
  }
  if (asking) {
    return "input";
  }
  switch (status) {
    case "running":
      return "working";
    case "paused":
      return "waiting";
    case undefined:
    case "completed":
    case "failed":
    case "aborted":
    case "cancelled":
    case "unknown":
      return "";
  }
}

/**
 * The stub shape `messages-tools.ts` passes on its DOM-only path. An empty `fileBasename` keeps the accessible name
 * to the row's label; a null `denial` keeps the row from reading as a policy refusal.
 */
const ROW_RENDER_INFO: ToolRenderInfo = {
  kind: "other",
  writesFile: false,
  filePath: "",
  fileBasename: "",
  diffSources: null,
  mcp: null,
  disclosed: null,
  denial: null,
};

/** An open tab is one click away, so what it covers is not history. Every device projects the same server tab set. */
function isOpenHere(s: ResumableSession): boolean {
  const chatID = s.chat_id ?? "";
  return chatID !== "" && hasTab("chat", chatID);
}

/**
 * Status plays no part: the launching chat's tab carries the run card and bar, and a live parentless run whose view
 * was dismissed has no other door back.
 */
function runOpenHere(r: WorkflowRun): boolean {
  if (hasTab("run", r.workflow_id)) {
    return true;
  }
  // Empty when the launching session is in no chat's chain; the run store learned the chat from the run's frames.
  const listed = r.parent_chat_id ?? "";
  const parent = listed !== "" ? listed : runChatID(r.workflow_id);
  return parent !== "" && hasTab("chat", parent);
}

function toRows(sessions: ResumableSession[], runs: WorkflowRun[]): HistoryRow[] {
  const rows: HistoryRow[] = [];
  // Over every run, hidden or not: deleting a chat also deletes a run hidden by its own open tab.
  const launched = new Map<string, number>();
  for (const r of runs) {
    const parent = r.parent_chat_id ?? "";
    if (parent !== "") {
      launched.set(parent, (launched.get(parent) ?? 0) + 1);
    }
  }
  for (const s of sessions) {
    // Filtered here so the key never reaches reconcile.
    if (isOpenHere(s)) {
      continue;
    }
    const description = s.description ?? "";
    rows.push({
      key: `s:${s.session_id}`,
      kind: "chat",
      title: s.title === "" ? "Untitled session" : s.title,
      updatedAt: s.updated_at,
      sub: description !== "" ? description : chatFacts(s.chat_id ?? "").join(" · "),
      outcome: null,
      mark: "",
      runCount: launched.get(s.chat_id ?? "") ?? 0,
      session: s,
    });
  }
  for (const r of runs) {
    if (runOpenHere(r)) {
      continue;
    }
    // The outcome is stated whatever launched the run: an agent-parented run's transcript may be gone.
    const endReason = r.end_reason ?? "";
    const status = classifyRunStatus(r.status);
    rows.push({
      key: `r:${r.workflow_id}`,
      kind: "run",
      title: r.name === "" ? "Untitled run" : r.name,
      updatedAt: r.updated_at,
      sub: runSub(r, endReason),
      outcome: runVerdict(status, endReason),
      mark: runMark(status, endReason, runPendingAsks(r.workflow_id).count > 0),
      runCount: 0,
      run: r,
    });
  }
  rows.sort((a, b) => b.updatedAt - a.updatedAt);
  return rows;
}

/** The unit is the conversation on both axes. */
const NOUNS: Nouns = {
  match: { one: "conversation", many: "conversations" },
  scanned: { one: "conversation", many: "conversations" },
};

const RUN_NOUNS: Nouns = {
  match: { one: "run", many: "runs" },
  scanned: { one: "run", many: "runs" },
};

const SEARCH_DEBOUNCE_MS = 250;

// Deduped, like git-tabs.ts: re-selecting the active pane does not re-swap panels.
const activeTab = signal<HistoryTab>("chats");

/**
 * Set the active pane without pushing a URL, for the router on back/forward to /history/<tab>. Safe before the tab
 * exists.
 */
export function forceHistoryTab(tab: HistoryTab): void {
  setHistoryTabRoute(tab);
  activeTab.value = tab;
}

function selectTab(tab: HistoryTab): void {
  if (tab === activeTab.peek()) {
    return;
  }
  setHistoryTabRoute(tab);
  pushRoute({ kind: "history", tab });
  activeTab.value = tab;
}

function chatsContainer(): HTMLElement | null {
  return document.getElementById("history-table");
}

function runsContainer(): HTMLElement | null {
  return document.getElementById("history-runs");
}

class HistoryController {
  private abort: AbortController | null = null;
  /**
   * The Chats pane's search popup. Closing clears the query, or a hidden box leaves the list narrowed unexplained. No
   * case toggle: `GET /api/chats/search` reads no `case` parameter.
   */
  readonly search: SearchPopup = createSearchPopup<null>({
    id: "hist-search",
    // A search, so the magnifier: the server reads every chat file, beyond the loaded list.
    kind: "search",
    label: "Search conversations",
    placeholder: "Search conversations\u2026",
    note: true,
    // The scan can read every chat file, so the pause is longer than the shell's default.
    debounceMs: SEARCH_DEBOUNCE_MS,
    host: () => document.getElementById("history-view"),
    query: (q) => {
      this.query = q;
      return null;
    },
    render: () => {
      void this.refresh();
    },
  });
  private query = "";

  /** A filter over the loaded list, so the funnel: there is no server-side run search. */
  readonly filter: SearchPopup = createSearchPopup<null>({
    id: "hist-filter",
    kind: "filter",
    label: "Filter workflow runs",
    placeholder: "Filter runs by name or recipe\u2026",
    note: true,
    host: () => document.getElementById("history-view"),
    query: (q) => {
      this.filterText = q.toLowerCase();
      return null;
    },
    render: () => {
      this.paintRuns();
    },
  });
  private filterText = "";

  /** Written by the constructor's effect, never by `load` directly. */
  private rows: HistoryRow[] = [];

  /** Kept through a failed reload, so only the first load paints the skeleton. */
  private answered = false;

  /**
   * Null while no valid answer is held. A signal, so the effect re-deriving the rows on the dock's queue and the tab
   * set also runs on each new answer.
   */
  private readonly verdict = signal<SessionListResponse | null>(null);

  constructor() {
    // A run's mark reads the dock's queue and every row reads the tab set, and neither change emits `runs:changed`, so a
    // plain rebuild would leave a working mark over a parked run, or a closed tab's chat and runs missing.
    effect(() => {
      const d = this.verdict.value;
      if (d === null) {
        return undefined;
      }
      tabSetVersion();
      this.rows = toRows(d.sessions, d.runs);
      this.paintChats(d);
      this.paintRuns();
      return undefined;
    });
  }

  teardown(): void {
    loadSessions.cancel();
    searchChats.cancel();
    this.abort?.abort();
    this.abort = null;
    // A null verdict stops the effect re-deriving rows for a closed page; `load()` re-sets it.
    this.verdict.value = null;
    // reset(), not close(): close's clear would repaint, a fetch no reader needs.
    this.query = "";
    this.search.reset();
    this.filterText = "";
    this.filter.reset();
  }

  /** The Chats pane shows the list or search, by the box; the Runs pane is the list either way. */
  async refresh(): Promise<void> {
    if (this.query === "") {
      this.setNote("");
      await this.load();
      return;
    }
    await this.runSearch(this.query);
  }

  private setNote(text: string): void {
    this.search.shell?.setNote(text);
  }

  private async runSearch(q: string): Promise<void> {
    const container = chatsContainer();
    if (container === null) {
      return;
    }
    loadSessions.cancel();
    this.abort?.abort();
    this.abort = new AbortController();
    const { signal } = this.abort;

    const res = await searchChats.dispatch(q);
    // A newer keystroke superseded this one, or the box was cleared.
    if (signal.aborted || this.query !== q) {
      return;
    }
    if (res === null) {
      this.setNote(emptyNote({ kind: "failed" }, NOUNS));
      return;
    }
    if (res.matches.length === 0) {
      // An unread chat must be stated, or an empty result implies the text is nowhere.
      this.setNote(
        emptyNote(
          classify({
            matched: res.matched,
            shown: 0,
            scanned: res.scanned,
            truncated: res.truncated,
          }),
          NOUNS,
        ),
      );
      container.replaceChildren(
        el("div", { className: "list-empty" }, "No matching conversations."),
      );
      return;
    }
    this.setNote(scanNote(res, res.matches.length, NOUNS));
    container.replaceChildren(...res.matches.map((m) => buildMatchRow(m, q)));
  }

  /** The Chats pane only while no search is open. */
  async load(): Promise<void> {
    const chats = chatsContainer();
    const runs = runsContainer();
    if (chats === null || runs === null) {
      return;
    }
    loadSessions.cancel();
    this.abort?.abort();
    this.abort = new AbortController();
    const { signal } = this.abort;

    const skeleton = this.answered
      ? null
      : skeletonTiming(() => {
          const drops = [showSkeleton(runs)];
          if (this.query === "") {
            drops.push(showSkeleton(chats));
          }
          return () => {
            for (const drop of drops) {
              drop();
            }
          };
        });
    const d = await loadSessions.dispatch(undefined);
    skeleton?.cancel();
    if (signal.aborted) {
      return;
    }
    // No misleading empty state on failure: offer a retry. The held answer goes too, or the next tab-set, dock or
    // filter change repaints it over the Retry.
    if (d === null) {
      this.verdict.value = null;
      runs.replaceChildren(this.buildError());
      if (this.query === "") {
        chats.replaceChildren(this.buildError());
      }
      return;
    }
    this.answered = true;
    this.verdict.value = d;
  }

  /** Only while no search is open: the search owns the pane then. */
  private paintChats(d: SessionListResponse): void {
    const chats = chatsContainer();
    if (chats === null || this.query !== "") {
      return;
    }
    this.paintPane(
      chats,
      this.rows.filter((r) => r.kind === "chat"),
      d.sessions_state === "unavailable"
        ? "Could not read previous conversations."
        : "No previous conversations in this workspace.",
    );
  }

  private paintRuns(): void {
    const runs = runsContainer();
    const d = this.verdict.peek();
    if (runs === null || d === null) {
      return;
    }
    const all = this.rows.filter((r) => r.kind === "run");
    const shown =
      this.filterText === "" ? all : all.filter((r) => matchesFilter(r, this.filterText));
    this.filter.shell?.setNote(this.runsNote(all.length, shown.length));
    this.paintPane(
      runs,
      shown,
      this.filterText !== ""
        ? "No workflow runs match the filter."
        : d.runs_state === "unavailable"
          ? "Could not read workflow runs."
          : "No previous workflow runs in this workspace.",
    );
  }

  /** Silent with no filter: a count restating the list is noise. */
  private runsNote(total: number, shown: number): string {
    if (this.filterText === "") {
      return "";
    }
    if (shown > 0) {
      return scanNote({ scanned: total, matched: shown, truncated: false }, shown, RUN_NOUNS);
    }
    return emptyNote(
      classify({ matched: 0, shown: 0, scanned: total, truncated: false }),
      RUN_NOUNS,
    );
  }

  private paintPane(container: HTMLElement, rows: HistoryRow[], emptyText: string): void {
    // Drop any non-keyed sibling before reconcile.
    for (const child of [...container.children]) {
      if (child.getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }
    if (rows.length === 0) {
      container.replaceChildren(el("div", { className: "list-empty" }, emptyText));
      return;
    }
    const refresh = (): void => {
      void this.refresh();
    };
    reconcile(container, rows, {
      key: (r) => r.key,
      mount: (r) => {
        const node = buildRow(r, refresh);
        sigChanged(node, [wireSignature(r)]);
        return node;
      },
      // Kept by key, repainted in place behind a signature, so a settled run gets its verdict without losing place or focus.
      update: (node, r) => {
        if (!sigChanged(node, [wireSignature(r)])) {
          return;
        }
        const fresh = buildRow(r, refresh);
        node.replaceChildren(...fresh.childNodes);
        const outcome = fresh.dataset["outcome"];
        if (outcome === undefined) {
          delete node.dataset["outcome"];
        } else {
          node.dataset["outcome"] = outcome;
        }
      },
    });
  }

  private buildError(): HTMLElement {
    const retry = el("button", { type: "button", className: "btn-small" }, "Retry");
    retry.addEventListener("click", () => {
      void this.load();
    });
    return el(
      "div",
      { className: "list-empty history-error" },
      el("span", {}, "Could not load previous sessions. Check your connection and try again."),
      retry,
    );
  }
}

/** The filter reads what the row shows: title and subtitle. */
function matchesFilter(row: HistoryRow, needle: string): boolean {
  return `${row.title} ${row.sub}`.toLowerCase().includes(needle);
}

/** `onGone` runs when the conversation was deleted since the fetch, so the caller refreshes. */
function openRow(row: HistoryRow, onGone: () => void): void {
  if (row.kind === "run" && row.run !== undefined) {
    // The parent chat nests the run's tab. Parentlessness is not passed: the composition root resolves it from the run store.
    void openRunView(row.run.workflow_id, row.title, row.run.parent_chat_id ?? "");
    return;
  }
  if (row.session !== undefined) {
    void openPreviousSession(row.session).then((outcome) => {
      if (outcome === "gone") {
        onGone();
      }
    });
  }
}

/**
 * Neither kind is recoverable. A chat runs `delete_chat` (chat file, its runs, its KAS session chain); a run runs
 * `_kiro/workflow/delete` (cancels if moving, then the run directory, lease, timer, end reason). True when gone.
 */
async function deleteRow(row: HistoryRow): Promise<boolean> {
  const label = row.kind === "run" ? "run" : "conversation";
  const n = row.runCount;
  const runs =
    n === 0
      ? ""
      : ` It also deletes the ${n === 1 ? "workflow run" : `${String(n)} workflow runs`} it started.`;
  const ok = await confirm(
    `Delete this ${label}? "${row.title}" and its stored history are removed for good.${runs}`,
    "Delete",
    "destructive",
  );
  if (!ok) {
    return false;
  }
  if (row.kind === "run" && row.run !== undefined) {
    return (await deleteRun.dispatch(row.run.workflow_id)) !== null;
  }
  const chatID = row.session?.chat_id ?? "";
  if (chatID === "") {
    return false;
  }
  // The tab is not closed here: the membership coordinator closes tabs for a deleted chat under the same lock.
  return (await deleteChatAction.dispatch(chatID)) !== null;
}

function buildRow(row: HistoryRow, refresh: () => void): HTMLElement {
  const del = buildDeleteButton(row, refresh);
  const node = entryRow({
    key: row.key,
    title: row.title,
    lead: buildLead(row),
    time: row.updatedAt > 0 ? { ms: row.updatedAt } : undefined,
    sub: row.sub !== "" ? { kind: "line", text: row.sub } : undefined,
    actions: del === null ? undefined : [del],
    open: {
      name: row.title,
      onOpen: () => {
        openRow(row, refresh);
      },
    },
  });
  if (row.outcome !== null) {
    // One writer for the vocabulary (tool-card.ts), so a run row and a tool card cannot spell a verdict differently.
    const openBtn = node.querySelector<HTMLElement>("button.entry-open") ?? node;
    applyOutcome(node, row.outcome, `Open ${row.title}`, ROW_RENDER_INFO, openBtn);
  }
  return node;
}

/**
 * An empty slot for a run with nothing to state, so every Runs title starts at one offset; chats have no slot.
 * `.tool-icon` is the shared outcome vocabulary's DOM contract.
 */
function buildLead(row: HistoryRow): HTMLElement | undefined {
  if (row.kind !== "run") {
    return undefined;
  }
  if (row.mark !== "") {
    return el(
      "span",
      { className: "entry-lead" },
      el("span", { className: "entry-mark", "data-status": row.mark }),
    );
  }
  if (row.outcome !== null) {
    return el(
      "span",
      { className: "entry-lead" },
      el("span", { className: "tool-icon" }, iconEl(ICON_TAB_RUN)),
    );
  }
  return el("span", { className: "entry-lead" });
}

/**
 * Null while a run moves: the run delete cancels a live run first, so the trash would be a stop control the confirm
 * cannot describe. Withheld, since a control that can only be declined is worse than none.
 */
function buildDeleteButton(row: HistoryRow, refresh: () => void): HTMLElement | null {
  if (row.mark !== "") {
    return null;
  }
  return el(
    "button",
    {
      type: "button",
      className: "icon-btn entry-delete",
      "data-history-delete": row.key,
      "aria-label": `Delete ${row.title}`,
      "data-tooltip": "Delete",
      onclick: () => {
        void deleteRow(row).then((gone) => {
          if (gone) {
            refresh();
          }
        });
      },
    },
    iconEl(ICON_TRASH),
  );
}

// Search is a second mode, not a filter: the list is the newest N sessions while search reads every chat file.

/**
 * Hands the reader to the chat's own find, stepped to the best hit. The tab opens first, awaited: the switch clears
 * the transcript's box. A chat that did not open gets no find.
 */
async function openMatch(match: Match, query: string): Promise<void> {
  const outcome = await openChatTab(match.id, match.name);
  if (outcome === "opened" && match.best !== undefined) {
    openChatFindAt(query, match.best);
  }
}

/** The hit count borrows the time slot as text: a count has no absolute form. */
function buildMatchRow(m: Match, query: string): HTMLElement {
  // A title-only match has no hit: say why it matched.
  const detail = m.best?.excerpt ?? "matches the conversation name";
  const more = m.hits > 1 ? `${String(m.hits)} matches` : m.hits === 1 ? "1 match" : "";
  return entryRow({
    key: `m:${m.id}`,
    title: m.name,
    sub: { kind: "line", text: detail },
    time: more !== "" ? { text: more } : undefined,
    open: {
      name: m.name,
      onOpen: () => {
        void openMatch(m, query);
      },
    },
    data: { "data-search-chat": m.id },
  });
}

/** `[data-key]`, not the reconciler's attribute: a search's rows carry only this page's key. */
function showSkeleton(container: HTMLElement): () => void {
  return paintPlaceholder(
    container,
    () => el("div", { className: "skeleton-rows", "aria-hidden": "true" }, ...entrySkeleton(4)),
    { content: "[data-key]" },
  );
}

const historyCtrl = new HistoryController();
registerCleanup(() => {
  historyCtrl.teardown();
});

/**
 * Routed to the active pane, as git.ts does. Handed to the dispatcher, not imported by it: this module is lazy and
 * must stay off the boot path.
 */
const historyFind: PageFind = {
  open: () => activeFind().open(),
  toggle: () => {
    activeFind().toggle();
  },
  focused: () => activeFind().focused(),
  kind: () => activeFind().kind(),
};

function activeFind(): SearchPopup {
  // The reactive read, so the toolbar's affordance effect re-runs on a pane switch.
  return activeTab.value === "runs" ? historyCtrl.filter : historyCtrl.search;
}

let inited = false;

function initHistoryView(): void {
  if (inited) {
    return;
  }
  inited = true;
  const paint = initSegmentedBar($.historyTabBar, {
    attr: "data-history-tab",
    idPrefix: "history",
    tabs: HISTORY_TABS.map((id) => ({ id, label: TAB_LABELS[id] })),
    onSelect: selectTab,
  });
  subscribe(activeTab, (tab) => {
    paint(tab);
    swapViews(() => {
      let active: HTMLElement | null = null;
      // The panel half of the pairing the controller's `aria-controls` writes.
      for (const panel of document.querySelectorAll<HTMLDivElement>("[data-history-panel]")) {
        const panelTab = panel.dataset["historyPanel"] ?? "";
        const isActive = panelTab === tab;
        panel.classList.toggle("hidden", !isActive);
        panel.setAttribute("role", "tabpanel");
        panel.id = `history-panel-${panelTab}`;
        panel.setAttribute("aria-labelledby", `history-tab-${panelTab}`);
        if (isActive) {
          active = panel;
        }
      }
      return active;
    });
    setPageSubtitle("history", TAB_LABELS[tab]);
  });
}

/**
 * The history tab's activation: wire the bar once and register the page's find. No fetch: `tabs.ts` calls
 * `refreshHistoryView` next.
 */
export function loadHistoryView(): void {
  initHistoryView();
  registerFind("history", historyFind);
}

/** A history tab's `refresh`. */
export function refreshHistoryView(): void {
  void historyCtrl.refresh();
}

/** Cancel the page's in-flight work; the restore path passes this as its tab's `onClose`. */
export function teardownHistoryView(): void {
  historyCtrl.teardown();
}

// A run starting or finishing changes the Runs pane. Gated on the view being on screen.
onBus(BUS_RUNS_CHANGED, () => {
  const view = document.getElementById("history-view");
  if (view !== null && view.offsetParent !== null) {
    void historyCtrl.load();
  }
});
