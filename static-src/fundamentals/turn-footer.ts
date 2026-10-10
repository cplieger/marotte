// Fundamental: TurnFooter — the turn card's outcome ledger. Three depths: the
// aggregate row answers "did it work", the per-file rows answer "what changed",
// a row's click answers "let me look". Tint and glyph come from the shared
// severity table (turn-severity.ts), never from a per-outcome rule here.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { copyClipboard } from "../actions/messages.js";
import { iconEl } from "../icon-el.js";
import { contextBreakdownRows } from "../context-breakdown.js";
import { ASK_LABEL, type AskBucket } from "../interaction.js";
import { ICON_INFO } from "../icons.js";
import { openAtLine, openChange, openChangeSet } from "../navigate.js";
import { sigChanged } from "../paint-sig.js";
import { formatElapsed } from "../strings.js";
import { kindNoun } from "../tool-kind-noun.js";
import { severityOf } from "../turn-severity.js";
import type { FileChange, ToolKind, TurnThroughput } from "../types.js";
import type { ContextBreakdown } from "../wire/types.gen.js";
import { COMMAND_KINDS, type TurnOutcome } from "../turns.js";
import { relBeneath, workspaceRoot } from "../workspace.js";

// Mounted on a turn card (`messages.ts`) and a DELEGATE card (`subagent-block.ts`), so every
// panel field is optional; Cost, Model and Diagnostics always withhold on a delegate.

/** The word the ledger line LEADS with, TOTAL over `TurnOutcome`. `completed` (the absence of a
 *  mark is the clean case) and `running` (29-turns.css hides it) carry none; the other six say
 *  their name in the ROW, since touch has no hover. */
const OUTCOME_LEAD: Record<TurnOutcome, string> = {
  running: "",
  completed: "",
  cancelled: "Cancelled",
  interrupted: "Interrupted",
  failed: "Failed",
  refused: "Refused",
  unknown: "Outcome unknown",
  empty: "Empty",
};

/** The per-turn summary inputs, sourced from turn metadata on the message. */
export interface TurnSummaryData {
  credits?: number;
  elapsedMs?: number;
  changedFiles?: Record<string, FileChange>;
  /** The model(s) that answered, distinct and in order. Rendered as one name
   *  normally and `a -> b` when a switch split the turn. Absent on every turn
   *  persisted before the field existed. */
  models?: string[];
  /** The turn's result, carried as the footer's tint so outcome is scannable
   *  down the transcript without reading a word. */
  outcome?: TurnOutcome;
  /** The info panel's facts, mirroring `TurnLedger`, which owns each field's absence rule. A section
   *  withholds on absence and never invents a zero. */
  toolMs?: number;
  kindCounts?: Partial<Record<ToolKind, number>>;
  delegateCount?: number;
  delegateMs?: number;
  startedAt?: number;
  endedAt?: number;
  stopReasonRaw?: string;
  truncated?: boolean;
  /** How the turn's asks were answered; no entry is no ask of that kind. */
  asks?: Partial<Record<AskBucket, number>>;
  /** `turn_close`'s KAS facts, verbatim. Recoveries are KAS's mechanism names, an
   *  open vocabulary; steering is document ids, file URIs for documents on disk. */
  requestIds?: string[];
  throughput?: TurnThroughput | undefined;
  recoveries?: string[];
  steering?: string[];
  engineErrorClass?: string;
  /** What the turn's last model request carried (`turn_close.context_breakdown`). */
  contextBreakdown?: ContextBreakdown | undefined;
}

/** KAS's recovery mechanism names (`AgentExecutionRecovered`), in words. A name this
 *  table does not know is shown verbatim. */
const RECOVERY_PHRASE: Readonly<Record<string, string>> = {
  empty: "retried an empty response",
  truncation: "retried a truncated response",
  streamError: "retried after a stream error",
  authExpiry: "retried after sign-in expired",
  strip: "retried after a thinking-signature error",
  stallToolContinuation: "continued a stalled tool call",
};

