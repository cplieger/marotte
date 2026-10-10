import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// Touch policy: decisions that LOOK wrong to a passing reader and are easy to "fix" back into a
// defect. The reason is on each assertion.
// ---------------------------------------------------------------------------

describe("pinch-zoom is enabled", () => {
  it("keeps `pinch-zoom` in the body's touch-action list", () => {
    // Pinch-zoom is ON, with the viewport meta's `maximum-scale` gone (WCAG 1.4.4): the two travel
    // together, or the meta change buys nothing. The layout mispositioning a pinch causes is an accepted
    // cost. `pinch-zoom` does not bring back the 300ms delay `touch-action: manipulation` removes.
    // Looked up in the `reset` layer, where 02-reset.css puts it.
    const reset = loadCSS("02-reset.css");
    const body = ruleContaining(reset, "body", "reset");
    expect(body.body).toMatch(/touch-action:\s*pan-x pan-y pinch-zoom/u);
  });
});

describe("a control that must stay visually small opts out of the box floor", () => {
  it("gives .shell-resize min-width and min-height of 0", () => {
    // A tabbable `role="separator"` matches the universal hit floor, which grew its BOX: absolutely
    // positioned across the panel at `z-index: 1`, a 44px box covered the header's buttons. It grows
    // its TARGET instead, as the floor's own comment names.
    const shell = loadCSS("21-shell-panel.css");
    const bar = ruleContaining(shell, ".shell-resize", "top");
    expect(bar.body).toMatch(/min-width:\s*0/u);
    expect(bar.body).toMatch(/min-height:\s*0/u);
    expect(bar.body, "the visual bar must stay thin").toMatch(/height:\s*var\(--shell-bar-h\)/u);

    const panel = ruleContaining(shell, ".shell-panel", "top");
    expect(panel.body, "and the hairline is 3px").toMatch(/--shell-bar-h:\s*0\.1875rem/u);
  });

  it("expands the target DOWNWARD, and nothing else pays for it", () => {
    // `.shell-panel` is `overflow: hidden`, so an upward expander is CLIPPED (6px real target), and
    // paying a downward reach in header padding made a 56px/89px header. The buttons out-stack the
    // expander instead, so this asserts the ABSENCE of that arithmetic; the hit tests below measure it.
    const shell = loadCSS("21-shell-panel.css");

    const expander = ruleContaining(shell, ".shell-resize::before", "top");
    expect(expander.body, "reaching DOWN, by the floor minus the bar it paints").toMatch(
      /inset:\s*0 0 calc\(var\(--shell-bar-h\) - var\(--hit-floor\)\)/u,
    );

    const header = ruleContaining(shell, ".shell-header", "top");
    expect(header.body, "the header is one content row").toMatch(/height:\s*2rem/u);
    expect(header.body, "with no block padding to hold a target").toMatch(
      /padding-inline:\s*var\(--sp-3\)/u,
    );
    expect(
      header.body.includes("--hit-floor") || header.body.includes("--shell-bar-h"),
      "the header restates no part of the target's arithmetic",
    ).toBe(false);

    // The half that makes all of the above safe, declared on the control that has
    // to win: the bar carries `z-index: 1`, so a button under the expander needs a
    // higher one or the seam takes its clicks.
    const button = ruleContaining(shell, ".shell-header-btn", "top");
    expect(button.body, "the buttons out-stack the bar").toMatch(/z-index:\s*2/u);
    expect(button.body, "and a z-index needs a position to apply").toMatch(/position:\s*relative/u);
  });
});

