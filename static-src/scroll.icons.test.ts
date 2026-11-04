// Icons that layout moved are re-snapped by the scroll owner in the frame layout moved them, limited to the near
// band's rows and their turns' own icons, and a row entering the band is snapped before its first paint.
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type * as Dom from "./dom.js";
import type * as Skeleton from "./skeleton.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { clearDeviceScale, setDeviceScale } from "./__test-helpers__/device-scale.js";
import { frames, platformScroll, scene, wheel } from "./__test-helpers__/scroll-scene.js";
import { initIconCrisp } from "./icon-crisp.js";

vi.mock("./dom.js", async (importOriginal) => {
  const { domProxy: proxy } = await import("./__test-helpers__/scroll-scene.js");
  return { ...(await importOriginal<typeof Dom>()), $: proxy() };
});
vi.mock("./skeleton.js", async (importOriginal) => ({
  ...(await importOriginal<typeof Skeleton>()),
  loadMoreSkeleton: () => document.createElement("div"),
}));

const scroll = await import("./scroll.js");
const { messagesEl, land, reset } = scene(scroll);
const SVG_NS = "http://www.w3.org/2000/svg";

let css: HTMLStyleElement;
let view: HTMLElement | null = null;
beforeAll(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  css = mountAppCSS();
  initIconCrisp();
});
afterAll(() => {
  css.remove();
  vi.useRealTimers();
});
afterEach(async () => {
  if (view !== null) {
    scroll.detach();
    view.remove();
    view = null;
  }
  scroll.getScrollEl().id = "messagesWrap";
  await clearDeviceScale();
});

/** An `ic-ui` icon: stroke 1 on a 24-unit grid at 16px. */
function icon(): SVGSVGElement {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("class", "ic-ui");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.style.cssText = "display:block;inline-size:16px;block-size:16px;";
  const p = document.createElementNS(SVG_NS, "path");
  p.setAttribute("d", "M6 9l6 6 6-6");
  svg.append(p);
  return svg;
}

/** How far the icon's grid line 3 units in sits from the stroke's crisp device phase (frac(1 x DPR / 2)). */
function offPhase(svg: SVGSVGElement, untranslated = false): number {
  const dpr = devicePixelRatio;
  const ty = untranslated ? parseFloat(svg.style.translate.split(" ")[1] ?? "0") || 0 : 0;
  const y = svg.getBoundingClientRect().y - ty + 3 * (16 / 24);
  const want = (((dpr / 2) % 1) + 1) % 1;
  const d = Math.abs(((((dpr * y) % 1) + 1) % 1) - want);
  return Math.min(d, 1 - d);
}

function row(
  px: number,
  withIcon: boolean,
): { row: HTMLElement; body: HTMLElement; svg: SVGSVGElement | null } {
  const r = document.createElement("div");
  r.className = "msg-row";
  const body = document.createElement("div");
  body.style.cssText = `height:${String(px)}px;width:100%;`;
  const svg = withIcon ? icon() : null;
  if (svg !== null) {
    body.append(svg);
  }
  r.append(body);
  return { row: r, body, svg };
}

function turn(
  parts: HTMLElement[],
  footerIcon: boolean,
): { card: HTMLElement; footer: SVGSVGElement | null } {
  const card = document.createElement("div");
  card.className = "turn";
  const body = document.createElement("div");
  body.className = "turn-body";
  body.append(...parts);
  card.append(body);
  let footer: SVGSVGElement | null = null;
  if (footerIcon) {
    const f = document.createElement("div");
    f.className = "turn-footer";
    footer = icon();
    f.append(footer);
    card.append(f);
  }
  return { card, footer };
}

/** Turn N-1: a far row with an icon, 1500px of plain flow, the row that grows, a row with an icon; turn N: a row
 *  with an icon and a footer icon. Following at the end, so the far row is outside the near band. */