function recoveryPhrase(m: string): string {
  return Object.hasOwn(RECOVERY_PHRASE, m) ? (RECOVERY_PHRASE[m] ?? m) : m;
}

const ASK_ORDER: readonly AskBucket[] = [
  "allowed",
  "always_allowed",
  "rejected",
  "answered",
  "skipped",
];

/** Reasons a footer is earned that are NOT in the summary data, passed by the
 *  consumer that can see them. Both are turn-card facts, so the delegate card
 *  passes neither. */
export interface FooterExtras {
  /** This turn is a rewind target, so the footer carries a Rewind control. */
  rewindable?: boolean;
  /** This turn has settled prose, so the footer carries the turn ACTIONS
   *  (copy / source / export) that act on it. */
  settledProse?: boolean;
}

/** Whether the footer is earned, READING `turnFacts`: a fact for the row, the outcome WORD, or a
 *  caller-mounted CONTROL, so an earned footer with nothing on it is unrepresentable. */
export function earnsTurnFooter(d: TurnSummaryData, extra: FooterExtras = {}): boolean {
  return (
    turnFacts(d).length > 0 ||
    OUTCOME_LEAD[d.outcome ?? "completed"] !== "" ||
    extra.rewindable === true ||
    extra.settledProse === true
  );
}

/** Build the footer element (empty until updateTurnFooter fills it). */
export function buildTurnFooter(d: TurnSummaryData): HTMLDivElement {
  const footer = el("div", {
    className: "turn-footer",
    role: "note",
    "aria-label": "Turn summary",
  }) as HTMLDivElement;

  const summary = el("button", {
    className: "turn-ledger-summary",
    type: "button",
  }) as HTMLButtonElement;
  // An `i`, LEADING, with the PURPOSE span first so the name opens with it. No `aria-label`: it would
  // hide the outcome word and fact. The tip anchors to the `i`'s ink.
  summary.appendChild(el("span", { className: "sr-only" }, "Turn details"));
  summary.appendChild(
    el("span", { className: "turn-ledger-info", "data-tooltip-anchor": "" }, iconEl(ICON_INFO)),
  );
  summary.appendChild(el("span", { className: "turn-ledger-glyph" }));
  summary.appendChild(el("span", { className: "turn-ledger-text" }));
  summary.appendChild(el("span", { className: "turn-fact" }));
  summary.addEventListener("click", () => {
    setInfoOpen(footer, !infoOpen(footer));
  });
  footer.appendChild(summary);

  footer.appendChild(el("div", { className: "turn-info-panel" }));

  updateTurnFooter(footer, d);
  return footer;
}

/** Recompute the footer from turn metadata. Idempotent, and preserves an
 *  expanded file list across repaints. */
export function updateTurnFooter(footer: HTMLElement, d: TurnSummaryData): void {
  const outcome = d.outcome ?? "completed";
  footer.dataset["outcome"] = outcome;
  // TWO attributes, two questions, one writer each. `data-outcome` carries the WORDS
  // (OUTCOME_LEAD above); `data-severity` carries hue, from the shared table rather than
  // from a per-outcome colour rule the stylesheet had to keep in step by hand.
  footer.dataset["severity"] = severityOf(outcome);

  const files = Object.entries(d.changedFiles ?? {});
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  const text = footer.querySelector<HTMLElement>(
    ":scope > .turn-ledger-summary > .turn-ledger-text",
  );
  if (text !== null) {
    text.textContent = summaryLine(d);
  }

  const slot = footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary > .turn-fact");
  if (slot !== null) {
    const facts = turnFacts(d);
    slot.hidden = facts.length === 0;
    slot.textContent = facts[0] ?? "";
  }

  // ALWAYS a disclosure, so `aria-expanded` is written unconditionally: every turn
  // that earned a footer at all has a panel to open.
  if (summary !== null) {
    summary.setAttribute("aria-expanded", infoOpen(footer) ? "true" : "false");
    syncLedgerTooltip(footer);
  }

  const panel = footer.querySelector<HTMLElement>(":scope > .turn-info-panel");
  if (panel !== null) {
    renderInfoPanel(panel, d, files);
  }
}

