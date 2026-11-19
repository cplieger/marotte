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
  rowTrims,
} from "./__test-helpers__/alignment.js";
import {
  SURFACES,
  clearSurface,
  frames,
  mountSurface,
  plantToolSearch,
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
  /** A label's tolerance where the site's dev offset lies inside a centre's: the trimmed label's. */
  readonly labelTol?: number;
  /** Holds a label's cap centre, not its cap-to-x band, to this tolerance: the band takes in a
   *  label raised by a row font smaller than its own. */
  readonly capTol?: number;
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
    must: [/^line: .*run-name \| .*ev-sum-steps$/],
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
      /^line: .*run-name \| .*ev-sum-frac$/,
    ],
  },
  {
    id: "a file row's size and date",
    surface: surface("the file browser's rows"),
    frame: ".fb-row",
    sizes: [1180],
    must: [/^line: .*fb-name.* \| .*fb-meta$/],
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
    must: [/^line: .*list-row-name \| .*list-row-meta$/],
  },
  {
    id: "a chipless tool row's and the system row's name and version",
    surface: surface("Settings, Tools"),
    frame: "#tools-list .list-row:not(:has(.tool-hit-chips))",
    sizes: [1180, 390],
    must: [
      /^line: .*list-row > span\.list-row-name \| .*list-row > span\.list-row-meta$/,
      /^line: .*list-row-system > span\.list-row-name \| .*list-row-system > span\.list-row-meta$/,
    ],
  },
  {
    id: "a knowledge row's name and count",
    surface: surface("Settings, Knowledge"),
    frame: "#knowledge-list .list-row",
    sizes: [1180, 390],
    must: [/^line: .*list-row-name \| .*knowledge-meta$/],
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
    must: [/^label: .*pill-model-name$/, /^line: .*pill-model-name \| .*pill-model-meta$/],
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
    labelTol: BASELINE_PX,
  },
  {
    id: "a setting's label and value",
    surface: surface("Settings, General"),
    frame: ".section-title-status-row",
    sizes: [1180, 390],
    must: [/^label: .*section-option-label$/, /^label: .*auto-compact-pct-value$/],
    labelTol: BASELINE_PX,
  },
  {
    id: "the global instructions' title",
    surface: surface("Settings, Knowledge"),
    frame: '[data-settings-panel="instructions"] .section-title-status-row',
    sizes: [1180, 390],
    must: [/^label: .*section-title-status-row > label\.section-title$/],
    capTol: BASELINE_PX,
  },
  {
    id: "a workflow's schedule button",
    surface: surface("the workflow list"),
    frame: ".recipe-sched-btn",
    sizes: [1180],
    must: [/^label: .*recipe-sched-count$/],
    labelTol: BASELINE_PX,
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

const r2 = (n: number): number => Math.round(n * 100) / 100;

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
    // Each pair of neighbouring runs on one line of a row: on one baseline.
    ...rowTrims(document.body, opts)
      .flatMap((l) =>
        l.runs.slice(1).map((r, i) => ({
          what: `line: ${l.runs[i]!.sel} | ${r.sel}`,
          delta: r2(r.baseline - l.runs[i]!.baseline),
          tol: BASELINE_PX,
          el: r.el,
        })),
      )
      .filter((f) => inside(f.el))
      .map(({ what, delta, tol }) => ({ what, delta, tol })),
    // A lowered run is held to its row's baseline by the line pairs above, not to the centre.
    ...labelShapes(document.body, opts)
      .filter((l) => inside(l.el) && !l.lowered)
      .map((l) => ({
        what: `label: ${l.sel}`,
        delta: site.capTol === undefined ? labelOffset(l) : l.capOff,
        tol: site.capTol ?? site.labelTol ?? CENTRE_PX,
      })),
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
 *  and under the effort bar, or the Fallback row's last baseline (its hint's, when it shows one). */
