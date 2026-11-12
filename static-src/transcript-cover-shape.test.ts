// The cover's skeleton is one turn filling the chat viewport: a prompt band at the top, reply lines
// down to the footer band, on the real transcript column. The real latest turn, which a chat opens
// scrolled to, has the same inline edges, the same bottom edge and the same footer band, so the
// cover's lift moves nothing. Real store, renderer, scroll controller and the whole app sheet, at
// the phone, tablet and desktop cases in both themes.
import { describe, it, expect, beforeAll, afterAll, beforeEach, afterEach } from "vitest";
import { page } from "vitest/browser";
import type { Session } from "./types.js";
import { makeSession } from "./__test-helpers__/model.js";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import type { Entry } from "./wire/types.gen.js";

const area = document.createElement("div");
area.id = "chat-area";
area.innerHTML =
  '<div id="chat-view" data-tab-view>' +
  '<div id="messages-wrap-outer"><div id="messages-wrap"><div id="messages"></div></div></div>' +
  '<button id="scroll-bottom" type="button"><span></span></button>' +
  '<button id="send-btn" type="button"></button><textarea id="prompt-input"></textarea>' +
  "</div>";
document.body.appendChild(area);

const store = await import("./store.js");
const messages = await import("./messages.js");
const cover = await import("./transcript-cover.js");

messages.mountChatView();

/** Past the cover's show window. */
const PAST_DELAY_MS = 320;

interface DeviceCase {
  readonly name: string;
  readonly width: number;
  readonly height: number;
  readonly pointer: "fine" | "coarse";
}

const DEVICES: readonly DeviceCase[] = [
  { name: "phone", width: 390, height: 844, pointer: "coarse" },
  { name: "tablet", width: 820, height: 1180, pointer: "coarse" },
  { name: "desktop", width: 1440, height: 900, pointer: "fine" },
];

let seq = 0;

function sealed(turnID: string, at: number, kind: Entry["kind"], payload: unknown): Entry {
  return { id: `${turnID}-e${String(at)}`, turn: turnID, kind, seq: at, ts: at + 1, payload };
}

function proseTurn(s: Session, n: number, paragraphs: number): void {
  const t = `${s.id}-t${String(n)}`;
  const text = Array.from(
    { length: paragraphs },
    (_, i) => `paragraph ${String(i)} of a reply that runs on for a while in plain prose`,
  ).join("\n\n");
  s.turns.set(t, {
    entries: [
      sealed(t, 0, "turn_open", { prompt: { id: `${t}-p`, text: "go on" }, source: "prompt", n }),
      sealed(t, 1, "text", { text }),
      sealed(t, 2, "turn_close", { outcome: "completed" }),
    ],
    openEntries: new Map(),
  });
  s.turn_order.push(t);
  s.turn_count = s.turn_order.length;
}

async function frames(n = 2): Promise<void> {
  for (let i = 0; i < n; i++) {
    await new Promise((r) => requestAnimationFrame(() => r(undefined)));
  }
}

/** Paint a chat whose latest turn has `paragraphs` paragraphs, then hold its cover past the window. */
async function paintThenCover(
  paragraphs: number,
): Promise<{ real: HTMLElement; skel: HTMLElement }> {
  const s = makeSession({ id: `cs-${String(++seq)}`, name: "shape" });
  proseTurn(s, 1, 6);
  proseTurn(s, 2, paragraphs);
  store.setSessions([s]);
  store.setActive(s.id);
  store.setPainted(s.id);
  store.bumpMessages(s.id, "load");
  await Promise.resolve();
  await frames(3);
  const turns = messages.activeTranscriptView()?.querySelectorAll<HTMLElement>(":scope > .turn");
  const real = turns?.[turns.length - 1];
  if (real === undefined) {
    throw new Error("the chat painted no turn");
  }
  cover.holdCover(s.id);
  await new Promise((r) => setTimeout(r, PAST_DELAY_MS));
  const skel = document.querySelector<HTMLElement>("#transcript-cover .turn");
  if (skel === null) {
    throw new Error("the cover shows no skeleton turn");
  }
  return { real, skel };
}

function rect(el: Element): DOMRect {
  return el.getBoundingClientRect();
}

function part(card: HTMLElement, sel: string): HTMLElement {
  const found = card.querySelector<HTMLElement>(`:scope > ${sel}`);
  if (found === null) {
    throw new Error(`no ${sel}`);
  }
  return found;
}

function shownCover(): HTMLElement {
  const c = document.getElementById("transcript-cover");
  if (c === null || c.hidden) {
    throw new Error("the cover is not shown");
  }
  return c;
}

let sheet: HTMLStyleElement;

beforeAll(() => {
  sheet = mountAppCSS();
  area.style.cssText = "position:fixed;inset:0";
});

afterAll(async () => {
  sheet.remove();
  area.remove();
  document.documentElement.removeAttribute("data-pointer");
  document.documentElement.removeAttribute("data-theme");
  await page.viewport(1280, 720);
});