/** The ledger LINE: the outcome's lead word only. No `?? ""`: OUTCOME_LEAD is total, so an absent
 *  outcome defaults its KEY instead. */
function summaryLine(d: TurnSummaryData): string {
  return OUTCOME_LEAD[d.outcome ?? "completed"];
}

/** The turn's facts, most important first; zero or absent contributes nothing. The ROW PAINTS
 *  `[0]`. Do NOT rotate the slot: a time-gated value fails WCAG 2.2.2 with no pause control. */
export function turnFacts(d: TurnSummaryData): readonly string[] {
  const facts: string[] = [];
  const files = Object.values(d.changedFiles ?? {});
  if (files.length > 0) {
    let added = 0;
    let removed = 0;
    for (const fc of files) {
      added += fc.lines_added;
      removed += fc.lines_removed;
    }
    let fact = `${String(files.length)} ${files.length === 1 ? "file" : "files"}`;
    if (added > 0) {
      fact += ` +${String(added)}`;
    }
    if (removed > 0) {
      fact += ` \u2212${String(removed)}`;
    }
    facts.push(fact);
  }
  // `kindCounts` is the ONLY count on the wire: it carries the whole walk at the
  // finest grain, so no aggregate sits beside it.
  const kinds = sortedKinds(d.kindCounts ?? {});
  for (const [kind, n] of kinds) {
    if (COMMAND_KINDS.has(kind)) {
      facts.push(`${String(n)} ${kindNoun(kind, n)}`);
    }
  }
  const delegates = d.delegateCount ?? 0;
  if (delegates > 0) {
    facts.push(`${String(delegates)} ${delegates === 1 ? "delegate" : "delegates"}`);
  }
  for (const [kind, n] of kinds) {
    if (!COMMAND_KINDS.has(kind)) {
      facts.push(`${String(n)} ${kindNoun(kind, n)}`);
    }
  }
  const wall = d.elapsedMs ?? 0;
  if (wall > 0) {
    facts.push(formatElapsed(wall));
  }
  const credits = d.credits ?? 0;
  if (credits > 0) {
    facts.push(`${creditFigure(credits)} credits`);
  }
  return facts;
}

/** A metered turn's credits. `toFixed(2)` alone renders a sub-cent charge as `0.00`, contradicting
 *  the `> 0` gate. */
function creditFigure(credits: number): string {
  return credits < 0.005 ? "<0.01" : credits.toFixed(2);
}

/** One `label · value` row. `<li>` because every section's rows are a list. */
function infoRow(label: string, value: string | Node): HTMLElement {
  return el(
    "li",
    { className: "turn-info-row" },
    el("span", { className: "turn-info-label" }, label),
    el("span", { className: "turn-info-value" }, value),
  );
}

/** Withholding is the panel's whole discipline: a delegate can fill three of the six and a
 *  mid-flight turn fewer, so a section that painted itself on absence would grow empty rows on most
 *  cards. */
function infoSection(title: string, rows: HTMLElement[], extra: Node[] = []): HTMLElement | null {
  if (rows.length === 0 && extra.length === 0) {
    return null;
  }
  const section = el("section", { className: "turn-info-section" });
  section.appendChild(el("h4", { className: "turn-info-title" }, title));
  section.append(...extra);
  if (rows.length > 0) {
    section.appendChild(el("ul", { className: "turn-info-rows" }, ...rows));
  }
  return section;
}

/** A wall-clock STAMP as a `<time>`: the machine-readable instant in `datetime`, the
 *  reader's own locale in the text. Same pair, from the same value, as `turn-header.ts`
 *  writes, so the panel's "Started" and the header's timestamp cannot disagree. */
