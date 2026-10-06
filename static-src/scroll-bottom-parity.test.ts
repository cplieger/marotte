// The resume control, docked at the foot of the timeline rail's own column.
import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { userEvent } from "vitest/browser";

import { loadCSS, mountAppCSS, ruleBody, ruleContaining } from "./__test-helpers__/css-rules.js";

const messagesCSS = loadCSS("13-messages.css");
const REVEAL_QUERY = "any-hover: hover";
/** The width at which the docked variant applies, and the width at which the gutter can hold the
 *  label. Both are the stylesheet's own thresholds. */
const DOCKED_PX = 920;
const LABEL_PX = 1120;

let style: HTMLStyleElement;
let area: HTMLElement | undefined;

interface Fixture {
  messages: HTMLElement;
  scroller: HTMLElement;
  card: HTMLElement;
  /** The last transcript control, so a case can reach the resume button by TAB from inside the
   *  transcript the way a reader would. */
  lastCardButton: HTMLElement;
  rail: HTMLElement;
  marker: HTMLElement;
  resume: HTMLButtonElement;
  label: HTMLElement;
}

/** The real transcript nesting, because every fact here is a consequence of it: the container
 *  query needs `#chat-area` to BE the container, the docked position needs
 *  `#messages-wrap-outer`'s `--rail-inset-*`, and the scrollHeight claim needs the control to be
 *  outside `#messages-wrap`. */
function build(chatWidth: number): Fixture {
  document.body.style.margin = "0";
  area = document.createElement("div");
  area.id = "chat-area";
  area.style.cssText = `position:fixed;top:0;left:0;margin:0;width:${String(chatWidth)}px;height:600px;`;
  const view = document.createElement("div");
  view.id = "chat-view";
  const outer = document.createElement("div");
  outer.id = "messages-wrap-outer";
  const scroller = document.createElement("div");
  scroller.id = "messages-wrap";
  const messages = document.createElement("div");
  messages.id = "messages";
  // Enough turns to overflow, so `scrollHeight` and the scroller's scroll room are real numbers
  // rather than zero.
  let first: HTMLElement | undefined;
  let lastButton: HTMLElement | undefined;
  for (let i = 0; i < 12; i++) {
    const card = document.createElement("div");
    card.className = "turn";
    card.style.cssText = "block-size:120px;";
    card.textContent = `turn ${String(i + 1)}`;
    const action = document.createElement("button");
    action.type = "button";
    action.className = "turn-action-btn";
    action.textContent = "copy";
    card.appendChild(action);
    messages.appendChild(card);
    first ??= card;
    lastButton = action;
  }
  scroller.appendChild(messages);
  outer.appendChild(scroller);

  const resume = document.createElement("button");
  resume.type = "button";
  resume.id = "scroll-bottom";
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  resume.appendChild(svg);
  const label = document.createElement("span");
  // The dynamic label, not the "Latest" fallback: the count is the fact the control exists to
  // carry, and it is also the widest thing it renders.
  label.textContent = "3 new blocks";
  resume.appendChild(label);
  outer.appendChild(resume);

  const rail = document.createElement("nav");
  rail.className = "turn-rail";
  const marker = document.createElement("button");
  marker.className = "rail-marker";
  marker.type = "button";
  marker.textContent = "7";
  rail.appendChild(marker);
  outer.appendChild(rail);

  view.appendChild(outer);
  area.appendChild(view);
  document.body.appendChild(area);
  if (first === undefined || lastButton === undefined) {
    throw new Error("no transcript card");
  }
  return {
    messages,
    scroller,
    card: first,
    lastCardButton: lastButton,
    rail,
    marker,
    resume,
    label,
  };
}

beforeAll(() => {
  style = mountAppCSS();
});

afterEach(() => {
  area?.remove();
  area = undefined;
  delete document.documentElement.dataset["pointer"];
});

afterAll(() => {
  style.remove();
});

/** The properties the control was brought into line with. Each one was a measured gap against
 *  `.rail-marker`; `color` is deliberately NOT among them — the marker rests at
 *  `--c-text-tertiary` because it is a passive mark, and this is a control the reader is meant
 *  to click. */
const ALIGNED = [
  "background-color",
  "border-top-color",
  "border-right-color",
  "border-bottom-color",
  "border-left-color",
  "border-radius",
  "min-height",
  "font-family",
  "font-size",
  "font-variant-numeric",
  "line-height",
] as const;

function styles(el: Element): Record<string, string> {
  const cs = getComputedStyle(el);
  return Object.fromEntries(ALIGNED.map((p) => [p, cs.getPropertyValue(p)]));
}

