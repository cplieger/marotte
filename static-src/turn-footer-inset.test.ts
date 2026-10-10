// THE FOOTER BAND'S GEOMETRY: the band is the ROW's height with smaller controls centred in it, every
// control sits the same air off all four band edges on both tiers, the `i` starts on the card's ink
// gutter, and the panel's ink sits one gutter off the CARD. Phone cases run in an IFRAME
// (`width <= 40rem`), desktop in the 1280x720 page.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

type Trailing = "actions" | "rewind";

const mounted = new WeakMap<Document, HTMLElement>();

interface Mounted {
  card: HTMLElement;
  footer: HTMLElement;
  ledger: HTMLElement;
  info: HTMLElement;
  headerText: HTMLElement;
  last: HTMLElement;
  panel: HTMLElement;
  title: HTMLElement;
  lastRow: HTMLElement;
}

/** A turn card with its header and its footer, as `buildTurnHeader`,
 *  `buildTurnFooter`, `mountTurnActions` and `mountRewind` assemble one: the
 *  ledger button (glyph, mark, word, fact), then whichever trailing control the
 *  case is about, then the info panel spanning every column beneath. */
function mountFooter(doc: Document, trailing: Trailing): Mounted {
  const card = doc.createElement("div");
  card.className = "turn";

  const header = doc.createElement("div");
  header.className = "turn-header";
  const req = doc.createElement("div");
  req.className = "turn-req";
  const headerText = doc.createElement("span");
  headerText.className = "turn-req-text";
  headerText.textContent = "What the reader asked for.";
  req.appendChild(headerText);
  header.appendChild(req);

  const footer = doc.createElement("div");
  footer.className = "turn-footer";

  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = doc.createElement("span");
  info.className = "turn-ledger-info";
  const infoGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  infoGlyph.setAttribute("class", "ic-ui");
  info.appendChild(infoGlyph);
  const mark = doc.createElement("span");
  mark.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  text.textContent = "Failed";
  const fact = doc.createElement("span");
  fact.className = "turn-fact";
  fact.textContent = "12.0s";
  ledger.append(info, mark, text, fact);

  footer.append(ledger);

  let last: HTMLElement = ledger;
  if (trailing === "actions") {
    const slot = doc.createElement("span");
    slot.className = "turn-actions-buttons";
    const more = doc.createElement("details");
    more.className = "turn-actions-more";
    const summary = doc.createElement("summary");
    summary.className = "turn-action-btn turn-action-more";
    const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    glyph.setAttribute("class", "ic-ui");
    summary.appendChild(glyph);
    const group = doc.createElement("span");
    group.className = "turn-actions-group";
    more.append(summary, group);
    slot.appendChild(more);
    footer.appendChild(slot);
    last = slot;
  } else if (trailing === "rewind") {
    const btn = doc.createElement("button");
    btn.type = "button";
    btn.className = "turn-rewind";
    const glyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
    glyph.setAttribute("class", "ic-ui");
    const label = doc.createElement("span");
    label.className = "turn-rewind-label";
    label.textContent = "Rewind";
    btn.append(glyph, label);
    footer.appendChild(btn);
    last = btn;
  }

  // `renderInfoPanel`'s shape: one section, its heading, then a list of rows. Present
  // at rest like the builder's, where it is `display: none` and so occupies no grid
  // row — every band measurement above is taken with it mounted.
  const panel = doc.createElement("div");
  panel.className = "turn-info-panel";
  const section = doc.createElement("section");
  section.className = "turn-info-section";
  const title = doc.createElement("h4");
  title.className = "turn-info-title";
  title.textContent = "Timings";
  const rows = doc.createElement("ul");
  rows.className = "turn-info-rows";
  const lastRow = doc.createElement("li");
  lastRow.className = "turn-info-row";
  const label = doc.createElement("span");
  label.className = "turn-info-label";
  label.textContent = "Wall clock";
  const value = doc.createElement("span");
  value.className = "turn-info-value";
  value.textContent = "12.0s";
  lastRow.append(label, value);
  rows.appendChild(lastRow);
  section.append(title, rows);
  panel.appendChild(section);
  footer.appendChild(panel);

  card.append(header, footer);
  // Only this helper's PREVIOUS card goes, never `body.replaceChildren`: the phone
  // frame is a child of the page's body, so wiping it detaches the iframe and every
  // later phone measurement reads 0 against a blank document.
  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return { card, footer, ledger, info, headerText, last, panel, title, lastRow };
}

interface MountedDelegate {
  footer: HTMLElement;
  ledger: HTMLElement;
  info: HTMLElement;
  headerInk: Element;
  last: HTMLElement;
}

