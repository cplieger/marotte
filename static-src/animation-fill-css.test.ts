// Every animation drops off its element when it finishes, except those that must stay.
// An entry animation declares only `from`, so a forwards fill changes nothing visible but
// keeps it attached, and Chromium drops subpixel antialiasing under an animated opacity
// (the classification lives at the keyframe library, css/03-base.css). An exit animation's
// last frame is a state the element lacks, so dropping its fill snaps it back. Both
// directions are pinned with real markup and `getAnimations()`.
import { describe, it, expect, beforeAll, afterAll } from "vitest";

import { mountAppCSS } from "./__test-helpers__/css-rules.js";

let sheet: HTMLStyleElement;
let stage: HTMLDivElement;

beforeAll(() => {
  sheet = mountAppCSS();
  stage = document.createElement("div");
  document.body.appendChild(stage);
});

afterAll(() => {
  sheet.remove();
  stage.remove();
});

/** Build `spec` (a tag plus classes and attributes) and return it, optionally
 *  inside `parent`, which is built the same way. */
interface Fixture {
  /** What the stylesheet's selector needs to match, in words. */
  readonly what: string;
  /** `tag.class.class[attr]`, the element under test. */
  readonly el: string;
  /** Ancestor chain, outermost first, when the rule needs one. */
  readonly under?: readonly string[];
}

