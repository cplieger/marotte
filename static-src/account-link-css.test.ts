// The status card's account row is its one link (legal: the card is the trigger's SIBLING).
// Pinned by real hit test, both inline corners, the required ink lift over the tinted card,
// and the block position after a real `makeExpandable` open.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { allRules, loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";
import { makeExpandable } from "./pill-expand.js";

const input = loadCSS("15-input.css");

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
  document.body.style.margin = "0";
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  delete document.documentElement.dataset["pointer"];
});

interface Card {
  card: HTMLElement;
  link: HTMLAnchorElement;
  plan: HTMLElement;
  footer: HTMLElement;
}

/**
 * The whole footer, with the card as the trigger's sibling inside `.popup-anchor`, which
 * `.pill-status-content`'s `bottom` is anchored against.
 */
function mountFooter(opts: { hidden?: boolean } = {}): Card & { btn: HTMLButtonElement } {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";
  sidebar.style.cssText = "position:fixed;top:200px;left:0;width:260px;";
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
  addr.textContent = "someone@example.invalid";
  btn.append(dot, addr);

  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  const detail = document.createElement("span");
  detail.className = "pill-detail";
  detail.textContent = "connected to marotte 1.2.3";
  const link = document.createElement("a");
  link.id = "st-account";
  link.className = "pill-account";
  link.href = "https://app.kiro.dev/account/usage";
  link.target = "_blank";
  link.rel = "noopener";
  if (opts.hidden === true) {
    link.hidden = true;
  }
  const lines = document.createElement("span");
  lines.className = "pill-account-lines";
  const plan = document.createElement("span");
  plan.className = "pill-account-plan";
  plan.id = "acct-plan";
  plan.textContent = "KIRO POWER";
  const meter = document.createElement("span");
  meter.className = "pill-account-meter";
  meter.id = "acct-meter";
  meter.textContent = "412 / 1,000 credits";
  lines.append(plan, meter);
  const mark = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  mark.setAttribute("class", "ic-ui");
  mark.setAttribute("aria-hidden", "true");
  mark.setAttribute("viewBox", "0 0 24 24");
  link.append(lines, mark);
  card.append(detail, link);

  anchor.append(btn, card);
  const actions = document.createElement("div");
  actions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  actions.appendChild(logout);
  footer.append(anchor, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { card, link, plan, footer, btn };
}

/**
 * Open the popup the way the app does: `makeExpandable` swaps `.hidden` for `[hidden]` and
 * `clampToViewport` positions the card on open.
 */
function open(btn: HTMLButtonElement, card: HTMLElement): void {
  makeExpandable(btn, card);
  btn.click();
  // Finish the entry transition: `getBoundingClientRect` reports the `scale(0.4)` box
  // mid-animation, and finishing is deterministic where waiting on frames is not.
  for (const a of card.getAnimations()) {
    a.finish();
  }
}

const TIERS = [
  ["fine", 24],
  ["coarse", 44],
] as const;

describe("the link's target", () => {
  it.each(TIERS)("clears the %s tier's floor and answers a hit at its centre", (tier, floor) => {
    document.documentElement.dataset["pointer"] = tier;
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const box = link.getBoundingClientRect();
    expect(box.height, `the link is ${box.height}px at the ${tier} tier`).toBeGreaterThanOrEqual(
      floor,
    );
    // A real hit test, never a style read: only a hit test sees the card's clip.
    const hit = document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2);
    expect(link.contains(hit), `centre answered ${hit?.nodeName ?? "null"}`).toBe(true);
  });

  it("generates no box at all while it is hidden", () => {
    // `.pill-account`'s author `display: flex` beats the UA `[hidden]` rule, so
    // `&[hidden] { display: none }` is required, or the empty row spends a card gap.
    const { link, card, btn } = mountFooter({ hidden: true });
    open(btn, card);
    expect(link.hidden).toBe(true);
    expect(getComputedStyle(link).display).toBe("none");
    expect(link.getBoundingClientRect().height).toBe(0);
  });
});

describe("the link's corner and box", () => {
  it.each(TIERS)("is flush with the card's padding box on BOTH inline edges at %s", (tier) => {
    document.documentElement.dataset["pointer"] = tier;
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const c = card.getBoundingClientRect();
    const cs = getComputedStyle(card);
    const l = link.getBoundingClientRect();
    // The clip box is the card's PADDING box, so the row is flush with the border, per edge.
    const left = l.left - (c.left + parseFloat(cs.borderLeftWidth));
    const right = c.right - parseFloat(cs.borderRightWidth) - l.right;
    expect(left, `leading edge is ${left}px inside the padding box`).toBeCloseTo(0, 1);
    expect(right, `trailing edge is ${right}px inside the padding box`).toBeCloseTo(0, 1);
  });

  it("takes the card family's derived inner corner", () => {
    // `--card-radius` = `--r-lg` − 1px − `--card-inset` = 3px, concentric only while the row
    // is flush on both edges.
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const probe = document.createElement("div");
    card.appendChild(probe);
    probe.style.setProperty("inline-size", "var(--card-radius)");
    const token = probe.getBoundingClientRect().width;
    probe.remove();
    expect(token, "--card-radius resolves inside the card").toBeGreaterThan(0);
    expect(parseFloat(getComputedStyle(link).borderTopLeftRadius)).toBeCloseTo(token, 1);
  });
});

