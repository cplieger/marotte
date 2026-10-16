// The run page's LAYOUT contract, as CSS rules rather than as rendered pixels.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import { loadCSS, ruleBody, allRules, mountAppCSS } from "./__test-helpers__/css-rules.js";

/** A rule's DECLARATIONS, with its comments stripped. Required by every negative assertion
 *  below: `ruleBody` returns the authored text, and each of these rules explains in prose which
 *  declaration it does not carry — so a bare `not.toMatch(/overflow: hidden/)` reads the
 *  comment and fails on the fix. */
function decls(css: string, selector: string): string {
  return ruleBody(css, selector).replace(/\/\*[\s\S]*?\*\//g, " ");
}

/** Every `overflow*` declaration a rule carries, in source order. The EXACT set rather than a
 *  substring match, because both defects this pins are invisible to one: `overflow: hidden` is a
 *  prefix of `overflow: hidden auto`, so a negative substring assertion fails on the correct
 *  value, and two competing declarations in one rule is precisely the shape that shipped. */
function overflowDecls(css: string, selector: string): string[] {
  return [...decls(css, selector).matchAll(/^\s*(overflow[a-z-]*\s*:[^;]+);/gm)].map((m) =>
    (m[1] ?? "").replace(/\s+/g, " ").trim(),
  );
}

/** Both exec tabs share one rule per box (18-pages.css), so each is addressed as the pair. */
const SCROLLER = '[id="run-view"] .page-content,\n[id="subagent-view"] .page-content';
const BODY = '[id="run-body"],\n[id="subagent-body"]';

describe("the run page claims its height", () => {
  // The omission itself. Every full-page view is in this list; a new one that is not renders inside
  // a wrapper with no height and looks like a styling bug.
  const heightSet = [
    '[id="chat-view"]',
    '[id="settings-view"]',
    '[id="git-view"]',
    '[id="editor-view"]',
    '[id="files-view"]',
    '[id="web-view"]',
    '[id="history-view"]',
    '[id="run-view"]',
    '[id="subagent-view"]',
    '[id="spec-view"]',
    '[id="docs-view"]',
  ];

  it("puts run-view in the view-height set with every other full-page view", () => {
    const body = ruleBody(loadCSS("16-login.css"), heightSet.join(",\n"));
    expect(/flex:\s*1/.test(body)).toBe(true);
    // Without `min-height: 0` a flex child refuses to shrink below its content, so the growing
    // region would push the pinned bottom bar off-screen instead of scrolling.
    expect(/min-height:\s*0/.test(body)).toBe(true);
  });

  // THE PAGE SCROLLS, and it is `.page-content` that scrolls rather than the view, because
  // `.run-bottom-bar` is pinned outside it.
  it("scrolls the page wrapper, not the panes", () => {
    // `overflow: hidden auto` is the two-value shorthand: `hidden` across, `auto` down. Exactly one
    // declaration, so no plain `overflow: hidden` can sit beside it.
    expect(overflowDecls(loadCSS("18-pages.css"), SCROLLER)).toEqual(["overflow: hidden auto"]);

    // A pane with an `overflow` on either axis is a scroll container again, and a scroll
    // container's automatic minimum size is 0.
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-pane")).toEqual([]);
  });

  // One block owns `overflow` for both tabs: two contradictory blocks at equal specificity in one
  // file let the later declaration silently win.
  it("declares the scroller exactly once, for both tabs", () => {
    const css = loadCSS("18-pages.css");
    const hits = allRules(css).filter(
      (r) => r.selector.includes(".page-content") && /run-view|subagent-view/.test(r.selector),
    );
    expect(hits.map((r) => r.selector)).toEqual([SCROLLER]);
  });

  // The page must SIZE TO CONTENT, or the scroller above has nothing taller than itself to scroll:
  // `flex: 1 1 0` plus `min-height: 0` made this column exactly the free space of its parent, so
  // `scrollHeight === clientHeight` and no scrollbar could ever appear.
  it("makes the exec page a content-sized column", () => {
    const page = ruleBody(loadCSS("31-exec-view.css"), ".ev-page");
    expect(/display:\s*flex/.test(page)).toBe(true);
    expect(/flex-direction:\s*column/.test(page)).toBe(true);
    expect(/flex:\s*1 0 auto/.test(page)).toBe(true);
  });

  // The results are the selected step's PRODUCT, so they render whole and the page scrolls. A
  // `max-block-size` on the region was a peek sized for scanning, and it only existed because the
  // region was competing for a fixed height.
  it("uncages the results region", () => {
    expect(decls(loadCSS("31-exec-view.css"), ".ev-r-body")).not.toMatch(/max-block-size:/);
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-r-body")).toEqual([]);
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-results")).toEqual([]);
  });

  // A per-result box may carry NO `overflow` on either axis: an `auto` on one side makes it a
  // scroll container, whose automatic minimum size is 0, which is exactly how a 331px report ended
  // up rendered in a 16px box.
  it("leaves a per-result box's overflow at its initial value", () => {
    expect(overflowDecls(loadCSS("31-exec-view.css"), ".ev-r-item-body")).toEqual([]);
  });

  // THE CLAMP IS THE ONE PLACE THAT SHAPE IS DELIBERATE, and it is the shape the `.ev-r-val` scar
  // above warns about — so it is spelled out rather than left to read as the same mistake.
  it("gives the clamped report exactly one overflow, beside its cap", () => {
    const css = loadCSS("31-exec-view.css");
    expect(overflowDecls(css, ".ev-r-text[data-clamped]")).toEqual(["overflow: hidden"]);
    expect(decls(css, ".ev-r-text[data-clamped]")).toMatch(
      /max-block-size:\s*var\(--clamp-h,\s*\d+lh\)/,
    );
  });

  // ONE OWNER for the two clamp openers on this page.
  it("gives both clamp openers one rule rather than a copy each", () => {
    const css = loadCSS("31-exec-view.css");
    const owners = allRules(css).filter((r) => /(^|,)\s*\.ev-r-more\s*$/m.test(r.selector));
    expect(owners).toHaveLength(1);
    expect(owners[0]?.selector).toBe(".ev-dock-ask-more,\n.ev-r-more");
  });

  // A container's state is a ROLL-UP of its children and must never repaint them.
  it("keeps every tree state rule on the child combinator", () => {
    const css = loadCSS("31-exec-view.css");
    const selectors = [...css.matchAll(/^\.ev-row\[data-state=[^\n{]*$/gm)].map((m) => m[0]);
    expect(selectors.length).toBeGreaterThan(0);
    for (const sel of selectors) {
      expect(sel, `${sel} must not be a descendant selector`).toContain("> .ev-row-main");
    }
  });

  // `#run-body` sits between the scroller and the page, so it has to SIZE TO CONTENT like
  // everything else below the scroller.
  it("sizes both bodies to their content", () => {
    const host = decls(loadCSS("18-pages.css"), BODY);
    expect(/flex:\s*1 0 auto/.test(host)).toBe(true);
    expect(host).not.toMatch(/min-height:\s*0/);
  });

  // WHICH BOX carries the measure is the half worth pinning, and it moved: a capped scroller puts
  // its scrollbar against the right edge of the cards rather than the view's, which reads as a
  // panel's inner scroller instead of the page's.
  it("measures both tabs at their body rather than at their scroller", () => {
    const css = loadCSS("18-pages.css");
    expect(decls(css, SCROLLER)).toMatch(/max-width:\s*none/);
    expect(decls(css, BODY)).toMatch(/max-width:\s*var\(--content-max-w\)/);
  });

  // The transcript is the OTHER side of that contract, and it is what makes the token assertion
  // above mean "the chat's column" rather than "some token both pages happen to share".
  it("measures the exec pages at the transcript's own token", () => {
    expect(decls(loadCSS("13-messages.css"), ".transcript-view")).toMatch(
      /max-width:\s*var\(--content-max-w\)/,
    );
  });

  // The exec pages share the prose measure; there is no wider `--run-page-max-w`.
  it("keeps the wide exec measure deleted", () => {
    const stripped = (name: string): string => loadCSS(name).replace(/\/\*[\s\S]*?\*\//g, " ");
    for (const name of ["01-tokens.css", "18-pages.css", "31-exec-view.css"]) {
      expect(stripped(name), `${name} must not reintroduce --run-page-max-w`).not.toMatch(
        /--run-page-max-w/,
      );
    }
  });

  it("lets both bodies fill their measure rather than shrink-wrapping to content", () => {
    const body = decls(loadCSS("18-pages.css"), BODY);
    expect(body).toMatch(/width:\s*100%/);
    expect(body).toMatch(/margin-inline:\s*auto/);
  });

  it("keeps the page measure a real token", () => {
    expect(loadCSS("01-tokens.css")).toMatch(/--content-max-w:\s*\d/);
  });
});

// The top edge: the first card stands as far under the toolbar as a list page's, and scrolled
// content never meets the toolbar, through a sticky band of page colour.
describe("the run page's top gap", () => {
  let style: HTMLStyleElement | undefined;
  let view: HTMLElement | undefined;
  afterEach(async () => {
    view?.remove();
    style?.remove();
    await browser.viewport(1280, 720);
  });

  function mountRunPage(): { scroller: HTMLElement; card: HTMLElement } {
    style = mountAppCSS();
    view = document.createElement("div");
    view.id = "run-view";
    view.style.blockSize = "600px";
    const scroller = document.createElement("div");
    scroller.className = "page-content";
    const body = document.createElement("div");
    body.id = "run-body";
    const card = document.createElement("div");
    card.style.blockSize = "3000px";
    body.append(card);
    scroller.append(body);
    view.append(scroller);
    document.body.append(view);
    return { scroller, card };
  }

  for (const [width, rest] of [
    [1280, 20],
    [390, 12],
  ] as const) {
    it(`rests the first card ${String(rest)}px under the toolbar at ${String(width)}px`, async () => {
      await browser.viewport(width, 844);
      const { scroller, card } = mountRunPage();
      const gap = card.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
      expect(gap).toBe(rest);
    });
  }

  for (const by of [4, 40, 400]) {
    it(`keeps a sticky band of page colour over scrolled content at ${String(by)}px`, async () => {
      await browser.viewport(1280, 720);
      const { scroller } = mountRunPage();
      scroller.scrollTop = by;
      const band = getComputedStyle(scroller, "::before");
      expect(band.position).toBe("sticky");
      expect(band.top).toBe("0px");
      expect(band.height).toBe("8px");
      const page = document.createElement("div");
      page.style.backgroundColor = "var(--c-bg-primary)";
      document.body.append(page);
      expect(band.backgroundColor).toBe(getComputedStyle(page).backgroundColor);
      page.remove();
      expect(Number.parseFloat(getComputedStyle(scroller).paddingTop)).toBe(0);
    });
  }
});