function sectionGaps(): { section: string; top: number; bottom: number; pad: number }[] {
  return [...document.querySelectorAll<HTMLElement>(".model-panel-main > .effort-row")].map(
    (row) => {
      const r = row.getBoundingClientRect();
      const cs = getComputedStyle(row);
      const inner = r.top + parseFloat(cs.borderTopWidth);
      const knob = row.querySelector(".effort-knob");
      const text = row.querySelector(".effort-label, .fallback-open")!;
      const hint = row.querySelector(".section-hint:not([hidden])");
      const band = inkBand(text);
      const end =
        knob === null
          ? inkBand(hint ?? text).endBaseline
          : knob.getBoundingClientRect().bottom;
      return {
        section: knob === null ? "Fallback" : "effort",
        top: band.top - inner,
        bottom: r.bottom - end,
        pad: parseFloat(cs.paddingTop),
      };
    },
  );
}

const CARDS = [
  { surface: "the model popup", sections: ["effort", "Fallback"] },
  { surface: "the model popup without a fallback", sections: ["effort"] },
] as const;

describe("the model card's sections", () => {
  for (const vp of VPS) {
    for (const card of CARDS) {
      it(`leave as much air above their text as under it in ${card.surface} at ${String(vp.w)}x${String(vp.h)}`, async () => {
        await page.viewport(vp.w, vp.h);
        await mountSurface(surface(card.surface), vp.size, vp.tier);
        await frames();
        const gaps = sectionGaps();
        expect(gaps.map((g) => g.section)).toEqual(card.sections);
        // Equal air is the ink band centred between the edges, held to a centre's tolerance.
        for (const g of gaps) {
          expect(Math.abs(g.top - g.bottom) / 2, JSON.stringify(g)).toBeLessThanOrEqual(CENTRE_PX);
        }
        const effort = gaps[0]!;
        expect(
          Math.abs(effort.top - effort.pad),
          `the padding is the air: ${JSON.stringify(effort)}`,
        ).toBeLessThanOrEqual(CENTRE_PX);
        // The track is the slider's whole target: inside its section and the card's clip.
        const track = q(".effort-track").getBoundingClientRect();
        for (const box of [q(".effort-track").parentElement!, q(".pill-model-list")]) {
          const r = box.getBoundingClientRect();
          expect(
            [track.top >= r.top, track.bottom <= r.bottom],
            `${box.className}: ${JSON.stringify({ track, r })}`,
          ).toEqual([true, true]);
        }
      });
    }
  }

  it("wrap the Fallback hint inside the card: two lines with equal air on a phone, no wider a card on desktop", async () => {
    const [desktop, phone] = VPS;
    const hintLines = (): number => {
      const r = document.createRange();
      r.selectNodeContents(q(".model-panel-main .section-hint"));
      return new Set([...r.getClientRects()].map((b) => Math.round(b.top))).size;
    };
    await page.viewport(phone.w, phone.h);
    await mountSurface(surface("the model popup"), phone.size, phone.tier);
    await frames();
    expect(hintLines()).toBeGreaterThanOrEqual(2);
    const fallback = sectionGaps().find((g) => g.section === "Fallback")!;
    expect(Math.abs(fallback.top - fallback.bottom) / 2, JSON.stringify(fallback)).toBeLessThanOrEqual(
      CENTRE_PX,
    );

    await page.viewport(desktop.w, desktop.h);
    await mountSurface(surface("the model popup"), desktop.size, desktop.tier);
    await frames();
    const card = q("#model-switch-list");
    // At max-content too: the card's narrow anchor slot caps its laid-out width by itself.
    for (const size of ["", "max-content"]) {
      card.style.inlineSize = size;
      const hint = q(".model-panel-main .section-hint");
      hint.hidden = false;
      await frames();
      const shown = card.getBoundingClientRect().width;
      hint.hidden = true;
      await frames();
      expect(card.getBoundingClientRect().width, `inline-size: ${size}`).toBe(shown);
    }
  });
});