function stampEl(ms: number): HTMLElement {
  const when = new Date(ms);
  const t = el(
    "time",
    {},
    when.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" }),
  );
  t.setAttribute("datetime", when.toISOString());
  return t;
}

/** MODEL TIME is withheld rather than clamped (overlapping tool calls make wall minus tool
 *  negative). */
function timingRows(d: TurnSummaryData): HTMLElement[] {
  const rows: HTMLElement[] = [];
  const wall = d.elapsedMs ?? 0;
  const tool = d.toolMs ?? 0;
  if (wall > 0) {
    rows.push(infoRow("Wall clock", formatElapsed(wall)));
  }
  if (tool > 0) {
    rows.push(infoRow("Tool time", formatElapsed(tool)));
  }
  if (tool > 0 && wall > tool) {
    rows.push(infoRow("Model time", formatElapsed(wall - tool)));
  }
  if ((d.startedAt ?? 0) > 0) {
    rows.push(infoRow("Started", stampEl(d.startedAt ?? 0)));
  }
  // WITHHELD while running: a live turn's `endedAt` is its last body message's `ts` and would paint
  // the START time as "Ended". Keyed on the OUTCOME, like Diagnostics below.
  if ((d.endedAt ?? 0) > 0 && (d.outcome ?? "completed") !== "running") {
    rows.push(infoRow("Ended", stampEl(d.endedAt ?? 0)));
  }
  return rows;
}

/** One row per non-zero tool kind, named through the shared noun vocabulary. Sorted by
 *  count then kind, so two repaints cannot reshuffle the rows: `kindCounts` is built in
 *  arrival order, which is not a fact about the turn worth showing. */
function kindRows(counts: Partial<Record<ToolKind, number>>): HTMLElement[] {
  return sortedKinds(counts).map(([kind, n]) => infoRow(kindNoun(kind, n), String(n)));
}

/** The non-zero kinds in the panel's order, read by the panel's rows and by the row's
 *  facts alike so the two surfaces cannot disagree about it. */
