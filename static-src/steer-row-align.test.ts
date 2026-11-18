// Every holding-area row (steer, queued, paused, compaction): beside a one-line message the
// state label shares the message's baseline and the glyph sits on that line's x-height middle; beside
// a longer message the state stays centred on the whole block. The label is a smaller font than the
// text, so two boxes merely centred on each other leave the text's baseline low of the label's.

import { describe, it, expect, afterEach } from "vitest";
import { page as browser } from "vitest/browser";
import { mountAppCSS } from "./__test-helpers__/css-rules.js";
import { mountPane, openStack, unmountView } from "./__test-helpers__/holding-area.js";
import { rowEntry, runAt, turnEntry } from "./__test-helpers__/run-order.js";
import type { SteerStackSource } from "./pending-steers.js";
import type { PendingSteer, RunEntryView, RunView } from "./types.js";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

const VIEWPORTS = [
  [1440, 900],
  [390, 844],
] as const;

const ONE_LINE = "also, marotte has been updated to dev 280";
const LONG =
  "check the deploy logs on borgcube for the caddy reload, then compare the cert expiry against the " +
  "renewal window and tell me whether the relay restarted cleanly after the last image bump went out";
const ATTACHED = "pasted-2026-11-17T11-09-47.png";

// A fraction of a pixel: box-centring alone leaves them about 0.4 px apart in this suite's fonts
// (about 1 px in a system UI font), and a shared strut puts them at 0.
const BASELINE_PX = 0.25;
// `vertical-align: middle` centres the glyph on the baseline plus half the x-height (CSS 2.2
// §10.8.1), a point whose distance from the line box's centre depends on the font.
const ICON_PX = 0.5;

let style: HTMLStyleElement | null = null;

afterEach(async () => {
  unmountView();
  style?.remove();
  style = null;
  localStorage.removeItem(LS_UI_STATE_KEY);
  await browser.viewport(1280, 720);
});

function sourceOf(steers: readonly PendingSteer[], run: RunView): SteerStackSource {
  return {
    read: () => ({ id: "c1", steers: [...steers], run }),
    edit: () => Promise.resolve(),
    editByClear: () => Promise.resolve(),
    remove: () => Promise.resolve(),
    discard: () => Promise.resolve(),
    waitingFor: "the agent",
  };
}

const queued = (id: string, text: string, attach = false): RunEntryView =>
  rowEntry(
    id,
    { after: "turns", turns: 1 },
    {
      row: {
        id,
        text,
        ...(attach && { attachments: [{ path: `/uploads/${ATTACHED}`, name: ATTACHED }] }),
      },
    },
  );

/** Every row kind the holding area draws, across three stacks: a running turn's, a paused queue's and
 *  a compaction's in flight. `phoneBaselines` is how many one-line rows keep their label at 390 px
 *  (the sender row's); the other stacks hide it there. */
const STACKS: readonly {
  name: string;
  steers: PendingSteer[];
  run: RunView;
  phoneBaselines?: number;
}[] = [
  {
    name: "behind a running turn",
    phoneBaselines: 1,
    steers: [
      { id: "steer-1", text: ONE_LINE, origin: "user" },
      { id: "steer-2", text: LONG, origin: "user" },
      {
        id: "steer-3",
        text: "1 new; run grelot inbox",
        origin: "user",
        caller: { label: "grelot-watch-grelot-ops-and-every-sibling" },
      },
    ],
    run: runAt(
      1,
      [
        turnEntry("t1"),
        queued("m-q1", ONE_LINE),
        queued("m-q2", LONG),
        queued("m-q3", "", true),
        queued("m-q4", "what does this screenshot show", true),
      ],
      { cancel: "t1", send: "steer" },
    ),
  },
  {
    name: "paused by a stop",
    steers: [],
    run: runAt(1, [
      rowEntry(
        "m-p1",
        { after: "paused" },
        { state: "paused", stop_paused: true, row: { id: "m-p1", text: ONE_LINE } },
      ),
    ]),
  },
  {
    name: "compacting",
    steers: [],
    run: runAt(1, [], { compacting: true }),
  },
];

const shown = (e: Element | null): e is HTMLElement =>
  e instanceof HTMLElement && e.checkVisibility() && e.getBoundingClientRect().height > 0;

/** The y of an element's first line's baseline: a zero-size inline-block at its start sits on it. */
function baseline(el: HTMLElement): number {
  const probe = document.createElement("span");
  probe.style.cssText = "display:inline-block;inline-size:0;block-size:0;vertical-align:baseline";
  el.prepend(probe);
  const y = probe.getBoundingClientRect().top;
  probe.remove();
  return y;
}

