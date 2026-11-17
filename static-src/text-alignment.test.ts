// Every row the alignment sweep fixed, on its real surface and stylesheet: two text runs side by side
// share a baseline, an icon or a native control sits on its text's centre line, and a label or a
// box's one line of text sits on its row's or box's centre. Each site is checked whole: every
// measurement `__test-helpers__/alignment.ts` takes inside it, in both themes. CI runs it in Chromium
// at a ratio of 1; `ALIGN_ENGINES=1` adds Chromium at 2 and WebKit at 1 and 2 (vitest.config.ts).
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { labelOffset, labelShapes, measureAlignment } from "./__test-helpers__/alignment.js";
import {
  SURFACES,
  clearSurface,
  frames,
  mountSurface,
  q,
  type Size,
  type Surface,
} from "./__test-helpers__/app-surfaces.js";

const { renderRegistryResult } = await import("./mcp-panels-search.js");
const { openModal } = await import("./modals.js");

/** Two runs on one strut or one baseline-sharing line meet to a rounding; box centring leaves
 *  them 0.4 to 1.7 px apart in this suite's fonts. */
const BASELINE_PX = 0.25;
/** A centre against a device-pixel grid: half a CSS pixel at a ratio of 1. */
const CENTRE_PX = 0.5;

const VPS = [
  { w: 1440, h: 900, size: 1180, tier: "fine" },
  { w: 390, h: 844, size: 390, tier: "coarse" },
] as const;
const THEMES = ["dark", "light"] as const;

const surface = (name: string): Surface => {
  const s = SURFACES.find((x) => x.name === name);
  if (s === undefined) {
    throw new Error(`no surface "${name}"`);
  }
  return s;
};

const mcpDialog = surface("the MCP dialog");
const permissions = surface("Settings, Permissions");

/** A registry entry carrying every piece the result row can show: version and status badge. */
const DEPRECATED_RESULT: Surface = {
  ...mcpDialog,
  name: "the MCP dialog, a deprecated versioned result",
  setup: (size) => {
    mcpDialog.setup(size);
    q("#mcp-search-results").replaceChildren(
      renderRegistryResult({
        name: "io.github.example/github",
        title: "GitHub",
        version: "1.4.2",
        status: "deprecated",
        description: "Issues, pull requests and code search over the GitHub API",
        remotes: [{ type: "streamable-http", url: "https://example.invalid/mcp" }],
      } as Parameters<typeof renderRegistryResult>[0]),
    );
    openModal(q<HTMLDivElement>("#mcp-modal"));
  },
};

/** Read-only rules with globs, in `permissions-ui.ts` `ruleRow`'s shape. */
const GLOB_RULES: Surface = {
  ...permissions,
  name: "Settings, Permissions, a rule with globs",
  setup: (size) => {
    permissions.setup(size);
    const span = (cls: string, t: string): HTMLSpanElement => {
      const e = document.createElement("span");
      e.className = cls;
      e.textContent = t;
      return e;
    };
    for (const [eff, cap, globs, src] of [
      ["allow", "fs_read", "/workspace/**  /data/**", "default"],
      ["deny", "shell", "rm -rf *", "~/.kiro/settings/cli.json"],
    ] as const) {
      const row = document.createElement("div");
      row.className = `native-rule native-rule-${eff}`;
      const words = span("native-rule-text", "");
      words.append(
        span("native-rule-cap mono", cap),
        span("native-rule-globs mono", globs),
        span("native-rule-src", src),
      );
      row.append(span(`native-rule-effect eff-${eff}`, eff), words);
      q("#native-policy-list").appendChild(row);
    }
  },
};

const question = surface("a question");

/** An option the agent recommends, its badge in `user-input.ts` `optionCard`'s shape. */
const RECOMMENDED: Surface = {
  ...question,
  name: "a question with a recommended option",
  setup: (size) => {
    question.setup(size);
    const badge = document.createElement("span");
    badge.className = "user-input-recommended";
    badge.textContent = "Recommended";
    q(".user-input-option-head").appendChild(badge);
  },
};

