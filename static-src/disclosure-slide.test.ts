// Every box the transcript opens and shuts slides with one motion, in every engine: the `createDisclosure` tween's
// tokens, driven by script, because a CSS `block-size: auto` transition runs only where `interpolate-size` ships.
import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { slideBox, slideTurnFolds } from "./disclosure-row.js";
import { buildReasoning } from "./fundamentals/reasoning.js";
import { buildTurnFooter } from "./fundamentals/turn-footer.js";
import { buildEvent } from "./messages-events.js";
import { syncCodeReferences } from "./code-refs.js";

let style: HTMLStyleElement;
const hosts: HTMLElement[] = [];

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
});

afterEach(() => {
  for (const h of hosts.splice(0)) {
    h.remove();
  }
  vi.unstubAllGlobals();
});

function mount(node: HTMLElement): HTMLElement {
  const host = document.createElement("div");
  host.style.width = "600px";
  host.appendChild(node);
  document.body.appendChild(host);
  hosts.push(host);
  return node;
}

function token(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

function ms(t: string): number {
  return t.endsWith("ms") ? parseFloat(t) : parseFloat(t) * 1000;
}

function heightSlides(box: Element): Animation[] {
  return box.getAnimations().filter((a) => {
    const fx = a.effect;
    return fx instanceof KeyframeEffect && fx.getKeyframes().some((k) => "blockSize" in k);
  });
}

/** The one height slide running on `box`, with its timing. */
function slideOn(box: Element): { duration: number; easing: string; from: number; to: number } {
  const slides = heightSlides(box);
  expect(slides, "one height slide").toHaveLength(1);
  const fx = slides[0]?.effect as KeyframeEffect;
  const timing = fx.getTiming();
  const frames = fx.getKeyframes();
  return {
    duration: Number(timing.duration),
    easing: timing.easing ?? "",
    from: parseFloat(String(frames[0]?.["blockSize"])),
    to: parseFloat(String(frames.at(-1)?.["blockSize"])),
  };
}

/** Run `box`'s slides to their end, then let the settle they resolve run. */
async function finish(box: Element): Promise<void> {
  await Promise.all(
    heightSlides(box).map((a) => {
      a.finish();
      return a.finished;
    }),
  );
  await Promise.resolve();
}

function summaryOf(details: Element): HTMLElement {
  const s = details.querySelector<HTMLElement>(":scope > summary");
  if (s === null) {
    throw new Error("no summary");
  }
  return s;
}

/** The transcript's native `<details>` disclosures, each closed and laid out. */
function detailsBoxes(): [string, HTMLDetailsElement][] {
  const reasoning = buildReasoning("the retry helper is in fetch.go, ".repeat(20), false, false);
  const compaction = buildEvent({
    id: "cmp",
    turn: "t",
    seq: 1,
    ts: 1,
    kind: "compaction",
    payload: { summary: "The work so far, **summarised**." },
  } as Parameters<typeof buildEvent>[0]) as HTMLDetailsElement;
  const wrap = document.createElement("div");
  syncCodeReferences(wrap, [
    { license_name: "MIT", repository: "acme/x", url: "https://example.com" },
  ]);
  const refs = wrap.querySelector<HTMLDetailsElement>("details");
  if (refs === null) {
    throw new Error("no code references");
  }
  return [
    ["reasoning", mount(reasoning.root) as HTMLDetailsElement],
    ["compaction", mount(compaction) as HTMLDetailsElement],
    ["code references", mount(refs) as HTMLDetailsElement],
  ];
}

describe("the transcript's <details> disclosures slide open and shut", () => {
  it("open on a summary click with the disclosure tween's timing, from the summary to the full box", async () => {
    for (const [name, d] of detailsBoxes()) {
      const shut = d.getBoundingClientRect().height;
      summaryOf(d).click();
      expect(d.open, name).toBe(true);
      const slide = slideOn(d);
      expect(slide.duration, name).toBe(ms(token("--uip-disclosure-duration")));
      expect(slide.easing, name).toBe(token("--uip-disclosure-easing"));
      expect(slide.from, name).toBeCloseTo(shut, 2);
      await finish(d);
      expect(slide.to, name).toBeCloseTo(d.getBoundingClientRect().height, 2);
      expect(d.getBoundingClientRect().height, `${name} grew`).toBeGreaterThan(shut);
    }
  });

  it("stay open until the closing slide ends, and a second click mid-close reopens", async () => {
    for (const [name, d] of detailsBoxes()) {
      summaryOf(d).click();
      await finish(d);
      summaryOf(d).click();
      expect(d.open, `${name} renders through the close`).toBe(true);
      expect(slideOn(d).to, name).toBeLessThan(d.getBoundingClientRect().height);
      summaryOf(d).click();
      await finish(d);
      expect(d.open, `${name} reopened`).toBe(true);
      summaryOf(d).click();
      await finish(d);
      expect(d.open, `${name} shut`).toBe(false);
    }
  });

  it("flip at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce") }));
    for (const [name, d] of detailsBoxes()) {
      summaryOf(d).click();
      expect(d.open, name).toBe(true);
      expect(heightSlides(d), name).toHaveLength(0);
    }
  });

  it("render the compaction's summary before the slide measures it", async () => {
    const [, compaction] = detailsBoxes()[1] ?? [];
    if (compaction === undefined) {
      throw new Error("no compaction");
    }
    summaryOf(compaction).click();
    expect(compaction.querySelector(".compaction-body strong")?.textContent).toBe("summarised");
    await finish(compaction);
  });

  it("rebuild a closing code-reference box shut, not reopened", async () => {
    const wrap = mount(document.createElement("div"));
    const one = { license_name: "MIT", repository: "acme/x", url: "https://example.com" };
    syncCodeReferences(wrap, [one]);
    const first = wrap.querySelector<HTMLDetailsElement>("details");
    if (first === null) {
      throw new Error("no code references");
    }
    summaryOf(first).click();
    await finish(first);
    summaryOf(first).click();
    expect(first.open, "still painted while it slides").toBe(true);
    syncCodeReferences(wrap, [one, { ...one, repository: "acme/y" }]);
    expect(wrap.querySelector<HTMLDetailsElement>("details")?.open).toBe(false);
  });

  it("slide a sealed reasoning trace shut rather than snapping it", async () => {
    const view = buildReasoning("thinking", true, true);
    mount(view.root);
    view.seal();
    expect(view.root.open, "still painted while it slides").toBe(true);
    slideOn(view.root);
    await finish(view.root);
    expect(view.root.open).toBe(false);
  });
});

