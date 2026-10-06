// THE MERGED IDENTITY CONTROL: its box, its target, and its two state channels.
//
// The sidebar footer's connection mark and address are ONE `<button
// id="account-btn">` — the status popup's trigger — so a reader presses a 240px row
// rather than an 8px disc, which was the app's smallest target. This file pins the
// three claims that restructure rests on, at every pointer tier:
//
//   THE BOX IS THE LOGOUT BUTTON'S, height and radius alike, centred in the band
//   rather than filling it. Measured against the REAL sibling at every tier, never
//   against `--footer-ctl-h`'s arithmetic: restating a derivation proves only that
//   it is self-consistent.
//
//   THE HIT FLOOR is what the box clears, not what sizes it, and it is reachable
//   only because chrome is stripped by named declarations rather than `all: unset`
//   (that shorthand resets `min-*` at the declaring selector's own specificity,
//   which is what made `.status-dot` invisible to the floor for a year).
//
//   NOTHING MOVED ON THE INLINE AXIS, verified against numbers READ OFF A REAL BUILD
//   BEFORE the merge rather than against any arithmetic.
//
//   THE HOVER is `.icon-btn`'s VALUE by user instruction — the footer's two controls
//   are a pair and must answer the pointer alike — but GATED on `any-hover`, which
//   `.icon-btn`'s own is not. Both halves are read out of the sheet, because
//   `getComputedStyle` on a forced `:hover` is unavailable here (a synthetic hover
//   drives no recalc and `CSS.forcePseudoState` is a devtools call).
//
// Real layout in the page's own document at four tiers. The two pointer tiers are an
// ATTRIBUTE (`pointer-tier.ts` writes `data-pointer` on `<html>`), so forcing the
// attribute over `mountAppCSS()` is the honest way to reach them — and the
// coarse-WIDE tier is the one width-keyed rules structurally cannot see, which is
// the class of bug the deleted `width <= 48rem` block could hide.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

const shell = loadCSS("10-shell-app.css");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  // Both attributes, in a hook, in every file that writes either: a later case's
  // premise is their ABSENCE.
  delete document.documentElement.dataset["pointer"];
  delete document.documentElement.dataset["touched"];
});

interface Footer {
  footer: HTMLElement;
  btn: HTMLButtonElement;
  dot: HTMLElement;
  addr: HTMLElement;
  logout: HTMLElement;
}

