// The focus ring on a control its clipping card does not clear: `40-a11y.css` moves the ring inside the border box for
// the members below; the criterion is its reach (`outline-width + outline-offset`) against the measured inset.
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { userEvent } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const a11y = loadCSS("40-a11y.css");

/** Every focusable the inset offset is declared for, as `40-a11y.css` spells it. */
const MEMBERS = [
  "a.subagent-header",
  ".subagent-container.has-disclosure > .subagent-header",
  ".subagent-foot > .subagent-footer > .turn-ledger-summary",
  ".pill-account",
  ".run-head",
  "a.run-step-head",
  ".run-open",
  ".tool-group-header",
  ".code-act-btn",
  ".git-repo-section-header",
  ".git-repo-section-header-toggle",
  ".forge-account-repos-summary",
] as const;

let style: HTMLStyleElement;
let host: HTMLElement;
/** Where every Tab starts, so focus arrives by keyboard. */
let sentinel: HTMLButtonElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
  host = document.createElement("div");
  // Definite width at the top: `content-visibility: auto` skips off-screen contents, reporting no boxes.
  host.style.cssText = "position:fixed;top:0;left:0;width:774px;";
  sentinel = document.createElement("button");
  sentinel.type = "button";
  sentinel.textContent = "start";
  document.body.appendChild(host);
});

afterAll(() => {
  style.remove();
  host.remove();
});

function node<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  attrs: Record<string, string> = {},
): HTMLElementTagNameMap[K] {
  const n = document.createElement(tag);
  n.className = className;
  for (const [name, value] of Object.entries(attrs)) {
    n.setAttribute(name, value);
  }
  return n;
}

/** Text on the span: `head.textContent = …` after an append replaces the children. */
function span(className: string, label: string): HTMLSpanElement {
  const n = node("span", className);
  n.textContent = label;
  return n;
}

/** The size `iconEl` produces, so an icon-only member has a real box. */
function glyph(): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  return svg;
}

function mount<T extends HTMLElement>(clipper: T): T {
  clipper.style.animation = "none";
  host.replaceChildren(sentinel, clipper);
  return clipper;
}

/** A real Tab arms `:focus-visible`; the loop reaches a control that is not the card's first focusable. */
async function focusByTab(target: HTMLElement): Promise<void> {
  sentinel.focus();
  for (let i = 0; i < 12; i++) {
    await userEvent.tab();
    if (document.activeElement === target) {
      return;
    }
  }
  throw new Error(`Tab never reached .${target.className}`);
}

interface Edges {
  top: number;
  bottom: number;
  left: number;
  right: number;
}

const px = (v: string): number => Number.parseFloat(v) || 0;
const round = (v: number): number => Math.round(v * 100) / 100;

/** `overflow: hidden` clips at the padding box: the border box less the clipper's own border. */
function clipBox(clipper: HTMLElement): Edges {
  const r = clipper.getBoundingClientRect();
  const cs = getComputedStyle(clipper);
  return {
    top: r.top + px(cs.borderTopWidth),
    bottom: r.bottom - px(cs.borderBottomWidth),
    left: r.left + px(cs.borderLeftWidth),
    right: r.right - px(cs.borderRightWidth),
  };
}

/** Positive is clipped; zero or negative is painted. */
function ringOverflow(target: HTMLElement, clipper: HTMLElement): Edges {
  const cs = getComputedStyle(target);
  const reach = px(cs.outlineWidth) + px(cs.outlineOffset);
  const t = target.getBoundingClientRect();
  const c = clipBox(clipper);
  return {
    top: round(c.top - (t.top - reach)),
    bottom: round(t.bottom + reach - c.bottom),
    left: round(c.left - (t.left - reach)),
    right: round(t.right + reach - c.right),
  };
}

/** Zero is flush; anything under the ring's reach is clipped by that much. */
function inset(target: Element, clipper: HTMLElement): Edges {
  const t = target.getBoundingClientRect();
  const c = clipBox(clipper);
  return {
    top: round(t.top - c.top),
    bottom: round(c.bottom - t.bottom),
    left: round(t.left - c.left),
    right: round(c.right - t.right),
  };
}

const EDGES = ["top", "bottom", "left", "right"] as const;

/** Read off the control: a floor control answers 3, a member 0. */
function reachOf(target: HTMLElement): number {
  const cs = getComputedStyle(target);
  return px(cs.outlineWidth) + px(cs.outlineOffset);
}

/** The criterion every inset is judged against; a premise case reads it off a real focused control. */
const FLOOR_REACH = 3;

/** Per edge, so a failure names the edge and its overflow. */
function expectRingInside(name: string, target: HTMLElement, clipper: HTMLElement): void {
  const over = ringOverflow(target, clipper);
  for (const edge of EDGES) {
    expect(
      over[edge],
      `${name}: ring overflows the clip box at ${edge} by ${over[edge]}px`,
    ).toBeLessThanOrEqual(0.5);
  }
}