async function transcript(): Promise<{
  wrap: HTMLElement;
  far: SVGSVGElement;
  farRow: HTMLElement;
  grow: HTMLElement;
  card: HTMLElement;
  later: SVGSVGElement;
  next: SVGSVGElement;
  footer: SVGSVGElement;
  prevFooter: SVGSVGElement;
}> {
  const wrap = await reset();
  wrap.id = "messages-wrap";
  const v = document.createElement("div");
  v.className = "transcript-view is-active";
  messagesEl.append(v);
  view = v;
  const far = row(20, true);
  const flow = document.createElement("div");
  flow.style.cssText = "height:1500px;";
  const grow = row(40, false);
  const later = row(30, true);
  const prev = turn([far.row, flow, grow.row, later.row], true);
  const next = row(30, true);
  const last = turn([next.row], true);
  scroll.attach({ el: v, scrollTop: 0, readingState: "following" });
  v.append(prev.card, last.card);
  await land();
  await new Promise((r) => setTimeout(r, 400));
  await land();
  expect(
    { far: far.row.hasAttribute("data-near"), near: grow.row.hasAttribute("data-near") },
    "the premise: the far row is outside the near band, the growing row inside it",
  ).toEqual({ far: false, near: true });
  return {
    wrap,
    far: far.svg!,
    farRow: far.row,
    grow: grow.body,
    card: prev.card,
    later: later.svg!,
    next: next.svg!,
    footer: last.footer!,
    prevFooter: prev.footer!,
  };
}

describe("an icon below a transcript row that grows", () => {
  it("is snapped in the growth's own delivery, and a skipped row is never read", async () => {
    await setDeviceScale(1.5);
    const t = await transcript();
    const moved = [t.later, t.next, t.footer];
    const before = moved.map((s) => s.style.translate);
    const farReads = vi.spyOn(t.far, "getScreenCTM");
    let inDelivery: { changed: boolean[]; off: boolean[]; wasOff: boolean[] } | null = null;
    let first = true;
    const ro = new ResizeObserver(() => {
      if (first) {
        first = false;
        return;
      }
      inDelivery ??= {
        changed: moved.map((s, i) => s.style.translate !== before[i]),
        off: moved.map((s) => offPhase(s) > 0.02),
        wasOff: moved.map((s) => offPhase(s, true) > 0.02),
      };
    });
    ro.observe(t.card);
    await frames(1);

    // One 14px line at line-height 1.6: 33.6 device pixels, a new phase.
    t.grow.style.height = "62.4px";
    await frames(3);
    ro.disconnect();
    const reads = farReads.mock.calls.length;
    farReads.mockRestore();
    const snapped = moved.map((s) => s.style.translate);
    document.body.append(icon());
    await new Promise((r) => setTimeout(r, 400));
    await frames(2);

    expect({
      inDelivery,
      reads,
      later: moved.map((s, i) => s.style.translate === snapped[i]),
    }).toEqual({
      inDelivery: {
        changed: [true, true, true],
        off: [false, false, false],
        wasOff: [true, true, true],
      },
      reads: 0,
      later: [true, true, true],
    });
  });

  it("snaps a row entering the band before the next frame's animation-frame callbacks", async () => {
    await setDeviceScale(1.5);
    const t = await transcript();
    let atFrame: { snapped: boolean; wasOff: boolean } | null = null;
    const io = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          requestAnimationFrame(() => {
            atFrame ??= { snapped: offPhase(t.far) <= 0.02, wasOff: offPhase(t.far, true) > 0.02 };
          });
        }
      },
      { root: t.wrap, rootMargin: `${String(Math.round(t.wrap.clientHeight * 1.5))}px 0px` },
    );
    io.observe(t.farRow);
    await frames(2);

    wheel(t.wrap, -3);
    platformScroll(t.wrap, t.wrap.scrollTop - 1100);
    await land(400);
    io.disconnect();

    expect({ near: t.farRow.hasAttribute("data-near"), atFrame }).toEqual({
      near: true,
      atFrame: { snapped: true, wasOff: true },
    });
  });
});
