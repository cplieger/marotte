// Tool group: a collapsible container over consecutive tool calls, with a per-kind or mixed summary.
// The header shows from the SECOND NON-HOOK CARD, or the second hook row in a lane with none (`paintGroup`); a shell
// holding one card is CLS_BARE and renders as that `.tool-call`, its hooks badge on the card's header line and its
// hook rows inside its box. The shell is mounted on the FIRST card and gains chrome by class, since re-seating a card
// replays its entry slide and drops focus; so `.tool-group` alone does not mean "two or more" (`groupIsBare`).
// Collapse is POSITIONAL: a group folds once something is posted after it, applied at construction when already
// superseded (the only silent route) and by `autoCollapseGroup` live. A user click takes over.
// A failed member folds like any other; the header's mark and count carry the failure.

import { el } from "@cplieger/ui";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { outcomeIcon } from "./icons.js";
import {
  foldGroupHooks,
  foldStrayHook,
  hookStatusShown,
  onHookVisibility,
  type HookFold,
} from "./hook-fold.js";
import { syncHookBadge } from "./hook-badge.js";
import { pressHold } from "./scroll.js";
import { kindNoun } from "./tool-kind-noun.js";
import type { ToolKind } from "./tool-schema.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui/disclosure";
import { queryAll } from "./dom.js";

/** The body is closed, whoever closed it: the header click toggles it, and find-in-chat's reveal reads
 *  it, so a close that leaves it off makes the next header click close the group again. */
const CLS_COLLAPSED = "tool-group-collapsed";
const CLS_USER_TOGGLED = "tool-group-user-toggled";
/** A shell with no header: fewer than two non-hook cards, or hook rows alone (`paintGroup` says when those count). A
 *  pure function of what `foldGroupHooks` and `placeStrays` leave, with ONE writer (paintGroup), so there is no
 *  second flag or counter to keep in step. */
const CLS_BARE = "tool-group-bare";

/** Each group's members as its last refresh folded them; `placeStrays` reads every sibling's. */
const folds = new WeakMap<HTMLElement, HookFold>();

/** The strays of hook-only siblings each group with work folds into its badge besides its own hooks. */
const adopted = new WeakMap<HTMLElement, readonly HTMLElement[]>();

/** Hook-only shells whose container's lane holds a non-hook card: their rows never form a group there. */
const besideWork = new WeakSet<HTMLElement>();

/** Whether each container's lane holds a non-hook card, mounted or not, as its renderer last said. */
const laneWork = new WeakMap<HTMLElement, boolean>();

/** Record whether `container`'s lane holds a non-hook card anywhere in its turn, mounted or not. Call it before the
 *  pass that mounts into `container`. */
export function setLaneHoldsWork(container: HTMLElement, holds: boolean): void {
  laneWork.set(container, holds);
}

/** The delegate's or run's card a tool-trigger hook belongs to, and whether the call that opened it was refused. */
export interface HookHost {
  /** Null while the card is not mounted: its hooks keep the fate they would have beside it, and no badge lists them. */
  readonly card: HTMLElement | null;
  readonly refused: boolean;
}

/** Each container's hook cards bound to a card host, as its renderer last said. */
const hookHosts = new WeakMap<HTMLElement, ReadonlyMap<HTMLElement, HookHost>>();

/** A delegate's or a workflow run's card, a hooks badge host outside any tool group. */
const CARD_HOST = ".subagent-block, .run-card";

/** Record which card hosts each mounted hook card of `container` (from the store, so a window that leaves out the
 *  card leaves the hook folded) and repaint every badge and row that moves. */
export function setHookHosts(
  container: HTMLElement,
  hosts: ReadonlyMap<HTMLElement, HookHost>,
): void {
  hookHosts.set(container, hosts);
  for (const [g, f] of placeStrays(container)) {
    paintGroup(g, f);
  }
}

/** Groups the reader had closed when they went bare, so the region they get back is born closed. */
const readerClosed = new WeakSet<HTMLElement>();

// Per-group disclosure controllers for .tool-group-body; the collapse STATE MACHINE stays marotte's.
// ABSENT on a BARE shell: minted by the refresh that makes the group non-bare, in the verdict's
// state, and dropped by the one that makes it bare again.
const groupCtls = new WeakMap<HTMLElement, DisclosureController>();

// The newest-element verdict, pushed per card append. Read ONCE, by the refresh that creates the
// controller. No entry means NOT superseded, the same default as `syncContainerCollapse`.
const groupSuperseded = new WeakMap<HTMLElement, boolean>();