beforeEach(() => {
  cover.dropCover();
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  store.setPainted("");
});

afterEach(() => {
  cover.dropCover();
});

describe("the skeleton is the latest turn's shape", () => {
  for (const device of DEVICES) {
    for (const theme of ["dark", "light"] as const) {
      it(`on a ${device.name} in the ${theme} theme, shares the real turn's edges and footer band`, async () => {
        await page.viewport(device.width, device.height);
        document.documentElement.setAttribute("data-pointer", device.pointer);
        document.documentElement.setAttribute("data-theme", theme);
        const { real, skel } = await paintThenCover(60);
        const port = rect(document.getElementById("messages-wrap") as HTMLElement);
        const r = rect(real);
        const k = rect(skel);
        expect(r.top, "the premise: the latest turn is taller than the viewport").toBeLessThan(
          port.top,
        );

        expect(k.left, "left edge").toBeCloseTo(r.left, 0);
        expect(k.right, "right edge").toBeCloseTo(r.right, 0);
        expect(k.bottom, "bottom edge").toBeCloseTo(r.bottom, 0);

        const realFoot = part(real, ".turn-footer");
        const skelFoot = part(skel, ".turn-footer");
        expect(rect(skelFoot).height, "footer band height").toBeCloseTo(rect(realFoot).height, 0);
        expect(rect(skelFoot).bottom, "footer band bottom").toBeCloseTo(rect(realFoot).bottom, 0);

        // One turn filling the viewport: the prompt band at the top of the transcript column.
        expect(
          document.querySelectorAll("#transcript-cover .turn"),
          "the skeleton is more than one turn",
        ).toHaveLength(1);
        const head = part(skel, ".turn-header");
        const columnTop = Number.parseFloat(
          getComputedStyle(real.parentElement as Element).paddingTop,
        );
        expect(rect(skel).top - port.top, "the card starts where the column does").toBeCloseTo(
          columnTop,
          0,
        );
        expect(rect(head).top, "the prompt band leads the card").toBeCloseTo(k.top + 1, 0);

        // The real surfaces' own fills, so the swap changes no colour under the reader.
        for (const [a, b, what] of [
          [skel, real, "card"],
          [head, part(real, ".turn-header"), "prompt band"],
          [skelFoot, realFoot, "footer band"],
        ] as const) {
          expect(getComputedStyle(a).backgroundColor, `${what} fill`).toBe(
            getComputedStyle(b).backgroundColor,
          );
        }

        // Reply lines run down to the footer band, whole lines only.
        const body = part(skel, ".turn-body");
        const bodyBox = rect(body);
        const pad = Number.parseFloat(getComputedStyle(body).paddingBottom);
        const bars = [...body.querySelectorAll<HTMLElement>(".skeleton")]
          .map((b) => rect(b))
          .filter((b) => b.height > 0 && b.top < bodyBox.bottom);
        expect(bars.length, "the reply has lines").toBeGreaterThan(3);
        for (const b of bars) {
          expect(b.bottom, "a reply line is cut by the body's edge").toBeLessThanOrEqual(
            bodyBox.bottom - pad + 0.5,
          );
        }
        const last = Math.max(...bars.map((b) => b.bottom));
        const lineHeight = Number.parseFloat(
          getComputedStyle(body.querySelector(".message") as Element).lineHeight,
        );
        expect(
          bodyBox.bottom - pad - last,
          "the reply lines stop short of the footer",
        ).toBeLessThan(lineHeight * 2);

        // The lift moves nothing under it.
        cover.dropCover();
        expect(rect(real).bottom).toBeCloseTo(r.bottom, 0);
        expect(rect(real).left).toBeCloseTo(r.left, 0);
      });
    }
  }

  it("keeps the edges on a short latest turn at the column's foot", async () => {
    await page.viewport(1440, 900);
    document.documentElement.setAttribute("data-pointer", "fine");
    const { real, skel } = await paintThenCover(2);
    const port = rect(document.getElementById("messages-wrap") as HTMLElement);
    expect(
      rect(real).top,
      "the premise: the latest turn is shorter than the viewport",
    ).toBeGreaterThan(port.top);
    expect(rect(skel).left).toBeCloseTo(rect(real).left, 0);
    expect(rect(skel).right).toBeCloseTo(rect(real).right, 0);
    expect(rect(skel).bottom).toBeCloseTo(rect(real).bottom, 0);
    expect(shownCover().scrollHeight, "the skeleton overflows its cover").toBe(
      shownCover().clientHeight,
    );
  });
});

describe("the skeleton under reduced motion", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("shows and lifts with no animation", async () => {
    await page.viewport(1440, 900);
    await paintThenCover(10);
    const node = shownCover();
    const moving = node
      .getAnimations({ subtree: true })
      .filter(
        (a) => Number(a.effect?.getComputedTiming().duration) > 1 && a.playState === "running",
      );
    expect(moving.map((a) => (a as CSSAnimation).animationName)).toEqual([]);
    cover.dropCover();
    expect(node.hidden, "the lift ran a fade").toBe(true);
  });
});