describe("the card's block position", () => {
  it.each(TIERS)("puts the card's bottom one --sp-1 above the TRIGGER at %s", (tier) => {
    // `bottom: calc(100% + var(--sp-1))` is relative to the ANCHOR, so the anchor must wrap
    // its trigger; measured against the trigger, so it holds at every tier.
    document.documentElement.dataset["pointer"] = tier;
    const { card, footer, btn } = mountFooter();
    open(btn, card);
    const gapProbe = document.createElement("div");
    footer.appendChild(gapProbe);
    gapProbe.style.setProperty("block-size", "var(--sp-1)");
    const sp1 = gapProbe.getBoundingClientRect().height;
    gapProbe.remove();
    expect(sp1).toBeGreaterThan(0);

    const top = btn.getBoundingClientRect().top;
    expect(
      card.getBoundingClientRect().bottom,
      `the card's bottom against the trigger's top ${top}`,
    ).toBeCloseTo(top - sp1, 0);
  });
});

describe("the ladder", () => {
  it("declares the surface AND the ink on hover, inside an any-hover at-rule", () => {
    // Two source assertions in one, because the two halves are one decision: the wash
    // alone cannot carry `--c-text-secondary` on this card at any depth.
    const body = ruleContaining(input, ".pill-account", "top").body;
    const hover = /@media\s*\(any-hover:\s*hover\)\s*\{\s*&:hover\s*\{([^}]*)\}/u.exec(body);
    expect(hover, "the hover sits inside an any-hover at-rule").not.toBeNull();
    expect(hover?.[1], "the app's aligned row wash").toMatch(/background:\s*var\(--c-hover\)/u);
    expect(hover?.[1], "and the ink lift, which the measurement requires").toMatch(
      /color:\s*var\(--c-text-primary\)/u,
    );
    expect(body, "never the primary-input query").not.toMatch(/@media\s*\(hover:\s*hover\)/u);
  });

  it("declares the same pair one rung deeper on press", () => {
    // A bare `a[href]` is outside 03-base.css's universal press, so this row declares its own;
    // on touch the press is the only feedback.
    const body = ruleContaining(input, ".pill-account", "top").body;
    const active = /&:active\s*\{([^}]*)\}/u.exec(body);
    expect(active, "the row declares its own press").not.toBeNull();
    expect(active?.[1]).toMatch(/background:\s*var\(--c-press\)/u);
    expect(active?.[1]).toMatch(/color:\s*var\(--c-text-primary\)/u);
  });

  it("lets the plan line INHERIT its resting ink rather than declaring it", () => {
    // Fails if a `.pill-account-plan { color }` rule returns: a declared colour beats the
    // inherited one and would pin the failing ink against the lift.
    const { card, link, plan, btn } = mountFooter();
    open(btn, card);
    expect(getComputedStyle(plan).color, "the plan line reads the row's ink").toBe(
      getComputedStyle(link).color,
    );
    expect(getComputedStyle(link).color, "which is the card's own ink at rest").toBe(
      getComputedStyle(card).color,
    );
    // And as source, because the computed halves would still agree if the rule were
    // reinstated with the same value — the point is that there is ONE writer.
    expect(input, "no .pill-account-plan colour rule").not.toMatch(
      /\.pill-account-plan\s*\{[^}]*color:/u,
    );
  });

  it("gives the external mark no colour of its own", () => {
    // `--c-text-tertiary` falls under 1.4.11's 3:1 in three of four states, so the mark
    // inherits the row. Read as SELECTORS: the stylesheet's own comment names the class.
    const selectors = allRules(input).map((r) => r.selector);
    expect(selectors.length, "the sheet parses").toBeGreaterThan(10);
    expect(
      selectors.filter((sel) => sel.includes("pill-account-out")),
      "no .pill-account-out rule exists",
    ).toEqual([]);
    const { card, link, btn } = mountFooter();
    open(btn, card);
    const mark = link.querySelector("svg");
    expect(mark, "the row carries the external mark").not.toBeNull();
    expect(getComputedStyle(mark as Element).color, "the mark inherits the row's ink").toBe(
      getComputedStyle(link).color,
    );
  });
});