/** A token in PX. An unregistered custom property's computed value is its own token stream, so
 *  reading it back answers `1.5rem`; assigning it to a real length property is what absolutizes
 *  it. */
function tokenPx(host: HTMLElement, token: string): number {
  const probe = document.createElement("span");
  probe.style.cssText = `position:absolute;visibility:hidden;block-size:var(${token})`;
  host.appendChild(probe);
  const px = parseFloat(getComputedStyle(probe).blockSize);
  probe.remove();
  return px;
}

/** Two used values agree to the pixel. `toBeCloseTo`'s second argument is a digit count rather
 *  than a tolerance, so a 0.5 there asks for something else entirely. */
function near(actual: number, expected: number, what: string): void {
  expect(
    Math.abs(actual - expected),
    `${what}: ${String(actual)} vs ${String(expected)}`,
  ).toBeLessThanOrEqual(0.5);
}

/** The RESTING values, which is what the parity claim is about. */
async function settled(...els: Element[]): Promise<void> {
  await Promise.allSettled(els.flatMap((el) => el.getAnimations()).map((a) => a.finished));
  await new Promise<void>((resolve) => {
    requestAnimationFrame(() => {
      resolve();
    });
  });
}

describe("the reveal gate is live in this browser", () => {
  it("matches any-hover, so every hover case below measures the gated rule", () => {
    // The premise. Under `(any-hover: none)` the label is always visible by design, so the
    // icon-only case would fail for the wrong reason and the always-visible case would pass for the
    // wrong one.
    expect(window.matchMedia(`(${REVEAL_QUERY})`).matches).toBe(true);
  });
});

describe("the docked control reads as a rail row", () => {
  // Both POINTER TIERS, because the marker's skin is tier-independent and a rule that reached for
  // `--hit-floor` in any of these properties would diverge on the coarse one.
  for (const tier of ["fine", "coarse"] as const) {
    it(`resolves the rail marker's own values for every property that was aligned, on a ${tier} pointer`, async () => {
      document.documentElement.dataset["pointer"] = tier;
      const { marker, resume } = build(LABEL_PX);
      await settled(resume, marker);
      expect(styles(resume)).toEqual(styles(marker));
    });
  }

  it("and those values are the marker's rather than two elements agreeing on nothing", () => {
    // The control. Equality above is satisfied by two unstyled boxes, which is exactly what a
    // deleted rule looks like — so pin the marker against the page it is NOT: an unclassed button
    // in the same tree.
    const { marker, resume } = build(LABEL_PX);
    const plain = document.createElement("button");
    plain.type = "button";
    plain.textContent = "7";
    area?.appendChild(plain);
    expect(styles(marker)).not.toEqual(styles(plain));
    expect(styles(resume)).not.toEqual(styles(plain));
  });

  it("paints the rail's row box and answers the tier's floor, on a coarse pointer", () => {
    // Read against the TOKENS rather than against the marker, which the parity case above already
    // covers: this is what makes that parity a statement about the column's own two numbers instead
    // of two rules agreeing with each other.
    document.documentElement.dataset["pointer"] = "coarse";
    const { resume, marker } = build(LABEL_PX);
    // Probed inside `#messages-wrap-outer`, which is where the gutter column's tokens are declared:
    // a probe outside it inherits neither and reads 0.
    const host = resume.parentElement;
    if (host === null) {
      throw new Error("no wrap-outer");
    }
    const mark = tokenPx(host, "--rail-mark");
    const floor = tokenPx(host, "--hit-floor");
    expect(floor - mark).toBe(20);

    for (const [el, what] of [
      [resume, "control"],
      [marker, "marker"],
    ] as const) {
      const box = el.getBoundingClientRect();
      near(box.height, mark, `${what} paints the row box`);
      // The target, by hit test, because an expander contributes nothing to the rect.
      const cx = box.left + box.width / 2;
      const cy = box.top + box.height / 2;
      expect(document.elementFromPoint(cx, cy - floor / 2 + 1), `${what} target top`).toBe(el);
      expect(document.elementFromPoint(cx, cy + floor / 2 - 1), `${what} target bottom`).toBe(el);
    }
  });
});