/** The rows whose every text run the alignment rule trims (15-input.css), each on a surface that
 *  draws it. */
const ROW_TYPES: readonly { row: string; surface: Surface; sizes: readonly Size[] }[] = [
  { row: ".list-row", surface: surface("Settings, Tools"), sizes: [1180, 390] },
  { row: ".list-row", surface: surface("Settings, Knowledge"), sizes: [1180, 390] },
  { row: ".fb-row", surface: surface("the file browser's rows"), sizes: [1180] },
  { row: ".turn-head-row", surface: TURN_HITS, sizes: [1180, 390] },
  { row: ".code-refs-item", surface: surface("a turn's code references"), sizes: [1180, 390] },
  { row: ".run-head", surface: surface("a run card"), sizes: [1180, 390] },
  { row: ".run-head", surface: surface("a run card's nested loop"), sizes: [1180, 390] },
  { row: ".history-row", surface: surface("the sidebar"), sizes: [1180, 390] },
  { row: ".section-title-status-row", surface: surface("Settings, General"), sizes: [1180, 390] },
  { row: ".recipe-sched-btn", surface: surface("the workflow list"), sizes: [1180] },
  { row: ".tool-diff-stats", surface: surface("a turn"), sizes: [1180, 390] },
];

// Every surface the sweep fixed a row on: no line there may set a trimmed run beside an
// untrimmed one, and each row type above shares one baseline across its runs.
const ROW_SURFACES = [
  ...new Set([...SITES.map((x) => x.surface), ...ROW_TYPES.map((x) => x.surface)]),
];

describe("every line of a row", () => {
  for (const vp of VPS) {
    for (const sf of ROW_SURFACES) {
      const types = ROW_TYPES.filter((t) => t.surface === sf && t.sizes.includes(vp.size));
      const sized = SITES.some((x) => x.surface === sf && x.sizes.includes(vp.size));
      if (types.length === 0 && !sized) {
        continue;
      }
      it(
        `trims every text run of a row or none, on one baseline, on ${sf.name} at ${String(vp.w)}x${String(vp.h)}`,
        { timeout: 60_000 },
        async () => {
          await page.viewport(vp.w, vp.h);
          await mountSurface(sf, vp.size, vp.tier);
          await frames();
          const lines = rowTrims(document.body, { skip: ".steer-row" });
          const mixed = lines
            .filter((l) => new Set(l.runs.map((r) => r.trimmed)).size > 1)
            .map(
              (l) =>
                `${l.sel}: ${l.runs.map((r) => `${r.sel}${r.trimmed ? " (trimmed)" : ""}`).join(", ")}`,
            );
          expect(mixed).toEqual([]);
          for (const t of types) {
            const own = lines.filter((l) => l.runs.every((r) => r.el.closest(t.row) !== null));
            expect(own.length, `a line of ${t.row}`).toBeGreaterThan(0);
            for (const l of own) {
              const ys = l.runs.map((r) => r.baseline);
              expect(
                Math.max(...ys) - Math.min(...ys),
                `${l.sel}: ${l.runs.map((r) => `${r.sel} @ ${String(r.baseline)}`).join(", ")}`,
              ).toBeLessThanOrEqual(BASELINE_PX);
            }
          }
        },
      );
    }
  }
});

const fileRows = surface("the file browser's rows");

/** A fine pointer's list too narrow for one line: the other arm of the stacked row (19-files.css). */
const NARROW_FILE_ROWS: Surface = {
  ...fileRows,
  name: "the file browser's rows in a narrow list",
  setup: async (size) => {
    await fileRows.setup(size);
    q(".fb-list-wrap").style.inlineSize = "24rem";
  },
};

/** A layout unit: Blink's 1/64 px, the finest step a box edge takes. */
const LAYOUT_UNIT_PX = 1 / 64;

