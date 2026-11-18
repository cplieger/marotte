// Every row the alignment sweep fixed, on its real surface and stylesheet: two text runs side by side
// share a baseline, an icon or a native control sits on its text's centre line, and a label or a
// box's one line of text sits on its row's or box's centre. Each site is checked whole: every
// measurement `__test-helpers__/alignment.ts` takes inside it, in both themes. CI runs it in Chromium
// at ratios 1 and 2; `ALIGN_ENGINES=1` adds WebKit at 1 and 2 (vitest.config.ts).
import { describe, it, expect, beforeAll, afterAll } from "vitest";
import { page } from "vitest/browser";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import {
  inkBand,
  labelOffset,
  labelShapes,
  measureAlignment,
} from "./__test-helpers__/alignment.js";
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

/** A registry entry carrying every piece the result row can show: version and status badge. */
const DEPRECATED_RESULT: Surface = {
  ...mcpDialog,
  name: "the MCP dialog, a deprecated versioned result",
  setup: async (size) => {
    await mcpDialog.setup(size);
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

const aTurn = surface("a turn");

/** A turn while a search runs: `messages.ts` `setHitCount` writes the count into the header. */
const TURN_HITS: Surface = {
  ...aTurn,
  name: "a turn with search hits",
  setup: async (size) => {
    await aTurn.setup(size);
    q(".turn-hit-count").textContent = "3";
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
    surface: surface("Settings, Permissions"),
    frame: ".native-rule",
    sizes: [1180],
    must: [
      /^baseline: .*native-rule-cap\.mono \| .*native-rule-globs/,
      /^baseline: .*native-rule-cap\.mono \| .*native-rule-src/,
    ],
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
    surface: surface("a question"),
    frame: ".user-input-option",
    sizes: [1180, 390],
    must: [/^box-text: .*button\.user-input-option :: /],
  },
  {
    id: "a recommended option's title and badge",
    surface: surface("a question with a recommended option"),
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
    id: "a turn header's number, time and hit count",
    surface: TURN_HITS,
    frame: ".turn-head-row",
    sizes: [1180, 390],
    must: [
      /^label: .*turn-head-row > span\.turn-n$/,
      /^label: .*turn-head-row > time\.turn-ts$/,
      /^label: .*turn-head-row > span\.turn-hit-count$/,
      /^baseline: .*turn-ts \| .*turn-hit-count/,
    ],
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
    must: [
      /^label: .*code-refs-license$/,
      /^baseline: .*code-refs-license \| .*code-refs-link-text/,
    ],
  },
  {
    id: "a run card's step fraction",
    surface: surface("a run card"),
    frame: ".ev-sum-progress",
    sizes: [1180, 390],
    must: [/^label: .*ev-sum-steps$/],
  },
  {
    id: "a loop's pass, inner pass and step fraction",
    surface: surface("a run card's nested loop"),
    frame: ".ev-sum-progress",
    sizes: [1180, 390],
    must: [
      /^baseline: .*ev-sum-frac \| .*ev-sum-inner/,
      /^baseline: .*ev-sum-inner \| .*ev-sum-dot/,
      /^baseline: .*ev-sum-dot \| .*ev-sum-steps/,
    ],
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
    id: "the model card's Fallback row",
    surface: surface("the model popup"),
    frame: ".fallback-open",
    sizes: [1180, 390],
    must: [/^baseline: .*pill-model-name \| .*pill-model-meta/, /^icon-text: .*disclosure-chevron/],
  },
  {
    id: "the model card's model rows",
    surface: surface("the model popup"),
    frame: ".pill-model-scroll .pill-model-item",
    sizes: [1180, 390],
    must: [/^label: .*pill-model-name$/, /^label: .*pill-model-meta$/],
  },
  {
    id: "the role card's name, shadow and scope",
    surface: surface("the role popup"),
    frame: ".pill-role-item",
    sizes: [1180, 390],
    must: [
      /^baseline: .*pill-role-name \| .*pill-role-shadow/,
      /^baseline: .*pill-role-shadow \| .*pill-role-scope/,
    ],
  },
  {
    id: "a closed chat's name and time",
    surface: surface("the sidebar"),
    frame: ".history-row",
    sizes: [1180, 390],
    must: [/^label: .*history-row-name$/, /^label: .*history-row-time$/],
  },
  {
    id: "a setting's label and value",
    surface: surface("Settings, General"),
    frame: ".section-title-status-row",
    sizes: [1180, 390],
    must: [/^label: .*section-option-label$/, /^label: .*auto-compact-pct-value$/],
  },
  {
    id: "a workflow's schedule button",
    surface: surface("the workflow list"),
    frame: ".recipe-sched-btn",
    sizes: [1180],
    must: [/^label: .*recipe-sched-count$/],
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

/** The air between each model card section's edges and its ink: above the first line's cap top,
 *  and under the effort bar or the Fallback row's baseline. */
function sectionGaps(): { section: string; top: number; bottom: number; pad: number }[] {
  return [...document.querySelectorAll<HTMLElement>(".model-panel-main > .effort-row")].map(
    (row) => {
      const r = row.getBoundingClientRect();
      const cs = getComputedStyle(row);
      const inner = r.top + parseFloat(cs.borderTopWidth);
      const knob = row.querySelector(".effort-knob");
      const text = row.querySelector(".effort-label, .fallback-open")!;
      const band = inkBand(text);
      const end = knob === null ? band.baseline : knob.getBoundingClientRect().bottom;
      return {
        section: knob === null ? "Fallback" : "effort",
        top: band.top - inner,
        bottom: r.bottom - end,
        pad: parseFloat(cs.paddingTop),
      };
    },
  );
}

describe("the model card's sections", () => {
  for (const vp of VPS) {
    it(`leave as much air above their text as under it at ${String(vp.w)}x${String(vp.h)}`, async () => {
      await page.viewport(vp.w, vp.h);
      await mountSurface(surface("the model popup"), vp.size, vp.tier);
      await frames();
      const gaps = sectionGaps();
      expect(gaps.map((g) => g.section)).toEqual(["effort", "Fallback"]);
      // Equal air is the ink band centred between the edges, held to a centre's tolerance.
      for (const g of gaps) {
        expect(Math.abs(g.top - g.bottom) / 2, JSON.stringify(g)).toBeLessThanOrEqual(CENTRE_PX);
      }
      const effort = gaps[0]!;
      expect(
        Math.abs(effort.top - effort.pad),
        `the padding is the air: ${JSON.stringify(effort)}`,
      ).toBeLessThanOrEqual(CENTRE_PX);
    });
  }
});