interface Site {
  readonly id: string;
  readonly surface: Surface;
  /** Every element the site's measurements must fall inside. */
  readonly frame: string;
  readonly sizes: readonly Size[];
  /** Measurements that must be taken, so a site that stopped rendering cannot pass: each matches
   *  `<kind>: <key>`, a label's key being its selector. */
  readonly must: readonly RegExp[];
}

const SITES: readonly Site[] = [
  {
    id: "the MCP result's name, version, badge and description",
    surface: DEPRECATED_RESULT,
    frame: ".mcp-result-summary",
    sizes: [1180, 390],
    must: [/^box-text: .*mcp-result-status/],
  },
  {
    id: "the MCP result's name and description",
    surface: mcpDialog,
    frame: ".mcp-result-summary",
    sizes: [1180, 390],
    must: [/^baseline: .*mcp-result-name \| .*mcp-result-desc/],
  },
  {
    id: "a policy rule's capability, globs and source",
    surface: GLOB_RULES,
    frame: ".native-rule",
    sizes: [1180],
    must: [/^baseline: .*native-rule-cap\.mono \| .*native-rule-globs/],
  },
  {
    id: "a tool row's name and source chip",
    surface: surface("Settings, Tools"),
    frame: "#tools-list .list-row",
    sizes: [1180, 390],
    must: [/^baseline: .*list-row-name \| .*tool-source-chip/, /^box-text: .*tool-source-chip/],
  },
  {
    id: "an attachment pill's clip and name",
    surface: surface("the composer"),
    frame: ".attachment-open",
    sizes: [1180, 390],
    must: [/^baseline: .*attachment-icon \| .*attachment-name/],
  },
  {
    id: "a chat option's glyph and checkbox",
    surface: surface("the chat options popup"),
    frame: ".chat-opt-btn, .chat-opt-row",
    sizes: [1180, 390],
    must: [/^icon-text: .*chat-opt-icon > svg/, /^icon-text: .*chat-opt-row > input/],
  },
  {
    id: "a merge method's radio",
    surface: surface("the merge dialog"),
    frame: ".pr-merge-method",
    sizes: [1180, 390],
    must: [/^icon-text: .*pr-merge-method > input/],
  },
  {
    id: "a question's sub-option checkbox",
    surface: surface("a question's sub-options"),
    frame: ".user-input-sub-option",
    sizes: [1180, 390],
    must: [/^icon-text: .*user-input-sub-option > input/],
  },
  {
    id: "a question's option title",
    surface: question,
    frame: ".user-input-option",
    sizes: [1180, 390],
    must: [/^box-text: .*button\.user-input-option :: /],
  },
  {
    id: "a recommended option's title and badge",
    surface: RECOMMENDED,
    frame: ".user-input-option-head",
    sizes: [1180, 390],
    must: [/^baseline: .*user-input-option-title \| .*user-input-recommended/],
  },
  {
    id: "a file decision row's path",
    surface: surface("a turn's file decisions"),
    frame: ".dock-file-row",
    sizes: [1180, 390],
    must: [/^box-text: .*li\.dock-file-row :: /],
  },
  {
    id: "a diff row's line number",
    surface: surface("a long edit's diff"),
    frame: ".diff-row",
    sizes: [1180, 390],
    must: [/^box-text: .*diff-row-(add|del) :: .*diff-gutter/],
  },
  {
    id: "a turn header's number",
    surface: surface("a turn"),
    frame: ".turn-head-row",
    sizes: [1180, 390],
    must: [/^label: .*turn-head-row > span\.turn-n$/],
  },
  {
    id: "an edit's +N -N stats",
    surface: surface("a turn"),
    frame: ".tool-diff-stats",
    sizes: [1180, 390],
    must: [/^label: .*diff-add-count$/, /^label: .*diff-del-count$/],
  },
  {
    id: "a code reference's licence",
    surface: surface("a turn's code references"),
    frame: ".code-refs-item",
    sizes: [1180, 390],
    must: [/^label: .*code-refs-license$/],
  },
  {
    id: "a run card's step fraction",
    surface: surface("a run card"),
    frame: ".ev-sum-progress",
    sizes: [1180, 390],
    must: [/^label: .*ev-sum-steps$/],
  },
  {
    id: "a file row's size and date",
    surface: surface("the file browser's rows"),
    frame: ".fb-row",
    sizes: [1180],
    must: [/^label: .*fb-meta$/],
  },
  {
    id: "a pull request row's number",
    surface: surface("the git page's pull requests"),
    frame: ".git-pr-row-title",
    sizes: [1180, 390],
    must: [/^baseline: .*git-pr-row-number \| .*git-pr-row-text/],
  },
  {
    id: "a tool row's version",
    surface: surface("Settings, Tools"),
    frame: "#tools-list .list-row",
    sizes: [1180, 390],
    must: [/^label: .*list-row-meta$/],
  },
  {
    id: "a doc entry's title",
    surface: surface("the Docs page"),
    frame: ".entry-name",
    sizes: [1180, 390],
    must: [/^label: .*entry-title$/],
  },
  {
    id: "the run dock's +N button",
    surface: surface("the run tab's dock rows"),
    frame: ".ev-dock-more",
    sizes: [390],
    must: [/^box-text: .*ev-dock-more :: /],
  },
];

