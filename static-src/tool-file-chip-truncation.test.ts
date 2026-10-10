// A tool card's file-name chip TRUNCATES WITH AN ELLIPSIS instead of cutting a glyph. Chromium
// exposes no "ellipsis painted" API, so the cases assert every condition for one on the element
// that must carry them: `text-overflow: ellipsis`, non-visible `overflow`, no-wrap `white-space`,
// and content overflowing its OWN content box. Fixtures come from the production builder (the
// chip's DOM is the subject); the short-name control gives the overflow case its meaning.

import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";
import { framesBudgetMs, testTimeoutFor } from "./__test-helpers__/frame-budget.js";

/** Worst case is six frames: `rendered()`'s three per `mount()`, twice. Past this
 *  suite's rAF throttle that is 6.1s, over vitest's 5s default — and a preempted
 *  wait reads as a bare timeout instead of naming the assertion that was wrong. */
const CHIP_TIMEOUT_MS = testTimeoutFor(framesBudgetMs(6));

// The card builder's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the imports are evaluated,
// which is why the imports below are dynamic.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

const { mountAppCSS } = await import("./__test-helpers__/css-rules.js");
const { buildToolCard } = await import("./tool-card.js");

/** A transcript-width column, IN the viewport rather than parked off-screen:
 *  `.tool-call` carries `content-visibility: auto`, and Chromium only lands the
 *  render decision for an on-screen host. */
const host = document.createElement("div");
host.style.cssText = "position:fixed;top:0;left:0;inline-size:760px;";
document.body.appendChild(host);

let style: HTMLStyleElement;

beforeAll(() => {
  style = mountAppCSS();
});

afterAll(() => {
  style.remove();
  host.remove();
  document.documentElement.removeAttribute("data-pointer");
});

afterEach(() => {
  host.replaceChildren();
});

/** 39 characters, comfortably past the ~26 the 12.5rem cap allows at --fs-xs. */
const LONG_PATH = "internal/agent/streaming_tools_translation_handlers.go";
const SHORT_PATH = "internal/agent/main.go";

/** Three lifecycle passes, copied from tool-group-height.test.ts — a count of passes rather than a
 *  wall-clock wait, so load does not move it. */
async function rendered(): Promise<void> {
  for (let i = 0; i < 3; i++) {
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        resolve();
      });
    });
  }
}

async function mount(path: string): Promise<{ chip: HTMLElement; name: HTMLElement }> {
  document.documentElement.dataset["pointer"] = "fine";
  const card = buildToolCard({
    id: "chip1",
    title: "Read File",
    kind: "read",
    status: "completed",
    live: false,
    input: { path },
  });
  host.appendChild(card);
  await rendered();
  const chip = card.querySelector<HTMLElement>("button.tool-file-link");
  const name = chip?.querySelector<HTMLElement>(".tool-file-name");
  if (chip === null || chip === undefined || name === null || name === undefined) {
    throw new Error(`the production builder produced no file chip for ${path}`);
  }
  return { chip, name };
}

describe(
  "a tool card's file-name chip truncates with an ellipsis",
  { timeout: CHIP_TIMEOUT_MS },
  () => {
    it("keeps a long file name INSIDE the chip instead of overflowing it", async () => {
      // Cause B: a flex item at `min-width: auto` refuses to shrink below its filename. Absolute x
      // positions from ONE layout pass; `getBoundingClientRect` reports the LAYOUT box regardless of an
      // ancestor's clip.
      const { chip, name } = await mount(LONG_PATH);
      expect(
        name.getBoundingClientRect().right,
        "the name's right edge must not run past the chip's: the span has to be able to SHRINK",
      ).toBeLessThanOrEqual(chip.getBoundingClientRect().right + 1);
    });

    it("truncates with an ellipsis, on the element that HOLDS the text", async () => {
      // Cause A: an ellipsis on the inline-flex button has no inline content to act on. All four
      // conditions on the span, plus proof it is really clipping.
      const { name } = await mount(LONG_PATH);
      const s = getComputedStyle(name);
      expect(s.textOverflow, "the span that holds the name must carry the ellipsis").toBe(
        "ellipsis",
      );
      expect(s.overflow, "and a clip boundary for it to act at").not.toBe("visible");
      expect(s.whiteSpace, "and no wrap opportunity, or it wraps instead of truncating").toBe(
        "nowrap",
      );
      expect(
        name.scrollWidth,
        "and the content must overflow the SPAN's own box, or the ellipsis has nothing to replace",
      ).toBeGreaterThan(name.clientWidth);
    });

    it("leaves a SHORT file name untruncated", async () => {
      // The control. Without it the case above proves the span overflows and nothing
      // about the cause: a span that overflowed whatever it held would satisfy it too.
      const { chip, name } = await mount(SHORT_PATH);
      expect(name.scrollWidth, "a name that fits is not clipped").toBe(name.clientWidth);
      // Read `maxWidth` off the computed style rather than hardcoding 200px: the root
      // font size is fluid in this app.
      expect(
        chip.offsetWidth,
        "and the chip shrinks to fit rather than sitting at its cap",
      ).toBeLessThan(parseFloat(getComputedStyle(chip).maxWidth));
    });
  },
);