/** Record whether this group's run is FOLLOWED by a later element. Written per card append: a run
 *  can straddle a cold-build slice boundary, and only the current block index knows. */
export function setGroupSuperseded(group: HTMLElement, superseded: boolean): void {
  groupSuperseded.set(group, superseded);
}

/** The card container inside a group shell. Cards are appended HERE, not to
 *  the group root, so the collapse region excludes the always-visible header. */
export function groupBody(group: HTMLElement): HTMLElement {
  return group.querySelector<HTMLElement>(":scope > .tool-group-body") ?? group;
}

/** Whether this shell renders with no header (`paintGroup` decides). Read it rather than the `.tool-group` selector,
 *  which does not mean "a group of two or more". */
export function groupIsBare(group: HTMLElement): boolean {
  return group.classList.contains(CLS_BARE);
}

// --- Header update ---

/** Build a `.tool-group` shell: header (role=button, tabindex, aria-expanded) and a click/keyboard
 *  collapse toggle. The caller appends the cards and owns per-container grouping. */
export function buildToolGroupShell(): HTMLDivElement {
  // Born BARE: an empty shell has nothing to group, so the class is right before the first refresh too.
  const group = el("div", { className: `tool-group ${CLS_BARE}` }) as HTMLDivElement;
  const header = el(
    "div",
    {
      className: "tool-group-header",
      role: "button",
      tabindex: "0",
      "aria-expanded": "true",
      [CHROME_ATTR]: "",
    },
    // The shared disclosure chevron, present in both states.
    chevronEl(),
    // The verdict slot, sharing `.tool-icon` for tints: paintGroupOutcome writes an outcome glyph (the
    // KIND is in the summary). Empty while running, where `14-tools.css` draws a hollow ring; the slot
    // sizes the box so the count text does not shift.
    el("span", { className: "tool-group-icon tool-icon" }),
    el("span", { className: "tool-group-count" }),
  ) as HTMLDivElement;
  header.addEventListener("click", () => {
    onHeaderClick(group, header);
  });
  header.addEventListener("keydown", (e: KeyboardEvent) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onHeaderClick(group, header);
    }
  });
  group.appendChild(header);
  group.appendChild(el("div", { className: "tool-group-body" }));
  // NO disclosure yet: a bare shell has nothing to disclose and its header is `display: none`.
  // `ensureGroupDisclosure` mints it from the refresh that first paints the shell non-bare.
  return group;
}

/** Bring a group's header into line with its members: the bare class, the body region (created
 *  only here, via `ensureGroupDisclosure`), the hooks badge, the summary and the verdict, and the
 *  same for each sibling `placeStrays` moved. Called after each append, on every status flip
 *  and on collapse toggle. */
export function refreshGroupHeader(group: HTMLElement): void {
  const fold = foldGroupHooks([
    ...group.querySelectorAll<HTMLElement>(":scope > .tool-group-body > .tool-call"),
  ]);
  folds.set(group, fold);
  const moved =
    group.parentElement === null
      ? new Map<HTMLElement, HookFold>()
      : placeStrays(group.parentElement);
  paintGroup(group, fold);
  for (const [g, f] of moved) {
    if (g !== group) {
      paintGroup(g, f);
    }
  }
}

/** Fold each hook-only sibling's strays into the nearest badge host in `container`, a group with work or a card
 *  host: the last one before it, else the first one after it. With no such host they stay rows. A gated hook goes by
 *  `setHookHosts`. Paints each card host's badge and returns the groups whose rows, badge or `besideWork` membership
 *  moved, with their folds. */