/** A delegate card with its header and its foot, as `buildSubagentCard` assembles
 *  one: the same `.turn-footer` row nested inside `.subagent-foot`, so the gutters
 *  under test are the ones 29-turns.css declares rather than a copy. */
function mountDelegate(doc: Document): MountedDelegate {
  const card = doc.createElement("div");
  card.className = "subagent-block";

  const header = doc.createElement("a");
  header.className = "subagent-header";
  header.href = "#";
  const icon = doc.createElement("span");
  icon.className = "subagent-icon tool-icon";
  const headerInk = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  headerInk.setAttribute("class", "ic-ui");
  icon.appendChild(headerInk);
  const name = doc.createElement("span");
  name.className = "subagent-name";
  name.textContent = "context-gatherer";
  header.append(icon, name);

  const foot = doc.createElement("div");
  foot.className = "subagent-foot";
  const footer = doc.createElement("div");
  footer.className = "turn-footer subagent-footer";

  const ledger = doc.createElement("button");
  ledger.type = "button";
  ledger.className = "turn-ledger-summary";
  const info = doc.createElement("span");
  info.className = "turn-ledger-info";
  const infoGlyph = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  infoGlyph.setAttribute("class", "ic-ui");
  info.appendChild(infoGlyph);
  const mark = doc.createElement("span");
  mark.className = "turn-ledger-glyph";
  const text = doc.createElement("span");
  text.className = "turn-ledger-text";
  const fact = doc.createElement("span");
  fact.className = "turn-fact";
  fact.textContent = "42.0s";
  ledger.append(info, mark, text, fact);

  footer.append(ledger);
  foot.appendChild(footer);
  card.append(header, foot);

  mounted.get(doc)?.remove();
  mounted.set(doc, card);
  doc.body.appendChild(card);
  return { footer, ledger, info, headerInk, last: fact };
}

/** Computed style in the element's OWN realm: Chromium's `window.getComputedStyle` returns an EMPTY
 *  declaration for another document's node, which reads as NaN layout findings. */
function cs(el: Element, pseudo?: string): CSSStyleDeclaration {
  const view = el.ownerDocument.defaultView ?? window;
  return view.getComputedStyle(el, pseudo ?? null);
}

/** The band in CSS px: the footer's own box (the card draws no internal seam). */
function band(footer: HTMLElement): number {
  return +footer.getBoundingClientRect().height.toFixed(2);
}

function insets(
  footer: HTMLElement,
  child: HTMLElement,
): { top: number; bottom: number; start: number; end: number } {
  const f = footer.getBoundingClientRect();
  const c = child.getBoundingClientRect();
  return {
    top: +(c.top - f.top).toFixed(2),
    bottom: +(f.bottom - c.bottom).toFixed(2),
    start: +(c.left - f.left).toFixed(2),
    end: +(f.right - c.right).toFixed(2),
  };
}

/** A length token off the document the case is measured in. */
function token(doc: Document, name: string): number {
  const probe = doc.createElement("div");
  probe.style.setProperty("block-size", `var(${name})`);
  doc.body.appendChild(probe);
  const px = probe.getBoundingClientRect().height;
  probe.remove();
  return px;
}

/** The size a control's target reaches on one axis, paint plus whatever its `::after`
 *  expander adds. The pseudo has no rect of its own, so this reads the resolved inset,
 *  negative where it overhangs. */
function target(el: HTMLElement, axis: "block" | "inline"): number {
  const rect = el.getBoundingClientRect();
  const paint = axis === "block" ? rect.height : rect.width;
  const after = cs(el, "::after");
  const start = parseFloat(axis === "block" ? after.insetBlockStart : after.insetInlineStart);
  const end = parseFloat(axis === "block" ? after.insetBlockEnd : after.insetInlineEnd);
  if (Number.isNaN(start) || Number.isNaN(end)) {
    return paint;
  }
  return +(paint - start - end).toFixed(2);
}

beforeAll(() => {
  style = mountAppCSS();
  frame = document.createElement("iframe");
  frame.width = "390";
  frame.height = "844";
  document.body.appendChild(frame);
  const inner = frame.contentDocument;
  if (inner === null) {
    throw new Error("iframe has no contentDocument");
  }
  phone = inner;
  phone.documentElement.dataset["pointer"] = "coarse";
  const sheet = phone.createElement("style");
  sheet.textContent = style.textContent;
  phone.head.appendChild(sheet);
});

afterAll(() => {
  frame.remove();
  style.remove();
});

