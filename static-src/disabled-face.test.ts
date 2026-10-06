import { describe, it, expect, beforeAll, afterAll, afterEach } from "vitest";

import { setBusy, setControlBusy } from "./dom.js";
import { allRules, mountAppCSS } from "./__test-helpers__/css-rules.js";

// Three states below "active", one owner each: UNAVAILABLE (`:disabled`) recedes (dimmed, `not-allowed`); BUSY
// (`:disabled[aria-busy="true"]`) does not (full opacity, `cursor: progress`, since its label reports the work);
// READOUT (a disabled `.send-btn`) is not refusing anything.

const sheets = import.meta.glob<string>("./css/*.css", {
  query: "?raw",
  import: "default",
  eager: true,
});

/** Selectors allowed to declare a disabled face of their own, and why. */
const OPT_OUTS = new Map([
  ["&:disabled", "the send button's readout opt-out, nested in .send-btn"],
]);

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

afterEach(() => {
  stage.replaceChildren();
});

function mount(cls: string): HTMLButtonElement {
  const b = document.createElement("button");
  b.className = cls;
  b.textContent = "Go";
  stage.appendChild(b);
  return b;
}

describe("the disabled face", () => {
  it("dims an unavailable control and refuses the pointer, from the token", () => {
    const b = mount("btn");
    b.disabled = true;

    const cs = getComputedStyle(b);
    expect(cs.opacity).toBe("0.4");
    expect(cs.cursor).toBe("not-allowed");
  });

  it("does NOT dim a busy control, and shows progress rather than refusal", () => {
    const b = mount("btn");
    setControlBusy(b, true);

    const cs = getComputedStyle(b);
    expect(b.disabled).toBe(true);
    expect(cs.opacity).toBe("1");
    expect(cs.cursor).toBe("progress");
  });

  it("returns a control to the unavailable face when its work ends", () => {
    const b = mount("btn");
    setControlBusy(b, true);
    setControlBusy(b, false);

    expect(b.disabled).toBe(false);
    expect(b.hasAttribute("aria-busy")).toBe(false);
    expect(getComputedStyle(b).opacity).toBe("1");
  });

  it("leaves a disabled READOUT undimmed", () => {
    const send = mount("send-btn");
    send.disabled = true;

    expect(getComputedStyle(send).opacity).toBe("1");
    expect(getComputedStyle(send).cursor).toBe("default");
  });

  it("is declared in one place: no family re-spells it", () => {
    const offenders: string[] = [];
    for (const [path, css] of Object.entries(sheets)) {
      for (const { selector, body } of allRules(css)) {
        if (!/:disabled/.test(selector)) {
          continue;
        }
        // A hover/active twin only suppresses the enabled paint; it is not a face.
        if (/:hover|:active/.test(selector)) {
          continue;
        }
        if (!/(?<![-\w])(?:opacity|cursor):/.test(body)) {
          continue;
        }
        // The two shared rules in 40-a11y.css are the owner.
        if (path.endsWith("40-a11y.css")) {
          continue;
        }
        if (OPT_OUTS.has(selector)) {
          continue;
        }
        offenders.push(`${path} { ${selector} }`);
      }
    }
    expect(offenders, "only 40-a11y.css and the declared opt-outs style :disabled").toEqual([]);
  });
});

describe("setBusy", () => {
  it("writes the literal ARIA value, so the attribute is readable and matchable", () => {
    const b = mount("btn");
    setBusy(b, true);

    // `toggleAttribute` writes "", which Chromium's accessibility tree reports as no busy property, and fails the selector.
    expect(b.getAttribute("aria-busy")).toBe("true");
    expect(b.matches('[aria-busy="true"]')).toBe(true);
    expect(b.ariaBusy).toBe("true");

    setBusy(b, false);
    expect(b.getAttribute("aria-busy")).toBeNull();
  });
});