function placeStrays(container: HTMLElement): Map<HTMLElement, HookFold> {
  const groups: [HTMLElement, HookFold][] = [];
  const into = new Map<HTMLElement, HTMLElement[]>();
  let last: HTMLElement[] | null = null;
  const before: HTMLElement[] = [];
  for (const c of container.querySelectorAll<HTMLElement>(
    `:scope > .tool-group, :scope > :is(${CARD_HOST})`,
  )) {
    const card = c.matches(CARD_HOST);
    const f = card ? undefined : folds.get(c);
    if (f !== undefined) {
      groups.push([c, f]);
    }
    if (card || (f !== undefined && f.work.length > 0)) {
      last = last === null ? before : [];
      into.set(c, last);
    } else if (f !== undefined) {
      (last ?? before).push(...f.strays);
    }
  }
  const bound = hookHosts.get(container);
  const shown = hookStatusShown();
  const gatedMoved = new Set<HTMLElement>();
  for (const [g, f] of groups) {
    for (const c of f.gated) {
      const h = bound?.get(c);
      if (placeGated(c, h, h !== undefined && !h.refused && shown, shown)) {
        gatedMoved.add(g);
      }
    }
  }
  // A card host lists every hook bound to it: folded, or a row while status shows, as a lone box lists its own.
  for (const [c, h] of bound ?? []) {
    if (h.card !== null && (c.hasAttribute("data-hook-folded") || (shown && !c.hidden))) {
      into.get(h.card)?.push(c);
    }
  }
  const folded = new Set([...into.values()].flat());
  // Strays need a mounted badge to join; whether the lane holds a tool box does not.
  const work = laneWork.get(container) === true;
  const moved = new Map<HTMLElement, HookFold>();
  for (const [g, f] of groups) {
    if (f.work.length > 0) {
      const list = into.get(g) ?? [];
      const was = adopted.get(g) ?? [];
      if (list.length !== was.length || list.some((c, i) => c !== was[i])) {
        adopted.set(g, list);
        moved.set(g, f);
      }
      continue;
    }
    // A group a window drop left with no non-hook card folds no sibling's strays any more.
    let changed = adopted.delete(g) || besideWork.has(g) !== work || gatedMoved.has(g);
    if (work) {
      besideWork.add(g);
    } else {
      besideWork.delete(g);
    }
    for (const s of f.strays) {
      if (s.hidden !== folded.has(s)) {
        foldStrayHook(s, folded.has(s));
        changed = true;
      }
    }
    if (changed) {
      moved.set(g, f);
    }
  }
  for (const [h, list] of into) {
    if (h.matches(CARD_HOST)) {
      // In page order: its strays and its bound hooks join it in two passes.
      list.sort((a, b) =>
        a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1,
      );
      syncHookBadge(h, list, h);
    }
  }
  return moved;
}

/** Give a gated hook its fate: folded into its card host's badge, a row beside a refused one, else what
 *  `foldGroupHooks` gave it. Returns whether it moved. */
function placeGated(
  c: HTMLElement,
  h: HookHost | undefined,
  folded: boolean,
  shown: boolean,
): boolean {
  const hidden = folded || (h?.refused !== true && !shown);
  if (c.hidden === hidden && c.hasAttribute("data-hook-folded") === folded) {
    return false;
  }
  c.hidden = hidden;
  c.toggleAttribute("data-hook-folded", folded);
  return true;
}

/** The ONE writer of the bare class: paint `group` from its fold and the strays it took in. */
function paintGroup(group: HTMLElement, fold: HookFold): void {
  // The summary reads the rows alone: the hooks badge counts every folded hook, and a hook is no operation.
  const rows = fold.rows.filter((c) => !c.hidden);
  // A group is made of non-hook cards. Hook rows alone form one only in a lane with none.
  const bare =
    fold.work.length > 0 ? fold.work.length < 2 : besideWork.has(group) || rows.length < 2;
  // A lone box's badge lists its hook rows too, which it keeps showing; a group's lists what it folded.
  const own = bare && fold.work.length > 0 ? fold.listed : fold.folded;
  const badged = [...own, ...(adopted.get(group) ?? [])];
  // Above the summary-span guard, which would otherwise exempt a shell without a count span.
  group.classList.toggle(CLS_BARE, bare);
  group.hidden = rows.length === 0 && badged.length === 0;
  // Deliberately ABOVE the summary-span guard too, and for the same reason: a shell carrying no
  // count span would otherwise never get its region. This is the ONE creation site.
  if (bare) {
    dropGroupDisclosure(group);
  } else {
    ensureGroupDisclosure(group, fold.rows);
  }
  syncHookBadge(group, badged, bare ? (fold.work[0] ?? group) : group);
  const header = group.querySelector(".tool-group-header .tool-group-count");
  if (header === null) {
    return;
  }
  const counts = countOutcomes(rows);
  // The summary states the aggregate FACT and names every non-clean population in
  // it, so a COLLAPSED group still says what happened. It never counts cards:
  // "Read 5 files" is right, "5 tool calls" is a bug.
  const summary = summarizeOperations(rows) + namedCounts(counts);
  // No "(collapsed)" suffix: the group is one box whose chevron and
  // aria-expanded already carry the state, so the word restated the chrome.
  header.textContent = summary;
  paintGroupOutcome(group, rows, counts);
}

