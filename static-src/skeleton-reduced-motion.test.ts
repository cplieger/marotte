import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";

import { emulateA11yMedia, resetA11yMedia } from "./__test-helpers__/a11y-media.js";
import { loadCSS, mountAppCSS, ruleContaining } from "./__test-helpers__/css-rules.js";

// ---------------------------------------------------------------------------
// REDUCED MOTION PARKS THE PULSE AT ITS MEAN, and without the arm the
// preference makes the app LOUDER.
//
// 40-a11y.css's global sweep runs every animation once for 0.01ms with no
// iteration left, and `vk-skeleton` declares no `animation-fill-mode` — so the
// element reverts to its OWN opacity, which is the initial value 1 unless a rule
// declares one. Against an animated ceiling of 0.4 that is 2.5x the intended
// strength for the one reader who asked for less.
//
// Two halves, because either alone passes while the app is broken. The SOURCE
// half pins that the fix sits inside the right media query rather than merely
// existing somewhere in the file. The RENDERED half is the only thing that can
// say what the element computes when that query MATCHES, and it needs the
// negative control beside it: 0.275 read in one mode says nothing until the
// other mode is shown to read something else.
//
// The sweep is NOT weakened, and the last case is what says so: a fix that
// "solved" this by editing 40-a11y.css would leave every other animation in the
// app running under the preference.
// ---------------------------------------------------------------------------

/** The mean of the keyframes' two amplitudes, (0.4 + 0.15) / 2 — the pulse's own
 *  average, which is what the arm parks at. */
const MEAN = "0.275";

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

function bar(): HTMLDivElement {
  const el = document.createElement("div");
  el.className = "skeleton skeleton-line";
  stage.appendChild(el);
  return el;
}

describe("the reduced-motion arm, in source", () => {
  it("declares opacity INSIDE the preference query", () => {
    // `ruleContaining` with a scope is the reader for exactly this: `.skeleton`
    // legitimately appears twice in this file, once at top level and once here,
    // and the whole point of the pair is that the two bodies differ.
    const arm = ruleContaining(loadCSS("30-utilities.css"), ".skeleton", "prefers-reduced-motion");
    expect(arm.body).toMatch(/(^|[\s;])opacity\s*:/);
  });

  it("leaves the global sweep in 40-a11y.css intact", () => {
    // Anchored on the two `!important` declarations rather than on line numbers:
    // that file is edited concurrently, and the sweep is what this arm exists to
    // work WITH rather than around.
    const a11y = loadCSS("40-a11y.css");
    expect(a11y, "the sweep still neutralises every animation's duration").toContain(
      "animation-duration: 0.01ms !important",
    );
    expect(a11y, "the sweep still leaves no iteration").toContain(
      "animation-iteration-count: 1 !important",
    );
  });
});

describe("the reduced-motion arm, rendered under the emulated preference", () => {
  beforeAll(async () => {
    await emulateA11yMedia({ reducedMotion: "reduce" });
  });

  afterAll(async () => {
    await resetA11yMedia();
  });

  it("parks a real bar at the pulse's mean", async () => {
    const el = bar();

    // The sweep runs the animation once for 0.01ms, so the parked value is what
    // the element reads AFTER that iteration rather than in the frame it was
    // appended. Polling on the product's own output costs a working test one
    // check and gives a failure a real value to report.
    await vi.waitFor(() => {
      expect(getComputedStyle(el).opacity).toBe(MEAN);
    });
  });

  it("keeps the pulse's own declaration, parking by opacity alone", () => {
    // The arm's whole content is one `opacity`. Three shapes it must not take,
    // each of which would park the bar and mean something else:
    //
    //   `animation: none` stops the pulse for this reader rather than letting the
    //   global sweep stop it, which duplicates a decision 40-a11y.css owns.
    //   `!important` claims the arm has something to beat, and it does not — the
    //   sweep writes `animation-*` and never `opacity`, and an animation outranks
    //   a normal declaration only while it is RUNNING.
    //   A second channel (a border, a shape) is not owed: a skeleton's channels
    //   are its geometry and its fill, and both survive the sweep untouched.
    const arm = ruleContaining(loadCSS("30-utilities.css"), ".skeleton", "prefers-reduced-motion");
    expect(arm.body).not.toMatch(/(^|[\s;])animation/);
    expect(arm.body).not.toContain("!important");
    expect(arm.body).not.toMatch(/(^|[\s;])border/);
  });
});

describe("no emulation, which is what makes the parked reading mean anything", () => {
  it("leaves a real bar's opacity to the running pulse", () => {
    const el = bar();
    const opacity = Number(getComputedStyle(el).opacity);

    // Not the initial value: an element whose opacity reads 1 here is one the
    // pulse is not driving at all, which is the state the arm is mistaken for.
    expect(opacity).not.toBe(1);
    // Inside the keyframes' own band, read as a range rather than a value
    // because the pulse is mid-flight at whatever instant this runs.
    expect(opacity).toBeGreaterThanOrEqual(0.15);
    expect(opacity).toBeLessThanOrEqual(0.4);

    // And the animation is INFINITE here, where the emulated case reports one
    // iteration. That pair is the difference the emulation actually makes, and
    // asserting it is what stops this control passing on a page where nothing is
    // animating for an unrelated reason.
    const [anim] = el.getAnimations();
    expect(anim?.effect?.getTiming().iterations).toBe(Infinity);
  });
});