describe("the turn footer's info panel slides with the footer", () => {
  it("opens and closes through the same slide", async () => {
    const footer = mount(buildTurnFooter({ elapsedMs: 1200, changedFiles: {} }));
    const ledger = footer.querySelector<HTMLElement>(":scope > .turn-ledger-summary");
    if (ledger === null) {
      throw new Error("no ledger");
    }
    const shut = footer.getBoundingClientRect().height;
    ledger.click();
    expect(footer.dataset["info"]).toBe("open");
    expect(slideOn(footer).from).toBeCloseTo(shut, 2);
    await finish(footer);
    expect(footer.getBoundingClientRect().height).toBeGreaterThan(shut);

    ledger.click();
    expect(footer.dataset["info"], "the panel renders through the close").toBe("open");
    await finish(footer);
    expect(footer.dataset["info"]).toBeUndefined();
    expect(footer.getBoundingClientRect().height).toBeCloseTo(shut, 2);
  });
});

/** A turn card holding an open body, laid out with the app's sheet. */
function turnCard(): { card: HTMLElement; body: HTMLElement; fold: (folded: boolean) => void } {
  const card = document.createElement("div");
  card.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  for (let i = 0; i < 6; i++) {
    const p = document.createElement("p");
    p.textContent = `step ${String(i)}`;
    body.appendChild(p);
  }
  card.appendChild(body);
  mount(card);
  const fold = (folded: boolean): void => {
    slideTurnFolds([{ body, folded, apply: () => card.toggleAttribute("data-folded", folded) }]);
  };
  return { card, body, fold };
}

function beats(box: Element): { fade: Animation; slide: Animation } {
  const anims = box.getAnimations();
  const fade = anims.find((a) =>
    (a.effect as KeyframeEffect).getKeyframes().some((k) => "opacity" in k),
  );
  const [slide] = heightSlides(box);
  expect(anims, "two beats").toHaveLength(2);
  if (fade === undefined || slide === undefined) {
    throw new Error("missing a beat");
  }
  return { fade, slide };
}

async function settle(box: Element): Promise<void> {
  const anims = box.getAnimations();
  for (const a of anims) {
    a.finish();
  }
  await Promise.all(anims.map((a) => a.finished));
  await Promise.resolve();
}

function turnToken(body: Element, name: string): number {
  return ms(getComputedStyle(body).getPropertyValue(name).trim());
}