describe("row actions a finger cannot hover are shown on a coarse pointer", () => {
  // `opacity: 0` revealed by `:hover` or keyboard focus is unreachable by touch, so on a tablet these
  // controls (Discard included) did not exist.
  it.each([["22-git-multirepo.css", ".git-file-actions"]])("reveals %s's %s", (file, selector) => {
    const css = loadCSS(file);
    const reveals = css
      .split("}")
      .filter((r) => r.includes(selector) && /opacity:\s*1/u.test(r))
      .map((r) => r.split("{")[0] ?? "");
    expect(
      reveals.some((sel) => sel.includes('data-pointer="coarse"')),
      `${selector} needs a reveal that is neither :hover nor :focus`,
    ).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// A native checkbox or radio PAINTS the box the hit floor grows, so the floor keeps the TARGET
// (WCAG 2.5.5, Apple's HIG) and the box stays small. Every case measures BOTH numbers.
// ---------------------------------------------------------------------------

/** The composer's `+` menu, as chat-options.ts builds it. Synthetic rather than
 *  the real card: the subject here is the stylesheet, and chat-options.test.ts
 *  owns the DOM this mirrors. */
const OPT_ROWS = [
  ["Attach a file", "Pick a workspace file or upload one (up to 10 MB)"],
  ["Set a goal", "The agent iterates toward it until it reports success"],
  ["Start a tangent", "Branch this conversation into a sub-chat that keeps its context"],
  ["Compact the context", "Summarize the history so far to free up context"],
] as const;

function optMenuHTML(): string {
  const actions = OPT_ROWS.map(
    ([name, hint]) => `<div class="chat-opt-entry"><button type="button" class="chat-opt-btn">
      <span class="chat-opt-icon"><svg width="14" height="14" viewBox="0 0 24 24"></svg></span>
      <span class="chat-opt-text"><span class="chat-opt-name">${name}</span
      ><span class="chat-opt-hint">${hint}</span></span>
    </button></div>`,
  ).join("");
  return `<span class="pill-slot" style="position:relative;display:block">
    <span class="pill-expand-content chat-options-card is-open">${actions}
      <label class="chat-opt-row"><input type="checkbox">
        <span class="chat-opt-text"><span class="chat-opt-name">Supervised mode</span
        ><span class="chat-opt-hint">Review this chat's file changes at the end of each turn</span></span>
      </label>
    </span></span>`;
}

const PERM_ROWS = {
  "a Settings > Permissions profile radio": `<div class="profile-list">
    <label class="perm-mode profile-row"><input type="radio"><span>Guarded</span>
      <p class="section-hint profile-desc">Confined reads, and it asks before anything else.</p></label>
  </div>`,
} as const;

const boxHost = document.createElement("div");
boxHost.style.cssText = "position:fixed;top:600px;left:40px;inline-size:420px;";

let boxStyle: HTMLStyleElement;

beforeAll(() => {
  boxStyle = mountAppCSS();
  document.body.appendChild(boxHost);
});

afterAll(() => {
  boxStyle.remove();
  boxHost.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  boxHost.replaceChildren();
});

/** `--hit-floor` in px at the tier currently set, read from the token rather than
 *  restated, so retiering moves every assertion below with it. */
function hitFloorPx(): number {
  const probe = document.createElement("div");
  probe.style.inlineSize = "var(--hit-floor)";
  boxHost.appendChild(probe);
  const px = probe.getBoundingClientRect().width;
  probe.remove();
  return px;
}

function mount(html: string): void {
  boxHost.innerHTML = html;
}

function tier(name: "fine" | "coarse"): void {
  document.documentElement.dataset["pointer"] = name;
}

describe("a native box control paints its own size and grows only its target", () => {
  it.each(Object.entries(PERM_ROWS))("%s", (_name, html) => {
    mount(html);
    const input = boxHost.querySelector("input");
    if (input === null) {
      throw new Error("fixture has no input");
    }

    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = input.getBoundingClientRect();
      // 1rem: the checkbox's own size (02-reset.css) and the radio's, declared
      // beside the floor because a UA-appearance radio has none of its own.
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);

      // And the target the floor exists for is still exactly the tier's floor,
      // carried by the `::after` expander instead of by the box.
      const floor = hitFloorPx();
      const target = getComputedStyle(input, "::after");
      expect(Number.parseFloat(target.width), `target width on ${t}`).toBeCloseTo(floor, 1);
      expect(Number.parseFloat(target.height), `target height on ${t}`).toBeCloseTo(floor, 1);
    }
  });

  it("the + menu's supervised switch paints at 1rem on both tiers", () => {
    mount(optMenuHTML());
    const input = boxHost.querySelector(".chat-opt-row > input");
    if (input === null) {
      throw new Error("fixture has no switch");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = input.getBoundingClientRect();
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);
    }
  });
});

describe("the + menu's switch takes its target from its row, not an expander", () => {
  it("gives the row itself at least --hit-floor on both tiers", () => {
    // The switch drops the expander because its <label> is the bigger target, so a shrunk row fails here.
    mount(optMenuHTML());
    const row = boxHost.querySelector(".chat-opt-row");
    if (row === null) {
      throw new Error("fixture has no switch row");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const floor = hitFloorPx();
      const box = row.getBoundingClientRect();
      expect(box.height, `row height on ${t}`).toBeGreaterThanOrEqual(floor);
      expect(box.width, `row width on ${t}`).toBeGreaterThanOrEqual(floor);
    }
  });

  it("leaves the bottom edge of the row above to that row", () => {
    // Why the carve-out exists: a checkbox against the row's top edge overhangs upward into the
    // neighbour (4px into Compact on coarse). Probed in the switch's own COLUMN.
    mount(optMenuHTML());
    const input = boxHost.querySelector(".chat-opt-row > input");
    const above = boxHost.querySelector(".chat-opt-entry:last-of-type .chat-opt-btn");
    if (input === null || above === null) {
      throw new Error("fixture is missing a row");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const col = input.getBoundingClientRect();
      const edge = above.getBoundingClientRect();
      const hit = document.elementFromPoint(col.left + col.width / 2, edge.bottom - 1);
      expect(above.contains(hit), `the row above owns its bottom edge on ${t}`).toBe(true);
    }
  });
});

// ---------------------------------------------------------------------------
// The file browser's git letter grows its TARGET, like `.shell-resize`: a real `role="button"`
// whose MARK is a fixed 1rem square (`19-files.css`). Unopted it rendered 44px under a finger.
// `files-row-metrics.test.ts` stays green with the expander deleted, so these cases guard it.
// Red-checked separately: no `::after` fails target/neighbour/source; no `min-*: 0` fails paint.
// ---------------------------------------------------------------------------

/** One `.fb-row` with a dirty FILE's letter, in `files.ts` `entryRow` order. `role="button"` is
 *  what makes the floor match. */
const LETTERED_ROW = `<div class="fb-row" role="listitem">
  <span class="fb-name fb-name-link">some-entry-with-a-long-name.ts</span
  ><span class="fb-git-letter git-st-m fb-git-clickable" role="button"
    aria-label="Git status: modified">M</span
  ><span class="fb-meta">1.2 KB   ·   2026-09-01   ·   -rw-r--r--</span>
</div>`;

/** `content-visibility: auto` is overridden: a SKIPPED subtree is not hit-testable (Chromium 151:
 *  every `elementFromPoint` answered `.fb-row`) while rect queries force layout. Stated, not
 *  awaited, since relevance is the renderer's and load-bound. */
function mountLetteredRow(): Element {
  mount(LETTERED_ROW);
  const row = boxHost.firstElementChild;
  if (!(row instanceof HTMLElement)) {
    throw new Error("fixture has no row");
  }
  row.style.contentVisibility = "visible";
  const badge = row.querySelector(".fb-git-clickable");
  if (badge === null) {
    throw new Error("fixture has no git letter");
  }
  return badge;
}

describe("the file browser's git letter grows its target, not its mark", () => {
  it("paints the letter at 1rem on both tiers", () => {
    // The assertion that fails if anyone deletes the `min-width: 0; min-height: 0`
    // pair: the floor then sets the letter's BOX to --hit-floor, so the mark is
    // 24px on a mouse and a 44px purple square under a finger.
    const badge = mountLetteredRow();
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const painted = badge.getBoundingClientRect();
      expect(painted.width, `painted width on ${t}`).toBe(16);
      expect(painted.height, `painted height on ${t}`).toBe(16);
    }
  });

  it("keeps the target at exactly --hit-floor on both tiers", () => {
    // The positioned pseudo's USED size, derived from the token so retiering moves it.
    const badge = mountLetteredRow();
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const floor = hitFloorPx();
      const target = getComputedStyle(badge, "::after");
      expect(Number.parseFloat(target.width), `target width on ${t}`).toBeCloseTo(floor, 1);
      expect(Number.parseFloat(target.height), `target height on ${t}`).toBeCloseTo(floor, 1);
    }
  });

  it("leaves .fb-name's own trailing edge to .fb-name", () => {
    // ASYMMETRIC: the letter sits between the name (opens the FILE) and inert metadata, so a centred
    // expander reached 6px into the name on coarse. Measured: the target begins at the name's right edge.
    const badge = mountLetteredRow();
    const name = boxHost.querySelector(".fb-name");
    if (name === null) {
      throw new Error("fixture has no name");
    }
    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const box = name.getBoundingClientRect();
      const mid = box.top + box.height / 2;
      expect(
        name.contains(document.elementFromPoint(box.right - 1, mid)),
        `the name owns its trailing edge on ${t}`,
      ).toBe(true);
      // The other half of the same fact: the target really is there, one pixel
      // further on. Without it the case passes with the expander deleted.
      expect(
        badge.contains(document.elementFromPoint(box.right + 1, mid)) ||
          document.elementFromPoint(box.right + 1, mid) === badge,
        `the letter's target starts at the name's edge on ${t}`,
      ).toBe(true);
    }
  });

  it("declares the opt-out and the expander on .fb-git-clickable itself", () => {
    // Source-level companions, matching the `.shell-resize` pair above: the three
    // measurements say the target is right, and cannot say WHICH selector carries
    // it, so a reader who moved either half elsewhere would leave them green.
    const files = loadCSS("19-files.css");
    const control = ruleContaining(files, ".fb-git-clickable", "top");
    expect(control.body).toMatch(/min-width:\s*0/u);
    expect(control.body).toMatch(/min-height:\s*0/u);

    // `ruleContaining` only indexes TOP-LEVEL selectors, so the nested expander is sliced out. Once
    // this block nests, use a reader in `css-rules.ts`.
    const at = control.body.indexOf("&::after {");
    expect(at, "the target is an ::after expander on this control").toBeGreaterThan(-1);
    const expander = control.body.slice(at, control.body.indexOf("}", at));
    expect(expander, "the target is derived from the tier's floor").toContain("var(--hit-floor)");
    expect(expander, "the start side takes the row's own gap").toContain("var(--sp-2)");
  });
});

