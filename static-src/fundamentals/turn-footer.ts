// Fundamental: TurnFooter — the turn card's outcome ledger. Three depths: the
// aggregate row answers "did it work", the per-file rows answer "what changed",
// a row's click answers "let me look". Tint and glyph come from the shared
// severity table (turn-severity.ts), never from a per-outcome rule here.

import { el } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import { iconEl } from "../icon-el.js";
import { breakdownItems, requestLine } from "../context-breakdown.js";
import { slideBox, targetOpen } from "../disclosure-row.js";
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
import {
  occurrenceKeys,
  renderPanel,
  type ItemPart,
  type PanelItem,
  type PanelRow,
  type PanelValue,
} from "../turn-info-panel.js";
import { relBeneath, workspaceRoot } from "../workspace.js";

// Mounted on a turn card (`messages.ts`) and a DELEGATE card (`subagent-block.ts`), so every
// panel field is optional; Model and cost, Context and Diagnostics always withhold on a delegate.

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
    slideBox(footer, !targetOpen(footer, infoOpen(footer)), (on) => {
      setInfoOpen(footer, on);
    });
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

/** The six sections in task order. Each withholds when it has no row (`renderPanel`), so a delegate
 *  fills at most Changed files, Work and Time, and a mid-flight turn fewer. Repainted only when the
 *  data moved: `updateTurnFooter` runs at chunk cadence on a live turn. */
function renderInfoPanel(
  panel: HTMLElement,
  d: TurnSummaryData,
  files: [string, FileChange][],
): void {
  if (!sigChanged(panel, panelSignature(d))) {
    return;
  }
  renderPanel(panel, [
    { title: "Changed files", rows: fileRows(files) },
    { title: "Work", rows: workRows(d) },
    { title: "Time", rows: timingRows(d) },
    { title: "Model and cost", rows: modelRows(d) },
    { title: "Context", rows: contextRows(d) },
    { title: "Diagnostics", rows: diagnosticRows(d) },
  ]);
}

/** The non-zero kinds sorted by count then kind, read by the panel's rows and by the row's facts
 *  alike so the two surfaces cannot disagree. `kindCounts` is built in arrival order, which is not
 *  a fact about the turn, and a repaint must not reshuffle the rows. */
function sortedKinds(counts: Partial<Record<ToolKind, number>>): [ToolKind, number][] {
  const entries = Object.entries(counts) as [ToolKind, number][];
  return entries.filter(([, n]) => n > 0).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
}

/** Fact rows keyed by label, plus its occurrence for a label that repeats ("Recovered"). */
function facts(pairs: readonly [string, PanelValue][]): PanelRow[] {
  const keys = occurrenceKeys(pairs.map(([label]) => label));
  return pairs.map(([label, value], i) => ({ kind: "fact", key: keys[i] ?? label, label, value }));
}

/** Sorted by path so a repaint cannot reshuffle rows under the cursor; "Review all changes" only
 *  when there are two or more, since a single file's row already opens its diff. */
function fileRows(files: [string, FileChange][]): PanelRow[] {
  const sorted = [...files].sort((a, b) => a[0].localeCompare(b[0]));
  const rows: PanelRow[] = sorted.map(([path, fc]) => ({
    kind: "file",
    path,
    added: fc.lines_added,
    removed: fc.lines_removed,
    isNew: fc.is_new_file === true,
    open: () => {
      openChange(path);
    },
  }));
  if (sorted.length >= 2) {
    rows.push({ kind: "review", count: sorted.length, open: openChangeSet });
  }
  return rows;
}

/** A label names the kind and the value counts it, so the label is the plural whatever the count.
 *  `other`'s noun is "calls", which as a label needs its kind. */
function kindLabel(kind: ToolKind): string {
  const noun = kind === "other" ? "other calls" : kindNoun(kind, 2);
  return noun.charAt(0).toUpperCase() + noun.slice(1);
}

function workRows(d: TurnSummaryData): PanelRow[] {
  const pairs: [string, string][] = sortedKinds(d.kindCounts ?? {}).map(([kind, n]) => [
    kindLabel(kind),
    String(n),
  ]);
  const asks = ASK_ORDER.filter((b) => (d.asks?.[b] ?? 0) > 0).map(
    (b) => `${String(d.asks?.[b] ?? 0)} ${ASK_LABEL[b].toLowerCase()}`,
  );
  if (asks.length > 0) {
    pairs.push(["Approvals", asks.join(" \u00b7 ")]);
  }
  if ((d.delegateCount ?? 0) > 0) {
    pairs.push(["Delegates", String(d.delegateCount ?? 0)]);
  }
  if ((d.delegateMs ?? 0) > 0) {
    pairs.push(["Delegate time", formatElapsed(d.delegateMs ?? 0)]);
  }
  return facts(pairs);
}

/** MODEL TIME is withheld rather than clamped (overlapping tool calls make wall minus tool
 *  negative). */