describe("the band and the control in it", () => {
  it("is on the tier the complaint was made on", () => {
    expect(phone.defaultView?.innerWidth).toBeLessThanOrEqual(640);
    expect(cs(phone.documentElement).getPropertyValue("--hit-floor").trim()).toBe("2.75rem");
  });

  it("gives a finger a 44px band with the control CENTRED in it, not filling it", () => {
    // The floor lifting the paint to the band is what put the wash flush against the card's
    // edges on a phone; the paint stays `--ctl-h-sm` and the expander carries the target.
    const { footer, ledger } = mountFooter(phone, "rewind");
    const floor = token(phone, "--hit-floor");
    expect(floor).toBeCloseTo(44, 0);
    expect(band(footer)).toBeCloseTo(floor, 0);
    expect(ledger.getBoundingClientRect().height).toBeCloseTo(token(phone, "--ctl-h-sm"), 0);
    expect(target(ledger, "block"), "the target still clears the floor").toBeGreaterThanOrEqual(
      floor,
    );
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px against bottom ${i.bottom}px`).toBeCloseTo(i.bottom, 1);
    expect(i.top, "so the wash keeps air above and below").toBeGreaterThan(1);
  });

  it("keeps the mouse tier's band at the dense height and CENTRES a smaller control", () => {
    // Neither may the band shrink to the control nor the control
    // stretch to the band, or the toggle paints a full-height slab beside pill-shaped buttons.
    expect(window.innerWidth).toBeGreaterThan(640);
    const { footer, ledger } = mountFooter(document, "rewind");
    const floor = token(document, "--hit-floor");
    const dense = token(document, "--ctl-h-dense");
    const small = token(document, "--ctl-h-sm");
    expect(floor).toBeCloseTo(24, 0);
    expect(dense).toBeCloseTo(32, 0);
    expect(band(footer)).toBeCloseTo(dense, 0);
    const h = ledger.getBoundingClientRect().height;
    expect(h, "the paint is the control box").toBeCloseTo(Math.max(small, floor), 0);
    expect(h, "which is shorter than the band").toBeLessThan(dense);
    expect(target(ledger, "block"), "and the target still clears the floor").toBeGreaterThanOrEqual(
      floor,
    );
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px against bottom ${i.bottom}px`).toBeCloseTo(i.bottom, 1);
    expect(i.top, "so the wash paints a pill rather than a slab").toBeGreaterThan(1);
  });

  it("keeps the ledger's box off the band's leading edge while the `i` keeps the card's ink gutter", () => {
    // The leading air clears the card's corner arc, so the control's own radius is not a
    // nesting, and the `i` still lines up with the HEADER's own text rather than a literal.
    for (const [where, doc] of [
      ["phone", phone],
      ["desktop", document],
    ] as const) {
      const { footer, ledger, info, headerText } = mountFooter(doc, "rewind");
      const i = insets(footer, ledger);
      const arc = parseFloat(cs(footer).borderEndStartRadius);
      expect(arc, `${where} the band rounds the card's corner`).toBeGreaterThan(0);
      expect(i.start, `${where} leading ${i.start}px against a ${arc}px arc`).toBeGreaterThan(arc);
      expect(info.getBoundingClientRect().left, `${where} ink gutter`).toBeCloseTo(
        headerText.getBoundingClientRect().left,
        1,
      );
    }
  });
});

describe("the trailing control", () => {
  /** The trailing control the case is about: Rewind, or the `…` trigger standing for the actions. */
  function trailingControl(m: Mounted): HTMLElement {
    const el = m.last.matches(".turn-rewind")
      ? m.last
      : m.last.querySelector<HTMLElement>(".turn-action-more");
    if (el === null) {
      throw new Error("no trailing control");
    }
    return el;
  }

  it("sits the same air off the band's top, bottom and trailing edge as the ledger", () => {
    // One rule for every control in the row, on both tiers: no fill reaches the card's edge.
    // The `…` trigger exists only where the actions collapse, so desktop measures Rewind.
    for (const [label, doc, trailing] of [
      ["phone actions", phone, "actions"],
      ["phone rewind", phone, "rewind"],
      ["desktop rewind", document, "rewind"],
    ] as const) {
      const m = mountFooter(doc, trailing);
      const lead = insets(m.footer, m.ledger);
      const i = insets(m.footer, trailingControl(m));
      expect(i.top, `${label} top`).toBeCloseTo(lead.top, 1);
      expect(i.bottom, `${label} bottom`).toBeCloseTo(lead.bottom, 1);
      expect(i.end, `${label} end against the ledger's start`).toBeCloseTo(lead.start, 1);
      expect(i.end, `${label} end`).toBeGreaterThan(1);
    }
  });

  it("paints the ledger's box: no resting fill and no resting border", () => {
    for (const trailing of ["actions", "rewind"] as const) {
      const m = mountFooter(phone, trailing);
      const el = trailingControl(m);
      expect(cs(el).backgroundColor, trailing).toBe(cs(m.ledger).backgroundColor);
      expect(cs(el).borderTopWidth, trailing).toBe("0px");
      expect(cs(el).borderTopLeftRadius, trailing).toBe(cs(m.ledger).borderTopLeftRadius);
    }
  });

  it("is a square when it is icon-only, with the hit floor back past its paint", () => {
    // Both trailing controls are icon-only on the phone; the expander carries the target on
    // each axis the paint is short of the floor.
    const floor = token(phone, "--hit-floor");
    for (const trailing of ["actions", "rewind"] as const) {
      const el = trailingControl(mountFooter(phone, trailing));
      const r = el.getBoundingClientRect();
      expect(r.width, `${trailing} square`).toBeCloseTo(r.height, 1);
      expect(r.height, `${trailing} paint under the floor`).toBeLessThan(floor);
      expect(target(el, "block"), `${trailing} target height`).toBeGreaterThanOrEqual(floor);
      expect(target(el, "inline"), `${trailing} target width`).toBeGreaterThanOrEqual(floor);
    }
  });
});