// A switch flip moves which members are rows on every mounted group at once.
onHookVisibility(() => {
  for (const group of queryAll<HTMLElement>(".tool-group")) {
    refreshGroupHeader(group);
  }
});

/** Give a non-bare group its body region ONCE, in its FINAL open/closed state, so the state machine
 *  alone moves a fold afterwards. */
function ensureGroupDisclosure(group: HTMLElement, calls: HTMLElement[]): void {
  if (groupCtls.has(group)) {
    return;
  }
  const collapsed = bornCollapsed(group, calls);
  groupCtls.set(group, createDisclosure(null, groupBody(group), { open: !collapsed }));
  if (collapsed) {
    markAutoCollapsed(group);
  }
}

/** Unwire the region of a group that went bare and leave it open: its header is hidden, so a
 *  closed region would hide its rows with no way back. `dispose()` pins `0px` on a closed region
 *  and keeps it inert, so both are cleared here. */
function dropGroupDisclosure(group: HTMLElement): void {
  const ctl = groupCtls.get(group);
  if (ctl === undefined) {
    return;
  }
  if (group.classList.contains(CLS_USER_TOGGLED) && group.classList.contains(CLS_COLLAPSED)) {
    readerClosed.add(group);
  } else {
    readerClosed.delete(group);
  }
  ctl.dispose();
  groupCtls.delete(group);
  const body = groupBody(group);
  body.style.removeProperty("height");
  body.inert = false;
  body.setAttribute("aria-hidden", "false");
  group.classList.remove(CLS_COLLAPSED);
  group.querySelector(".tool-group-header")?.setAttribute("aria-expanded", "true");
}

/** Whether the region is created CLOSED: a reader's decision first, then `autoCollapseGroup`'s
 *  carve-outs at construction (the verdict, a running member). */
function bornCollapsed(group: HTMLElement, calls: HTMLElement[]): boolean {
  if (group.classList.contains(CLS_USER_TOGGLED)) {
    return readerClosed.has(group);
  }
  if (groupSuperseded.get(group) !== true) {
    return false;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return false;
    }
  }
  return true;
}

/** The auto-collapse marking, shared by the born-folded and live paths. The caller refreshes the
 *  summary: this runs inside `refreshGroupHeader`, which would re-enter. */
function markAutoCollapsed(group: HTMLElement): void {
  group.classList.add(CLS_COLLAPSED);
  group.querySelector<HTMLElement>(".tool-group-header")?.setAttribute("aria-expanded", "false");
}

/** How many settled members failed, refused and were stopped, in ONE walk, for the header. `denied`
 *  counts in none: a policy refusal never RAN. */
interface GroupCounts {
  readonly failures: number;
  readonly declined: number;
  readonly aborted: number;
}

function countOutcomes(calls: HTMLElement[]): GroupCounts {
  let failures = 0;
  let declined = 0;
  let aborted = 0;
  for (const c of calls) {
    const outcome = c.dataset["outcome"];
    if (outcome === "fail") {
      failures++;
    } else if (outcome === "declined") {
      declined++;
    } else if (outcome === "warn") {
      aborted++;
    }
  }
  return { failures, declined, aborted };
}

/** Each word is the one a tool ROW announces (`tool-card.ts` `outcomeWord`). Worst-first, as
 *  `paintGroupOutcome` orders the mark. */
function namedCounts({ failures, declined, aborted }: GroupCounts): string {
  const parts: string[] = [];
  if (failures > 0) {
    parts.push(`${String(failures)} failed`);
  }
  if (declined > 0) {
    parts.push(`${String(declined)} declined`);
  }
  if (aborted > 0) {
    parts.push(`${String(aborted)} aborted`);
  }
  return parts.map((p) => ` \u00b7 ${p}`).join("");
}

/** Tint the group's mark to the worst member status (`data-outcome`), with that state's SHAPE.
 *  `denied` folds onto `ok`. In a MIXED group `declined` (the WORK) outranks `warn` (the READER);
 *  the summary names both. `outcomeIcon`, not `applyOutcome`: no identity glyph to keep.
 *  `running` writes no node (the CSS ring). */
function paintGroupOutcome(group: HTMLElement, calls: HTMLElement[], counts: GroupCounts): void {
  const icon = group.querySelector<HTMLElement>(".tool-group-icon");
  if (icon === null) {
    return;
  }
  const running = calls.some((c) => c.dataset["outcome"] === "running");
  const state =
    counts.failures > 0
      ? "fail"
      : running
        ? "running"
        : counts.declined > 0
          ? "declined"
          : counts.aborted > 0
            ? "warn"
            : "ok";
  group.dataset["outcome"] = state;
  icon.classList.remove("is-ok", "is-fail", "is-warn", "is-declined", "is-running");
  icon.classList.add(`is-${state}`);
  if (state === "running") {
    icon.replaceChildren();
  } else {
    icon.replaceChildren(iconEl(outcomeIcon(state)));
  }
  icon.setAttribute("aria-hidden", "true");
}