describe("the stacked file row's meta line", () => {
  for (const [vp, sf] of [
    [VPS[0], NARROW_FILE_ROWS],
    [VPS[1], fileRows],
  ] as const) {
    it(`stays in the row's content box and holds its text at ${String(vp.w)}x${String(vp.h)}`, async () => {
      await page.viewport(vp.w, vp.h);
      await mountSurface(sf, vp.size, vp.tier);
      await frames();
      const metas = [...document.querySelectorAll<HTMLElement>("#fb-list .fb-row > .fb-meta")];
      expect(metas.length, "a file row's meta").toBeGreaterThan(0);
      const off: string[] = [];
      for (const meta of metas) {
        const row = meta.parentElement!;
        const rcs = getComputedStyle(row);
        expect(rcs.display, "the row stacks its meta under its name").toBe("grid");
        const rb = row.getBoundingClientRect();
        // The row's padding is sized so its two line boxes are its content height (19-files.css).
        const content = {
          top: rb.top + parseFloat(rcs.borderTopWidth) + parseFloat(rcs.paddingTop),
          bottom: rb.bottom - parseFloat(rcs.borderBottomWidth) - parseFloat(rcs.paddingBottom),
        };
        const box = meta.getBoundingClientRect();
        // The meta clips at its box (`overflow: hidden`), so its text's content area must fit it.
        const range = document.createRange();
        range.selectNodeContents(meta);
        const text = range.getClientRects()[0]!;
        const past = {
          "box below the row's content": box.bottom - content.bottom,
          "box above the row's content": content.top - box.top,
          "text below the box": text.bottom - box.bottom,
          "text above the box": box.top - text.top,
        };
        for (const [what, px] of Object.entries(past)) {
          if (px > LAYOUT_UNIT_PX) {
            off.push(`${meta.textContent}: ${what} by ${String(r2(px))} px`);
          }
        }
      }
      expect(off, `at DPR ${String(window.devicePixelRatio)}`).toEqual([]);
    });
  }
});

/** Turns the row trim (15-input.css) off: every trimmed box gets its untrimmed line box back. */
const UNTRIM_CSS =
  "[data-untrim] { text-box: normal !important; padding-block: 0 !important;" +
  " margin-block: 0 !important; min-block-size: auto !important; }";

const TOOL_SEARCH: Surface = {
  ...surface("Settings, Tools"),
  name: "the Add-tool search",
  setup: plantToolSearch,
};

const hitHeights = (): number[] =>
  [...document.querySelectorAll(".tool-hit")].map((h) => h.getBoundingClientRect().height);

describe("the Add-tool search's hit rows", () => {
  for (const vp of VPS) {
    it(`keep their untrimmed height at ${String(vp.w)}x${String(vp.h)}`, async () => {
      await page.viewport(vp.w, vp.h);
      await mountSurface(TOOL_SEARCH, vp.size, vp.tier);
      await frames();
      const got = hitHeights();
      expect(got.length, "the search's hits").toBe(3);
      const trimmed = [...document.querySelectorAll(".tool-hit *")].filter(
        (e) => getComputedStyle(e).getPropertyValue("text-box-trim") !== "none",
      );
      expect(trimmed.length, "the trim reaches the hits").toBeGreaterThan(0);
      const untrim = document.createElement("style");
      untrim.textContent = UNTRIM_CSS;
      document.head.append(untrim);
      for (const e of trimmed) {
        e.setAttribute("data-untrim", "");
      }
      await frames();
      const want = hitHeights();
      untrim.remove();
      for (const e of trimmed) {
        e.removeAttribute("data-untrim");
      }
      // Exact to a layout unit: a box edge's finest step.
      expect(
        got.filter((h, i) => Math.abs(h - want[i]!) >= LAYOUT_UNIT_PX),
        `at DPR ${String(window.devicePixelRatio)}, untrimmed ${want.join(", ")} px`,
      ).toEqual([]);
    });
  }
});
