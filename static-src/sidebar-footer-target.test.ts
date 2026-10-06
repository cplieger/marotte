// THE ACCOUNT ROW IS A TARGET, NOT A LINE OF TEXT.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;

/** The footer as `static/index.html` authors it: the anchor holding the merged trigger, the
 *  trigger holding the mark, the address and the `.sr-only` subject, the card as the trigger's
 *  SIBLING, then the trailing action cluster. */
function mountFooter(email: string): {
  footer: HTMLElement;
  btn: HTMLButtonElement;
  addr: HTMLElement;
  dot: HTMLElement;
  logout: HTMLElement;
} {
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

  // Out of flow (`position: absolute`), so the button has exactly TWO flex items.
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
  actions.appendChild(logout);

  footer.append(anchor, actions);
  sidebar.appendChild(footer);
  document.body.replaceChildren(sidebar);
  return { footer, btn, addr, dot, logout };
}

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

describe("the account row's box", () => {
  it("holds the mark and does not reach over the logout button", () => {
    // The row may not overlap its neighbour, which is the footer's other control and a 44px target
    // of its own on a finger.
    const { btn, dot, logout } = mountFooter("someone@example.invalid");
    const b = btn.getBoundingClientRect();
    const d = dot.getBoundingClientRect();
    expect(b.right).toBeLessThanOrEqual(logout.getBoundingClientRect().left);
    expect(d.left, "the mark sits inside the trigger").toBeGreaterThanOrEqual(b.left);
    expect(d.right).toBeLessThanOrEqual(b.right);
  });
});

describe("what the row must not cost the address", () => {
  it("keeps the address vertically centred in the band", () => {
    // Two centrings compose: the footer centres the button, the button centres the address.
    // Measured as the ink's own centre against the band's, which is the property the cap-band trim
    // exists to make exact (label-centring.test.ts owns the trim itself).
    const { footer, addr } = mountFooter("someone@example.invalid");
    const f = footer.getBoundingClientRect();
    const border = parseFloat(getComputedStyle(footer).borderTopWidth);
    const range = document.createRange();
    const text = addr.firstChild;
    if (text === null) {
      throw new Error("the address has no text node");
    }
    range.selectNodeContents(text);
    const ink = range.getBoundingClientRect();
    const inkCentre = ink.top + ink.height / 2;
    const bandCentre = f.top + border + (f.height - border) / 2;
    expect(
      Math.abs(inkCentre - bandCentre),
      `ink centre ${inkCentre} against the band's ${bandCentre}`,
    ).toBeLessThan(1.5);
  });

  it("still clips an address too long for the row, on one line", () => {
    const { addr } = mountFooter(
      "a-very-long-address-that-cannot-possibly-fit-in-this-row@example.invalid",
    );
    expect(getComputedStyle(addr).textOverflow).toBe("ellipsis");
    expect(getComputedStyle(addr).overflowX).toBe("hidden");
    expect(addr.scrollWidth).toBeGreaterThan(addr.clientWidth);
    // ONE LINE, so the clip is horizontal. Measured against a SHORT address's box rather than the
    // parent's height: this box is content-height, so a wrap shows up as it being taller than one
    // line rather than as it exceeding the band.
    const long = addr.getBoundingClientRect().height;
    const { addr: shortAddr } = mountFooter("a@b.invalid");
    expect(long).toBeCloseTo(shortAddr.getBoundingClientRect().height, 0);
  });

  it("keeps the box a BLOCK container, which is what the ellipsis needs", () => {
    // What the assertions above CANNOT see: `display: flex` with
    // `align-items: center` fills the band and centres the ink just as well, and Chromium still
    // reports `text-overflow: ellipsis`, `overflow-x: hidden` and `scrollWidth > clientWidth` —
    // every one of those passes.
    const { addr } = mountFooter("someone@example.invalid");
    const display = getComputedStyle(addr).display;
    expect(
      ["flex", "inline-flex", "grid", "inline-grid"],
      `the address resolves to display: ${display}`,
    ).not.toContain(display);
  });
});