// --- Summarizers (pure, no state dependency) ---

/** The summary of a group's rows, hooks left out. A group of nothing but hook rows has no other operation to name,
 *  so it summarizes those. */
function summarizeOperations(rows: HTMLElement[]): string {
  const operations = rows.filter((c) => c.dataset["kind"] !== "hook");
  return summarize(operations.length > 0 ? operations : rows);
}

export interface CallInfo {
  kind: ToolKind;
  title: string;
  filename: string;
  mcpServer: string;
  /** A steering box's document names, as its title shows them; empty for any other kind. */
  steeringDocs: readonly string[];
}

export function summarize(calls: HTMLElement[]): string {
  const n = calls.length;
  if (n === 0) {
    return "0 tool calls";
  }
  const infos: CallInfo[] = calls.map((c) => ({
    kind: (c.dataset["kind"] ?? "other") as ToolKind,
    title: c.dataset["title"] ?? "",
    filename: c.dataset["filename"] ?? "",
    mcpServer: c.dataset["mcpServer"] ?? "",
    steeringDocs: steeringDocsOf(c),
  }));

  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const firstKind = infos[0]!.kind;
  const allSame = infos.every((i) => i.kind === firstKind);
  if (allSame) {
    return summarizeSameKind(firstKind, infos);
  }
  return summarizeMixed(infos);
}

/** The names a steering card stamped as `data-steering-docs`, a JSON array of strings. */
function steeringDocsOf(card: HTMLElement): string[] {
  const raw = card.dataset["steeringDocs"];
  if (raw === undefined) {
    return [];
  }
  const parsed: unknown = JSON.parse(raw);
  return Array.isArray(parsed) ? parsed.filter((n): n is string => typeof n === "string") : [];
}

/** `many` defaults to `noun` plus "s". */
const TOOL_KIND_LABELS: Readonly<
  Record<ToolKind, { verb: string; noun: string; many?: string; samplesFrom: "files" | "titles" }>
> = {
  read: { verb: "Read", noun: "file", samplesFrom: "files" },
  edit: { verb: "Edited", noun: "file", samplesFrom: "files" },
  write: { verb: "Wrote", noun: "file", samplesFrom: "files" },
  delete: { verb: "Deleted", noun: "file", samplesFrom: "files" },
  move: { verb: "Moved", noun: "file", samplesFrom: "files" },
  search: { verb: "Searched", noun: "search", many: "searches", samplesFrom: "titles" },
  execute: { verb: "Ran", noun: "command", samplesFrom: "titles" },
  shell: { verb: "Ran", noun: "shell command", samplesFrom: "titles" },
  hook: { verb: "Ran", noun: "hook", samplesFrom: "titles" },
  steering: { verb: "Loaded", noun: "steering doc", samplesFrom: "titles" },
  fetch: { verb: "Fetched", noun: "URL", samplesFrom: "titles" },
  think: { verb: "", noun: "thinking step", samplesFrom: "titles" },
  switch_mode: { verb: "", noun: "mode switch", samplesFrom: "titles" },
  mcp: { verb: "", noun: "integration call", samplesFrom: "titles" },
  browser: { verb: "Browsed", noun: "page", samplesFrom: "titles" },
  command: { verb: "Ran", noun: "command", samplesFrom: "titles" },
  other: { verb: "Ran", noun: "call", samplesFrom: "titles" },
};

export function summarizeSameKind(kind: ToolKind, infos: CallInfo[]): string {
  const n = infos.length;
  if (kind === "mcp") {
    return summarizeMCP(infos);
  }
  if (kind === "steering") {
    return summarizeSteering(infos);
  }

  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  const label = TOOL_KIND_LABELS[kind] ?? TOOL_KIND_LABELS.other;
  if (label.verb === "") {
    const plural = n === 1 ? label.noun : kindNoun(kind, n);
    return `${String(n)} ${plural}`;
  }
  const samples = infos.flatMap((i) => sampleOf(i, label.samplesFrom)).filter((s) => s !== "");
  return labelWithSamples(n, label.noun, label.verb, samples, label.many);
}

