// Tool group: a collapsible container over consecutive tool calls, with a per-kind or mixed summary.
// The box exists only FROM THE SECOND CALL: a one-member shell carries CLS_BARE and renders
// pixel-identical to a standalone `.tool-call`. The shell is mounted on the FIRST card and gains
// chrome by class, since re-seating a card replays its entry slide and drops focus; so the
// `.tool-group` selector alone does not mean "two or more" (consult `groupIsBare`). Collapse is
// POSITIONAL: a group folds once something is posted after it, applied at construction when already
// superseded (the only silent route) and by `autoCollapseGroup` live. A user click takes over.
// FAILURE IS NOT NOISE: a failed member blocks or undoes the fold and sets the header's mark.

import { el } from "@cplieger/reactive";
import { chevronEl } from "./chevron.js";
import { CHROME_ATTR } from "./chrome-attr.js";
import { iconEl } from "./icon-el.js";
import { outcomeIcon } from "./icons.js";
import { setUserScrolledUp } from "./scroll.js";
import { kindNoun } from "./tool-kind-noun.js";
import type { ToolKind } from "./tool-schema.js";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";

const CLS_COLLAPSED = "tool-group-collapsed";
const CLS_AUTO_COLLAPSED = "tool-group-auto-collapsed";
const CLS_USER_TOGGLED = "tool-group-user-toggled";
/** A shell holding fewer than two members: no header, no group chrome. A pure
 *  function of the member count with ONE writer (refreshGroupHeader), so there is
 *  no second flag or counter to keep in step. */
const CLS_BARE = "tool-group-bare";

// Per-group disclosure controllers for .tool-group-body; the collapse STATE MACHINE stays marotte's.
// ABSENT on a BARE shell: minted once by the refresh that makes the group non-bare, in the verdict's
// state.
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

/** Whether this shell is holding fewer than two members, so it renders as a plain
 *  card with no header. Read it rather than the `.tool-group` selector, which does
 *  not mean "a group of two or more". */
export function groupIsBare(group: HTMLElement): boolean {
  return group.classList.contains(CLS_BARE);
}

// --- Header update ---

/** Build a `.tool-group` shell: header (role=button, tabindex, aria-expanded) and a click/keyboard
 *  collapse toggle. The caller appends the cards and owns per-container grouping. */
export function buildToolGroupShell(): HTMLDivElement {
  // Born BARE, so "bare ⇔ fewer than two members" holds at every instant, construction included.
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
  // `ensureGroupDisclosure` mints it when the second card lands.
  return group;
}

/** Bring a group's header into line with its members: the bare class, the body region (created
 *  only here, via `ensureGroupDisclosure`), the summary and the verdict. Called after each append,
 *  on every status flip and on collapse toggle. */
export function refreshGroupHeader(group: HTMLElement): void {
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  // The ONE writer of the bare class, a pure function of the member count. Above the summary-span
  // guard, which would otherwise exempt a shell without a count span.
  group.classList.toggle(CLS_BARE, calls.length < 2);
  // Deliberately ABOVE the summary-span guard too, and for the same reason: it needs
  // only `calls`, and a shell carrying no count span would otherwise never get its
  // region. This is the ONE creation site.
  ensureGroupDisclosure(group, calls);
  const header = group.querySelector(".tool-group-header .tool-group-count");
  if (header === null) {
    return;
  }
  const counts = countOutcomes(calls);
  // The summary states the aggregate FACT and names every non-clean population in
  // it, so a COLLAPSED group still says what happened. It never counts cards:
  // "Read 5 files" is right, "5 tool calls" is a bug.
  const summary = summarize(calls) + namedCounts(counts);
  // No "(collapsed)" suffix: the group is one box whose chevron and
  // aria-expanded already carry the state, so the word restated the chrome.
  header.textContent = summary;
  paintGroupOutcome(group, calls, counts);
}

/** Give a non-bare group its body region ONCE, in its FINAL open/closed state, so the state machine
 *  alone moves a fold afterwards. A non-bare → bare drop (`pruneEmptyContainers`) leaves the
 *  controller in place: disposing would also need `region.style.height` cleared, since
 *  `dispose()` pins `0px` for a closed region. */
function ensureGroupDisclosure(group: HTMLElement, calls: HTMLElement[]): void {
  if (groupIsBare(group) || groupCtls.has(group)) {
    return;
  }
  const collapsed = bornCollapsed(group, calls);
  groupCtls.set(group, createDisclosure(null, groupBody(group), { open: !collapsed }));
  if (collapsed) {
    markAutoCollapsed(group);
  }
}

/** Whether the region is created CLOSED: `autoCollapseGroup`'s carve-outs at construction (the
 *  verdict, then a reader's decision, a failure, a running member). */
function bornCollapsed(group: HTMLElement, calls: HTMLElement[]): boolean {
  if (groupSuperseded.get(group) !== true || group.classList.contains(CLS_USER_TOGGLED)) {
    return false;
  }
  if (countOutcomes(calls).failures > 0) {
    return false;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return false;
    }
  }
  return true;
}

/** The AUTO-collapsed marking: one writer for the class and the header state. The caller refreshes
 *  the summary: this runs inside `refreshGroupHeader`, which would re-enter. */
function markAutoCollapsed(group: HTMLElement): void {
  group.classList.add(CLS_AUTO_COLLAPSED);
  group.querySelector<HTMLElement>(".tool-group-header")?.setAttribute("aria-expanded", "false");
}

/** How many settled members failed, refused and were stopped, in ONE walk. Only `failures` blocks
 *  the FOLD; stopped and refused are settles. `denied` counts in none: a policy refusal never RAN. */
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