describe("every + menu row's label starts on one x", () => {
  it("puts an action row's name and the switch row's name in the same column", () => {
    // The switch row lacked its neighbours' transparent 1px border and led with the checkbox, so its
    // label started 9px (fine) / 29px (coarse) right of theirs.
    mount(optMenuHTML());
    const names = [...boxHost.querySelectorAll(".chat-opt-name")];
    expect(names.length, "one name per row").toBe(OPT_ROWS.length + 1);

    for (const t of ["fine", "coarse"] as const) {
      tier(t);
      const lefts = names.map((n) => n.getBoundingClientRect().left);
      const [first] = lefts;
      for (const left of lefts) {
        expect(left, `every row's label shares one inline start on ${t}`).toBeCloseTo(
          first ?? 0,
          1,
        );
      }
    }
  });
});

// ---------------------------------------------------------------------------
// THE SHELL PANEL'S RESIZE BAR, HIT-TESTED: a declared 24/44px target says nothing about whether it
// is REACHABLE (the clipped upward expander measured 6px). The bar is `top: 0` inside the panel,
// so this host sits at the viewport top for `elementFromPoint` to answer.
// ---------------------------------------------------------------------------

/** The shell panel as `static/index.html` authors it, down to one header button.
 *  The `shell-closed` class it SHIPS with is deliberately absent — that state is
 *  `height: 0`, so the fixture would have no geometry to measure. */
