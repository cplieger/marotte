// ---------------------------------------------------------------------------
// History: previous chats and previous workflow runs, both sourced from KAS, on
// two panes behind one segmented bar (`/history` and `/history/runs`). marotte
// archives nothing: this is a picker over `GET /api/sessions`. A CHAT row opens
// its marotte chat (`chat_id`) or is adopted first via `resume_session`; a RUN
// row is not a session, so it opens the read-only run view.
// ---------------------------------------------------------------------------

import { el, signal, subscribe, effect } from "@cplieger/reactive";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
import { $ } from "./dom.js";
import { hasTab, setHistoryTab as setHistoryTabRoute } from "./tabs.js";
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

/** A chat row and a run row share the list, so they share a shape. Every field
 *  the row RENDERS is here, which is what makes `wireSignature(row)` a total
 *  repaint guard by construction. */
interface HistoryRow {
  /** Reconcile key. Prefixed per kind so a session and a run can never collide. */
  key: string;
  kind: "chat" | "run";
  title: string;
  updatedAt: number;
  /** The one subtitle line, already joined. */
  sub: string;
  /** The verdict this row states as a glyph, or null when there is none to
   *  state: any chat, and a run that is still moving or carries a status this
   *  client has no verdict for. */
  outcome: RunVerdict | null;
  /** The workflow mark for a run still moving, `""` for a settled run and for
   *  every chat. `input` is an ask the dock holds for this run, read the way
   *  every other mark surface reads it (`runPendingAsks`). */
  mark: TabRunDotStatus | "";
  session?: ResumableSession;
  run?: WorkflowRun;
}

/** The facts line for a chat row, read from the STORE rather than added to
 *  `/api/sessions`: `/api/chats` already carries every chat's header (model, mode,
 *  usage, message count) and KAS's session row carries none of it, so the join
 *  costs no request and no wire field. A chat the store does not know yields an
 *  EMPTY list rather than placeholders: "unknown model · 0 turns" would be a lie. */
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
  // Credits only when metered: a 0.00 on every unmetered row is noise, and this
  // is the one number that answers "what did that conversation cost".
  if (s.usage.credits > 0) {
    facts.push(`${s.usage.credits.toFixed(2)} cr`);
  }
  return facts;
}

/** A run row's subtitle: what marotte did to it when a bound stopped it, the
 *  recipe when the label is not already the recipe, how long it took, and the
 *  conversation that launched it when the store holds that chat. */
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
  const started = r.started_at ?? 0;
  if (started > 0 && r.updated_at > started) {
    parts.push(formatDuration(r.updated_at - started));
  }
  const parent = get(r.parent_chat_id ?? "")?.name ?? "";
  if (parent !== "") {
    parts.push(`from ${parent}`);
  }
  return parts.join(" · ");
}

/** A coarse duration, because the reader wants an order of magnitude rather than
 *  a stopwatch: seconds under a minute, minutes under an hour, then hours. */
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

/** The run statuses that carry a verdict. Two are `ToolStatus` members and the
 *  third is the member `applyOutcome` was widened by, so these pass straight
 *  into the shared outcome vocabulary with no translation table here. */
type RunVerdict = "completed" | "failed" | "aborted";

/** How a bounded termination reads. The keys are the server's vocabulary
 *  (marotte.WorkflowRun.EndReason); the sentences are the reader's.
 *
 *  A run stopped by one of marotte's own bounds is the one ending KAS's status
 *  cannot describe: both bounds terminate through the same cancel a person uses,
 *  so the status is `aborted` for a backstop and for a click alike. This is where
 *  the difference is stated. */
const END_REASON_TEXT: Readonly<Record<string, string>> = {
  overran: "stopped: it ran past its time limit",
  step_cap: "stopped: a step ran past its turn limit",
  orphaned: "stopped: the server restarted while it was running",
};

/** A run's verdict, or null for a run with none to state: a live run's liveness
 *  is the workflow mark's, and an unknown status is never guessed into a green
 *  check. A RECOGNISED `end_reason` OUTRANKS the status: a bound cancels the run,
 *  so KAS reports `aborted` at best and `running` while the frame is in flight,
 *  and a row reading as moving for a run marotte already stopped is the lie the
 *  field exists to remove. Recognised rather than non-empty, so one vocabulary
 *  decides both the sentence and the verdict. */
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
      // A user stop reads as the stop a bound produces; `RunVerdict` carries one.
      return "aborted";
    case undefined:
    case "running":
    case "paused":
    case "unknown":
      return null;
  }
}

