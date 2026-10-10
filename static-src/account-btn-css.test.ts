// The merged identity control: the sidebar footer's mark and address are ONE
// `<button id="account-btn">`, the status popup's trigger. Pinned at every pointer tier:
// its box matches the REAL logout button's (never `--footer-ctl-h`'s arithmetic), it
// clears the hit floor because chrome is stripped by named declarations rather than
// `all: unset`, nothing moved on the inline axis, and its hover is `.icon-btn`'s value
// gated on `any-hover`. The pointer tiers are a `data-pointer` attribute, so forcing it
// is how a case reaches them; coarse-wide is the tier width-keyed rules cannot see.
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
  // In a hook, in every file that writes it: a later case's premise is its ABSENCE.
  delete document.documentElement.dataset["pointer"];
});

interface Footer {
  footer: HTMLElement;
  btn: HTMLButtonElement;
  dot: HTMLElement;
  addr: HTMLElement;
  logout: HTMLElement;
}

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
  // `.icon-btn` declares no height, so without a glyph the box is the bare hit floor and
  // every height comparison would measure a box the app never renders.
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

/** `coarse-wide` is reachable ONLY through the attribute, so a width-keyed rule cannot see it. */
const TIERS: readonly (readonly [name: string, apply: () => void])[] = [
  ["fine", () => (document.documentElement.dataset["pointer"] = "fine")],
  ["coarse-wide", () => (document.documentElement.dataset["pointer"] = "coarse")],
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
    // A real `elementFromPoint`: only a hit test sees a clip, an overlapping sibling or a
    // zero-width box. Probes sit off the corners, which hit testing rounds.
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
    // `.account-btn`'s (0,1,0) `min-block-size` outranks the floor's zero-specificity rule,
    // so the floor holds by VALUE (`--footer-ctl-h` is a `max()` over it).
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
    // ONE mark rule at every tier. Not pinned here: `.status-dot`'s `display: block` (a flex
    // item is blockified anyway); `status-dot-css.test.ts` mounts the mark unparented.
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
  // Readings off a real build before the merge, not the derivation in `.account-btn`'s
  // comment: the footer's content edges at 1280px with a 259px sidebar, 16 and 243.
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
    // The trailing bleed leaves `--sp-2` less than the 12px gap between the two boxes.
    const { btn, logout } = mountFooter();
    const gap = logout.getBoundingClientRect().left - btn.getBoundingClientRect().right;
    expect(gap, `the two controls are ${gap}px apart`).toBeGreaterThanOrEqual(4);
  });
});

describe("the state channels, read out of the sheet", () => {
  // Source assertions, because a forced `:hover` drives no recalc here and
  // `CSS.forcePseudoState` is a devtools call. The subject is the cascade anyway.

  it("hovers to the same value .icon-btn does", () => {
    // The footer's two controls are a PAIR. `.icon-btn`'s hover is a nested `&:hover`, so the
    // read reaches into its rule body.
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
    // The one deliberate divergence: an ungated wash latches under a finger, far more visible
    // on a 240px row. `any-hover`, never `hover`, which drops touch-primary devices.
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
    // 03-base.css's universal `:active` already paints `--c-press` here, so a bespoke one
    // would compound past it.
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
    // 40-a11y.css rings every `button` at zero specificity; `.pill-account`'s offset lives in
    // its inset-offset list.
    const btnBody = ruleContaining(shell, ".account-btn", "top").body;
    expect(btnBody).not.toMatch(/&:focus-visible/u);
    expect(btnBody, "and no outline of any kind").not.toMatch(/outline/u);
  });

  it("resets chrome by NAMED declarations, never all: unset", () => {
    // `all: unset` resets `min-*` at this selector's (0,1,0), making a control INVISIBLE to
    // the app-wide floor. Naming the declarations keeps the floor as a backstop.
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
    // `min-width: 0` opts out of the inline floor (the box is the band's width); nothing opts
    // out of the block axis.
    expect(btnBody).toMatch(/min-width:\s*0/u);
    expect(btnBody, "the block axis keeps the floor as its backstop").not.toMatch(
      /min-height:\s*0/u,
    );
  });
});