function timingRows(d: TurnSummaryData): PanelRow[] {
  const pairs: [string, PanelValue][] = [];
  const wall = d.elapsedMs ?? 0;
  const tool = d.toolMs ?? 0;
  if (wall > 0) {
    pairs.push(["Wall clock", formatElapsed(wall)]);
  }
  if (tool > 0) {
    pairs.push(["Tool time", formatElapsed(tool)]);
  }
  if (tool > 0 && wall > tool) {
    pairs.push(["Model time", formatElapsed(wall - tool)]);
  }
  if ((d.startedAt ?? 0) > 0) {
    pairs.push(["Started", { stamp: d.startedAt ?? 0 }]);
  }
  // WITHHELD while running: a live turn's `endedAt` is its last body message's `ts` and would paint
  // the START time as "Ended". Keyed on the OUTCOME, like Diagnostics below.
  if ((d.endedAt ?? 0) > 0 && (d.outcome ?? "completed") !== "running") {
    pairs.push(["Ended", { stamp: d.endedAt ?? 0 }]);
  }
  return facts(pairs);
}

/** "Requests" is not a row: it is the count the Request IDs disclosure already shows. */
function modelRows(d: TurnSummaryData): PanelRow[] {
  const pairs: [string, string][] = [];
  // Arrow when a mid-turn switch split the turn: two names in order say the turn changed hands.
  const models = d.models ?? [];
  if (models.length > 0) {
    pairs.push(["Answered by", models.join(" \u2192 ")]);
  }
  if ((d.credits ?? 0) > 0) {
    pairs.push(["Credits", creditFigure(d.credits ?? 0)]);
  }
  const output = outputEstimate(d.throughput);
  if (output !== "") {
    pairs.push(["Output (estimated)", output]);
  }
  return facts(pairs);
}

function contextRows(d: TurnSummaryData): PanelRow[] {
  const rows: PanelRow[] = [];
  const b = d.contextBreakdown;
  if (b !== undefined) {
    rows.push(...facts([["Last request", requestLine(b)]]));
  }
  const steering = d.steering ?? [];
  if (steering.length > 0) {
    rows.push({
      kind: "list",
      label: "Added this turn",
      value: plural(steering.length, "steering document", "steering documents"),
      items: steeringItems(steering),
    });
  }
  if (b !== undefined && b.categories.length > 0) {
    rows.push({
      kind: "list",
      label: "Breakdown",
      value: plural(b.categories.length, "category", "categories"),
      items: breakdownItems(b, documentPart),
    });
  }
  return rows;
}

function plural(n: number, one: string, many: string): string {
  return `${String(n)} ${n === 1 ? one : many}`;
}

/** Only on a turn that did not end clean (`running` has not ended) for the stop reason and the
 *  truncation; the raw reason is VERBATIM and never branched on, since the wire declares that enum
 *  OPEN. Every other row is stated on ANY turn: a clean turn can have needed a recovery, and a
 *  request id is what a support case asks for whatever the outcome. */
function diagnosticRows(d: TurnSummaryData): PanelRow[] {
  const outcomeNow = d.outcome ?? "completed";
  const unclean = outcomeNow !== "completed" && outcomeNow !== "running";
  const pairs: [string, string][] = [];
  if (unclean && (d.stopReasonRaw ?? "") !== "") {
    pairs.push(["Stop reason", d.stopReasonRaw ?? ""]);
  }
  if (unclean && d.truncated === true) {
    pairs.push(["Truncated", "yes"]);
  }
  for (const m of d.recoveries ?? []) {
    pairs.push(["Recovered", recoveryPhrase(m)]);
  }
  if ((d.engineErrorClass ?? "") !== "") {
    pairs.push(["Engine error", d.engineErrorClass ?? ""]);
  }
  const rows = facts(pairs);
  const ids = d.requestIds ?? [];
  if (ids.length > 0) {
    const keys = occurrenceKeys(ids);
    rows.push({
      kind: "list",
      label: "Request IDs",
      value: String(ids.length),
      items: ids.map((id, i) => ({
        key: keys[i] ?? id,
        className: "turn-info-item",
        parts: [{ key: "id", sig: id, build: () => el("code", {}, id) }],
      })),
    });
  }
  return rows;
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

/** The steering KAS added this turn: a workspace document links into the editor, which cannot open
 *  a global one (`~/.kiro`), so any other id is plain text. */
function steeringItems(ids: readonly string[]): PanelItem[] {
  const keys = occurrenceKeys(ids);
  return ids.map((id, i) => {
    const path = filePathOf(id);
    return {
      key: keys[i] ?? id,
      className: "turn-info-item",
      parts: [documentPart(path?.slice(path.lastIndexOf("/") + 1) ?? id, id)],
    };
  });
}

/** A document's name, a button into the editor when `uri` is a file under the workspace. The sig
 *  is the name and the path it opens, so a root change that keeps the link keeps its node. */
function documentPart(name: string, uri: string): ItemPart {
  const path = filePathOf(uri);
  const root = workspaceRoot();
  const target = path !== null && root !== "" && relBeneath(root, path) !== null ? path : null;
  return {
    key: "name",
    sig: join(name, target ?? ""),
    build: () => (target === null ? el("span", {}, name) : documentLink(name, target)),
  };
}

function documentLink(name: string, path: string): HTMLElement {
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

/** SORTED by path, matching `fileRows`' own sort: a `Record`'s insertion order is the order the
 *  paths happened to arrive in, so an unsorted signature would move for a set that did not change
 *  and repaint the panel for nothing. */
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