/** The workflow mark for a run that is still moving, `""` otherwise. Written the
 *  way `run-bar.ts` writes it, so 12-tabs.css paints one look for one run.
 *
 *  A recognised `end_reason` outranks everything, as in `runVerdict`: a bound
 *  already stopped that run, so a `running` status and a queued ask are both frames
 *  yet to land. An ask outranks the status (`runStatusFor`'s precedence): KAS leaves
 *  an asking run `running`, so the status alone paints a parked run as working. */
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

/** What `applyOutcome` needs from a caller that has no tool call behind it, the
 *  same stub shape `messages-tools.ts` passes on its own DOM-only path. Two
 *  fields reach the output and both matter: an empty `fileBasename` keeps the
 *  accessible name to the row's own label, and a null `denial` keeps the row
 *  from being repainted as a policy refusal. */
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

/** Whether a session's chat is open in a tab ON THIS DEVICE.
 *
 *  Ownership is the server's fact and travels as `chat_id`; "open here" is this
 *  device's, held in localStorage, which is why the predicate lives on the
 *  client and reuses the tab store's own `hasTab` rather than a second one. A
 *  chat tab's id IS its chat id, so no mapping is needed. */
function isOpenHere(s: ResumableSession): boolean {
  const chatID = s.chat_id ?? "";
  return chatID !== "" && hasTab("chat", chatID);
}

function toRows(sessions: ResumableSession[], runs: WorkflowRun[]): HistoryRow[] {
  const rows: HistoryRow[] = [];
  for (const s of sessions) {
    // A chat open here is not history: its tab is one click away, so a row would be
    // a second door to the room the user is in. An owned-but-CLOSED session stays.
    // Filtered here so the dropped key never reaches reconcile.
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
      session: s,
    });
  }
  for (const r of runs) {
    // A run's OUTCOME is stated whatever launched it: the server lists an
    // agent-parented run because a closed or evicted transcript leaves it no other
    // door, and a blank outcome gives the reader no reason to open this one.
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
      run: r,
    });
  }
  rows.sort((a, b) => b.updatedAt - a.updatedAt);
  return rows;
}

/** This search's unit is the conversation on both axes: a match IS a chat, and
 *  the scan reads chats. */
const NOUNS: Nouns = {
  match: { one: "conversation", many: "conversations" },
  scanned: { one: "conversation", many: "conversations" },
};

const RUN_NOUNS: Nouns = {
  match: { one: "run", many: "runs" },
  scanned: { one: "run", many: "runs" },
};

/** Debounce so a search is per-pause, not per-keystroke. */
const SEARCH_DEBOUNCE_MS = 250;

// Deduped, like git-tabs.ts: a same-value write is a no-op, so re-selecting the
// active pane does not re-swap panels.
const activeTab = signal<HistoryTab>("chats");