describe("a turn's fold runs its two beats in every engine", () => {
  it("leaves no CSS transition on the body, so no engine depends on interpolate-size", () => {
    const { card, body } = turnCard();
    expect(getComputedStyle(body).transitionDuration).toBe("0s");
    card.setAttribute("data-folded", "");
    expect(getComputedStyle(body).transitionDuration).toBe("0s");
  });

  it("closes by fading the content out, then sliding shut once the fade is nearly done", async () => {
    const { card, body, fold } = turnCard();
    const open = body.getBoundingClientRect().height;
    fold(true);
    expect(card.hasAttribute("data-folded"), "the card's state flips at once").toBe(true);
    expect(getComputedStyle(body).contentVisibility, "still rendered for the fade").toBe("visible");
    const { fade, slide } = beats(body);
    const exit = turnToken(body, "--dur-exit");
    expect(fade.effect?.getTiming().duration).toBe(exit);
    expect(fade.effect?.getTiming().delay ?? 0).toBe(0);
    const t = slide.effect?.getTiming();
    expect(t?.delay).toBeCloseTo(exit * 0.8, 5);
    expect(t?.duration).toBe(turnToken(body, "--fold-slide"));
    const frames = (slide.effect as KeyframeEffect).getKeyframes();
    expect(parseFloat(String(frames[0]?.["blockSize"]))).toBeCloseTo(open, 1);
    expect(parseFloat(String(frames.at(-1)?.["blockSize"]))).toBe(0);
    await settle(body);
    expect(body.getBoundingClientRect().height).toBe(0);
    expect(getComputedStyle(body).contentVisibility).toBe("hidden");
    expect(body.getAnimations()).toHaveLength(0);
  });

  it("opens by sliding with the content transparent, then fading it in", async () => {
    const { body, fold } = turnCard();
    const open = body.getBoundingClientRect().height;
    fold(true);
    await settle(body);
    fold(false);
    const { fade, slide } = beats(body);
    const slideMs = turnToken(body, "--fold-slide");
    expect(slide.effect?.getTiming().delay ?? 0).toBe(0);
    expect(slide.effect?.getTiming().duration).toBe(slideMs);
    expect(fade.effect?.getTiming().delay).toBe(slideMs);
    expect(fade.effect?.getTiming().duration).toBe(turnToken(body, "--dur-standard"));
    expect(getComputedStyle(body).opacity, "transparent while it slides").toBe("0");
    const frames = (slide.effect as KeyframeEffect).getKeyframes();
    expect(parseFloat(String(frames[0]?.["blockSize"]))).toBe(0);
    expect(parseFloat(String(frames.at(-1)?.["blockSize"]))).toBeCloseTo(open, 1);
    await settle(body);
    expect(body.getBoundingClientRect().height).toBeCloseTo(open, 1);
    expect(getComputedStyle(body).opacity).toBe("1");
  });

  it("reopens from where a close was interrupted", async () => {
    const { body, fold } = turnCard();
    fold(true);
    const { slide } = beats(body);
    slide.currentTime = Number(slide.effect?.getTiming().delay) + 100;
    const mid = body.getBoundingClientRect().height;
    expect(mid).toBeGreaterThan(0);
    fold(false);
    const frames = (beats(body).slide.effect as KeyframeEffect).getKeyframes();
    expect(parseFloat(String(frames[0]?.["blockSize"]))).toBeCloseTo(mid, 1);
    await settle(body);
    expect(getComputedStyle(body).contentVisibility).toBe("visible");
  });

  it("flips at once under reduced motion", () => {
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce") }));
    const { card, body, fold } = turnCard();
    fold(true);
    expect(card.hasAttribute("data-folded")).toBe(true);
    expect(body.getAnimations()).toHaveLength(0);
    expect(body.getBoundingClientRect().height).toBe(0);
  });
});

describe("a bulk fold", () => {
  it("measures every body before the first flip and flips them all before measuring again", () => {
    const cards = [turnCard(), turnCard(), turnCard()];
    const log: string[] = [];
    const rect = Element.prototype.getBoundingClientRect;
    vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      log.push("read");
      return rect.call(this);
    });
    try {
      slideTurnFolds(
        cards.map(({ card, body }, i) => ({
          body,
          folded: true,
          apply: () => {
            log.push(`flip ${String(i)}`);
            card.toggleAttribute("data-folded", true);
          },
        })),
      );
    } finally {
      vi.restoreAllMocks();
    }
    const firstFlip = log.indexOf("flip 0");
    expect(log.slice(0, firstFlip), "one read per body before the first flip").toEqual([
      "read",
      "read",
      "read",
    ]);
    expect(log.slice(firstFlip, firstFlip + 3)).toEqual(["flip 0", "flip 1", "flip 2"]);
    for (const { body } of cards) {
      expect(body.getAnimations(), "every body still slides").toHaveLength(2);
    }
  });
});

describe("slideBox", () => {
  it("still flips a box whose easing the engine refuses", () => {
    const box = mount(document.createElement("div"));
    box.style.setProperty("--uip-disclosure-easing", "not-an-easing");
    let open = false;
    const apply = (o: boolean): void => {
      open = o;
      box.style.blockSize = o ? "80px" : "20px";
    };
    apply(false);
    expect(() => {
      slideBox(box, true, apply);
    }).not.toThrow();
    expect(open).toBe(true);
    slideBox(box, false, apply);
    expect(open).toBe(false);
    expect(heightSlides(box)).toHaveLength(0);
  });

  it("flips an unrendered box at once", () => {
    const box = document.createElement("div");
    const applied: boolean[] = [];
    slideBox(box, true, (o) => applied.push(o));
    expect(applied).toEqual([true]);
    expect(heightSlides(box)).toHaveLength(0);
  });
});