function sortedKinds(counts: Partial<Record<ToolKind, number>>): [ToolKind, number][] {
  const entries = Object.entries(counts) as [ToolKind, number][];
  return entries.filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** The eight sections, in order, each withheld when it has nothing to state. Rebuilt
 *  wholesale but only when the data moved: `updateTurnFooter` runs at chunk cadence on a
 *  live turn, and every file row is a button with a tooltip that opens a diff. */
function renderInfoPanel(
  panel: HTMLElement,
  d: TurnSummaryData,
  files: [string, FileChange][],
): void {
  if (!sigChanged(panel, panelSignature(d))) {
    return;
  }
  const sections: HTMLElement[] = [];

  const timings = infoSection("Timings", timingRows(d));
  if (timings !== null) {
    sections.push(timings);
  }

  // The file rows keep their own `<ul class="turn-ledger-files">`, nested here
  // inside Work: `renderFileRows`, `fileRow` and `reviewRow` all address it.
  const kinds = kindRows(d.kindCounts ?? {});
  const list = el("ul", { className: "turn-ledger-files" });
  renderFileRows(list, files);
  const work = infoSection("Work", kinds, files.length > 0 ? [list] : []);
  if (work !== null) {
    sections.push(work);
  }

  const askRows = ASK_ORDER.filter((b) => (d.asks?.[b] ?? 0) > 0).map((b) =>
    infoRow(ASK_LABEL[b], String(d.asks?.[b] ?? 0)),
  );
  const approvals = infoSection("Approvals", askRows);
  if (approvals !== null) {
    sections.push(approvals);
  }

  const delegateRows: HTMLElement[] = [];
  if ((d.delegateCount ?? 0) > 0) {
    delegateRows.push(infoRow("Dispatched", String(d.delegateCount ?? 0)));
  }
  if ((d.delegateMs ?? 0) > 0) {
    delegateRows.push(infoRow("Time", formatElapsed(d.delegateMs ?? 0)));
  }
  const delegates = infoSection("Delegates", delegateRows);
  if (delegates !== null) {
    sections.push(delegates);
  }

  const steering = d.steering ?? [];
  const contextRows =
    steering.length > 0 ? [infoRow("Steering added", steeringList(steering))] : [];
  if (d.contextBreakdown !== undefined) {
    contextRows.push(...contextBreakdownRows(d.contextBreakdown, infoRow, documentName));
  }
  const context = infoSection("Context", contextRows);
  if (context !== null) {
    sections.push(context);
  }

  const cost = infoSection(
    "Cost",
    (d.credits ?? 0) > 0 ? [infoRow("Credits", creditFigure(d.credits ?? 0))] : [],
  );
  if (cost !== null) {
    sections.push(cost);
  }

  // Arrow when a mid-turn switch split the turn, which is the one case where the
  // plural matters: two names in order say the turn changed hands.
  const models = d.models ?? [];
  const modelRows: HTMLElement[] = [];
  if (models.length > 0) {
    modelRows.push(infoRow("Answered by", models.join(" \u2192 ")));
  }
  const requests = d.requestIds ?? [];
  if (requests.length > 0) {
    modelRows.push(infoRow("Requests", String(requests.length)));
  }
  const output = outputEstimate(d.throughput);
  if (output !== "") {
    modelRows.push(infoRow("Output, estimated", output));
  }
  const model = infoSection("Model", modelRows);
  if (model !== null) {
    sections.push(model);
  }

  // Only on a turn that did not end clean (`running` has not ended). The raw reason is VERBATIM and
  // never branched on: the wire declares that enum OPEN.
  const outcomeNow = d.outcome ?? "completed";
  const unclean = outcomeNow !== "completed" && outcomeNow !== "running";
  const diagRows: HTMLElement[] = [];
  if (unclean && (d.stopReasonRaw ?? "") !== "") {
    diagRows.push(infoRow("Stop reason", d.stopReasonRaw ?? ""));
  }
  if (unclean && d.truncated === true) {
    diagRows.push(infoRow("Truncated", "yes"));
  }
  // Every row below is stated on ANY turn: a clean turn can have needed a recovery, and
  // a request id is what a support case asks for whatever the outcome.
  for (const m of d.recoveries ?? []) {
    diagRows.push(infoRow("Recovered", recoveryPhrase(m)));
  }
  if ((d.engineErrorClass ?? "") !== "") {
    diagRows.push(infoRow("Engine error", d.engineErrorClass ?? ""));
  }
  if (requests.length > 0) {
    diagRows.push(infoRow("Request IDs", requestIDList(requests)));
  }
  const diagnostics = infoSection("Diagnostics", diagRows);
  if (diagnostics !== null) {
    sections.push(diagnostics);
  }

  panel.replaceChildren(...sections);
}

/** Everything the panel renders, as signature parts. Total over `TurnSummaryData` by
 *  TYPE, which is what a signature needs: a field added to that interface fails the type
 *  check here rather than leaving a panel that stops updating. */
function panelSignature(d: TurnSummaryData): string[] {
  const num = (n: number | undefined): string => (n === undefined ? "" : String(n));
  const parts: Record<keyof TurnSummaryData, string> = {
    credits: num(d.credits),
    elapsedMs: num(d.elapsedMs),
    changedFiles: filesSignature(d.changedFiles),
    models: join(...(d.models ?? [])),
    outcome: d.outcome ?? "",
    toolMs: num(d.toolMs),
    kindCounts: join(...sortedKinds(d.kindCounts ?? {}).flatMap(([k, n]) => [k, String(n)])),
    delegateCount: num(d.delegateCount),
    delegateMs: num(d.delegateMs),
    startedAt: num(d.startedAt),
    endedAt: num(d.endedAt),
    stopReasonRaw: d.stopReasonRaw ?? "",
    truncated: d.truncated === true ? "1" : "",
    asks: join(...ASK_ORDER.map((b) => num(d.asks?.[b]))),
    requestIds: join(...(d.requestIds ?? [])),
    throughput:
      d.throughput === undefined
        ? ""
        : join(String(d.throughput.estimated_tokens), String(d.throughput.active_streaming_ms)),
    recoveries: join(...(d.recoveries ?? [])),
    steering: join(...(d.steering ?? [])),
    engineErrorClass: d.engineErrorClass ?? "",
    // A decoded wire record, so the whole of it is the signature.
    contextBreakdown: d.contextBreakdown === undefined ? "" : JSON.stringify(d.contextBreakdown),
  };
  // The root decides which steering ids link (`steeringList`).
  return [...Object.values(parts), workspaceRoot()];
}

/** `≈N tokens · ≈N tokens/s`, or "" when KAS estimated nothing. The rate needs time
 *  chunks were actually arriving, so it is withheld without one. */
function outputEstimate(t: TurnThroughput | undefined): string {
  if (t === undefined || t.estimated_tokens <= 0) {
    return "";
  }
  const tokens = `\u2248${Math.round(t.estimated_tokens).toLocaleString()} tokens`;
  if (t.active_streaming_ms <= 0) {
    return tokens;
  }
  const rate = Math.round(t.estimated_tokens / (t.active_streaming_ms / 1000));
  return `${tokens} \u00b7 \u2248${rate.toLocaleString()} tokens/s`;
}

/** The steering KAS added: a workspace document links into the editor, which
 *  cannot open a global one (`~/.kiro`), so any other id is plain text. */
function steeringList(ids: readonly string[]): HTMLElement {
  const list = el("span", { className: "turn-info-list" });
  for (const id of ids) {
    const path = filePathOf(id);
    list.appendChild(documentName(path?.slice(path.lastIndexOf("/") + 1) ?? id, id));
  }
  return list;
}

/** A document's name, a button into the editor when `uri` is a file under the workspace. */
function documentName(name: string, uri: string): HTMLElement {
  const path = filePathOf(uri);
  const root = workspaceRoot();
  if (path === null || root === "" || relBeneath(root, path) === null) {
    return el("span", {}, name);
  }
  const btn = el(
    "button",
    { className: "turn-info-link", type: "button", "data-tooltip": path },
    name,
  );
  btn.addEventListener("click", () => {
    openAtLine(path);
  });
  return btn;
}

/** The absolute path a `file:` URI names, or null for any other id. */
function filePathOf(id: string): string | null {
  if (!id.startsWith("file://")) {
    return null;
  }
  try {
    const path = decodeURIComponent(new URL(id).pathname);
    return path === "" ? null : path;
  } catch {
    return null;
  }
}

function requestIDList(ids: readonly string[]): HTMLElement {
  const list = el("span", { className: "turn-info-list turn-info-ids" });
  for (const id of ids) {
    list.appendChild(el("code", {}, id));
  }
  const copy = el(
    "button",
    { className: "btn-small turn-info-copy", type: "button" },
    ids.length === 1 ? "Copy" : "Copy all",
  );
  copy.addEventListener("click", () => {
    void copyClipboard.dispatch(ids.join("\n"));
  });
  list.appendChild(copy);
  return list;
}

/** SORTED by path, matching `renderFileRows`' own sort: a `Record`'s insertion order
 *  is the order the paths happened to arrive in, so an unsorted signature would move
 *  for a set that did not change and repaint the panel for nothing. */
function filesSignature(files: Record<string, FileChange> | undefined): string {
  return join(
    ...Object.entries(files ?? {})
      .sort((a, b) => a[0].localeCompare(b[0]))
      .flatMap(([path, fc]) => [
        path,
        String(fc.lines_added),
        String(fc.lines_removed),
        fc.is_new_file === true ? "1" : "",
      ]),
  );
}

/** One row per changed file: `path +N −M`, with a new-file badge. Every row
 *  opens that file's diff — the aggregate answers whether it worked, rows
 *  answer what changed, the click answers let me look. */
function renderFileRows(list: HTMLElement, files: [string, FileChange][]): void {
  // Sorted by path so a repaint cannot reshuffle rows under the cursor.
  const sorted = [...files].sort((a, b) => a[0].localeCompare(b[0]));
  const rows: HTMLElement[] = sorted.map(([path, fc]) => fileRow(path, fc));
  const review = reviewRow(sorted.length);
  if (review !== null) {
    rows.push(review);
  }
  list.replaceChildren(...rows);
}

function fileRow(path: string, fc: FileChange): HTMLElement {
  const item = el("li", { className: "turn-ledger-file" });
  // `data-tooltip`, not `title`: the styled tooltip system is what every other
  // hover in the app uses, and a UA tooltip beside it reads as foreign chrome.
  const btn = el("button", {
    className: "turn-file-row",
    type: "button",
    "data-tooltip": `Open the diff for ${path}`,
  }) as HTMLButtonElement;

  // The path is the row's ink AND what the hover text is about, so it is where the
  // tooltip points: the button spans the panel's width while its content sits at
  // the leading edge, which put the tip 264px to the right of the name.
  btn.appendChild(el("span", { className: "turn-file-path", "data-tooltip-anchor": "" }, path));
  if (fc.is_new_file === true) {
    btn.appendChild(el("span", { className: "turn-file-badge" }, "new"));
  }
  const delta = el("span", { className: "turn-file-delta" });
  if (fc.lines_added > 0) {
    delta.appendChild(el("span", { className: "turn-file-add" }, `+${String(fc.lines_added)}`));
  }
  if (fc.lines_removed > 0) {
    delta.appendChild(
      el("span", { className: "turn-file-del" }, `\u2212${String(fc.lines_removed)}`),
    );
  }
  btn.appendChild(delta);

  btn.addEventListener("click", () => {
    openChange(path);
  });
  item.appendChild(btn);
  return item;
}

/** `Review changes`: the multi-file seam, offered once per turn beneath the
 *  per-file rows. Appears only when the turn touched more than one file — a
 *  single-file turn's row above already opens that diff. */
function reviewRow(count: number): HTMLElement | null {
  if (count < 2) {
    return null;
  }
  const btn = el(
    "button",
    {
      className: "turn-review-all",
      type: "button",
      "data-tooltip": "Review every changed file in the git view",
    },
    `Review changes (${String(count)} files)`,
  ) as HTMLButtonElement;
  btn.addEventListener("click", () => {
    openChangeSet();
  });
  return el("li", { className: "turn-ledger-file turn-ledger-review" }, btn);
}

/** The attribute NAME is a three-surface fact — this writer, three selectors in
 *  `29-turns.css`, two hand-built test fixtures — and a half-finished rename fails
 *  SILENTLY: CSS keyed on an attribute nothing writes leaves the panel shut forever. */
function infoOpen(footer: HTMLElement): boolean {
  return footer.dataset["info"] === "open";
}

function setInfoOpen(footer: HTMLElement, on: boolean): void {
  if (on) {
    footer.dataset["info"] = "open";
  } else {
    delete footer.dataset["info"];
  }
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary !== null) {
    summary.setAttribute("aria-expanded", on ? "true" : "false");
  }
  syncLedgerTooltip(footer);
}

/** The trigger's hover text: ONE clause, what the click does. It names no outcome —
 *  OUTCOME_LEAD is total, so the row already says it, and a status whose only channel is
 *  a hover has no channel at all on a phone. */
function syncLedgerTooltip(footer: HTMLElement): void {
  const summary = footer.querySelector<HTMLButtonElement>(":scope > .turn-ledger-summary");
  if (summary === null) {
    return;
  }
  summary.setAttribute(
    "data-tooltip",
    infoOpen(footer) ? "Hide turn details" : "Show turn details",
  );
}
