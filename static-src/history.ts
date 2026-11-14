// Previous chats and workflow runs from KAS, on two panes (`/history`, `/history/runs`). marotte archives nothing: a
// picker over `GET /api/sessions`. A chat row opens its chat or adopts it via `resume_session`; a run row opens the
// read-only run view.

import { computed, el, signal, subscribe, effect } from "@cplieger/reactive";
import { skeletonTiming } from "./legacy-ui/skeleton.js";
import { $, maybeEl, queryAll } from "./dom.js";
import {
  activeChatRef,
  descendantChatsOf,
  hasTab,
  historyPaneTab,
  setHistoryPane,
  tabSetVersion,
} from "./tabs.js";
import type { TabRunDotStatus } from "./tabs.js";
import type { HistoryTab } from "./route-path.js";
import { onBus, BUS_RUNS_CHANGED } from "./bus.js";
import { engineRead } from "./engine-read.js";
import { reconcile } from "./reconcile.js";
import { paintPlaceholder } from "./skeleton.js";
import { creditFigure } from "./strings.js";
import { swapViews } from "./view-swap.js";
import { initSegmentedBar } from "./segmented-bar.js";
import { setPageSubtitle } from "./page-title.js";
import { entryRow, entrySkeleton } from "./entry-row.js";
import { sigChanged, wireSignature } from "./paint-sig.js";
import { loadSessions } from "./actions/chat.js";
import { registerCleanup, retryNetwork } from "./actions/index.js";
import { serverImpactOf } from "./actions/close-impact.js";
import { deleteStopsWork } from "./close-busy.js";
import { actionNotice } from "./notice-subject.js";
import { openPreviousSession, openChatTab } from "./chat.js";
import { searchChats } from "./actions/chat-search.js";
import { openChatFindAt } from "./find-in-chat.js";
import { CHAT_SEARCH_DEBOUNCE_MS } from "./chat-search.js";
import { parseChatQuery } from "./chat-query.js";
import { classify, emptyNote, scanNote } from "./textsearch/copy.js";
import type { Nouns } from "./textsearch/copy.js";
import type { CloseImpact, Match, RunStandingKind } from "./wire/types.gen.js";
import { openRunView } from "./run-view.js";
import { runChatID } from "./run-store.js";
import { runPendingAsks } from "./decision-dock.js";
import { deleteRun } from "./actions/runs.js";
import { deleteChat as deleteChatAction } from "./actions/chat.js";
import { confirm } from "./confirm.js";
import { labelForMode } from "./roles.js";
import { applyOutcome } from "./tool-card.js";
import type { ToolRenderInfo } from "./tool-schema.js";
import { ICON_TAB_RUN, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import { senderWords } from "./sent-by.js";
import { createSearchPopup } from "./search-popup.js";
import type { SearchPopup } from "./search-popup.js";
import { registerFind } from "./find-registry.js";
import type { PageFind } from "./find-registry.js";
import type { ResumableSession, SessionListResponse, WorkflowRun } from "./types.js";
import { classifyRunStatus, runStatusTerminal, type ClassifiedRunStatus } from "./run-status.js";

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
 * The row's second line when the agent declared no description: the owning chat's facts as `/api/sessions` states
 * them. Never the client's open chats, which hold only the chats with a tab and so lose a chat the moment it closes.
 */
function chatFacts(s: ResumableSession): string[] {
  const facts: string[] = [];
  if ((s.model ?? "") !== "") {
    facts.push(s.model ?? "");
  }
  if ((s.current_mode_id ?? "") !== "") {
    facts.push(labelForMode(s.current_mode_id ?? ""));
  }
  const turns = s.turn_count ?? 0;
  if (turns > 0) {
    facts.push(`${String(turns)} ${turns === 1 ? "turn" : "turns"}`);
  }
  // Credits only when metered: 0.00 on every unmetered row is noise.
  const credits = s.credits ?? 0;
  if (credits > 0) {
    facts.push(`${creditFigure(credits)} cr`);
  }
  return facts;
}

/** A live run's position, in the run tab's words; the node id is shown as the run tab labels it. */
const STANDING_TEXT: Readonly<Record<RunStandingKind, (node: string) => string>> = {
  step: (node) => `at ${node}`,
  watch: (node) => `waiting on ${node}`,
  pass_limit: (node) => `${node} reached its pass limit`,
};

/** What marotte did when a bound stopped it, the recipe when the label differs, where a live run stands, a
 *  finished run's duration, and the launching chat. */
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
  if (r.standing !== undefined) {
    parts.push(STANDING_TEXT[r.standing.kind](r.standing.node));
  }
  // From creation, not `started_at`: KAS restamps that each time it resumes a run, which would drop every
  // stretch before the last resume. Finished runs only: a live row's `updated_at` is its last (re)start.
  const status = classifyRunStatus(r.status);
  const ended = status !== undefined && runStatusTerminal(status);
  const started = r.started_at === undefined ? 0 : (r.created_at ?? r.started_at);
  if (ended && started > 0 && r.updated_at > started) {
    parts.push(formatDuration(r.updated_at - started));
  }
  const parent = r.parent_chat_name ?? "";
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
  overran: "stopped: its recipe's next scheduled slot came due",
  stalled: "stopped: it showed no activity for too long",
  orphaned: "stopped: the server restarted while it was running",
  unattended: "stopped: it paused with nobody to resume it",
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
  toolName: "",
  writesFile: false,
  filePath: "",
  fileBasename: "",
  diffSources: null,
  mcp: null,
  disclosed: null,
  loaded: null,
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
      sub: description !== "" ? description : chatFacts(s).join(" · "),
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

/** Search-as-you-type waits for this many characters: under three the trigram filter cannot narrow the scan, which
 *  then reads every chat. Enter searches any length. */
const SEARCH_MIN_RUNES = 3;

/** Whether typing alone leaves `q` to Enter: no filter, and no word long enough to give the server's prefilter a
 *  trigram (each word counts on its own, as `queryTrigrams` reads them). */
function underFloor(q: string): boolean {
  const pq = parseChatQuery(q);
  if (pq.file !== "" || pq.tool !== "" || pq.turn > 0) {
    return false;
  }
  return !pq.terms.some((t) => t.words.some((w) => Array.from(w).length >= SEARCH_MIN_RUNES));
}

/** The note while the typed text is under the floor. */
const UNDER_FLOOR_NOTE = "Type 3 characters, or press Enter to search";

/** The pane History shows, null while it is not on screen. */
const shownTab = computed(() => historyPaneTab());

function chatsContainer(): HTMLElement | null {
  return maybeEl("history-table");
}

function runsContainer(): HTMLElement | null {
  return maybeEl("history-runs");
}

/** A note shown in place of a pane's rows; a list item, since both panes are ARIA lists. */
function paneNote(className: string, ...children: (Node | string)[]): HTMLElement {
  return el("div", { className, role: "listitem" }, ...children);
}

class HistoryController {
  private abort: AbortController | null = null;
  /** Closing clears the query, or a hidden box leaves the list narrowed unexplained. No case toggle:
   *  `GET /api/chats/search` reads no `case` parameter. */
  readonly search: SearchPopup = createSearchPopup<null>({
    id: "hist-search",
    // A search, so the magnifier: the server reads every chat file, beyond the loaded list.
    kind: "search",
    label: "Search conversations",
    placeholder: "Search conversations\u2026",
    note: true,
    // Find in chat's pause: the same words find the same chat on both surfaces, typed the same way.
    debounceMs: CHAT_SEARCH_DEBOUNCE_MS,
    host: () => maybeEl("history-view"),
    query: (q, ctx) => {
      const short = !ctx.submitted && q.trim() !== "" && underFloor(q);
      this.underFloor = short;
      this.query = short ? "" : q;
      return null;
    },
    render: () => {
      void this.refresh();
    },
  });
  private query = "";
  /** The typed text is under `SEARCH_MIN_RUNES` and the pause, not Enter, ran it: the list shows, with the hint. */
  private underFloor = false;

  /** A search by name and recipe over the loaded runs. History has one find kind on both panes, so the
   *  toolbar button keeps its glyph and its name when the pane switches. */
  readonly runsFind: SearchPopup = createSearchPopup<null>({
    id: "hist-run-search",
    kind: "search",
    label: "Search workflow runs",
    placeholder: "Search runs by name or recipe\u2026",
    note: true,
    host: () => maybeEl("history-view"),
    query: (q) => {
      this.runQuery = q.toLowerCase();
      return null;
    },
    render: () => {
      this.paintRuns();
    },
  });
  private runQuery = "";

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

  /** History left the screen: drop its reads and stop following the dock; the boxes keep their text for the next
   *  show, which reads again. */
  suspend(): void {
    loadSessions.cancel();
    searchChats.cancel();
    this.abort?.abort();
    this.abort = null;
    historyRead.reset();
    // A null verdict stops the effect re-deriving rows off screen; `load()` re-sets it.
    this.verdict.value = null;
  }

  teardown(): void {
    this.suspend();
    // reset(), not close(): close's clear would repaint, a fetch no reader needs.
    this.query = "";
    this.underFloor = false;
    this.search.reset();
    this.runQuery = "";
    this.runsFind.reset();
  }

  /** The Chats pane shows the list or search, by the box; the Runs pane is the list either way. */
  async refresh(): Promise<void> {
    if (this.query === "") {
      this.setNote(this.underFloor ? UNDER_FLOOR_NOTE : "");
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
      container.replaceChildren(paneNote("list-empty", "No matching conversations."));
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
    const settle = historyRead.begin();
    const d = await loadSessions.dispatch(undefined);
    skeleton?.cancel();
    if (signal.aborted) {
      settle(null);
      return;
    }
    // No misleading empty state on failure: offer a retry. The held answer goes too, or the next tab-set, dock or
    // query change repaints it over the Retry.
    if (d === null) {
      this.verdict.value = null;
      runs.replaceChildren(this.buildError());
      if (this.query === "") {
        chats.replaceChildren(this.buildError());
      }
      settle(false);
      return;
    }
    this.answered = true;
    this.verdict.value = d;
    settle(d.sessions_state !== "unavailable" && d.runs_state !== "unavailable");
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
    const shown = this.runQuery === "" ? all : all.filter((r) => matchesRunQuery(r, this.runQuery));
    this.runsFind.shell?.setNote(this.runsNote(all.length, shown.length));
    this.paintPane(
      runs,
      shown,
      this.runQuery !== ""
        ? "No workflow runs match the search."
        : d.runs_state === "unavailable"
          ? "Could not read workflow runs."
          : "No previous workflow runs in this workspace.",
    );
  }

  /** Silent with no query: a count restating the list is noise. */
  private runsNote(total: number, shown: number): string {
    if (this.runQuery === "") {
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
      container.replaceChildren(paneNote("list-empty", emptyText));
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
    return paneNote(
      "list-empty history-error",
      el("span", {}, "Could not load previous sessions. Check your connection and try again."),
      retry,
    );
  }
}

/** A run is found by what its row shows: title and subtitle. */
function matchesRunQuery(row: HistoryRow, needle: string): boolean {
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
 * Neither kind is recoverable. A chat runs `delete_chat` (chat file, its runs, its KAS session chain, and the chat tabs
 * under its tabs, whose work the confirm names); a run runs `_kiro/workflow/delete` (cancels if moving, then the run
 * directory, lease, timer, end reason). True when gone.
 */
async function deleteRow(row: HistoryRow): Promise<boolean> {
  const label = row.kind === "run" ? "run" : "conversation";
  const n = row.runCount;
  const runs =
    n === 0
      ? ""
      : ` It also deletes the ${n === 1 ? "workflow run" : `${String(n)} workflow runs`} it started.`;
  const chatID = row.kind === "run" ? "" : (row.session?.chat_id ?? "");
  const stops = deleteStopsWork(chatID === "" ? [] : descendantChatsOf(chatID));
  const ok = await confirm({
    title: `Delete this ${label}?`,
    body: `"${row.title}" and its stored history are removed for good.${runs}${
      stops.text === "" ? "" : ` ${stops.text}`
    }`,
    confirmLabel: "Delete",
    variant: "destructive",
  });
  if (!ok) {
    return false;
  }
  if (row.kind === "run" && row.run !== undefined) {
    return (await deleteRun.dispatch(row.run.workflow_id)) !== null;
  }
  if (chatID === "") {
    return false;
  }
  return deleteChatAsked(chatID, row.title, stops.impact);
}

/** Send the delete with what the confirm named; a stale confirm (another device changed what the delete stops in
 *  the chats under its tabs) asks again on the merge, and each answer is a person's, so the loop needs no cap.
 *  The tab is not closed here: the membership coordinator closes tabs for a deleted chat under the same lock. */
async function deleteChatAsked(
  chatID: string,
  title: string,
  named: CloseImpact,
): Promise<boolean> {
  let impact = named;
  for (;;) {
    const args = { id: chatID, impact };
    const outcome = await deleteChatAction.dispatch(args).outcome;
    if (outcome.status !== "error") {
      return outcome.status === "success";
    }
    const server = serverImpactOf(outcome.error);
    if (server === undefined) {
      if (outcome.error.code !== "cancelled") {
        const retry = retryNetwork(outcome.error)
          ? {
              onClick: () => {
                void deleteChatAction.dispatch(args);
              },
            }
          : undefined;
        actionNotice(chatID, `Could not delete chat: ${outcome.error.message}`, "error", retry);
      }
      return false;
    }
    const again = deleteStopsWork(descendantChatsOf(chatID), { sent: impact, server });
    const yes = await confirm({
      title: `Delete "${title}" anyway?`,
      body: `What deleting it also stops changed.${again.text === "" ? "" : ` ${again.text}`}`,
      confirmLabel: "Delete",
      variant: "destructive",
    });
    if (!yes) {
      return false;
    }
    impact = again.impact;
  }
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

/** The tab opens first, awaited: the switch clears the transcript's box. A chat that did not open,
 *  or that the reader left for another tab meanwhile, gets no find. */
async function openMatch(match: Match, query: string): Promise<void> {
  const outcome = await openChatTab(match.id, match.name);
  if (outcome === "opened" && match.best !== undefined && activeChatRef() === match.id) {
    openChatFindAt(query, match.best);
  }
}

/** The hit count borrows the time slot as text: a count has no absolute form. */
function buildMatchRow(m: Match, query: string): HTMLElement {
  // A title-only match has no hit: say why it matched. Words the reader did not send name their sender first.
  const best = m.best;
  const sender = senderWords(best);
  const line =
    best === undefined
      ? "matches the conversation name"
      : sender === undefined
        ? best.excerpt
        : `${sender}: ${best.excerpt}`;
  // No one message holds every word: the row says so before the line from one of them.
  const detail = m.spread ? `Words spread across messages: ${line}` : line;
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
  return shownTab.value === "runs" ? historyCtrl.runsFind : historyCtrl.search;
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
    onSelect: setHistoryPane,
  });
  subscribe(shownTab, (tab) => {
    if (tab === null) {
      historyCtrl.suspend();
      return;
    }
    paint(tab);
    swapViews(() => {
      let active: HTMLElement | null = null;
      // The panel half of the pairing the controller's `aria-controls` writes.
      for (const panel of queryAll<HTMLDivElement>("[data-history-panel]")) {
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
 * History's show: wire the bar once and register the page's find. No fetch: `tabs.ts` calls `refreshHistoryView`
 * next.
 */
export function loadHistoryView(): void {
  initHistoryView();
  registerFind("history", historyFind);
}

/** Fetch History's lists again: on each show and each invalidation while it shows. */
export function refreshHistoryView(): void {
  void historyCtrl.refresh();
}

function historyShowing(): boolean {
  const view = maybeEl("history-view");
  return view !== null && view.offsetParent !== null;
}

const historyRead = engineRead(() => {
  void historyCtrl.load();
}, historyShowing);

// A run starting or finishing changes the Runs pane. Gated on the view being on screen.
onBus(BUS_RUNS_CHANGED, () => {
  if (historyShowing()) {
    void historyCtrl.load();
  }
});