function setTheme(t: (typeof THEMES)[number]): void {
  if (t === "dark") {
    document.documentElement.removeAttribute("data-theme");
  } else {
    document.documentElement.dataset["theme"] = t;
  }
}

/** Every measurement inside the site's frames, keyed `<kind>: <key>`, with its signed offset. */
function measureSite(site: Site): { what: string; delta: number; tol: number }[] {
  const frames = [...document.querySelectorAll(site.frame)];
  const inside = (e: Element): boolean => frames.some((f) => f.contains(e));
  // The holding area's rows have their own suite (steer-row-align.test.ts). A phone's file
  // decision row is 52px: its 44px diff button inside the row's padding.
  const opts = { skip: ".steer-row", maxBox: 56 };
  return [
    ...measureAlignment(document.body, opts)
      .filter((f) => inside(f.frame))
      .map((f) => ({
        what: `${f.kind}: ${f.key}`,
        delta: f.delta,
        tol: f.kind === "baseline" ? BASELINE_PX : CENTRE_PX,
      })),
    ...labelShapes(document.body, opts)
      .filter((l) => inside(l.el))
      .map((l) => ({ what: `label: ${l.sel}`, delta: labelOffset(l), tol: CENTRE_PX })),
  ];
}

let style: HTMLStyleElement;
beforeAll(() => {
  style = mountAppCSS();
});
afterAll(async () => {
  clearSurface();
  style.remove();
  setTheme("dark");
  await page.viewport(1280, 720);
});

describe("text and icon alignment", () => {
  for (const vp of VPS) {
    for (const site of SITES) {
      if (!site.sizes.includes(vp.size)) {
        continue;
      }
      it(`${site.id} at ${String(vp.w)}x${String(vp.h)}`, { timeout: 60_000 }, async () => {
        await page.viewport(vp.w, vp.h);
        const off: string[] = [];
        for (const theme of THEMES) {
          setTheme(theme);
          await mountSurface(site.surface, vp.size, vp.tier);
          await frames();
          const got = measureSite(site);
          for (const re of site.must) {
            expect(
              got.some((m) => re.test(m.what)),
              `${theme}: a measurement matching ${String(re)}`,
            ).toBe(true);
          }
          for (const m of got) {
            if (Math.abs(m.delta) > m.tol) {
              off.push(`${theme}: ${m.what} is ${String(m.delta)} px (within ${String(m.tol)})`);
            }
          }
        }
        expect(off, `at DPR ${String(window.devicePixelRatio)}`).toEqual([]);
      });
    }
  }
});