/** A control left on the floor's outside ring, with the clearance that earns it. */
function expectClears(name: string, target: HTMLElement, clipper: HTMLElement): void {
  expect(getComputedStyle(target).outlineOffset, `${name}: not on the floor's offset`).toBe("1px");
  const reach = reachOf(target);
  const gap = inset(target, clipper);
  for (const edge of EDGES) {
    expect(
      gap[edge],
      `${name}: only ${gap[edge]}px inside the clip box at ${edge}, against ${reach}px of reach`,
    ).toBeGreaterThanOrEqual(reach);
  }
  expectRingInside(name, target, clipper);
}

// Fixtures mirror the builders: the subject is the stylesheet, and the builders drag in the store.

/** A leaf as born: empty tail (0 tall) and foot (`display: none`), so the head is the card. */
function subagentCard(opts: { foot: boolean }): {
  clipper: HTMLElement;
  head: HTMLElement;
  ledger: HTMLElement | null;
} {
  const head = node("a", "subagent-header", { href: "/chat/c/subagent/u-1" });
  head.append(
    node("span", "subagent-icon tool-icon"),
    node("span", "subagent-name"),
    node("span", "subagent-head-chevron", { "aria-hidden": "true" }),
  );
  const tail = node("div", "subagent-tail", { "aria-hidden": "true" });
  const foot = node("div", "subagent-foot");
  let ledger: HTMLElement | null = null;
  if (opts.foot) {
    const footer = node("div", "turn-footer subagent-footer", { role: "note" });
    const summary = node("button", "turn-ledger-summary", { type: "button" });
    // The trigger as `buildTurnFooter` assembles it, with the `.sr-only` name.
    const info = node("span", "turn-ledger-info");
    info.appendChild(glyph());
    // The `i` leads the painted content, as buildTurnFooter appends it.
    const fact = node("span", "turn-fact");
    fact.textContent = "3 commands";
    summary.append(
      span("sr-only", "Turn details"),
      info,
      node("span", "turn-ledger-glyph"),
      span("turn-ledger-text", "Cancelled"),
      fact,
    );
    footer.appendChild(summary);
    foot.appendChild(footer);
    ledger = summary;
  }
  const clipper = node("div", "subagent-block");
  clipper.append(head, tail, foot);
  return { clipper: mount(clipper), head, ledger };
}

/** The container: the head is the disclosure's own `role="button"`. */
function subagentContainer(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "subagent-header", { role: "button", tabindex: "0" });
  head.append(node("span", "subagent-icon tool-icon"), node("span", "subagent-name"));
  const body = node("div", "subagent-body");
  body.appendChild(node("div", "subagent-block")).textContent = "a stage card";
  const clipper = node("div", "subagent-block subagent-container has-disclosure");
  clipper.append(head, body, node("div", "subagent-foot"));
  return { clipper: mount(clipper), head };
}

/** Plus one step so the head's bottom edge is not the card's. */
function runCard(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "run-head", { role: "button", tabindex: "0" });
  head.append(span("run-name", "recipe"), span("run-count", "step 1 of 3"));
  const body = node("div", "run-body");
  body.appendChild(node("div", "run-step")).textContent = "a step row";
  const clipper = node("div", "run-card");
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** Its link clears the clip box by one pixel, one of the two tightest clearances. */
function runCardWithFoot(): { clipper: HTMLElement; open: HTMLElement } {
  const head = node("div", "run-head", { role: "button", tabindex: "0" });
  head.append(span("run-name", "recipe"), span("run-count", "step 1 of 3"));
  const body = node("div", "run-body");
  body.appendChild(node("div", "run-step")).textContent = "a step row";
  const foot = node("div", "run-foot");
  const open = node("a", "run-open", { href: "/run/wf-1" });
  open.textContent = "Open run";
  foot.append(span("run-ledger", "3 steps"), open);
  const clipper = node("div", "run-card");
  clipper.append(head, body, foot);
  return { clipper: mount(clipper), open };
}

/** A step row's head is its only child, so all four edges are the head's. */
function runStep(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("a", "run-step-head", { href: "/run/wf-1#node=step" });
  head.append(node("span", "run-step-glyph"), span("run-step-name", "build"));
  const clipper = node("div", "run-step");
  clipper.appendChild(head);
  return { clipper: mount(clipper), head };
}