/** Set the active pane without pushing a URL — the router's entry point when
 *  back/forward lands on /history/<tab>. Safe before the tab exists: the route
 *  sync is a no-op then and `openTab` sets the route directly. */
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
  /** The Chats pane's search box, the transcript's popup (search-popup.ts) reached
   *  by the toolbar's magnifier and Ctrl-F. Closing it clears the query: a hidden
   *  box holding `redis` would leave the page showing three of forty conversations
   *  with nothing on screen saying why. NO match-case toggle, because
   *  `GET /api/chats/search` reads no `case` parameter (`chat.searchOneChat` owns
   *  that decision); a toggle here would be wired to nothing. */
  readonly search: SearchPopup = createSearchPopup<null>({
    id: "hist-search",
    // A SEARCH, so it carries the magnifier: the server reads every chat file on
    // disk, so this box finds conversations the loaded list does not contain.
    kind: "search",
    label: "Search conversations",
    placeholder: "Search conversations\u2026",
    note: true,
    // The scan can read every chat file on disk, so the pause is longer than
    // the shell's default: a search is per-pause here, not per-keystroke.
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

  /** The Runs pane's box: a FILTER over the loaded list, so it carries the
   *  funnel. There is no server-side run search to promise more than that. */
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

  /** The rows of the last answered `/api/sessions`, both kinds, newest first.
   *  Written by `derive` below, never by `load` directly. */
  private rows: HistoryRow[] = [];

  /** Whether `/api/sessions` has ANSWERED. No chats and no runs is an answer, and the
   *  container cannot tell it from a list this client has never read. */
  private answered = false;

  /** The last answer. A signal, because the rows derive from it AND from the dock's
   *  queue (the effect below); a plain field would leave the second input unwatched. */
  private readonly verdict = signal<SessionListResponse | null>(null);

  constructor() {
    // An effect, because a run row's mark reads the dock's queue (`runPendingAsks`)
    // and an ask arriving or answered emits no `runs:changed`: a plain rebuild would
    // hold a working mark over a parked run until the next fetch.
    effect(() => {
      const d = this.verdict.value;
      if (d === null) {
        return undefined;
      }
      this.rows = toRows(d.sessions, d.runs);
      this.paintRuns();
      return undefined;
    });
  }

  teardown(): void {
    loadSessions.cancel();
    searchChats.cancel();
    this.abort?.abort();
    this.abort = null;
    // A null verdict is what stops the effect above re-deriving rows for a closed
    // page on every dock change; `load()` re-sets it when the page reopens.
    this.verdict.value = null;
    // reset() rather than close(): the close's clear repaints the full list, and
    // that repaint is a fetch this page no longer has a reader for.
    this.query = "";
    this.search.reset();
    this.filterText = "";
    this.filter.reset();
  }

  /** Route the Chats pane to the list or to search, depending on the box. The
   *  Runs pane is the list either way. */
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

  /** Render matching CHATS for the current query. */
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
    // A newer keystroke already superseded this one, or the box was cleared.
    if (signal.aborted || this.query !== q) {
      return;
    }
    if (res === null) {
      this.setNote(emptyNote({ kind: "failed" }, NOUNS));
      return;
    }
    if (res.matches.length === 0) {
      // An unread chat must be stated: otherwise an empty result implies the
      // text is nowhere, when one of the chats could not be read.
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

  /** Fetch both lists and paint both panes — the Chats pane only while no search
   *  is open, because a search is what that pane shows then. */
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
    // Don't paint a misleading empty state on failure — offer a retry.
    if (d === null) {
      runs.replaceChildren(this.buildError());
      if (this.query === "") {
        chats.replaceChildren(this.buildError());
      }
      return;
    }
    this.answered = true;
    // The effect derives `rows` synchronously, so the chats paint below reads THIS answer's.
    this.verdict.value = d;
    if (this.query === "") {
      this.paintPane(
        chats,
        this.rows.filter((r) => r.kind === "chat"),
        d.sessions_state === "unavailable"
          ? "Couldn't read previous conversations."
          : "No previous conversations in this workspace.",
      );
    }
  }

  /** The Runs pane, from the rows in hand, narrowed by the filter box. */
  private paintRuns(): void {
    const runs = runsContainer();
    if (runs === null || !this.answered) {
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
        : this.verdict.peek()?.runs_state === "unavailable"
          ? "Couldn't read workflow runs."
          : "No previous workflow runs in this workspace.",
    );
  }

  /** Silent with no filter (a count restating the list is noise). */
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
    // Drop any non-keyed sibling (skeleton / empty / error / a search's rows)
    // before reconcile.
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
      // A reload keeps the element by key, so its content is repainted IN PLACE
      // behind a signature: a run that settled since the last read gets its mark
      // replaced by its verdict without the row losing its place or its focus.
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
      el("span", {}, "Couldn't load previous sessions. Check your connection and try again."),
      retry,
    );
  }
}

/** The filter reads what the row shows: its title and its subtitle line. */
function matchesFilter(row: HistoryRow, needle: string): boolean {
  return `${row.title} ${row.sub}`.toLowerCase().includes(needle);
}

/** Open a history row: a chat resumes, a run opens its read-only review.
 *
 *  `onGone` runs when the row's conversation turned out to be DELETED — the
 *  retention-off close erased it after this list was fetched — so the caller
 *  refreshes and the dead row drops out of the server-derived list. */