const SHELL_PANEL = `<div class="shell-panel">
  <div class="shell-resize" role="separator" tabindex="0"></div>
  <div class="shell-header">
    <span class="shell-title"><svg class="ic-ui" viewBox="0 0 24 24"></svg><span>Shell</span></span>
    <button type="button" class="shell-header-btn" aria-label="Close shell">
      <svg class="ic-inline" viewBox="0 0 24 24"></svg>
    </button>
  </div>
  <div class="shell-terminal"></div>
</div>`;

const shellHost = document.createElement("div");
shellHost.style.cssText = "position:fixed;top:0;left:40px;inline-size:420px;";

describe("the shell resize bar's real target", () => {
  beforeAll(() => {
    document.body.appendChild(shellHost);
  });

  afterAll(() => {
    shellHost.remove();
  });

  function mountPanel(): { bar: Element; button: Element } {
    shellHost.innerHTML = SHELL_PANEL;
    const bar = shellHost.querySelector(".shell-resize");
    const button = shellHost.querySelector(".shell-header-btn");
    if (bar === null || button === null) {
      throw new Error("fixture is missing an element");
    }
    return { bar, button };
  }

  /** What owns the point, as a click would find it. */
  function ownerAt(x: number, y: number): Element | null {
    return document.elementFromPoint(x, y);
  }

  it.each([
    ["fine", 24],
    ["coarse", 44],
  ] as const)("is exactly --hit-floor tall on %s (%ipx)", (t, expected) => {
    tier(t);
    const { bar } = mountPanel();
    const floor = hitFloorPx();
    expect(floor, `--hit-floor on ${t}`).toBe(expected);

    const box = bar.getBoundingClientRect();
    expect(box.height, "the painted bar stays a 3px hairline").toBeCloseTo(3, 1);

    // Sampled at the bar's centre, which is over the TITLE: that is the header's
    // inert region and the only span where the target is unobstructed, since the
    // buttons deliberately out-stack it at the row's trailing end (case ii).
    const x = box.left + box.width / 2;
    // (i) the bar owns every row from its own top edge down to the floor.
    for (let dy = 0.5; dy < floor; dy += 1) {
      expect(ownerAt(x, box.top + dy), `the bar owns y+${dy} on ${t}`).toBe(bar);
    }
    // (iii) and not one row further — which is what makes this a MEASUREMENT of
    // the reach rather than a lower bound: the painted bar plus the floor minus the
    // bar, reaching down.
    expect(ownerAt(x, box.top + floor + 0.5), `the target ends at the floor on ${t}`).not.toBe(bar);
  });

  it.each(["fine", "coarse"] as const)("leaves the header button its own whole box on %s", (t) => {
    // (ii) The bar lies over the header at `z-index: 1`; on coarse the expander covers the buttons
    // WHOLE, so only `.shell-header-btn`'s `z-index: 2` separates them. Red-checked: deleting it, or
    // making `.shell-header` a stacking context, fails; a bare `position: relative` does not.
    tier(t);
    const { bar, button } = mountPanel();
    const box = button.getBoundingClientRect();
    expect(box.height, `the button has a box on ${t}`).toBeGreaterThan(0);

    for (const [name, y] of [
      ["top edge", box.top + 0.5],
      ["centre", box.top + box.height / 2],
      ["bottom edge", box.bottom - 0.5],
    ] as const) {
      const hit = ownerAt(box.left + box.width / 2, y);
      expect(hit, `the resize bar must not own the button's ${name} on ${t}`).not.toBe(bar);
      expect(hit !== null && button.contains(hit), `the button owns its ${name} on ${t}`).toBe(
        true,
      );
    }
  });

  it.each([
    ["fine", 32],
    ["coarse", 45],
  ] as const)("stays one content row tall on %s (%ipx)", (t, expected) => {
    // (iv) The reader-visible half: hardcoded heights, so re-deriving the header from the floor fails.
    // 45 is 44 plus the 1px border `box-sizing: border-box` charges.
    tier(t);
    const { bar } = mountPanel();
    const head = bar.parentElement?.querySelector(".shell-header");
    expect(head, "fixture has a header").not.toBeNull();
    expect(head?.getBoundingClientRect().height, `header height on ${t}`).toBeCloseTo(expected, 1);
  });
});
