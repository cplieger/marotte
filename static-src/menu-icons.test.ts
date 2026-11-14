// The navigation glyphs in static/index.html and their icons.ts twins are one drawing per concept. A glyph with no
// registry owner is absent from the table, having nothing to disagree with.
import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import {
  ICON_PR_EMPTY,
  ICON_REPO,
  ICON_SUBAGENT_INTROSPECT,
  ICON_TAB_AGENT,
  ICON_TAB_CHAT,
  ICON_EXTERNAL,
  ICON_TAB_DOCS,
  ICON_TAB_FILES,
  ICON_TAB_GIT,
  ICON_TAB_RUN,
  ICON_TAB_SETTINGS,
  ICON_TAB_SPEC,
  ICON_TOOL_TERMINAL,
  toolIcon,
} from "./icons.js";
import { loadedResources } from "./tool-schema.js";

/** Whitespace runs only: inside path data whitespace separates coordinates. */
function norm(markup: string): string {
  return markup.replace(/\s+/g, " ").trim();
}

/** The <svg> attributes legitimately differ (index.html adds a class and aria-hidden), so only the children are compared. */
function inner(svg: string): string {
  return norm(svg.replace(/^<svg\b[^>]*>/, "").replace(/<\/svg>$/, ""));
}

/**
 * Scoped to the anchor's element, so a reordered markup cannot pick up a sibling's glyph. `close` because the account
 * row is an `<a>`; without it the slice would run on and match the logout button by accident.
 */
function glyphOf(anchor: string, close = "</button>"): string {
  const at = indexHtml.indexOf(anchor);
  expect(at, `static/index.html has no ${anchor}`).toBeGreaterThan(-1);
  const end = indexHtml.indexOf(close, at);
  expect(end, `${anchor} has no ${close}`).toBeGreaterThan(at);
  const host = indexHtml.slice(at, end);
  const m = /<svg\b[\s\S]*?<\/svg>/.exec(host);
  expect(m, `${anchor} carries no inline svg`).not.toBeNull();
  return inner(m?.[0] ?? "");
}

/** The glyph a `load_resource` card draws for one id. */
function loadIcon(id: string): string {
  return toolIcon({
    kind: "other",
    toolName: "load_resource",
    loaded: loadedResources("load_resource", { id }),
  });
}

/** `</button>` by default; the account link takes `</a>`. */
const PAIRS: readonly (readonly [
  label: string,
  anchor: string,
  registry: string,
  close?: string,
])[] = [
  // The button and the tab it opens are one destination.
  ["sidebar Kiro docs", 'id="docs-btn"', ICON_TAB_DOCS],
  ["sidebar Files", 'id="files-btn"', ICON_TAB_FILES],
  ["sidebar Git", 'id="git-btn"', ICON_TAB_GIT],
  ["toolbar Settings", 'id="settings-btn"', ICON_TAB_SETTINGS],
  // The shell toggle is not here (see the divergence test). Docs categories the app already draws elsewhere.
  ["docs tab Steering", 'data-docs-tab="steering"', toolIcon({ kind: "steering", toolName: "" })],
  ["docs tab Hooks", 'data-docs-tab="hooks"', toolIcon({ kind: "hook", toolName: "" })],
  ["docs tab Skills", 'data-docs-tab="skills"', ICON_SUBAGENT_INTROSPECT],
  ["docs tab Agents", 'data-docs-tab="agents"', ICON_TAB_AGENT],
  // A Tool Load card draws the tab of the document it loaded (a skill load: the test below).
  ["docs tab Powers", 'data-docs-tab="powers"', loadIcon("power::x::overview")],
  ["docs tab Specs", 'data-docs-tab="specs"', ICON_TAB_SPEC],
  ["docs tab Workflows", 'data-docs-tab="workflows"', ICON_TAB_RUN],
  // History's panes name what a chat tab and a run tab draw.
  ["history tab Chats", 'data-history-tab="chats"', ICON_TAB_CHAT],
  ["history tab Runs", 'data-history-tab="runs"', ICON_TAB_RUN],
  // The PR empty state renders inside the tab whose icon names it.
  ["git tab Pull requests", 'data-git-tab="prs"', ICON_PR_EMPTY],
  // The account row leaves the app, so it takes ICON_EXTERNAL; `</a>` scopes the slice to the link.
  ["status card account link", 'id="st-account"', ICON_EXTERNAL, "</a>"],
];