/** The panel SLIDES open: `block-size` and `padding-block` both transition, so a read
 *  taken in the same task as the flip returns the PRE-transition value — measured, 0px
 *  of padding against a resolved 12. Wait for the animations the flip starts. */
async function settle(el: Element): Promise<void> {
  await new Promise<void>((done) => {
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        done();
      });
    });
  });
  await Promise.all(el.getAnimations().map((a) => a.finished.catch(() => undefined)));
}

describe("the panel the ledger opens", () => {
  it("holds its ink one gutter off the CARD on all four edges", async () => {
    // Measured against the CARD, because the band's inset sits between the two, and held
    // against the `i`'s own ink gutter so one value stays the card's ink gutter.
    const { card, footer, info, panel, title, lastRow } = mountFooter(document, "rewind");
    footer.dataset["info"] = "open";
    await settle(panel);
    const gutter =
      info.getBoundingClientRect().left - card.getBoundingClientRect().left - card.clientLeft;
    expect(gutter).toBeGreaterThan(0);

    const p = cs(panel);
    for (const [edge, value] of Object.entries({
      top: p.paddingBlockStart,
      bottom: p.paddingBlockEnd,
    })) {
      expect(parseFloat(value), `panel ${edge}`).toBeCloseTo(gutter, 1);
    }

    // `.turn` declares no padding, so its border box plus `clientLeft` IS the column
    // every child's ink is measured in.
    const cardBox = card.getBoundingClientRect();
    const inner = { left: cardBox.left + card.clientLeft, width: card.clientWidth };
    const panelBox = panel.getBoundingClientRect();
    expect(
      panelBox.left + parseFloat(p.paddingInlineStart) - inner.left,
      "the panel's ink starts on the card's gutter",
    ).toBeCloseTo(gutter, 1);
    expect(
      inner.left + inner.width - (panelBox.right - parseFloat(p.paddingInlineEnd)),
      "the panel's ink ends on the card's gutter",
    ).toBeCloseTo(gutter, 1);

    // Rendered, because the declaration is only half the claim: the panel's first ink
    // has to land in the card's ink column, and its last row has to clear the bottom.
    expect(title.getBoundingClientRect().left, "the heading lines up with the `i`").toBeCloseTo(
      info.getBoundingClientRect().left,
      1,
    );
    expect(
      panelBox.bottom - lastRow.getBoundingClientRect().bottom,
      "the last row clears the panel's bottom edge",
    ).toBeCloseTo(gutter, 1);
  });
});

describe("the delegate card mounts this row and gets the same gutters", () => {
  it("lines the `i` up with its own header's ink, on the card's own 12px", () => {
    // `.subagent-header` is `padding: var(--sp-2) var(--sp-3)`, so a delegate's footer shares every
    // card's gutter. Compared against the header glyph, not a literal.
    const { headerInk, info } = mountDelegate(document);
    expect(info.getBoundingClientRect().left).toBeCloseTo(
      headerInk.getBoundingClientRect().left,
      1,
    );
  });

  it("keeps the row's gutters symmetric, and insets the fact to match the `i`", () => {
    // With no trailing control the flexible track is slack; the button's insets keep its press box
    // symmetric on the `i` and the fact.
    const { footer, ledger, last } = mountDelegate(document);
    const lead = parseFloat(cs(ledger).paddingInlineStart);
    expect(lead).toBeGreaterThan(0);
    expect(parseFloat(cs(footer).paddingInlineEnd)).toBeCloseTo(
      parseFloat(cs(footer).paddingInlineStart),
      1,
    );
    expect(parseFloat(cs(ledger).paddingInlineEnd)).toBeCloseTo(lead, 1);
    expect(
      last.getBoundingClientRect().right,
      "the fact is inset inside the button, not flush with it",
    ).toBeCloseTo(ledger.getBoundingClientRect().right - lead, 1);
  });
});