export interface CallInfo {
  kind: ToolKind;
  title: string;
  filename: string;
  mcpServer: string;
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
  }));

  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const firstKind = infos[0]!.kind;
  const allSame = infos.every((i) => i.kind === firstKind);
  if (allSame) {
    return summarizeSameKind(firstKind, infos);
  }
  return summarizeMixed(infos);
}

const TOOL_KIND_LABELS: Readonly<
  Record<ToolKind, { verb: string; noun: string; samplesFrom: "files" | "titles" }>
> = {
  read: { verb: "Read", noun: "file", samplesFrom: "files" },
  edit: { verb: "Edited", noun: "file", samplesFrom: "files" },
  write: { verb: "Wrote", noun: "file", samplesFrom: "files" },
  delete: { verb: "Deleted", noun: "file", samplesFrom: "files" },
  move: { verb: "Moved", noun: "file", samplesFrom: "files" },
  search: { verb: "Searched", noun: "search", samplesFrom: "titles" },
  execute: { verb: "Ran", noun: "command", samplesFrom: "titles" },
  shell: { verb: "Ran", noun: "shell command", samplesFrom: "titles" },
  hook: { verb: "Ran", noun: "hook", samplesFrom: "titles" },
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

  // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
  const label = TOOL_KIND_LABELS[kind] ?? TOOL_KIND_LABELS.other;
  if (label.verb === "") {
    const plural = n === 1 ? label.noun : kindNoun(kind, n);
    return `${String(n)} ${plural}`;
  }
  const samples =
    label.samplesFrom === "files"
      ? infos.map((i) => i.filename).filter((s) => s !== "")
      : infos.map((i) => i.title).filter((s) => s !== "");
  return labelWithSamples(n, label.noun, label.verb, samples);
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

export function labelWithSamples(n: number, noun: string, verb: string, samples: string[]): string {
  const pluralNoun = n === 1 ? noun : `${noun}s`;
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

function onHeaderClick(group: HTMLDivElement, _header: HTMLDivElement): void {
  // No bare guard here, unlike both collapse paths below, and the asymmetry is
  // not an oversight: a bare header is `display: none`, so it is out of the
  // accessibility tree and out of tab order and this handler is unreachable.
  group.classList.add(CLS_USER_TOGGLED);
  const wasAuto = group.classList.contains(CLS_AUTO_COLLAPSED);
  if (wasAuto) {
    group.classList.remove(CLS_AUTO_COLLAPSED);
  }
  group.classList.toggle(CLS_COLLAPSED);
  const collapsedNow = group.classList.contains(CLS_COLLAPSED);
  // Drive the body region's disclosure to match the class-derived state
  // (preserving the existing model, where a click on an AUTO-collapsed group
  // converts it to a USER collapse and it stays closed).
  const ctl = groupCtls.get(group);
  if (collapsedNow) {
    ctl?.close();
  } else {
    ctl?.open();
  }
  _header.setAttribute("aria-expanded", collapsedNow ? "false" : "true");
  refreshGroupHeader(group);
  if (!collapsedNow || wasAuto) {
    setUserScrolledUp(true);
  }
}

export function maybeCollapseGroup(node: HTMLElement): void {
  const group = node.closest<HTMLElement>(".tool-group");
  if (group === null) {
    return;
  }
  // A bare group is never collapsed, so there is nothing to re-open. Its lone
  // card carries its own failure mark, which is the actionable signal a group
  // header would otherwise be standing in for.
  if (groupIsBare(group)) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];

  // A failure defeats collapse in BOTH directions: it blocks an auto-collapse and re-opens a group
  // that folded before the member settled. Failures only; a stopped member folds.
  if (countOutcomes(calls).failures > 0) {
    if (
      group.classList.contains(CLS_AUTO_COLLAPSED) &&
      !group.classList.contains(CLS_USER_TOGGLED)
    ) {
      group.classList.remove(CLS_AUTO_COLLAPSED);
      groupCtls.get(group)?.open();
      group.querySelector<HTMLElement>(".tool-group-header")?.setAttribute("aria-expanded", "true");
      refreshGroupHeader(group);
    }
    return;
  }
}

/** Collapse a group whose run of consecutive calls just ENDED. Exempt: a BARE group, a user toggle,
 *  a failure inside, a running member (whose status flip re-runs maybeCollapseGroup). THE LIVE PATH
 *  ONLY, and animated; a group condemned at creation is closed silently by `ensureGroupDisclosure`. */
export function autoCollapseGroup(group: HTMLElement): void {
  // A BARE group's body region IS the lone card, and its header is hidden — so
  // closing the disclosure would make the card vanish with no affordance to bring
  // it back. Correctness, not tidiness.
  if (groupIsBare(group)) {
    return;
  }
  const ctl = groupCtls.get(group);
  if (ctl === undefined) {
    // No region, so no class: a CLS_AUTO_COLLAPSED group with an open body is unactionable. Unreachable
    // through `refreshGroupHeader`; agreement with `ensureGroupDisclosure`.
    return;
  }
  if (
    group.classList.contains(CLS_AUTO_COLLAPSED) ||
    group.classList.contains(CLS_USER_TOGGLED) ||
    group.classList.contains(CLS_COLLAPSED)
  ) {
    return;
  }
  const calls = [
    ...group.querySelectorAll(":scope > .tool-group-body > .tool-call"),
  ] as HTMLElement[];
  // The FAILURE count alone, never the stopped one: a stopped member neither
  // blocks the fold nor auto-opens the body, matching the delegate card.
  if (countOutcomes(calls).failures > 0) {
    return;
  }
  for (const c of calls) {
    if (c.dataset["startMs"] !== undefined) {
      return;
    }
  }
  markAutoCollapsed(group);
  ctl.close();
  refreshGroupHeader(group);
}
