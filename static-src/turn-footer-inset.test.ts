// THE FOOTER BAND'S GEOMETRY, in real layout: the band is the ROW's own height with its
// controls painting their own smaller box centred in it, the `i` starts on the card's
// ink gutter (which the button carries, not the row), the trailing gutter equals that
// leading one whatever the trailing child is, Rewind may not go flush because it paints
// a resting border, and the panel holds its ink one gutter off the CARD. The `…`
// collapse and Rewind's word live behind `width <= 40rem`, so the phone cases are
// measured in an IFRAME and the desktop ones in the page (1280x720).
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let style: HTMLStyleElement;
let frame: HTMLIFrameElement;
let phone: Document;

type Trailing = "actions" | "rewind";

/** The card this helper last mounted, per document. */
const mounted = new WeakMap<Document, HTMLElement>();

interface Mounted {
  card: HTMLElement;
  footer: HTMLElement;
  ledger: HTMLElement;
  /** The `i`, whose leading edge is the ink the gutter is about. */
  info: HTMLElement;
  /** The header's request text, the ink the footer's has to line up with. */
  headerText: HTMLElement;
  last: HTMLElement;
  /** The panel the ledger opens, and the two rows of ink the gutter has to hold off
   *  its four edges. */
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
  /** The `i`, whose leading edge is the ink the gutter is about. */
  info: HTMLElement;
  /** The header's identity glyph, the ink this card's foot has to line up with. */
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

/** Computed style resolved in the element's OWN realm. `window.getComputedStyle`
 *  hands back an EMPTY declaration for a node in another document in Chromium, so
 *  every read below would be `""` for the iframe cases and every derived number
 *  `NaN` — which reads as a layout finding rather than a cross-realm call. */
function cs(el: Element, pseudo?: string): CSSStyleDeclaration {
  const view = el.ownerDocument.defaultView ?? window;
  return view.getComputedStyle(el, pseudo ?? null);
}

/** The band, in CSS px. It IS the footer's box: both helpers here used to subtract
 *  the row's own `border-top`, which was the seam between the body and the ledger
 *  rather than part of the row — and the card draws no internal seam any more
 *  (29-turns.css's file header), so the term was structurally zero and went. The
 *  numbers below are unchanged by that: the border was inside this border box, so
 *  its removal left the box's top edge where it was and took 1px off the height,
 *  which is what the subtraction had been correcting for. */
function band(footer: HTMLElement): number {
  return +footer.getBoundingClientRect().height.toFixed(2);
}

/** The gaps between a child's box and the band it sits in. */
function insets(
  footer: HTMLElement,
  child: HTMLElement,
): { top: number; bottom: number; end: number } {
  const f = footer.getBoundingClientRect();
  const c = child.getBoundingClientRect();
  return {
    top: +(c.top - f.top).toFixed(2),
    bottom: +(f.bottom - c.bottom).toFixed(2),
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

/** The height a control's target reaches, paint plus whatever its `::after`
 *  expander adds on the block axis. The pseudo has no rect of its own, so this
 *  reads the resolved inset — negative where it overhangs. */
function targetHeight(el: HTMLElement): number {
  const paint = el.getBoundingClientRect().height;
  const after = cs(el, "::after");
  const start = parseFloat(after.insetBlockStart);
  const end = parseFloat(after.insetBlockEnd);
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

  it("gives a finger a 44px band with the control filling it", () => {
    const { footer, ledger } = mountFooter(phone, "rewind");
    const floor = token(phone, "--hit-floor");
    expect(floor).toBeCloseTo(44, 0);
    // Band and control coincide here and there is nothing left to centre: the floor is
    // 44 against `--ctl-h-dense` 40, so the row's `max()` and the control's own
    // `max(--ctl-h-sm, --hit-floor)` both resolve to the floor.
    expect(band(footer)).toBeCloseTo(floor, 0);
    expect(ledger.getBoundingClientRect().height).toBeCloseTo(floor, 0);
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px`).toBeCloseTo(0, 1);
    expect(i.bottom, `bottom ${i.bottom}px`).toBeCloseTo(0, 1);
  });

  it("keeps the mouse tier's band at the dense height and CENTRES a smaller control", () => {
    // The tier the two come apart on, and the defect that named this case: the band must
    // not shrink to the control and the control must not stretch to the band, or the
    // toggle's hover and press paint a full-height slab where the buttons at the row's
    // other end paint a pill. The paint is 61-mcp-tools.css's `--ctl-h-sm` lifted by the
    // same floor, so the two ends cannot diverge.
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
    expect(targetHeight(ledger), "and the target still clears the floor").toBeGreaterThanOrEqual(
      floor,
    );
    const i = insets(footer, ledger);
    expect(i.top, `top ${i.top}px against bottom ${i.bottom}px`).toBeCloseTo(i.bottom, 1);
    expect(i.top, "so the wash paints a pill rather than a slab").toBeGreaterThan(1);
  });

  it("reaches the band's leading edge while the `i` keeps the card's ink gutter", () => {
    // The gutter moved from the footer onto the button, so the box reaches the edge
    // and the ink does not. Compared against the HEADER's own text rather than a
    // literal, because lining up with the rest of the card is the whole property.
    const { footer, ledger, info, headerText } = mountFooter(document, "rewind");
    expect(cs(footer).paddingInlineStart).toBe("0px");
    expect(ledger.getBoundingClientRect().left).toBeCloseTo(footer.getBoundingClientRect().left, 1);
    expect(info.getBoundingClientRect().left).toBeCloseTo(
      headerText.getBoundingClientRect().left,
      1,
    );
  });
});

describe("the trailing control", () => {
  it("keeps Rewind off the band's edges, because it paints a resting border", () => {
    // The one child that may not go flush. Both tiers, because its border is there
    // at every width and the band is tight on the mouse tier too.
    for (const [where, doc] of [
      ["phone", phone],
      ["desktop", document],
    ] as const) {
      const { footer, last } = mountFooter(doc, "rewind");
      expect(cs(last).borderTopWidth, `${where} resting border`).not.toBe("0px");
      const i = insets(footer, last);
      expect(i.top, `${where} top ${i.top}px`).toBeGreaterThan(1);
      expect(i.bottom, `${where} bottom ${i.bottom}px`).toBeGreaterThan(1);
      expect(i.end, `${where} end ${i.end}px`).toBeGreaterThan(1);
    }
  });

  it("gives Rewind's target the hit floor back past its paint", () => {
    // What the inset above costs, and the expander is what pays it: the paint is
    // smaller than the floor, the target is not.
    for (const [where, doc] of [
      ["phone", phone],
      ["desktop", document],
    ] as const) {
      const { last } = mountFooter(doc, "rewind");
      const floor = token(doc, "--hit-floor");
      const paint = last.getBoundingClientRect().height;
      expect(paint, `${where} paint ${paint}px against floor ${floor}px`).toBeLessThan(floor);
      expect(targetHeight(last), `${where} target`).toBeGreaterThanOrEqual(floor);
      // Width stays on the floor, so the expander only grows the block axis and
      // cannot reach the control 8px to its left.
      expect(cs(last, "::after").insetInlineStart).toBe("0px");
      expect(last.getBoundingClientRect().width).toBeGreaterThanOrEqual(floor);
    }
  });

  it("lets the … trigger take the control box, because it paints nothing at rest", () => {
    // The other trailing child, and the opposite call: it takes the shared control box
    // and needs no inset of its own, which on this tier is the band exactly.
    const { footer, last } = mountFooter(phone, "actions");
    const trigger = last.querySelector<HTMLElement>(".turn-action-more");
    expect(trigger).not.toBeNull();
    if (trigger === null) {
      return;
    }
    expect(cs(trigger).backgroundColor).toBe("rgba(0, 0, 0, 0)");
    expect(cs(trigger).borderTopWidth).toBe("0px");
    expect(trigger.getBoundingClientRect().height).toBeCloseTo(band(footer), 0);
  });

  it("ends on the SAME gutter the `i` starts on, whatever the trailing child is", () => {
    // The retired `:has()` rule tightened this edge to 4px for a row ending in a
    // control, which put Rewind's box 4px from the card while the `i`'s ink sat 12px
    // from the other side — reported as the two insets not matching. Held against
    // the LEADING declaration rather than a literal, and over both trailing
    // children, because a value that only matched for the text case is the shape
    // being removed. The fact slot is not a trailing child at all — it sits inside the
    // ledger button — so the two controls are the whole population.
    for (const trailing of ["actions", "rewind"] as const) {
      const { footer, ledger, last } = mountFooter(document, trailing);
      const lead = parseFloat(cs(ledger).paddingInlineStart);
      expect(lead, `${trailing} leading`).toBeGreaterThan(0);
      expect(parseFloat(cs(footer).paddingInlineEnd), `${trailing} trailing`).toBeCloseTo(lead, 1);
      expect(insets(footer, last).end, `${trailing} last child`).toBeCloseTo(lead, 1);
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
    // Measured against the CARD, the only reading the panel's asymmetric inset can be
    // judged by (`.turn-info-panel` in 29-turns.css owns why it is asymmetric), and held
    // against the ledger's own gutter so one value stays the card's ink gutter.
    const { card, footer, ledger, info, panel, title, lastRow } = mountFooter(document, "rewind");
    footer.dataset["info"] = "open";
    await settle(panel);
    const gutter = parseFloat(cs(ledger).paddingInlineStart);
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
    // `.subagent-header` is `padding: var(--sp-2) var(--sp-3)`, so a delegate's
    // identity ink starts on the same gutter every other card's does — which is what
    // the retired 8px override in 14-tools.css was wrong about: the FOOT was the
    // outlier inside its own card, not a denser surface. Compared against the header
    // glyph rather than a literal, the way the turn card's case compares against its
    // header text.
    const { headerInk, info } = mountDelegate(document);
    expect(info.getBoundingClientRect().left).toBeCloseTo(
      headerInk.getBoundingClientRect().left,
      1,
    );
  });

  it("keeps the row's gutters symmetric, and insets the fact to match the `i`", () => {
    // With no trailing control there is nothing whose box ends on the trailing gutter:
    // the fact sits INSIDE the button (29-turns.css), so the flexible track beside it
    // is the row's slack. The two gutters are still one value, and the button's own
    // insets are what make its press box symmetric on its content — the `i` and the
    // fact sit the same distance inside it.
    const { footer, ledger, last } = mountDelegate(document);
    const lead = parseFloat(cs(ledger).paddingInlineStart);
    expect(lead).toBeGreaterThan(0);
    expect(parseFloat(cs(footer).paddingInlineEnd)).toBeCloseTo(lead, 1);
    expect(parseFloat(cs(ledger).paddingInlineEnd)).toBeCloseTo(lead, 1);
    expect(
      last.getBoundingClientRect().right,
      "the fact is inset inside the button, not flush with it",
    ).toBeCloseTo(ledger.getBoundingClientRect().right - lead, 1);
  });
});