/** The y of an element's first line's baseline less half its x-height: a 1ex inline-block on it. */
function xMiddle(el: HTMLElement): number {
  const probe = document.createElement("span");
  probe.style.cssText = "display:inline-block;inline-size:0;block-size:1ex;vertical-align:baseline";
  el.prepend(probe);
  const r = probe.getBoundingClientRect();
  probe.remove();
  return r.bottom - r.height / 2;
}

const centre = (r: DOMRect): number => r.top + r.height / 2;

describe("a holding-area row's state and message", () => {
  for (const [w, h] of VIEWPORTS) {
    for (const stack of STACKS) {
      it(`share one line at ${String(w)}x${String(h)}, ${stack.name}`, async () => {
        await browser.viewport(w, h);
        style = mountAppCSS();
        openStack(true);
        const chat = await mountPane(h, 0, false, sourceOf(stack.steers, stack.run));
        const rows = [...chat.area.querySelectorAll<HTMLElement>(".steer-stack > .steer-row")];
        expect(rows.length, "rows drawn").toBeGreaterThan(0);
        let baselines = 0;
        for (const row of rows) {
          const what = `${row.dataset["kind"] ?? "steer"} row "${row.dataset["tooltip"] ?? ""}"`;
          const text = row.querySelector<HTMLElement>(".steer-text")!;
          const state = row.querySelector<HTMLElement>(".steer-state")!;
          const label = row.querySelector<HTMLElement>(".steer-state-label");
          const glyph = row.querySelector(".steer-state-icon > *");
          const tr = text.getBoundingClientRect();
          const lineH = parseFloat(getComputedStyle(text).lineHeight);
          const lines = Math.round(tr.height / lineH);
          expect(glyph, `${what}: a glyph`).not.toBeNull();
          expect(
            state.getBoundingClientRect().right,
            `${what}: state beside text`,
          ).toBeLessThanOrEqual(tr.left + 0.5);
          if (lines === 1) {
            if (shown(label)) {
              baselines += 1;
              expect(
                Math.abs(baseline(label) - baseline(text)),
                `${what}: baselines`,
              ).toBeLessThanOrEqual(BASELINE_PX);
            }
            expect(
              Math.abs(centre(glyph!.getBoundingClientRect()) - xMiddle(text)),
              `${what}: glyph on the line's x-height middle`,
            ).toBeLessThanOrEqual(ICON_PX);
          } else {
            // A longer message keeps the state centred on the whole message (26-dock.css `.steer-row`).
            const st = shown(label) ? label : state;
            expect(
              Math.abs(centre(st.getBoundingClientRect()) - centre(tr)),
              `${what}: state on the message's centre`,
            ).toBeLessThanOrEqual(1);
          }
        }
        if (w === 1440) {
          expect(baselines, "baseline checks").toBeGreaterThan(0);
        } else if (stack.phoneBaselines !== undefined) {
          expect(baselines, "baseline checks").toBe(stack.phoneBaselines);
        }
      });
    }
  }

  it("ellipsises a long sender on its line at phone width, the message the wider column", async () => {
    await browser.viewport(390, 844);
    style = mountAppCSS();
    openStack(true);
    const chat = await mountPane(844, 0, false, sourceOf(STACKS[0]!.steers, STACKS[0]!.run));
    const row = [...chat.area.querySelectorAll<HTMLElement>(".steer-row")].find(
      (r) => r.querySelector(".steer-state-sender") !== null,
    )!;
    const state = row.querySelector<HTMLElement>(".steer-state")!;
    const text = row.querySelector<HTMLElement>(".steer-text")!;
    // Whichever box between the sender and the column clips it, it clips with an ellipsis.
    const boxes: HTMLElement[] = [];
    for (let e: HTMLElement | null = row.querySelector(".steer-state-sender"); e !== row;) {
      boxes.push(e!);
      e = e!.parentElement;
    }
    const clip = boxes.find((e) => e.scrollWidth > e.clientWidth && e.clientWidth > 0);
    expect(clip, "the sender overflows its column").toBeDefined();
    expect(getComputedStyle(clip!).textOverflow).toBe("ellipsis");
    const lineH = parseFloat(getComputedStyle(text).lineHeight);
    expect(state.getBoundingClientRect().height, "the state is one line").toBeLessThanOrEqual(
      lineH + 0.5,
    );
    expect(text.getBoundingClientRect().width).toBeGreaterThan(state.getBoundingClientRect().width);
  });
});