function build(spec: string): HTMLElement {
  const [head = "div", ...rest] = spec.split(/(?=[.[])/u);
  const node = document.createElement(head === "" ? "div" : head);
  for (const part of rest) {
    if (part.startsWith(".")) {
      node.classList.add(part.slice(1));
      continue;
    }
    const attr = part.slice(1, -1);
    const [name = "", value = ""] = attr.split("=");
    node.setAttribute(name, value.replace(/^"|"$/gu, ""));
  }
  return node;
}

function mount(f: Fixture): HTMLElement {
  const node = build(f.el);
  let host: HTMLElement = stage;
  for (const step of f.under ?? []) {
    const wrap = build(step);
    host.appendChild(wrap);
    host = wrap;
  }
  host.appendChild(node);
  return node;
}

/** Entry animations: the last keyframe is the element's own value, so nothing
 *  may still be attached once the animation has run. */
const ENTRY: readonly Fixture[] = [
  {
    what: "a streamed text chunk",
    el: "span[data-vk-chunk-enter]",
    under: ["div.message.assistant"],
  },
  {
    what: "a completed code block",
    el: "pre[data-vk-block-enter]",
    under: ["div.message.assistant"],
  },
  {
    what: "a completed blockquote",
    el: "blockquote[data-vk-block-enter]",
    under: ["div.message.assistant"],
  },
  { what: "a completed table", el: "table[data-vk-block-enter]", under: ["div.message.assistant"] },
  { what: "a tooltip", el: "div.uip-tooltip" },
  { what: "a model picker card", el: "button.picker-btn" },
  { what: "a transcript boundary", el: "div.boundary" },
  { what: "a file browser row", el: "div.fb-row" },
  { what: "a tab row taking the drag's slot", el: "div.tab.tab-slotted" },
  { what: "the shell entering fullscreen", el: "div.shell-panel.shell-fullscreen" },
  // A no-fill entry, as the control that says these assertions are not vacuous:
  // it detaches for a reason that has nothing to do with the sweep.
  { what: "a tab entering the strip", el: "div.tab.entering" },
];

/**
 * A container whose height the CONTENT decides may carry no entry animation: a composited
 * layer costs area x DPR^2 x 4 bytes (a tall turn card is tens of MB on a DPR-3 phone),
 * enough to crash WebKit.
 */
const UNBOUNDED: readonly Fixture[] = [
  { what: "an appended chat element", el: "div[data-chat-entry]" },
  { what: "a turn card", el: "div.turn[data-chat-entry]" },
  { what: "a tool card", el: "div.tool-call" },
  { what: "a tool group", el: "div.tool-group" },
  { what: "a delegated-work card", el: "div.subagent-block" },
  { what: "a plan card", el: "div.plan-message" },
  { what: "a run card", el: "div.run-card" },
];

/** Exit animations: the last keyframe is a state the element does not otherwise
 *  have, so the animation MUST still be applied when it ends. */
const EXIT: readonly Fixture[] = [
  { what: "a tab leaving the strip", el: "div.tab.exiting" },
  { what: "a sub-tab merging into its parent", el: "div.tab.exiting.exiting-merge" },
  {
    what: "a dismissed dock card",
    el: "div.dock-outgoing",
    under: ['div.decision-dock[data-dock-phase="leaving"]'],
  },
  {
    what: "a dock card advancing out",
    el: "div.dock-outgoing",
    under: ['div.decision-dock[data-dock-phase="advancing"]'],
  },
  { what: "the shell leaving fullscreen", el: "div.shell-panel.shell-fullscreen-leaving" },
];

describe("an entry animation detaches when it finishes", () => {
  for (const f of ENTRY) {
    it(`drops off ${f.what}`, async () => {
      const node = mount(f);
      const running = node.getAnimations();
      // Guard against a selector that stopped matching, which makes "none left" trivially true.
      expect(running, `the rule reaches ${f.what}`).toHaveLength(1);
      await Promise.all(running.map((a) => a.finished));
      expect(node.getAnimations(), `nothing left attached to ${f.what}`).toEqual([]);
    });
  }
});

describe("a content-sized container costs no compositing layer", () => {
  for (const f of UNBOUNDED) {
    it(`animates nothing on ${f.what}`, () => {
      expect(mount(f).getAnimations(), `${f.what} is animating again`).toEqual([]);
    });
  }

  it("keeps the streamed text's own fades, which are small elements", () => {
    // The negative control: this suite would also pass with every animation in the
    // app deleted, and the transcript still has to materialise as text arrives.
    const chunk = mount({
      what: "chunk",
      el: "span[data-vk-chunk-enter]",
      under: ["div.message.assistant"],
    });
    expect(chunk.getAnimations()).toHaveLength(1);
  });
});

describe("an exit animation holds its end state", () => {
  for (const f of EXIT) {
    it(`stays applied to ${f.what}`, async () => {
      const node = mount(f);
      const running = node.getAnimations();
      expect(running, `the rule reaches ${f.what}`).toHaveLength(1);
      await Promise.all(running.map((a) => a.finished));
      expect(node.getAnimations(), `still applied to ${f.what}`).toHaveLength(1);
    });
  }
});

describe("the fade a streamed chunk actually shows", () => {
  it("starts the span invisible, so text does not pop in at full opacity", () => {
    const node = mount({
      what: "a streamed text chunk",
      el: "span[data-vk-chunk-enter]",
      under: ["div.message.assistant"],
    });
    expect(Number(getComputedStyle(node).opacity)).toBeLessThan(1);
  });
});

/** Keyframe names whose only stop is the start, so the animation ends at
 *  whatever the element itself declares. Read off the assembled bundle. */
function fromOnlyKeyframes(css: string): Set<string> {
  const out = new Set<string>();
  for (const m of css.matchAll(/@keyframes\s+([\w-]+)\s*\{/gu)) {
    let depth = 0;
    let body = "";
    for (let i = m.index + m[0].length - 1; i < css.length; i++) {
      if (css[i] === "{") {
        depth++;
      } else if (css[i] === "}") {
        depth--;
        if (depth === 0) {
          body = css.slice(m.index + m[0].length, i);
          break;
        }
      }
    }
    const stops = [...body.matchAll(/(?:^|[{}\s,])(from|to|\d+%)\s*(?=[,{])/gu)].map((s) => s[1]);
    if (stops.length > 0 && stops.every((s) => s === "from" || s === "0%")) {
      out.add(m[1] ?? "");
    }
  }
  return out;
}

/**
 * Every finite `animation` shorthand in the bundle, whitespace-normalized so a wrapped
 * declaration cannot hide its fill keyword.
 */
function finiteAnimations(css: string): string[] {
  const out: string[] = [];
  for (const m of css.matchAll(/(?:^|[{;\s])animation\s*:([^;}]*)/gu)) {
    const decl = (m[1] ?? "").split(/\s+/u).filter(Boolean).join(" ");
    if (decl !== "" && decl !== "none" && !decl.includes("infinite")) {
      out.push(decl);
    }
  }
  return out;
}

// The whole-corpus sweep: a rule added later cannot arrive carrying a forwards fill.
describe("the whole stylesheet", () => {
  it("never pairs a from-only keyframe with a forwards fill", () => {
    const css = sheet.textContent ?? "";
    const fromOnly = fromOnlyKeyframes(css);
    expect(fromOnly.size, "the keyframe library parsed").toBeGreaterThan(5);

    const offenders = finiteAnimations(css).filter(
      (decl) =>
        /\b(?:both|forwards)\b/u.test(decl) &&
        [...fromOnly].some((name) => new RegExp(`\\b${name}\\b`, "u").test(decl)),
    );
    expect(offenders, "every from-only animation takes `backwards` or no fill").toEqual([]);
  });
});