describe("icon-only at rest, the label on hover", () => {
  it("is the rail's own column wide at rest, with no label in the box", () => {
    // Compared against the RAIL, which is `--rail-w` wide by declaration, rather than against a
    // probe reading that token: it is declared on `#messages-wrap-outer`, so a probe mounted
    // anywhere else resolves nothing and stretches (measured).
    const { resume, label, rail } = build(LABEL_PX);
    expect(getComputedStyle(label).display).toBe("none");
    expect(resume.getBoundingClientRect().width).toBe(rail.getBoundingClientRect().width);
  });

  it("shows the label on a real hover, and grows only to the RIGHT", async () => {
    const { resume, label } = build(LABEL_PX);
    const before = resume.getBoundingClientRect();

    await userEvent.hover(resume);

    const after = resume.getBoundingClientRect();
    expect(getComputedStyle(label).display).toBe("block");
    expect(after.width).toBeGreaterThan(before.width);
    // Pinned by its inline-START edge, which is what keeps the growth in the gutter instead of back
    // over the turn cards.
    expect(after.left).toBe(before.left);
    // And bounded: `max-inline-size` keeps it clear of the wrapper's own end.
    expect(after.right).toBeLessThan(area?.getBoundingClientRect().right ?? 0);
  });

  it("shows the label on keyboard focus too", async () => {
    // Hover alone would hide the count from a keyboard user permanently.
    const { resume, label, lastCardButton, card } = build(LABEL_PX);
    // THE POINTER IS PARKED FIRST, and this is not hygiene.
    await userEvent.hover(card);
    expect(resume.matches(":hover")).toBe(false);

    lastCardButton.focus();
    await userEvent.tab();
    expect(document.activeElement).toBe(resume);
    expect(resume.matches(":focus-visible")).toBe(true);
    expect(resume.matches(":hover")).toBe(false);
    expect(getComputedStyle(label).display).toBe("block");
  });

  it("withholds the label where the gutter cannot hold it", async () => {
    // At the docked threshold the leftover gutter is `--rail-w` and the label wants about 80px
    // more, so hovering could only produce a clipped or empty expansion. The width query is a FLOOR
    // on the reveal, not the reveal itself.
    const { resume, label } = build(DOCKED_PX);
    const before = resume.getBoundingClientRect().width;
    await userEvent.hover(resume);
    expect(getComputedStyle(label).display).toBe("none");
    expect(resume.getBoundingClientRect().width).toBe(before);
  });
});

describe("revealing the label moves nothing else", () => {
  it("leaves the transcript's scrollHeight and its own cards where they were", async () => {
    // The control is `position: absolute` in `#messages-wrap-outer`, a SIBLING of `#messages-wrap`,
    // so this is out of reach by construction — which is the whole reason a geometric reveal is
    // admissible here and is not in the turn footer.
    const { resume, messages, scroller, card } = build(LABEL_PX);
    // The two facts the claim RESTS on, asserted rather than assumed: the control is out of flow,
    // and it is not inside the scroller. Without them the numbers below are true by accident of
    // this fixture and could not fail.
    expect(getComputedStyle(resume).position).toBe("absolute");
    expect(scroller.contains(resume)).toBe(false);

    const scrollHeightBefore = messages.scrollHeight;
    const scrollableBefore = scroller.scrollHeight - scroller.clientHeight;
    const cardBefore = card.getBoundingClientRect();
    expect(scrollableBefore).toBeGreaterThan(100);

    await userEvent.hover(resume);

    expect(messages.scrollHeight).toBe(scrollHeightBefore);
    expect(scroller.scrollHeight - scroller.clientHeight).toBe(scrollableBefore);
    expect(card.getBoundingClientRect().toJSON()).toEqual(cardBefore.toJSON());
  });
});

describe("the reveal, read as source", () => {
  it("is gated on any-hover, never on hover", () => {
    // Those queries report only the PRIMARY input, and iPadOS answers `hover: none` with a trackpad
    // attached, so a `hover: hover` gate drops the rule on every touch-primary device. Only source
    // can answer which query a rule sits in.
    const rest = ruleContaining(messagesCSS, '[id="scroll-bottom"] > span', REVEAL_QUERY);
    expect(rest.body).toMatch(/display:\s*none/u);
    expect(messagesCSS).not.toContain("@media (hover: hover)");
  });

  it("leaves the label visible where there is no hover to reveal it with", () => {
    // Outside the query the width-gated rule stands unchanged: a touch device has no gesture to ask
    // with, so always-on is the right default rather than a fallback. Read as source because a test
    // page cannot answer `(any-hover: none)`.
    const container = ruleBody(messagesCSS, "@container chat-area (width >= 70rem)");
    const beforeHoverGate = container.split("@media")[0] ?? "";
    expect(beforeHoverGate).not.toBe("");
    const shown = ruleContaining(beforeHoverGate, '[id="scroll-bottom"] > span', "top");
    expect(shown.body).toMatch(/display:\s*block/u);
  });

  it("reveals on focus-visible as well as hover", () => {
    const shown = ruleContaining(messagesCSS, '[id="scroll-bottom"]:hover > span', REVEAL_QUERY);
    expect(shown.selector).toContain('[id="scroll-bottom"]:focus-visible > span');
    expect(shown.body).toMatch(/display:\s*block/u);
  });
});