function toolGroup(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("div", "tool-group-header", {
    role: "button",
    tabindex: "0",
    "aria-expanded": "true",
  });
  head.append(node("span", "tool-icon"), span("tool-group-count", "Ran 3 commands"));
  const body = node("div", "tool-group-body");
  body.appendChild(node("div", "tool-call")).textContent = "a member card";
  const clipper = node("div", "tool-group");
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** `.code-head`'s 2px block padding is the only thing between an action button and the clip. */
function codeBlock(): { clipper: HTMLElement; copy: HTMLElement; run: HTMLElement } {
  const copy = node("button", "code-act-btn", { type: "button", "aria-label": "Copy" });
  copy.appendChild(glyph());
  const run = node("button", "code-act-btn", { type: "button", "aria-label": "Type in shell" });
  run.appendChild(glyph());
  const actions = node("div", "code-actions");
  actions.append(copy, run);
  const head = node("div", "code-head");
  head.append(span("code-lang", "bash"), actions);
  const pre = document.createElement("pre");
  pre.appendChild(document.createElement("code")).textContent = "echo one\necho two\n";
  const clipper = node("div", "code-wrap", { "data-code-state": "final" });
  clipper.append(head, pre);
  return { clipper: mount(clipper), copy, run };
}

/** `.git-repo-section-header` is the button, chevron first. */
function gitRepoSection(): { clipper: HTMLElement; head: HTMLElement } {
  const head = node("button", "git-repo-section-header", { type: "button" });
  head.append(
    node("span", "disclosure-chevron git-repo-section-chevron", { "aria-hidden": "true" }),
    span("git-repo-section-name", "marotte"),
  );
  const body = node("div", "git-file-list");
  body.textContent = "one changed file";
  const clipper = node("section", "git-repo-section", { "data-repo": "marotte" });
  clipper.append(head, body);
  return { clipper: mount(clipper), head };
}

/** The PRs tab's header is a `padding: 0` row holding the toggle, since + New PR cannot nest in a button. */
function gitRepoSectionPRs(): {
  clipper: HTMLElement;
  head: HTMLElement;
  newBtn: HTMLElement;
} {
  const row = node("div", "git-repo-section-header git-repo-section-header-row");
  const head = node("button", "git-repo-section-header-toggle", { type: "button" });
  head.append(
    node("span", "disclosure-chevron git-repo-section-chevron", { "aria-hidden": "true" }),
    node("span", "git-repo-section-forge-icon git-repo-section-forge-github", {
      "aria-hidden": "true",
    }),
    span("git-repo-section-name", "cplieger/marotte"),
    span("git-repo-section-meta", "2 open"),
  );
  const newBtn = node("button", "btn-small btn-primary", { type: "button" });
  newBtn.textContent = "+ New PR";
  row.append(head, newBtn);
  const body = node("div", "git-repo-section-body");
  body.appendChild(node("div", "git-repo-section-body-inner")).textContent = "a pr row";
  const clipper = node("section", "git-repo-section", { "data-repo": "cplieger/marotte" });
  clipper.append(row, body);
  return { clipper: mount(clipper), head, newBtn };
}

/** The repo list's toggle shares a `padding: 0` header row; its padding insets text, not box. */
function forgeAccountRow(): {
  clipper: HTMLElement;
  open: () => void;
  summary: HTMLElement;
  signOut: HTMLElement;
  cloneAll: HTMLElement;
  repoBtn: HTMLElement;
} {
  const manage = node("a", "btn-small forge-account-manage", { href: "https://example.test/" });
  manage.append(span("", "Manage"), glyph());
  const signOut = node("button", "btn-small btn-danger", { type: "button" });
  signOut.textContent = "Sign out";
  const actions = node("div", "forge-account-actions");
  actions.append(manage, signOut);
  const identity = node("div", "forge-account-identity");
  identity.append(
    span("forge-account-primary", "someone@example.test"),
    span("forge-account-meta", "@someone \u00b7 github.com"),
  );
  const top = node("div", "forge-account-row-top");
  top.append(identity, actions);

  const cloneAll = node("button", "btn-small forge-account-repos-clone-all", { type: "button" });
  cloneAll.textContent = "Clone all";
  const summary = node("button", "forge-account-repos-summary", {
    type: "button",
    "aria-expanded": "false",
  });
  summary.append(
    node("span", "disclosure-chevron forge-account-repos-chevron", { "aria-hidden": "true" }),
    node("span", "forge-account-repos-icon", { "aria-hidden": "true" }),
    span("forge-account-repos-label", "3 repos, 1 cloned locally"),
  );
  const batch = node("div", "forge-account-repos-actions");
  batch.appendChild(cloneAll);
  const head = node("div", "forge-account-repos-head");
  head.append(summary, batch);
  const repoBtn = node("button", "btn-small", { type: "button" });
  repoBtn.textContent = "Clone";
  const repoRow = node("li", "forge-account-repo-row");
  repoRow.append(
    node("span", "forge-account-repo-state"),
    span("forge-account-repo-name", "owner/repo"),
    repoBtn,
  );
  const list = node("ul", "forge-account-repos-list");
  list.appendChild(repoRow);
  // Closed as `createDisclosure` leaves it, without the height transition.
  const body = node("div", "forge-account-repos-body");
  body.style.cssText = "height:0px;overflow:hidden;";
  body.inert = true;
  body.appendChild(list);
  const block = node("div", "forge-account-repos", { "data-account-id": "github:github.com" });
  block.append(head, body);
  const open = (): void => {
    summary.setAttribute("aria-expanded", "true");
    body.style.height = "";
    body.inert = false;
  };

  const clipper = node("li", "forge-account-row", { "data-id": "github:github.com" });
  clipper.append(top, block);
  const outer = node("ul", "forge-account-list");
  outer.appendChild(clipper);
  mount(outer);
  return { clipper, open, summary, signOut, cloneAll, repoBtn };
}

/** The changed-file list clips; its rows give their controls the padding. */
function gitFileList(): { clipper: HTMLElement; path: HTMLElement; discard: HTMLElement } {
  const path = node("button", "git-file-path", { type: "button" });
  path.textContent = "static-src/app.ts";
  const stage = node("button", "btn-small", { type: "button" });
  stage.textContent = "Stage";
  const discard = node("button", "btn-small btn-danger", { type: "button" });
  discard.textContent = "Discard";
  const rowActions = node("span", "git-file-actions");
  rowActions.append(stage, discard);
  const top = node("div", "git-file-row-top");
  top.append(span("git-file-status git-st-m", "M"), path, rowActions);
  const row = node("li", "git-file-row");
  row.appendChild(top);
  const clipper = node("ul", "git-file-list", { "aria-label": "Changed" });
  clipper.appendChild(row);
  const inner = node("div", "git-repo-section-body-inner");
  inner.appendChild(clipper);
  const body = node("div", "git-repo-section-body uip-disclosure-region");
  body.appendChild(inner);
  const section = node("section", "git-repo-section", { "data-repo": "marotte" });
  section.appendChild(body);
  mount(section);
  return { clipper, path, discard };
}

/** One shape: the checkbox lives in `.diff-pane-toolbar` whether or not labels are shown. */
function diffPane(opts: { labelled: boolean }): { clipper: HTMLElement; toggle: HTMLElement } {
  const toggle = node("input", "", { type: "checkbox" });
  const label = node("label", "diff-pane-ws-toggle");
  label.append(toggle, span("", "Ignore whitespace"));
  const toolbar = node("div", "diff-pane-toolbar");
  toolbar.appendChild(label);
  const clipper = node("div", "diff-pane");
  clipper.appendChild(toolbar);
  if (opts.labelled) {
    const header = node("div", "diff-pane-header");
    header.append(
      span("diff-pane-label diff-pane-label-old", "HEAD"),
      span("diff-pane-label diff-pane-label-new", "working tree"),
    );
    clipper.appendChild(header);
  }
  const body = node("div", "diff-pane-body");
  body.textContent = "diff rows";
  clipper.appendChild(body);
  return { clipper: mount(clipper), toggle };
}

/** The chevron button is out of flow inside `.tool-summary`, so no padding produces its clearance. */
function toolCard(): { clipper: HTMLElement; disclosure: HTMLElement } {
  const disclosure = node("button", "tool-disclosure", {
    "aria-expanded": "false",
    "aria-label": "Toggle tool details",
  });
  disclosure.appendChild(node("span", "disclosure-chevron", { "aria-hidden": "true" }));
  const header = node("div", "tool-header");
  header.append(node("span", "tool-icon"), span("tool-title", "Run Command"), disclosure);
  const summary = node("div", "tool-summary has-disclosure");
  summary.appendChild(header);
  const details = node("div", "tool-details");
  details.appendChild(node("div", "tool-output")).textContent = "one line of output";
  const clipper = node("div", "tool-call", { "data-kind": "execute", "data-depth1": "output" });
  clipper.append(summary, details);
  return { clipper: mount(clipper), disclosure };
}

/** The list clips with no padding of its own, so `.mcp-row` provides the inset. */
function mcpRow(): { clipper: HTMLElement; del: HTMLElement } {
  const edit = node("button", "btn-small", { type: "button" });
  edit.textContent = "Edit";
  const del = node("button", "btn-small btn-danger", { type: "button" });
  del.textContent = "Delete";
  const actions = node("div", "mcp-row-actions");
  actions.append(edit, del);
  const check = node("input", "", { type: "checkbox" });
  const toggle = node("label", "toggle mcp-toggle");
  toggle.append(check, node("span", "toggle-slider"));
  const nameLine = node("div", "mcp-row-name-line");
  nameLine.append(node("span", "mcp-dot", { role: "img" }), span("mcp-row-name", "github"));
  const meta = node("div", "mcp-row-meta");
  meta.appendChild(span("mcp-row-meta-text", "stdio"));
  const body = node("div", "mcp-row-body");
  body.append(nameLine, meta);
  const row = node("div", "mcp-row", { "data-server-id": "github" });
  row.append(toggle, body, actions);
  const clipper = node("div", "mcp-server-list");
  clipper.appendChild(row);
  return { clipper: mount(clipper), del };
}

/** The info panel only has a box open; it clips with `overflow: clip`. */
function turnInfoPanel(): { clipper: HTMLElement; row: HTMLElement } {
  const row = node("button", "turn-file-row", { type: "button" });
  row.append(span("turn-file-path", "static-src/app.ts"), node("span", "turn-file-delta"));
  const item = node("li", "turn-ledger-file");
  item.appendChild(row);
  const list = node("ul", "turn-ledger-files");
  list.appendChild(item);
  const section = node("section", "turn-info-section");
  section.append(node("h4", "turn-info-title"), list);
  section.querySelector("h4")!.textContent = "Work";
  const panel = node("div", "turn-info-panel");
  panel.style.transition = "none";
  panel.appendChild(section);
  const summary = node("button", "turn-ledger-summary", { type: "button" });
  summary.appendChild(span("sr-only", "Turn details"));
  summary.append(
    node("span", "turn-ledger-glyph"),
    span("turn-ledger-text", ""),
    span("turn-fact", "2m 14s"),
  );
  const footer = node("div", "turn-footer", { role: "note", "data-info": "open" });
  footer.append(summary, panel);
  mount(footer);
  return { clipper: panel, row };
}

/** The model card gives its padding to the scroller so the effort section bleeds to both edges. */
function modelCard(): { clipper: HTMLElement; item: HTMLElement; tier: HTMLElement } {
  const item = node("button", "pill-model-item", { type: "button", role: "option" });
  item.append(span("", "a-model"), span("pill-model-meta", "1x"));
  const scroll = node("div", "pill-model-scroll");
  scroll.appendChild(item);
  const row = node("div", "effort-row");
  // The caption names the dimension and live tier, so the knob carries no text.
  const caption = span("effort-label", "Effort: ");
  caption.appendChild(span("effort-value", "high"));
  row.appendChild(caption);
  const track = node("div", "effort-track", { "data-tiers": "3" });
  // The knob is the section's only tab stop.
  const tier = node("div", "effort-knob", {
    role: "slider",
    tabindex: "0",
    "aria-label": "Reasoning effort",
    "data-level": "high",
  });
  track.appendChild(tier);
  row.appendChild(track);
  const clipper = node("span", "pill-expand-content pill-model-list is-open");
  clipper.append(scroll, row);
  const slot = node("span", "pill-slot");
  slot.appendChild(clipper);
  mount(slot);
  return { clipper, item, tier };
}

// ---------------------------------------------------------------------------

describe("the premise: a real Tab arms the floor's ring", () => {
  it("puts a 2px solid outline on the head of a delegate's card", async () => {
    const { head } = subagentCard({ foot: false });
    await focusByTab(head);
    expect(head.matches(":focus-visible")).toBe(true);
    const cs = getComputedStyle(head);
    expect(cs.outlineStyle).toBe("solid");
    expect(cs.outlineWidth).toBe("2px");
  });

  it("reaches 3px outside the border box, which is the criterion itself", async () => {
    // Read off a control the rule does not name: `--focus-offset` plus the ring width.
    const { newBtn } = gitRepoSectionPRs();
    await focusByTab(newBtn);
    expect(reachOf(newBtn)).toBe(FLOOR_REACH);
  });
});

describe("the premise: every one of these cards clips at its padding box", () => {
  // `content-visibility: auto` applies paint containment and clips to the same edge, so removing the clip is no remedy.
  it("with paint containment as well, on the two card ROOTS", () => {
    for (const build of [() => subagentCard({ foot: false }), runCard]) {
      const { clipper } = build();
      const cs = getComputedStyle(clipper);
      expect(cs.overflow, clipper.className).toBe("hidden");
      expect(cs.contentVisibility, clipper.className).toBe("auto");
    }
  });

  it("by overflow alone, on the five whose corners the clip rounds", () => {
    for (const build of [runStep, toolGroup, codeBlock, gitRepoSection, forgeAccountRow]) {
      const { clipper } = build();
      expect(getComputedStyle(clipper).overflow, clipper.className).toBe("hidden");
    }
  });
});

describe("the ring is painted inside the card that clips it", () => {
  it("on a delegate's card as BORN, where every edge is the card's", async () => {
    // The ordinary state: empty tail and hidden foot, so the head is the card on all four edges.
    const { clipper, head } = subagentCard({ foot: false });
    await focusByTab(head);
    const flush = inset(head, clipper);
    for (const edge of EDGES) {
      expect(flush[edge], `born card: ${edge} edge is not flush (${flush[edge]}px)`).toBeLessThan(
        0.5,
      );
    }
    expectRingInside("born card", head, clipper);
  });

  it("on a delegate's card with a foot, flush on three edges", async () => {
    const { clipper, head } = subagentCard({ foot: true });
    await focusByTab(head);
    const flush = inset(head, clipper);
    expect(flush.top).toBeLessThan(0.5);
    expect(flush.left).toBeLessThan(0.5);
    expect(flush.right).toBeLessThan(0.5);
    expect(flush.bottom).toBeGreaterThan(0.5);
    expectRingInside("leaf head", head, clipper);
  });

  it("on a delegate's FOOT ledger, flush on the edge the row does not pad", async () => {
    // Inline-start decides it: the row declares no leading padding.
    const { clipper, ledger } = subagentCard({ foot: true });
    if (ledger === null) {
      throw new Error("the fixture built no ledger button");
    }
    await focusByTab(ledger);
    const flush = inset(ledger, clipper);
    expect(flush.left, `foot ledger: inline-start is not flush (${flush.left}px)`).toBeLessThan(
      1.5,
    );
    expectRingInside("foot ledger", ledger, clipper);
  });

  it("on a pipeline CONTAINER's head, the disclosure shape on that same class", async () => {
    // One class, two focusable shapes: an anchor head and a `role="button"` head.
    const { clipper, head } = subagentContainer();
    await focusByTab(head);
    expectRingInside("container head", head, clipper);
  });

  it("on a run card's head", async () => {
    const { clipper, head } = runCard();
    await focusByTab(head);
    expectRingInside("run head", head, clipper);
  });

  it("on a run card's FOOT link, which stopped clearing the card in 2026-09", async () => {
    // Flush now that the foot's padding is the link's own hit box.
    const { clipper, open } = runCardWithFoot();
    await focusByTab(open);
    const flush = inset(open, clipper);
    expect(flush.bottom, `run foot link: bottom is not flush (${flush.bottom}px)`).toBeLessThan(
      1.5,
    );
    expectRingInside("run foot link", open, clipper);
  });

  it("on a run step's head, where every edge is the row's", async () => {
    const { clipper, head } = runStep();
    await focusByTab(head);
    const flush = inset(head, clipper);
    for (const edge of EDGES) {
      expect(flush[edge], `run step: ${edge} edge is not flush (${flush[edge]}px)`).toBeLessThan(
        0.5,
      );
    }
    expectRingInside("run step head", head, clipper);
  });

  it("on a tool group's header", async () => {
    const { clipper, head } = toolGroup();
    await focusByTab(head);
    expectRingInside("tool group header", head, clipper);
  });

  it("on a code block's action buttons, which are 2px inside rather than flush", async () => {
    // The mildest member: 2px block padding against 3px reach.
    const { clipper, copy, run } = codeBlock();
    await focusByTab(copy);
    const gap = inset(copy, clipper);
    expect(gap.top, `code copy button: block-start inset against the floor's reach`).toBe(2);
    expect(gap.top).toBeLessThan(FLOOR_REACH);
    expect(gap.right, "the other three edges are clear").toBeGreaterThanOrEqual(FLOOR_REACH);
    expectRingInside("code copy button", copy, clipper);
    await focusByTab(run);
    expectRingInside("code run button", run, clipper);
  });

  it("on a git repo section's header", async () => {
    const { clipper, head } = gitRepoSection();
    await focusByTab(head);
    expectRingInside("git section header", head, clipper);
  });

  it("on the PRs tab's toggle, the second focusable in that same card", async () => {
    // The toggle nested in a `padding: 0` row; flush top and left only.
    const { clipper, head } = gitRepoSectionPRs();
    await focusByTab(head);
    const flush = inset(head, clipper);
    expect(flush.top).toBeLessThan(0.5);
    expect(flush.left).toBeLessThan(0.5);
    expect(flush.bottom).toBeGreaterThan(0.5);
    expect(flush.right).toBeGreaterThan(0.5);
    expectRingInside("prs section toggle", head, clipper);
  });

  it("on an account's repo-list toggle, flush on the inline-start edge in either state", async () => {
    // A full-bleed child of a `padding: 0` row inside a `padding: 0` clipping card.
    const { clipper, open, summary } = forgeAccountRow();
    await focusByTab(summary);
    const closed = inset(summary, clipper);
    expect(closed.left).toBeLessThan(0.5);
    expect(closed.bottom, "closed: a following box would take the block-end edge").toBeLessThan(
      0.5,
    );
    expectRingInside("account repos toggle (closed)", summary, clipper);

    open();
    await focusByTab(summary);
    const opened = inset(summary, clipper);
    expect(opened.left).toBeLessThan(0.5);
    expect(opened.bottom).toBeGreaterThan(0.5);
    expectRingInside("account repos toggle (open)", summary, clipper);
  });
});

describe("the inset band clears the content it now sits over", () => {
  // Moving the ring inward lands the band inside the border box, so a member's padding must exceed the offset.
  it("on every member, on all four edges", async () => {
    // All four edges: the band lands at the same depth everywhere.
    const builders: [string, () => HTMLElement][] = [
      ["leaf head", () => subagentCard({ foot: false }).head],
      ["container head", () => subagentContainer().head],
      ["run head", () => runCard().head],
      ["run step head", () => runStep().head],
      ["tool group header", () => toolGroup().head],
      ["code action button", () => codeBlock().copy],
      ["git section header", () => gitRepoSection().head],
      ["prs section toggle", () => gitRepoSectionPRs().head],
      ["account repos summary", () => forgeAccountRow().summary],
      // The tightest block padding in the list (4px against the band's 2px).
      ["status card account link", () => statusCardLink()],
    ];
    for (const [name, build] of builders) {
      const head = build();
      await focusByTab(head);
      const cs = getComputedStyle(head);
      // A negative offset puts the band that far inside, so its depth is the offset's magnitude.
      const depth = -px(cs.outlineOffset);
      expect(depth, `${name}: offset is not the inset token`).toBeGreaterThan(0);
      const first = head.firstElementChild;
      const last = head.lastElementChild;
      if (first === null || last === null) {
        throw new Error(`${name}: the fixture built no child to clear`);
      }
      const near = inset(first, head);
      const far = inset(last, head);
      expect(
        near.top,
        `${name}: the band reaches its first child at block-start`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        near.left,
        `${name}: the band reaches its first child at inline-start`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        far.bottom,
        `${name}: the band reaches its last child at block-end`,
      ).toBeGreaterThanOrEqual(depth);
      expect(
        far.right,
        `${name}: the band reaches its last child at inline-end`,
      ).toBeGreaterThanOrEqual(depth);
    }
  });
});

describe("the inset band clears the content it now sits over, on the two card feet", () => {
  // `firstElementChild` names the wrong child here: the ledger leads with its `.sr-only` name.
  it("clears the delegate ledger's `i` on the edge the gutter moved for", async () => {
    // 12px, shared with the turn card (29-turns.css), on the button rather than the footer.
    const { ledger } = subagentCard({ foot: true });
    if (ledger === null) {
      throw new Error("the fixture built no ledger button");
    }
    await focusByTab(ledger);
    const depth = -px(getComputedStyle(ledger).outlineOffset);
    expect(depth, "the ledger is not on the inset token").toBeGreaterThan(0);
    const info = ledger.querySelector<HTMLElement>(".turn-ledger-info");
    const text = ledger.querySelector<HTMLElement>(".turn-ledger-text");
    if (info === null || text === null) {
      throw new Error("the fixture built no ledger ink");
    }
    const near = inset(info, ledger);
    expect(near.left, "the band reaches the `i`").toBeGreaterThanOrEqual(depth);
    expect(near.top, "the band reaches the `i` at block-start").toBeGreaterThanOrEqual(depth);
    expect(
      inset(text, ledger).bottom,
      "the band reaches the outcome word at block-end",
    ).toBeGreaterThanOrEqual(depth);
  });

  it("clears `.run-open`'s label and its icon", async () => {
    // The tightest member: 4px inline padding against the 2px band.
    const { open } = runCardWithFoot();
    await focusByTab(open);
    const depth = -px(getComputedStyle(open).outlineOffset);
    expect(depth, "the link is not on the inset token").toBeGreaterThan(0);
    expect(px(getComputedStyle(open).paddingInlineStart)).toBeGreaterThanOrEqual(depth);
    expect(px(getComputedStyle(open).paddingInlineEnd)).toBeGreaterThanOrEqual(depth);
    // Block clearance is the link's centred line box, not padding.
    const label = open.firstChild;
    expect(label?.nodeType, "the link leads with its label text").toBe(Node.TEXT_NODE);
    const range = document.createRange();
    range.selectNodeContents(open);
    const ink = range.getBoundingClientRect();
    const box = open.getBoundingClientRect();
    expect(ink.top - box.top, "the band reaches the label at block-start").toBeGreaterThanOrEqual(
      depth,
    );
    expect(
      box.bottom - ink.bottom,
      "the band reaches the label at block-end",
    ).toBeGreaterThanOrEqual(depth);
  });
});

describe("the exclusions: a focusable whose clipper clears the reach", () => {
  it("pins the + New PR button, which shares a clipper with a member", async () => {
    const { clipper, newBtn } = gitRepoSectionPRs();
    await focusByTab(newBtn);
    expectClears("+ New PR", newBtn, clipper);
  });

  it("pins the account row's own controls, which the toggle beside them does not", async () => {
    // Decided per control: real padding insets some, a full-bleed toggle does not.
    const { clipper, open, signOut, cloneAll, repoBtn } = forgeAccountRow();
    await focusByTab(signOut);
    expect(inset(signOut, clipper).top).toBe(8);
    expectClears("account Sign out", signOut, clipper);
    await focusByTab(cloneAll);
    expect(inset(cloneAll, clipper).bottom).toBe(8);
    expectClears("account Clone all", cloneAll, clipper);
    open();
    await focusByTab(repoBtn);
    expect(inset(repoBtn, clipper).bottom).toBe(reachOf(repoBtn) + 1);
    expectClears("account repo Clone", repoBtn, clipper);
  });

  it("pins the changed-file list's controls", async () => {
    const { clipper, path, discard } = gitFileList();
    await focusByTab(path);
    expect(inset(path, clipper).top).toBe(14);
    expectClears("git file path", path, clipper);
    await focusByTab(discard);
    expect(inset(discard, clipper).top).toBe(8);
    expectClears("git file Discard", discard, clipper);
  });

  it("pins the diff pane's whitespace checkbox against its toolbar's padding", async () => {
    // The checkbox is the row's tallest item: clearance is `.diff-pane-toolbar`'s padding.
    const { clipper, toggle } = diffPane({ labelled: false });
    await focusByTab(toggle);
    expect(inset(toggle, clipper).top, "the toolbar's own padding-block").toBe(4);
    expectClears("whitespace checkbox", toggle, clipper);
  });

  it("keeps that number when the pane also carries column labels", async () => {
    // The label row is a sibling of the toolbar, so both shapes are one number.
    const { clipper, toggle } = diffPane({ labelled: true });
    await focusByTab(toggle);
    expect(inset(toggle, clipper).top).toBe(4);
    expectClears("whitespace checkbox (labelled pane)", toggle, clipper);
  });

  it("pins the tool card's disclosure, whose clearance is no padding at all", async () => {
    // The chevron is absolute inside `.tool-header`: a declared `inset-inline-start`.
    const { clipper, disclosure } = toolCard();
    await focusByTab(disclosure);
    const gap = inset(disclosure, clipper);
    expect(gap.left, "a declared inset-inline-start").toBe(12);
    expect(gap.top, "half the header's spare block room").toBe(6);
    expectClears("tool card disclosure", disclosure, clipper);
  });

  it("pins the MCP row's action buttons", async () => {
    // `.mcp-row`'s own padding plus the trailing gutter.
    const { clipper, del } = mcpRow();
    await focusByTab(del);
    const gap = inset(del, clipper);
    expect(gap.top).toBe(8);
    expect(gap.bottom).toBe(8);
    expectClears("mcp row Delete", del, clipper);
  });

  it("pins the model card's rows and tiers", async () => {
    // 8px inside the clip box on both sides.
    const { clipper, item, tier } = modelCard();
    await focusByTab(item);
    expect(inset(item, clipper).top).toBe(8);
    expectClears("model row", item, clipper);
    await focusByTab(tier);
    // 8: the row's own --sp-2; the knob fills the bar, which fills the line (15-input.css).
    expect(inset(tier, clipper).bottom).toBe(8);
    expectClears("effort tier", tier, clipper);
  });

  it("leaves a control outside every card alone", async () => {
    // Not `.ev-row`: `exec-view/tree.ts` builds those with `tabindex` so they are members.
    const plain = node("button", "btn", { type: "button" });
    plain.textContent = "unclipped";
    const bare = node("div", "focus-ring-clip-bare-host");
    bare.appendChild(plain);
    mount(bare);
    await focusByTab(plain);
    expect(getComputedStyle(plain).outlineOffset).toBe("1px");
    const own = ringOverflow(plain, plain);
    for (const edge of EDGES) {
      expect(own[edge], `unclipped control: ring is not outside at ${edge}`).toBeGreaterThan(0);
    }
  });
});

describe("the exclusion that is not a clearance: a row already on the inset token", () => {
  it("keeps `.turn-file-row`'s band inside the panel that clips it", async () => {
    // `.turn-info-panel` clips a spanning row; `29-turns.css` declares the same inset offset for it.
    const { clipper, row } = turnInfoPanel();
    await focusByTab(row);
    expect(getComputedStyle(clipper).overflow, "the info panel clips").toBe("clip");
    const gap = inset(row, clipper);
    for (const edge of ["top", "bottom", "left"] as const) {
      expect(
        gap[edge],
        `turn file row: ${edge} edge is outside the clip box (${gap[edge]}px)`,
      ).toBeGreaterThan(0);
    }
    expect(gap.right, "turn file row: flush with the clip box on the trailing edge").toBe(0);
    expect(reachOf(row), "the row's own offset cancels the ring's reach").toBe(0);
    expectRingInside("turn file row", row, clipper);
  });
});

/** The status card as `static/index.html` authors it; `overflow: auto` clips like hidden. */
function statusCard(): { clipper: HTMLElement; link: HTMLAnchorElement } {
  const clipper = node("span", "pill-expand-content pill-status-content is-open");
  clipper.append(
    span("pill-detail", "connected to marotte 1.2.3"),
    node("span", "pill-sep"),
    span("pill-detail", "kiro-cli 2.21.4"),
  );
  const link = node("a", "pill-account", {
    id: "st-account",
    href: "https://app.kiro.dev/account/usage",
    target: "_blank",
    rel: "noopener",
  });
  const lines = node("span", "pill-account-lines");
  lines.append(span("pill-account-plan", "KIRO POWER"), span("pill-account-meter", "412 / 1,000"));
  link.append(lines, glyph(), span("sr-only", "Open account usage at app.kiro.dev"));
  clipper.appendChild(link);
  const slot = node("span", "pill-slot");
  slot.appendChild(clipper);
  mount(slot);
  return { clipper, link };
}

function statusCardLink(): HTMLElement {
  return statusCard().link;
}

describe("the ring is painted inside the popup card that clips it", () => {
  it("on the account link, flush on both inline edges of the card", async () => {
    const { clipper, link } = statusCard();
    await focusByTab(link);
    expect(getComputedStyle(link).outlineOffset, "the link takes the inset offset").toBe("-2px");
    // Flush on both inline edges (the concentric-corner claim), with real block clearance.
    const gap = inset(link, clipper);
    expect(gap.left, "flush with the card's padding box at the leading edge").toBeLessThan(0.5);
    expect(gap.right, "flush at the trailing edge too").toBeLessThan(0.5);
    expect(gap.top, "the detail rows are above it").toBeGreaterThan(0.5);
    expect(reachOf(link), "the inset offset cancels the ring's reach").toBe(0);
    expectRingInside("account link", link, clipper);
  });

  it("clips at its padding box, which is what makes the offset necessary", () => {
    const { clipper } = statusCard();
    // `auto` clips identically, which is why this card is here.
    expect(getComputedStyle(clipper).overflow).toBe("auto");
    expect(getComputedStyle(clipper).paddingLeft, "and it has padding of its own").not.toBe("0px");
  });
});

describe("read as source: one rule, one owner for the ring", () => {
  it("declares the inset offset once for every member", () => {
    // `ruleContaining` requires one rule per selector: the members share one body.
    const bodies = new Set(
      MEMBERS.map((m) => ruleContaining(a11y, `${m}:focus-visible`, "top").body),
    );
    expect([...bodies]).toHaveLength(1);
  });

  it("declares the OFFSET and nothing else, so the floor keeps the ring", () => {
    const rule = ruleContaining(a11y, "a.subagent-header:focus-visible", "top");
    expect(rule.body).toMatch(/outline-offset:\s*var\(--focus-offset-inset\)/u);
    // A second spelling of the ring is `css-tokens.node.test.ts`'s to catch.
    expect(rule.body).not.toMatch(/(^|[^-])outline\s*:/u);
  });
});