describe("hand-authored glyphs in static/index.html", () => {
  for (const [label, anchor, registry, close] of PAIRS) {
    it(`${label} draws the icons.ts glyph`, () => {
      expect(glyphOf(anchor, close)).toBe(inner(registry));
    });
  }

  // Two entries of one menu may not draw one mark, owner or not.
  for (const [bar, attr] of [
    ["Settings", "data-settings-tab"],
    ["docs", "data-docs-tab"],
    ["git", "data-git-tab"],
    ["history", "data-history-tab"],
  ] as const) {
    it(`gives every ${bar} tab a distinct glyph`, () => {
      const tabs = [...indexHtml.matchAll(new RegExp(`${attr}="([^"]+)"`, "g"))].map((m) => m[1]);
      expect(tabs.length).toBeGreaterThan(1);
      const byGlyph = new Map<string, string[]>();
      for (const tab of tabs) {
        const g = glyphOf(`${attr}="${tab}"`);
        byGlyph.set(g, [...(byGlyph.get(g) ?? []), tab ?? ""]);
      }
      const shared = [...byGlyph.values()].filter((names) => names.length > 1);
      expect(shared, `${bar} tabs sharing one glyph`).toEqual([]);
    });
  }

  // Every glyph the bar can show counts: both toggles swap at runtime.
  it("gives every glyph in the sidebar header a distinct mark", () => {
    const at = indexHtml.indexOf('class="sidebar-header-actions"');
    expect(at, "static/index.html has no sidebar-header-actions").toBeGreaterThan(-1);
    const bar = indexHtml.slice(at, indexHtml.indexOf("</div>", at));
    const glyphs = [...bar.matchAll(/<svg\b[\s\S]*?<\/svg>/g)].map((m) => inner(m[0]));
    // Moon, sun, system half-disc, mouse, hand, close.
    expect(glyphs).toHaveLength(6);
    expect(new Set(glyphs).size, "sidebar-header glyphs sharing one mark").toBe(glyphs.length);
  });

  // The one sanctioned divergence: the toolbar draws a boxed prompt, because a bare `>_` reads short and high among boxed
  // neighbours.
  it("keeps the shell toggle's prompt in a box, unlike the bare tool glyph", () => {
    const shell = glyphOf('id="shell-btn"');
    expect(shell, "the frame is the whole point of the divergence").toContain("<rect");
    expect(shell).not.toBe(inner(ICON_TOOL_TERMINAL));
    expect(inner(ICON_TOOL_TERMINAL), "the tool glyph stays bare").not.toContain("<rect");
  });

  // At 16px a 3-unit step is 2 CSS px, so strokes on multiples of 3 share one pixel phase.
  it("keeps the shell box's strokes on the 3-unit grid", () => {
    const rect = /<rect\b[^>]*>/.exec(glyphOf('id="shell-btn"'))?.[0] ?? "";
    const num = (attr: string): number =>
      Number.parseFloat(new RegExp(`${attr}="([\\d.]+)"`).exec(rect)?.[1] ?? "NaN");

    const x = num("x");
    const y = num("y");
    for (const [name, edge] of [
      ["left", x],
      ["right", x + num("width")],
      ["top", y],
      ["bottom", y + num("height")],
    ] as const) {
      expect(edge % 3, `${name} edge of the shell box`).toBe(0);
    }
    expect(num("width"), "the shell box is square").toBe(num("height"));
  });

  // The browser and the read family share one drawing on purpose: Lucide `library-big` (lucide-icons/lucide
  // icons/library-big.svg, minus its stroke-width, which 03-base.css owns). Read is resolved through `toolIcon`, since
  // a title override would defeat the share without the constant moving.
  it("draws Lucide library-big for the docs button, the docs tab and the read family", () => {
    const libraryBig =
      '<rect width="8" height="18" x="3" y="3" rx="1"/><path d="M7 3v18"/>' +
      '<path d="M20.4 18.9c.2.5-.1 1.1-.6 1.3l-1.9.7c-.5.2-1.1-.1-1.3-.6L11.1 5.1c-.2-.5.1-1.1.6-1.3l1.9-.7c.5-.2 1.1.1 1.3.6Z"/>';
    expect(glyphOf('id="docs-btn"'), "the sidebar button").toBe(libraryBig);
    expect(inner(ICON_TAB_DOCS), "the docs tab").toBe(libraryBig);
    expect(inner(toolIcon({ kind: "read", toolName: "" })), "the read family").toBe(libraryBig);
  });

  // Two spellings of one bolt pass the distinct-glyph test above, so the marks themselves are pinned: Skills is Lucide
  // `graduation-cap` (lucide-icons/lucide icons/graduation-cap.svg, minus stroke-width), Powers keeps its bolt.
  it("draws a graduation cap for Skills and keeps the bolt for Powers", () => {
    const graduationCap =
      '<path d="M21.42 10.922a1 1 0 0 0-.019-1.838L12.83 5.18a2 2 0 0 0-1.66 0L2.6 9.08a1 1 0 0 0 0 1.832l8.57 3.908a2 2 0 0 0 1.66 0z"/>' +
      '<path d="M22 10v6"/><path d="M6 12.5V16a6 3 0 0 0 12 0v-3.5"/>';
    const zap =
      '<path d="M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63' +
      'l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z"/>';
    expect(glyphOf('data-docs-tab="skills"'), "the Skills tab").toBe(graduationCap);
    expect(glyphOf('data-docs-tab="powers"'), "the Powers tab").toBe(zap);
  });

  it("draws the Skills tab's constant on a skill load card", () => {
    expect(loadIcon("skill::x")).toBe(ICON_SUBAGENT_INTROSPECT);
  });

  // Introspect's old book was indistinguishable from the read glyph at 16px in one transcript.
  it("keeps the introspect subagent out of the book family", () => {
    expect(inner(ICON_SUBAGENT_INTROSPECT), "the library").not.toBe(inner(ICON_TAB_DOCS));
    expect(inner(ICON_SUBAGENT_INTROSPECT), "nor the closed book").not.toBe(inner(ICON_REPO));
  });
});