/** The footer as `static/index.html` authors it. */
function mountFooter(email = "someone@example.invalid"): Footer {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  const footer = document.createElement("div");
  footer.className = "sidebar-footer";

  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";
  const btn = document.createElement("button");
  btn.type = "button";
  btn.id = "account-btn";
  btn.className = "account-btn pill-expandable";
  const dot = document.createElement("span");
  dot.id = "status-dot";
  dot.className = "status-dot connected";
  dot.setAttribute("aria-hidden", "true");
  const addr = document.createElement("span");
  addr.id = "user-email";
  addr.className = "sidebar-email";
  addr.textContent = email;
  const subject = document.createElement("span");
  subject.className = "sr-only";
  subject.textContent = "Account and connection status";
  btn.append(dot, addr, subject);
  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  anchor.append(btn, card);

  const actions = document.createElement("div");
  actions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  // The glyph is load-bearing: `.icon-btn` declares no height, so an empty one is
  // the bare hit floor (24px fine) where production is glyph plus padding (32px),
  // and every height comparison below would measure a box the app never renders.
  const glyph = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  glyph.setAttribute("class", "ic-ui");
  glyph.setAttribute("viewBox", "0 0 24 24");
  logout.appendChild(glyph);
  actions.appendChild(logout);

  footer.append(anchor, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { footer, btn, dot, addr, logout };
}

/** A token's rendered length in CSS pixels, so a size claim is against the
 *  declaration rather than against a number restated here. */
function tokenPx(name: string): number {
  const probe = document.createElement("div");
  probe.style.setProperty("inline-size", `var(${name})`);
  document.body.appendChild(probe);
  const v = probe.getBoundingClientRect().width;
  probe.remove();
  return v;
}

/** The four TIERS, and what only each one catches. `coarse-wide` is the important
 *  one: it is reachable ONLY through the attribute, so any rule that used to carry
 *  the touch treatment behind `width <= 48rem` was invisible to it. */
const TIERS: readonly (readonly [name: string, apply: () => void])[] = [
  ["fine", () => (document.documentElement.dataset["pointer"] = "fine")],
  ["coarse-wide", () => (document.documentElement.dataset["pointer"] = "coarse")],
  ["hybrid", () => (document.documentElement.dataset["touched"] = "")],
  ["bare", () => undefined],
];

describe("the merged control's box", () => {
  it.each(TIERS)("is the logout button's height at the %s tier", (name, apply) => {
    // An equality against the sibling, so it holds whatever the tokens resolve to. A
    // pinned 32/44 here would be the derivation restated.
    apply();
    const { btn, logout } = mountFooter();
    const b = btn.getBoundingClientRect().height;
    const l = logout.getBoundingClientRect().height;
    expect(b, `${name}: trigger ${b}px against the logout button's ${l}px`).toBeCloseTo(l, 0);
  });

  it.each(TIERS)("takes the logout button's corner radius at the %s tier", (name, apply) => {
    // Both read `--r` today, so this is a divergence test rather than a restatement
    // of either rule.
    apply();
    const { btn, logout } = mountFooter();
    const r = getComputedStyle(btn).borderTopLeftRadius;
    expect(r, `${name}: trigger ${r}`).toBe(getComputedStyle(logout).borderTopLeftRadius);
    expect(parseFloat(r), "and it is a real corner, not a square").toBeGreaterThan(0);
  });

  it.each(TIERS)("is centred in the band rather than filling it at the %s tier", (name, apply) => {
    // One property, two halves: shorter than the content band, with equal slack above
    // and below. Without the second, any height passes.
    apply();
    const { footer, btn } = mountFooter();
    const f = footer.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    expect(getComputedStyle(footer).paddingBlockStart, "the footer has no block padding").toBe(
      "0px",
    );
    const band = f.height - border;
    expect(b.height, `${name}: trigger ${b.height}px in a ${band}px band`).toBeLessThan(band);
    expect(b.top - (f.top + border), `${name}: slack above against below`).toBeCloseTo(
      f.bottom - b.bottom,
      0,
    );
  });

  it.each(TIERS)("answers a hit on all four of its own edges at the %s tier", (name, apply) => {
    // A real `elementFromPoint`, never a style read: only a hit test sees a clip, an
    // overlapping sibling or a zero-width box. Each probe off the CORNERS, because
    // `border-radius` is honoured by hit testing. The band above and below this pill
    // belongs to the footer, so the case above is what stops "its own edges" passing
    // for a box of any height.
    apply();
    const { btn } = mountFooter();
    const b = btn.getBoundingClientRect();
    const midX = b.left + b.width / 2;
    const midY = b.top + b.height / 2;
    for (const [edge, x, y] of [
      ["top", midX, b.top + 1],
      ["bottom", midX, b.bottom - 1],
      ["leading", b.left + 1, midY],
      ["trailing", b.right - 1, midY],
    ] as const) {
      // The mark, the address and the subject are non-interactive spans INSIDE the
      // trigger, so a probe legitimately answers one of them.
      const hit = document.elementFromPoint(x, y);
      expect(btn.contains(hit), `${name}: ${edge} edge answered ${hit?.className ?? "null"}`).toBe(
        true,
      );
    }
  });

  it.each(TIERS)("clears the app-wide hit floor at the %s tier", (name, apply) => {
    // `.account-btn`'s (0,1,0) `min-block-size` outranks the floor's zero-specificity
    // rule, so the floor holds by VALUE (`--footer-ctl-h` is a `max()` over it) rather
    // than by cascade. Read off the page, so one case covers every tier that moves it.
    apply();
    const { btn } = mountFooter();
    const floor = tokenPx("--hit-floor");
    expect(floor, "the tier declares a floor").toBeGreaterThan(0);
    const h = btn.getBoundingClientRect().height;
    expect(h, `${name}: ${h}px against a ${floor}px floor`).toBeGreaterThanOrEqual(floor);
  });

  it("keeps the band at 52px while the coarse floor lifts both controls to 44", () => {
    // What `min-height` on the band is FOR: it holds two 44px controls without growing.
    document.documentElement.dataset["pointer"] = "coarse";
    const { footer, btn, logout } = mountFooter();
    expect(logout.getBoundingClientRect().height, "the floor applies").toBeCloseTo(44, 0);
    expect(btn.getBoundingClientRect().height).toBeCloseTo(44, 0);
    expect(footer.getBoundingClientRect().height).toBeCloseTo(52, 0);
  });

  it.each(TIERS)("renders the mark at --dot-size at the %s tier", (_name, apply) => {
    // ONE mark rule at every tier, where the phone tier used to carry a second
    // spelling (a transparent 44px grid with the mark on a `::before`).
    //
    // WHAT THIS CASE DOES NOT PIN, stated so nobody reads it as covering more than it
    // does: `.status-dot`'s `display: block` is NOT observable here. The mark is a
    // flex ITEM of `.account-btn`, so it is blockified regardless and planting
    // `display: inline` leaves every case in this file green — verified. That
    // declaration replaced an ACCIDENT (`all: unset` resets `display` to `inline`, on
    // which width and height do not apply, so the old disc rendered only because its
    // parent was a flex container), and the case that can see it is
    // `status-dot-css.test.ts`'s, which mounts the mark unparented in `<body>`.
    apply();
    const { dot } = mountFooter();
    const box = dot.getBoundingClientRect();
    const size = tokenPx("--dot-size");
    expect(size).toBeGreaterThan(0);
    expect(box.width).toBeCloseTo(size, 1);
    expect(box.height).toBeCloseTo(size, 1);
  });
});

describe("nothing moved", () => {
  // OBSERVED readings off a real build, not the derivation in `.account-btn`'s
  // comment (encoding that would prove only that it is self-consistent): the mark's
  // x and the logout button's right edge equal the footer's own content edges at
  // 1280px with a 259px sidebar, 16 and 243.
  const PRE_CHANGE = { dotLeft: 16, logoutRight: 243 } as const;

  it.each(["fine", "coarse"])("keeps the mark's x and the logout's right edge at %s", (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const { dot, logout } = mountFooter();
    expect(
      dot.getBoundingClientRect().left,
      `the mark's left edge was ${String(PRE_CHANGE.dotLeft)} before the merge`,
    ).toBeCloseTo(PRE_CHANGE.dotLeft, 0);
    expect(
      logout.getBoundingClientRect().right,
      `the logout button's right edge was ${String(PRE_CHANGE.logoutRight)} before the merge`,
    ).toBeCloseTo(PRE_CHANGE.logoutRight, 0);
  });

  it("leaves the logout button real separation, not an ambiguity", () => {
    // The trailing bleed leaves `--sp-2` less than the footer's 12px gap between the
    // two boxes. Real separation: only one of the two can be hovered at a time, and
    // both hover to the same token.
    const { btn, logout } = mountFooter();
    const gap = logout.getBoundingClientRect().left - btn.getBoundingClientRect().right;
    expect(gap, `the two controls are ${gap}px apart`).toBeGreaterThanOrEqual(4);
  });
});

describe("the state channels, read out of the sheet", () => {
  // TWO SOURCE assertions rather than a computed read, because a forced `:hover` is
  // unavailable in this project: a synthetic hover drives no style recalc and
  // `CSS.forcePseudoState` is a devtools call. The subject is the CASCADE anyway —
  // which value, and inside which at-rule.

  it("hovers to the same value .icon-btn does", () => {
    // The user's instruction: the footer's two controls are a PAIR. `.icon-btn`'s
    // hover is a NESTED `&:hover` inside its own rule body, so the read has to reach
    // into that body rather than looking for a top-level `.icon-btn:hover`.
    const iconBody = ruleContaining(shell, ".icon-btn", "top").body;
    const iconHover = /&:hover\s*\{([^}]*)\}/u.exec(iconBody);
    expect(iconHover, ".icon-btn declares a nested &:hover").not.toBeNull();

    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    const btnHover = /&:hover\s*\{([^}]*)\}/u.exec(btnBody);
    expect(btnHover, ".account-btn declares a nested &:hover").not.toBeNull();

    const value = (body: string): string => {
      const m = /background:\s*([^;]+);/u.exec(body);
      expect(m, "the hover declares a background").not.toBeNull();
      return (m?.[1] ?? "").trim();
    };
    expect(
      value(btnHover?.[1] ?? ""),
      "the pair must hover to one token, or they answer the pointer differently",
    ).toBe(value(iconHover?.[1] ?? ""));
  });

  it("gates its hover on any-hover while .icon-btn's is ungated", () => {
    // The one DELIBERATE divergence, and stated as such at the rule: `.icon-btn`'s
    // ungated hover latches under a finger until the next tap elsewhere, which is a
    // pre-existing app-wide property of a shared class and not this change's to fix —
    // while an ungated wash on a 240px row is far more visible than on a 32px glyph.
    // `any-hover`, never `hover`: the latter reports only the PRIMARY input and drops
    // the rule on every touch-primary device.
    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    expect(btnBody, "the hover sits inside an any-hover at-rule").toMatch(
      /@media\s*\(any-hover:\s*hover\)\s*\{\s*&:hover/u,
    );
    expect(btnBody, "never the primary-input query").not.toMatch(/@media\s*\(hover:\s*hover\)/u);

    const iconBody = ruleContaining(shell, ".icon-btn", "top").body;
    expect(iconBody, ".icon-btn's own hover is ungated, which is the divergence").not.toMatch(
      /@media\s*\(any-hover:\s*hover\)/u,
    );
  });

  it("takes the app-wide press rather than declaring one, like the logout button", () => {
    // 03-base.css's universal `:where(button, summary, [role="button"]):active` reaches
    // both controls and paints `--c-press`, so a bespoke `:active` here would compound
    // one step past it. `.sidebar-email:active { background: var(--c-press) }` was
    // exactly that rule and is deleted.
    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    expect(btnBody, "no bespoke press").not.toMatch(/&:active/u);
    expect(loadCSS("10-shell-app.css"), "and the address declares none either").not.toMatch(
      /\.sidebar-email:active/u,
    );
    // And the transition omits `box-shadow` character for character with `.icon-btn`,
    // so the press wash appears and leaves in one frame on both.
    expect(btnBody).not.toMatch(/transition:[^;]*box-shadow/u);
  });

  it("declares no focus ring of its own, so the floor owns it", () => {
    // 40-a11y.css rings every `button` at ZERO specificity, and its own comment says
    // the hand-rolled rings are deletable. `.pill-account` is the one control in this
    // change that needs an OFFSET override, and that lives in 40-a11y.css's
    // inset-offset list rather than at the rule.
    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    expect(btnBody).not.toMatch(/&:focus-visible/u);
    expect(btnBody, "and no outline of any kind").not.toMatch(/outline/u);
  });

  it("resets chrome by NAMED declarations, never all: unset", () => {
    // The cascade fact the rule's comment rests on: `all: unset` resets
    // `min-width`/`min-height` at this selector's own (0,1,0) against the app-wide
    // floor's zero, so a control declaring it is not overriding the floor — it is
    // INVISIBLE to it. That is what made `.status-dot` an 8px button at every tier.
    // Naming the declarations keeps the floor reachable as a backstop.
    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    expect(btnBody, "all: unset would make the floor unreachable").not.toMatch(/all:\s*unset/u);
    // Both sides, because a stretch left standing beside the `min-block-size` wins the
    // cross axis and silently restores the full-band fill.
    expect(btnBody, "the box reads the footer's control height").toMatch(
      /min-block-size:\s*var\(--footer-ctl-h\)/u,
    );
    expect(btnBody, "and nothing stretches it back into the band").not.toMatch(
      /align-self:\s*stretch/u,
    );
    // `min-width: 0` opts out of the floor's INLINE axis deliberately (the box is the
    // band's whole width, so a 44px inline floor describes nothing) while nothing
    // opts out of the block axis.
    expect(btnBody).toMatch(/min-width:\s*0/u);
    expect(btnBody, "the block axis keeps the floor as its backstop").not.toMatch(
      /min-height:\s*0/u,
    );
  });
});
