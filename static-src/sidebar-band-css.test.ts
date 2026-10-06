// THE SIDEBAR'S TWO ENDS ARE ONE BAND.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import sheet from "./css/10-shell-app.css?raw";

let style: HTMLStyleElement;

/** The panel's two ends, as `static/index.html` authors them: each holds a leading identity
 *  element and a trailing `.icon-btn`, which is the shape the shared band is about. The footer
 *  is the panel's LAST child, which is what makes the bottom inset the panel's to pay rather
 *  than the footer's. */
function mountSidebar(): { header: HTMLElement; footer: HTMLElement; buttons: HTMLElement[] } {
  const sidebar = document.createElement("nav");
  sidebar.id = "sidebar";

  const header = document.createElement("div");
  header.className = "sidebar-header";
  const logo = document.createElement("div");
  logo.className = "logo";
  logo.textContent = "marotte";
  const headerActions = document.createElement("div");
  headerActions.className = "sidebar-header-actions";
  const settings = document.createElement("button");
  settings.type = "button";
  settings.className = "icon-btn";
  headerActions.appendChild(settings);
  header.append(logo, headerActions);

  const footer = document.createElement("div");
  footer.className = "sidebar-footer";
  const anchor = document.createElement("div");
  anchor.className = "popup-anchor";
  // The mark and the address are ONE button inside the anchor now, with the card as its sibling —
  // the identity element this band is about.
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
  const subject = document.createElement("span");
  subject.className = "sr-only";
  subject.textContent = "Account and connection status";
  btn.append(dot, addr, subject);
  const card = document.createElement("span");
  card.id = "status-card";
  card.className = "pill-expand-content pill-status-content hidden";
  anchor.append(btn, card);
  const footerActions = document.createElement("div");
  footerActions.className = "sidebar-footer-actions";
  const logout = document.createElement("button");
  logout.type = "button";
  logout.id = "logout-btn";
  logout.className = "icon-btn";
  footerActions.appendChild(logout);
  footer.append(anchor, footerActions);

  sidebar.append(header, footer);
  document.body.replaceChildren(sidebar);
  return { header, footer, buttons: [settings, logout] };
}

/** A band is its BOX, hairline included. `02-reset.css` makes everything `border-box`, so the
 *  header's `border-block-end` is spent OUT of the band rather than added to it — which is what
 *  puts its hairline on the same line the chat toolbar's bottom edge lands on, the property
 *  01-tokens.css states at the token. */
function band(el: HTMLElement): number {
  return +el.getBoundingClientRect().height.toFixed(2);
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

describe("the two end bands", () => {
  it("render the same height", () => {
    const { header, footer } = mountSidebar();
    const h = band(header);
    const f = band(footer);
    expect(h, `header ${h}px against footer ${f}px`).toBeCloseTo(f, 1);
  });

  it("each spend ONE hairline inside that band, so their content bands agree too", () => {
    // Stated rather than left for a reader to trip over: BOTH ends now carry a divider — the
    // header's bottom hairline and the footer's top one, both solid — and each is spent out of the
    // shared band value, so the two boxes agree AND the two content bands agree at box − 1.
    const { header, footer } = mountSidebar();
    const top = parseFloat(getComputedStyle(header).borderBottomWidth);
    const bottom = parseFloat(getComputedStyle(footer).borderTopWidth);
    expect(top, "the header carries a bottom hairline").toBeCloseTo(1, 1);
    expect(bottom, "the footer carries a top hairline").toBeCloseTo(1, 1);
    expect(header.clientHeight).toBeCloseTo(footer.clientHeight, 1);
    expect(header.clientHeight).toBeCloseTo(band(header) - top, 1);
    expect(footer.clientHeight).toBeCloseTo(band(footer) - bottom, 1);
  });

  it("resolve to the app's chrome band, so the header's hairline still lines up", () => {
    // The value rather than only the agreement: the header is one of the bars that meet at the top
    // of the app, and `.chat-toolbar` reads the same token, so a sidebar-local value that drifted
    // from it would misalign the two hairlines across the panel's edge.
    const { header } = mountSidebar();
    const probe = document.createElement("div");
    probe.style.setProperty("block-size", "var(--titlebar-h)");
    document.body.appendChild(probe);
    const token = probe.getBoundingClientRect().height;
    probe.remove();
    expect(token).toBeGreaterThan(0);
    expect(band(header)).toBeCloseTo(token, 1);
  });

  it("clear both controls at the coarse hit floor, which is what the band is FOR", () => {
    // The floor lifts each end's `.icon-btn` from 32px to 44px, so the band has to hold 44 without
    // growing — otherwise `min-height` is doing nothing and the ends agree only on the mouse tier.
    document.documentElement.dataset["pointer"] = "coarse";
    try {
      const { header, footer, buttons } = mountSidebar();
      for (const btn of buttons) {
        expect(btn.getBoundingClientRect().height, "the floor applies").toBeCloseTo(44, 0);
      }
      expect(band(header)).toBeCloseTo(band(footer), 1);
      expect(band(footer)).toBeGreaterThan(44);
    } finally {
      delete document.documentElement.dataset["pointer"];
    }
  });
});

describe("neither end spells its own length", () => {
  it("declares the band once, and both ends read that declaration", () => {
    // The rendered assertions above pass for two literals that happen to agree, so this is the half
    // that catches the drift before it renders: exactly one writer of the band, and a `var()` read
    // at each end.
    const writers = [...sheet.matchAll(/--sidebar-band-h\s*:/g)];
    expect(writers, "one writer of --sidebar-band-h").toHaveLength(1);
    for (const [name, selector] of [
      ["header", ".sidebar-header"],
      ["footer", ".sidebar-footer"],
    ] as const) {
      const rule = sheet.match(new RegExp(`\\n\\${selector} \\{([^}]*)\\}`));
      expect(rule, `${name}'s rule is in this sheet`).not.toBeNull();
      const body = rule?.[1] ?? "";
      expect(body, `${name} reads the band`).toMatch(/min-height:[^;]*var\(--sidebar-band-h\)/);
      expect(body, `${name} declares no block-size literal`).not.toMatch(
        /(?:min-)?height:[^;]*\d+(?:\.\d+)?(?:rem|px|em)/,
      );
    }
  });
});