function openRow(row: HistoryRow, onGone: () => void): void {
  if (row.kind === "run" && row.run !== undefined) {
    // The parent chat nests the run's tab under its conversation rather than at the
    // end of the strip. Whether the RUN is parentless is not passed: that is the
    // run's own fact, resolved from the run store by the composition root.
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

/** Delete a history row and the state behind it, after confirming; neither kind is
 *  recoverable. A CHAT row runs `delete_chat`, which removes the chat file AND
 *  reaps every KAS session in the chat's chain; a RUN row runs
 *  `_kiro/workflow/delete`, which cancels a moving run and then removes its run
 *  directory plus marotte's lease, timer and end reason. Returns true when the row
 *  is gone, so the caller can refresh rather than wait for a poll. */
async function deleteRow(row: HistoryRow): Promise<boolean> {
  const label = row.kind === "run" ? "run" : "conversation";
  const ok = await confirm(
    `Delete this ${label}? "${row.title}" and its stored history are removed for good.`,
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
  // The TAB is not closed here: the membership coordinator closes every tab for a
  // deleted chat under the same lock that removes the record, and emits the
  // removal. A `close_tab` from here would be a second close for a tab the server
  // has already dropped.
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
    // ONE writer for the vocabulary: tint, shape and word all come from
    // tool-card.ts, so a run row and a tool card cannot spell the same verdict
    // differently. The name lands on the open control, because that is the
    // control a reader reaches.
    const openBtn = node.querySelector<HTMLElement>("button.entry-open") ?? node;
    applyOutcome(node, row.outcome, `Open ${row.title}`, ROW_RENDER_INFO, openBtn);
  }
  return node;
}

/** The leading slot: the workflow mark while a run moves, the outcome glyph once
 *  it has settled, and an EMPTY slot for a run with neither to state, so every
 *  title in the Runs pane starts at one offset. A chat row has no slot at all.
 *
 *  `.tool-icon` is the DOM contract of the shared outcome vocabulary, and the
 *  run's own icon is the identity glyph `applyOutcome` captures and tints. */
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

/** The row's delete control, or null while a run is still moving: `deleteRow`'s
 *  run arm CANCELS a live run before it removes the directory, so on a live row the
 *  trash would be a stop control the confirm cannot describe, and stopping a run
 *  belongs to the run page. Withheld rather than refused in `deleteRow`, because a
 *  control whose click can only be declined is worse than none. */
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

// Cross-chat search: a SECOND mode over the Chats pane, not a filter of the loaded
// list, because the list is the newest N sessions while search reads every chat
// file, so filtering what is on screen would answer a narrower question than the
// box appears to ask. Opening a match hands over to that chat's own find.

/** Open a matched conversation and hand the reader to its own find, carrying the
 *  query and stepped to the best hit — so the count the row showed is reachable
 *  rather than a number to retype. The tab first, awaited: the switch closes and
 *  clears the transcript's box, so the handoff has to run after it. A chat that
 *  did not open (deleted since the search) gets no find, and a title-only match
 *  has no hit to step to. */
async function openMatch(match: Match, query: string): Promise<void> {
  const outcome = await openChatTab(match.id, match.name);
  if (outcome === "opened" && match.best !== undefined) {
    openChatFindAt(query, match.best);
  }
}

/** A match in the same row shape as a session: the hit count borrows the time
 *  slot as TEXT, because a count has no absolute form to put in a tooltip. */
function buildMatchRow(m: Match, query: string): HTMLElement {
  // A title-only match carries no best hit: say why it matched instead of
  // rendering an empty line.
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

/** Skeleton rows while the fetch is in flight. `[data-key]` rather than the
 *  reconciler's attribute: this page writes its own key beside it, and a search's
 *  rows carry only that one. */
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

/** This page's find, routed to the ACTIVE pane — git.ts's shape for the same
 *  question: a page with panes has one search affordance and it belongs to
 *  whatever is on screen. Handed to the dispatcher rather than imported by it:
 *  this module is lazily loaded (it pulls chat.ts in behind it) and the dispatcher
 *  must not put it on the boot path. */
const historyFind: PageFind = {
  open: () => activeFind().open(),
  toggle: () => {
    activeFind().toggle();
  },
  focused: () => activeFind().focused(),
  kind: () => activeFind().kind(),
};

function activeFind(): SearchPopup {
  // The REACTIVE read, so the toolbar's affordance effect re-runs on a pane
  // switch. Outside an effect it is an ordinary read.
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

/** The history tab's ACTIVATION: wire the bar once and register the page's find.
 *  No fetch here — `tabs.ts` `refreshRow` calls `refreshHistoryView` right after. */
export function loadHistoryView(): void {
  initHistoryView();
  registerFind("history", historyFind);
}

/** A history tab's `refresh`. */
export function refreshHistoryView(): void {
  void historyCtrl.refresh();
}

/** Cancel the page's in-flight work. The restore path passes this as its tab's
 *  `onClose`; the module-level cleanup above covers app teardown, not a close. */
export function teardownHistoryView(): void {
  historyCtrl.teardown();
}

// A run starting or finishing changes the Runs pane, and a workflow with twenty
// steps would otherwise leave a stale row until the user reopened the page.
// Gated on the view being on screen, so this never becomes a background fetch
// for a page nobody is looking at.
onBus(BUS_RUNS_CHANGED, () => {
  const view = document.getElementById("history-view");
  if (view !== null && view.offsetParent !== null) {
    void historyCtrl.load();
  }
});