function sampleOf(info: CallInfo, from: "files" | "titles"): string[] {
  return [from === "files" ? info.filename : info.title];
}

/** Counts the documents a run of steering boxes loaded, not the boxes, and samples their names. */
function summarizeSteering(infos: CallInfo[]): string {
  const label = TOOL_KIND_LABELS.steering;
  const names = dedup(infos.flatMap((i) => i.steeringDocs));
  return labelWithSamples(names.length, label.noun, label.verb, names, label.many);
}

export function summarizeMCP(infos: CallInfo[]): string {
  const n = infos.length;
  const servers = new Set(infos.map((i) => i.mcpServer).filter((s) => s !== ""));
  if (servers.size === 1) {
    // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
    const server = infos[0]!.mcpServer;
    const titles = dedup(infos.map((i) => i.title).filter((s) => s !== ""));
    const head = `Called ${String(n)} ${server} tool${n === 1 ? "" : "s"}`;
    if (titles.length === 0) {
      return head;
    }
    const shown = titles.slice(0, 2);
    const more = titles.length - shown.length;
    const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
    return `${head}: ${tail}`;
  }
  const counts = new Map<string, number>();
  for (const i of infos) {
    counts.set(i.mcpServer, (counts.get(i.mcpServer) ?? 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([srv, c]) => `${String(c)} ${srv}`);
  return `${String(n)} integration call${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

/** `unnamed` counts items no sample names, so the "+N more" tail includes them. */
export function labelWithSamples(
  n: number,
  noun: string,
  verb: string,
  samples: string[],
  many = `${noun}s`,
): string {
  const pluralNoun = n === 1 ? noun : many;
  const head = `${verb} ${String(n)} ${pluralNoun}`;
  const uniq = dedup(samples);
  if (uniq.length === 0) {
    return head;
  }
  const shown = uniq.slice(0, 2);
  const more = uniq.length - shown.length;
  const tail = more > 0 ? `${shown.join(", ")}, +${String(more)} more` : shown.join(", ");
  return `${head}: ${tail}`;
}

export function summarizeMixed(infos: CallInfo[]): string {
  const n = infos.length;
  // Keyed by ToolKind rather than string: `kindNoun`'s table is total over the
  // union, so a key outside it has no noun rather than a fallback one.
  const counts = new Map<ToolKind, number>();
  for (const i of infos) {
    counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1);
  }
  const sorted = [...counts.entries()].sort((a, b) => b[1] - a[1]);
  const parts = sorted.map(([k, c]) => `${String(c)} ${kindNoun(k, c)}`);
  return `${String(n)} operation${n === 1 ? "" : "s"}: ${parts.join(", ")}`;
}

function dedup(arr: string[]): string[] {
  return [...new Set(arr)];
}

// --- Collapse / expand ---

function onHeaderClick(group: HTMLDivElement, header: HTMLDivElement): void {
  // No bare guard, unlike `autoCollapseGroup`: a bare header is `display: none`, so this is unreachable on one.
  group.classList.add(CLS_USER_TOGGLED);
  const collapsedNow = group.classList.toggle(CLS_COLLAPSED);
  const ctl = groupCtls.get(group);
  if (collapsedNow) {
    ctl?.close();
  } else {
    ctl?.open();
  }
  header.setAttribute("aria-expanded", collapsedNow ? "false" : "true");
  refreshGroupHeader(group);
  if (!collapsedNow) {
    pressHold();
  }
}

/** Collapse a group whose run of consecutive calls just ENDED. Exempt: a BARE group, a user toggle,
 *  a running member (folded by the render pass after its settle drops `data-start-ms`). THE LIVE PATH
 *  ONLY, and animated; a group condemned at creation is closed silently by `ensureGroupDisclosure`. */
export function autoCollapseGroup(group: HTMLElement): void {
  // A BARE group's body holds every row it shows, and its header is hidden, so closing
  // the region would make them vanish with no affordance to bring them back.
  if (groupIsBare(group)) {
    return;
  }
  const ctl = groupCtls.get(group);
  if (ctl === undefined) {
    // No region, so nothing to close: a collapsed class over an open body would be unactionable. Unreachable
    // through `refreshGroupHeader`; agreement with `ensureGroupDisclosure`.
    return;
  }
  if (group.classList.contains(CLS_USER_TOGGLED) || group.classList.contains(CLS_COLLAPSED)) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return;
    }
  }
  markAutoCollapsed(group);
  ctl.close();
  refreshGroupHeader(group);
}
